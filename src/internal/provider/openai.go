package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"sort"
	"strings"
	"time"
)

// OpenAI 实现了 OpenAI 兼容的 API 调用（同样适用于其他兼容服务）。
type OpenAI struct {
	id      string
	baseURL string
	apiKey  string
	model   string
	client  *http.Client

	// 重试配置
	maxRetries int
	retryBase  time.Duration
	retryMax   time.Duration
}

// NewOpenAI 创建一个 OpenAI 兼容 provider。
func NewOpenAI(id, baseURL, apiKey, model string, timeout time.Duration) *OpenAI {
	if baseURL == "" {
		baseURL = "https://api.openai.com/v1"
	}
	baseURL = strings.TrimRight(baseURL, "/")
	return &OpenAI{
		id:         id,
		baseURL:    baseURL,
		apiKey:     apiKey,
		model:      model,
		client:     &http.Client{Timeout: timeout},
		maxRetries: 3,
		retryBase:  500 * time.Millisecond,
		retryMax:   5 * time.Second,
	}
}

func (o *OpenAI) ID() string    { return o.id }
func (o *OpenAI) Model() string { return o.model }

// Chat 调用 chat completions 端点，支持流式输出，含重试和指数退避。
func (o *OpenAI) Chat(
	ctx context.Context,
	messages []Message,
	tools []ToolDefinition,
	handler StreamHandler,
) (*Message, error) {
	stream := handler != nil
	body := o.buildRequest(messages, tools, stream)

	jsonBody, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

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

		msg, err := o.doRequest(ctx, jsonBody, stream, handler)
		if err == nil {
			return msg, nil
		}

		// 可重试的错误：rate limit、server error
		if IsFailover(err) {
			lastErr = err
			continue
		}
		// 不可重试的错误
		return nil, err
	}

	// 所有重试耗尽，返回 failover 错误以触发 provider 切换
	return nil, lastErr
}

// doRequest 执行一次 HTTP 请求。
func (o *OpenAI) doRequest(
	ctx context.Context,
	jsonBody []byte,
	stream bool,
	handler StreamHandler,
) (*Message, error) {
	req, err := http.NewRequestWithContext(ctx, "POST", o.baseURL+"/chat/completions", bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if o.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+o.apiKey)
	}

	resp, err := o.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == 429 || resp.StatusCode == 529 {
		return nil, NewFailoverError("rate_limit", fmt.Errorf("status %d", resp.StatusCode))
	}
	if resp.StatusCode >= 500 {
		return nil, NewFailoverError("server_error", fmt.Errorf("status %d", resp.StatusCode))
	}
	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		return nil, NewFailoverError("auth_error", fmt.Errorf("status %d", resp.StatusCode))
	}
	if resp.StatusCode != 200 {
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("API error %d: %s", resp.StatusCode, string(errBody))
	}

	if stream {
		return o.readStream(resp.Body, handler)
	}
	return o.readFull(resp.Body)
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
	// 添加 20% jitter
	jitter := time.Duration(float64(d) * 0.2 * rand.Float64())
	return d + jitter
}

// buildRequest 构建 OpenAI API 请求体。
func (o *OpenAI) buildRequest(messages []Message, tools []ToolDefinition, stream bool) map[string]any {
	body := map[string]any{
		"model":  o.model,
		"stream": stream,
	}

	// 转换消息格式
	msgs := make([]map[string]any, 0, len(messages))
	for _, m := range messages {
		msg := map[string]any{"role": string(m.Role), "content": m.Content}
		if m.Role == RoleTool && m.ToolCallID != "" {
			msg["tool_call_id"] = m.ToolCallID
		}
		if len(m.ToolCalls) > 0 {
			tcs := make([]map[string]any, 0, len(m.ToolCalls))
			for _, tc := range m.ToolCalls {
				tcs = append(tcs, map[string]any{
					"id":   tc.ID,
					"type": "function",
					"function": map[string]any{
						"name":      tc.Name,
						"arguments": tc.Arguments,
					},
				})
			}
			msg["tool_calls"] = tcs
		}
		msgs = append(msgs, msg)
	}
	body["messages"] = msgs

	// 工具定义
	if len(tools) > 0 {
		toolList := make([]map[string]any, 0, len(tools))
		for _, t := range tools {
			toolList = append(toolList, map[string]any{
				"type": "function",
				"function": map[string]any{
					"name":        t.Name,
					"description": t.Description,
					"parameters":  t.Parameters,
				},
			})
		}
		body["tools"] = toolList
	}

	return body
}

