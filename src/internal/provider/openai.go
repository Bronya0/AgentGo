package provider

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"strings"
	"time"

	goai "github.com/sashabaranov/go-openai"
)

// OpenAI 使用 sashabaranov/go-openai 实现 Provider 接口。
// 兼容所有 OpenAI 格式的 API（OpenAI、DeepSeek、MiniMax、Ollama 等）。
type OpenAI struct {
	id         string
	model      string
	client     *goai.Client
	maxRetries int
	retryBase  time.Duration
	retryMax   time.Duration
}

// NewOpenAI 创建一个 OpenAI 兼容 provider。
func NewOpenAI(id, baseURL, apiKey, model string, timeout time.Duration) *OpenAI {
	cfg := goai.DefaultConfig(apiKey)
	if baseURL != "" {
		cfg.BaseURL = strings.TrimRight(baseURL, "/")
	}
	cfg.HTTPClient = &http.Client{Timeout: timeout}
	return &OpenAI{
		id:         id,
		model:      model,
		client:     goai.NewClientWithConfig(cfg),
		maxRetries: 3,
		retryBase:  500 * time.Millisecond,
		retryMax:   5 * time.Second,
	}
}

func (o *OpenAI) ID() string    { return o.id }
func (o *OpenAI) Model() string { return o.model }

// Chat 调用 chat completions 端点，含指数退避重试。
func (o *OpenAI) Chat(
	ctx context.Context,
	messages []Message,
	tools []ToolDefinition,
	handler StreamHandler,
) (*Message, error) {
	req := o.buildRequest(messages, tools, handler != nil)

	var lastErr error
	for attempt := 0; attempt <= o.maxRetries; attempt++ {
		if attempt > 0 {
			delay := o.backoff(attempt)
			slog.Debug("retrying API call", "provider", o.id, "attempt", attempt, "delay", delay)
			select {
			case <-time.After(delay):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}

		var msg *Message
		var err error
		if handler != nil {
			msg, err = o.doStream(ctx, req, handler)
		} else {
			msg, err = o.doFull(ctx, req)
		}
		if err == nil {
			return msg, nil
		}
		if IsFailover(err) {
			lastErr = err
			continue
		}
		return nil, err
	}
	return nil, lastErr
}

// doFull 执行非流式请求。
func (o *OpenAI) doFull(ctx context.Context, req goai.ChatCompletionRequest) (*Message, error) {
	req.Stream = false
	resp, err := o.client.CreateChatCompletion(ctx, req)
	if err != nil {
		return nil, o.mapError(err)
	}
	if len(resp.Choices) == 0 {
		return nil, fmt.Errorf("no choices in response")
	}
	slog.Debug("token usage", "provider", o.id,
		"prompt", resp.Usage.PromptTokens, "completion", resp.Usage.CompletionTokens)

	choice := resp.Choices[0]
	out := &Message{Role: RoleAssistant, Content: choice.Message.Content, Reasoning: choice.Message.ReasoningContent}
	for _, tc := range choice.Message.ToolCalls {
		out.ToolCalls = append(out.ToolCalls, ToolCall{
			ID:        tc.ID,
			Name:      tc.Function.Name,
			Arguments: tc.Function.Arguments,
		})
	}
	return out, nil
}

// doStream 执行流式请求，文本增量通过 handler 推送，工具调用在流结束后组装。
func (o *OpenAI) doStream(ctx context.Context, req goai.ChatCompletionRequest, handler StreamHandler) (*Message, error) {
	req.Stream = true
	stream, err := o.client.CreateChatCompletionStream(ctx, req)
	if err != nil {
		return nil, o.mapError(err)
	}
	defer stream.Close()

	out := &Message{Role: RoleAssistant}

	// 工具调用按 index 暂存，流结束后组装
	type partialTC struct {
		id   string
		name string
		args strings.Builder
	}
	tcMap := make(map[int]*partialTC)
	maxIdx := -1

	for {
		resp, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			handler(StreamDelta{Done: true})
			break
		}
		if err != nil {
			return nil, o.mapError(err)
		}
		if len(resp.Choices) == 0 {
			continue
		}

		delta := resp.Choices[0].Delta
		if delta.Content != "" {
			out.Content += delta.Content
			handler(StreamDelta{Text: delta.Content})
		}
		if delta.ReasoningContent != "" {
			out.Reasoning += delta.ReasoningContent
			handler(StreamDelta{Reasoning: delta.ReasoningContent})
		}

		for _, tc := range delta.ToolCalls {
			idx := 0
			if tc.Index != nil {
				idx = *tc.Index
			}
			p, ok := tcMap[idx]
			if !ok {
				p = &partialTC{}
				tcMap[idx] = p
			}
			if tc.ID != "" {
				p.id = tc.ID
			}
			if tc.Function.Name != "" {
				p.name = tc.Function.Name
			}
			if tc.Function.Arguments != "" {
				p.args.WriteString(tc.Function.Arguments)
			}
			if idx > maxIdx {
				maxIdx = idx
			}
		}
	}

	// 组装完整工具调用
	for i := 0; i <= maxIdx; i++ {
		p, ok := tcMap[i]
		if !ok {
			continue
		}
		tc := ToolCall{ID: p.id, Name: p.name, Arguments: p.args.String()}
		out.ToolCalls = append(out.ToolCalls, tc)
		handler(StreamDelta{ToolCall: &tc})
	}

	return out, nil
}

// buildRequest 构建 goai.ChatCompletionRequest。
func (o *OpenAI) buildRequest(messages []Message, tools []ToolDefinition, stream bool) goai.ChatCompletionRequest {
	req := goai.ChatCompletionRequest{Model: o.model, Stream: stream}

	for _, m := range messages {
		msg := goai.ChatCompletionMessage{
			Role:       string(m.Role),
			Content:    m.Content,
			ReasoningContent: m.Reasoning,
			ToolCallID: m.ToolCallID,
		}
		for _, tc := range m.ToolCalls {
			msg.ToolCalls = append(msg.ToolCalls, goai.ToolCall{
				ID:   tc.ID,
				Type: goai.ToolTypeFunction,
				Function: goai.FunctionCall{
					Name:      tc.Name,
					Arguments: tc.Arguments,
				},
			})
		}
		req.Messages = append(req.Messages, msg)
	}

	for _, t := range tools {
		req.Tools = append(req.Tools, goai.Tool{
			Type: goai.ToolTypeFunction,
			Function: &goai.FunctionDefinition{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  t.Parameters,
			},
		})
	}
	return req
}

// mapError 将 goai 的错误映射到 Provider 错误类型。
func (o *OpenAI) mapError(err error) error {
	if err == nil {
		return nil
	}
	var apiErr *goai.APIError
	if errors.As(err, &apiErr) {
		switch {
		case apiErr.HTTPStatusCode == 429 || apiErr.HTTPStatusCode == 529:
			return NewFailoverError("rate_limit", err)
		case apiErr.HTTPStatusCode >= 500:
			return NewFailoverError("server_error", err)
		case apiErr.HTTPStatusCode == 401 || apiErr.HTTPStatusCode == 403:
			return NewFailoverError("auth_error", err)
		}
	}
	return err
}

// backoff 计算指数退避时间（含 jitter）。
func (o *OpenAI) backoff(attempt int) time.Duration {
	d := o.retryBase
	for i := 1; i < attempt; i++ {
		d *= 2
	}
	if d > o.retryMax {
		d = o.retryMax
	}
	return d + time.Duration(float64(d)*0.2*rand.Float64())
}