// readFull 解析非流式响应。
func (o *OpenAI) readFull(body io.Reader) (*Message, error) {
	var resp struct {
		Choices []struct {
			Message struct {
				Role      string `json:"role"`
				Content   string `json:"content"`
				ToolCalls []struct {
					ID       string `json:"id"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
		Usage *struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
			TotalTokens      int `json:"total_tokens"`
		} `json:"usage"`
	}
	if err := json.NewDecoder(body).Decode(&resp); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	if len(resp.Choices) == 0 {
		return nil, fmt.Errorf("no choices in response")
	}
	if resp.Usage != nil {
		slog.Debug("token usage", "provider", o.id, "prompt", resp.Usage.PromptTokens,
			"completion", resp.Usage.CompletionTokens, "total", resp.Usage.TotalTokens)
	}
	choice := resp.Choices[0]
	msg := &Message{
		Role:    RoleAssistant,
		Content: choice.Message.Content,
	}
	for _, tc := range choice.Message.ToolCalls {
		msg.ToolCalls = append(msg.ToolCalls, ToolCall{
			ID:        tc.ID,
			Name:      tc.Function.Name,
			Arguments: tc.Function.Arguments,
		})
	}
	return msg, nil
}

// readStream 解析 SSE 流式响应并组装完整消息。
func (o *OpenAI) readStream(body io.Reader, handler StreamHandler) (*Message, error) {
	scanner := bufio.NewScanner(body)
	// 增大扫描缓冲区，防止长行截断
	scanner.Buffer(make([]byte, 0, 64*1024), 512*1024)
	msg := &Message{Role: RoleAssistant}

	// 用于组装分段到达的 tool_calls
	type partialTC struct {
		ID   string
		Name string
		Args strings.Builder
	}
	toolCalls := make(map[int]*partialTC)
	maxIndex := -1

	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			handler(StreamDelta{Done: true})
			break
		}

		var chunk struct {
			Choices []struct {
				Delta struct {
					Content   string `json:"content"`
					ToolCalls []struct {
						Index    int    `json:"index"`
						ID       string `json:"id"`
						Function struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			slog.Debug("stream: malformed SSE chunk", "err", err)
			continue
		}
		if len(chunk.Choices) == 0 {
			continue
		}
		delta := chunk.Choices[0].Delta

		// 文本增量
		if delta.Content != "" {
			msg.Content += delta.Content
			handler(StreamDelta{Text: delta.Content})
		}

		// 工具调用增量
		for _, tc := range delta.ToolCalls {
			ptc, ok := toolCalls[tc.Index]
			if !ok {
				ptc = &partialTC{}
				toolCalls[tc.Index] = ptc
			}
			if tc.Index > maxIndex {
				maxIndex = tc.Index
			}
			if tc.ID != "" {
				ptc.ID = tc.ID
			}
			if tc.Function.Name != "" {
				ptc.Name = tc.Function.Name
			}
			ptc.Args.WriteString(tc.Function.Arguments)
		}
	}

	// 组装完整的 tool calls — 按实际存在的 index 排序，避免 index 间隙导致丢失
	if len(toolCalls) > 0 {
		indices := make([]int, 0, len(toolCalls))
		for idx := range toolCalls {
			indices = append(indices, idx)
		}
		sort.Ints(indices)
		for _, idx := range indices {
			ptc := toolCalls[idx]
			msg.ToolCalls = append(msg.ToolCalls, ToolCall{
				ID:        ptc.ID,
				Name:      ptc.Name,
				Arguments: ptc.Args.String(),
			})
		}
	}

	return msg, scanner.Err()
}
