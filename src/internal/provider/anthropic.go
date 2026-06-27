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
	"strings"
	"time"
)

// Anthropic 实现 Anthropic Messages API 的 Provider 接口。
type Anthropic struct {
	id         string
	model      string
	baseURL    string
	apiKey     string
	client     *http.Client
	maxRetries int
	retryBase  time.Duration
	retryMax   time.Duration
	maxTokens  int
}

// NewAnthropic 创建一个 Anthropic provider。
func NewAnthropic(id, baseURL, apiKey, model string, timeout time.Duration) *Anthropic {
	if baseURL == "" {
		baseURL = "https://api.anthropic.com"
	}
	return &Anthropic{
		id:         id,
		model:      model,
		baseURL:    strings.TrimRight(baseURL, "/"),
		apiKey:     apiKey,
		client:     &http.Client{Timeout: timeout},
		maxRetries: 3,
		retryBase:  500 * time.Millisecond,
		retryMax:   5 * time.Second,
		maxTokens:  8192,
	}
}

func (a *Anthropic) ID() string    { return a.id }
func (a *Anthropic) Model() string { return a.model }

// --- Anthropic API request/response types ---

type anthropicRequest struct {
	Model     string             `json:"model"`
	Messages  []anthropicMessage `json:"messages"`
	MaxTokens int                `json:"max_tokens"`
	System    string             `json:"system,omitempty"`
	Stream    bool               `json:"stream"`
	Tools     []anthropicTool    `json:"tools,omitempty"`
}

// anthropicMessage 的 Content 可以是 string 或 []anthropicContentBlock。
// Anthropic API 要求 assistant 的 tool_use 和 user 的 tool_result 使用数组格式。
type anthropicMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

type anthropicTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"input_schema"`
}

type anthropicResponse struct {
	ID         string `json:"id"`
	Type       string `json:"type"`
	Role       string `json:"role"`
	Content    []anthropicContentBlock `json:"content"`
	StopReason string `json:"stop_reason"`
	Usage      struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

type anthropicContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
	// tool_use
	ID    string         `json:"id,omitempty"`
	Name  string         `json:"name,omitempty"`
	Input map[string]any `json:"input,omitempty"`
	// tool_result
	ToolUseID string `json:"tool_use_id,omitempty"`
	Content   string `json:"content,omitempty"` // tool_result 的文本内容
	IsError   bool   `json:"is_error,omitempty"`
	// image
	Source *anthropicImageSource `json:"source,omitempty"`
}

// anthropicImageSource 用于 image content block。
type anthropicImageSource struct {
	Type      string `json:"type"`       // "base64"
	MediaType string `json:"media_type"` // "image/png", "image/jpeg"
	Data      string `json:"data"`       // base64 编码数据
}

// --- Streaming types ---

type anthropicStreamEvent struct {
	Type  string `json:"type"`
	Index int    `json:"index,omitempty"`
	Delta *struct {
		Type string `json:"type"`
		Text string `json:"text,omitempty"`
		// tool_use
		ID    string `json:"id,omitempty"`
		Name  string `json:"name,omitempty"`
		Input string `json:"partial_json,omitempty"`
	} `json:"delta,omitempty"`
	Message struct {
		Usage struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage,omitempty"`
	} `json:"message,omitempty"`
	Usage struct {
		OutputTokens int `json:"output_tokens"`
	} `json:"usage,omitempty"`
}

// Chat 发起对话请求。
func (a *Anthropic) Chat(
	ctx context.Context,
	messages []Message,
	tools []ToolDefinition,
	handler StreamHandler,
) (*Message, error) {
	req := a.buildRequest(messages, tools, handler != nil)

	var lastErr error
	for attempt := 0; attempt <= a.maxRetries; attempt++ {
		if attempt > 0 {
			delay := a.backoff(attempt)
			slog.Debug("retrying API call", "provider", a.id, "attempt", attempt, "delay", delay)
			select {
			case <-time.After(delay):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}

		var msg *Message
		var err error
		if handler != nil {
			msg, err = a.doStream(ctx, req, handler)
		} else {
			msg, err = a.doFull(ctx, req)
		}
		if err == nil {
			if msg.Usage.TotalTokens == 0 {
				msg.Usage.TotalTokens = msg.Usage.PromptTokens + msg.Usage.CompletionTokens
			}
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
func (a *Anthropic) doFull(ctx context.Context, req anthropicRequest) (*Message, error) {
	req.Stream = false
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", a.baseURL+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", a.apiKey)
	httpReq.Header.Set("anthropic-version", "2023-06-01")

	resp, err := a.client.Do(httpReq)
	if err != nil {
		return nil, a.mapError(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, a.mapHTTPError(resp)
	}

	var result anthropicResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}

	slog.Debug("token usage", "provider", a.id,
		"prompt", result.Usage.InputTokens, "completion", result.Usage.OutputTokens)

	out := &Message{
		Role: RoleAssistant,
		Usage: Usage{
			PromptTokens:     result.Usage.InputTokens,
			CompletionTokens: result.Usage.OutputTokens,
			TotalTokens:      result.Usage.InputTokens + result.Usage.OutputTokens,
		},
	}

	for _, block := range result.Content {
		switch block.Type {
		case "text":
			out.Content += block.Text
		case "tool_use":
			inputJSON, _ := json.Marshal(block.Input)
			out.ToolCalls = append(out.ToolCalls, ToolCall{
				ID:        block.ID,
				Name:      block.Name,
				Arguments: string(inputJSON),
			})
		}
	}
	return out, nil
}

// doStream 执行流式请求。
func (a *Anthropic) doStream(ctx context.Context, req anthropicRequest, handler StreamHandler) (*Message, error) {
	req.Stream = true
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", a.baseURL+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", a.apiKey)
	httpReq.Header.Set("anthropic-version", "2023-06-01")

	resp, err := a.client.Do(httpReq)
	if err != nil {
		return nil, a.mapError(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, a.mapHTTPError(resp)
	}

	out := &Message{Role: RoleAssistant}

	// 工具调用按 index 暂存
	type partialTC struct {
		id   string
		name string
		args strings.Builder
	}
	tcMap := make(map[int]*partialTC)
	maxIdx := -1

	scanner := bufio.NewScanner(resp.Body)
	// 增大 buffer 以防 SSE 行过长
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			break
		}

		var event anthropicStreamEvent
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			continue
		}

		switch event.Type {
		case "content_block_delta":
			if event.Delta == nil {
				continue
			}
			switch event.Delta.Type {
			case "text_delta":
				out.Content += event.Delta.Text
				handler(StreamDelta{Text: event.Delta.Text})
			case "input_json_delta":
				p, ok := tcMap[event.Index]
				if !ok {
					p = &partialTC{}
					tcMap[event.Index] = p
				}
				p.args.WriteString(event.Delta.Input)
				if event.Index > maxIdx {
					maxIdx = event.Index
				}
			}
		case "content_block_start":
			if event.Delta != nil && event.Delta.Type == "tool_use" {
				p, ok := tcMap[event.Index]
				if !ok {
					p = &partialTC{}
					tcMap[event.Index] = p
				}
				p.id = event.Delta.ID
				p.name = event.Delta.Name
				if event.Index > maxIdx {
					maxIdx = event.Index
				}
			}
		case "message_delta":
			if event.Usage.OutputTokens > 0 {
				out.Usage.CompletionTokens = event.Usage.OutputTokens
			}
		case "message_start":
			if event.Message.Usage.InputTokens > 0 {
				out.Usage.PromptTokens = event.Message.Usage.InputTokens
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

	handler(StreamDelta{Done: true})
	return out, nil
}

// buildRequest 构建 Anthropic API 请求。
// 正确转换内部 Message 格式到 Anthropic 的 content block 格式：
//   - assistant 的 tool_calls → content 数组中的 tool_use 块
//   - tool result → user 消息中的 tool_result 块
func (a *Anthropic) buildRequest(messages []Message, tools []ToolDefinition, stream bool) anthropicRequest {
	req := anthropicRequest{
		Model:     a.model,
		MaxTokens: a.maxTokens,
		Stream:    stream,
	}

	// Anthropic 使用顶层 system 字段
	var systemParts []string
	var apiMessages []anthropicMessage

	for _, m := range messages {
		switch m.Role {
		case RoleSystem:
			systemParts = append(systemParts, m.Content)
		case RoleUser:
			// 多模态 user 消息（含图片）
			if len(m.ContentParts) > 0 {
				blocks := make([]anthropicContentBlock, 0, len(m.ContentParts))
				for _, p := range m.ContentParts {
					switch p.Type {
					case "image_url":
						blocks = append(blocks, anthropicContentBlock{
							Type:    "image",
							Source:  &anthropicImageSource{Type: "base64", MediaType: detectMediaType(p.ImageURL), Data: extractBase64Data(p.ImageURL)},
						})
					default:
						blocks = append(blocks, anthropicContentBlock{Type: "text", Text: p.Text})
					}
				}
				apiMessages = append(apiMessages, anthropicMessage{Role: "user", Content: blocks})
			} else {
				apiMessages = append(apiMessages, anthropicMessage{Role: "user", Content: m.Content})
			}
		case RoleAssistant:
			// assistant 消息可能同时包含文本和 tool_calls
			if len(m.ToolCalls) > 0 {
				blocks := make([]anthropicContentBlock, 0, len(m.ToolCalls)+1)
				if m.Content != "" {
					blocks = append(blocks, anthropicContentBlock{Type: "text", Text: m.Content})
				}
				for _, tc := range m.ToolCalls {
					var input map[string]any
					if tc.Arguments != "" {
						_ = json.Unmarshal([]byte(tc.Arguments), &input)
					}
					blocks = append(blocks, anthropicContentBlock{
						Type:  "tool_use",
						ID:    tc.ID,
						Name:  tc.Name,
						Input: input,
					})
				}
				apiMessages = append(apiMessages, anthropicMessage{Role: "assistant", Content: blocks})
			} else {
				apiMessages = append(apiMessages, anthropicMessage{Role: "assistant", Content: m.Content})
			}
		case RoleTool:
			// Anthropic 用 user message 携带 tool_result content block
			apiMessages = append(apiMessages, anthropicMessage{
				Role: "user",
				Content: []anthropicContentBlock{{
					Type:        "tool_result",
					ToolUseID:   m.ToolCallID,
					Content:     m.Content,
					IsError:     false,
				}},
			})
		}
	}

	req.Messages = apiMessages
	req.System = strings.Join(systemParts, "\n\n")

	for _, t := range tools {
		schema, ok := t.Parameters.(map[string]any)
		if !ok {
			schema = map[string]any{
				"type":       "object",
				"properties": t.Parameters,
			}
		}
		req.Tools = append(req.Tools, anthropicTool{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: schema,
		})
	}
	return req
}

// mapError 将网络错误映射到 Provider 错误类型。
func (a *Anthropic) mapError(err error) error {
	if err == nil {
		return nil
	}
	if strings.Contains(err.Error(), "429") || strings.Contains(err.Error(), "rate") {
		return NewFailoverError("rate_limit", err)
	}
	if strings.Contains(err.Error(), "500") || strings.Contains(err.Error(), "502") || strings.Contains(err.Error(), "503") {
		return NewFailoverError("server_error", err)
	}
	return err
}

// mapHTTPError 将 HTTP 响应错误映射到 Provider 错误类型。
func (a *Anthropic) mapHTTPError(resp *http.Response) error {
	body, _ := io.ReadAll(resp.Body)
	err := fmt.Errorf("anthropic API error %d: %s", resp.StatusCode, string(body))

	switch resp.StatusCode {
	case 429:
		return NewFailoverError("rate_limit", err)
	case 401, 403:
		return NewFailoverError("auth_error", err)
	default:
		if resp.StatusCode >= 500 {
			return NewFailoverError("server_error", err)
		}
		return err
	}
}

// backoff 计算指数退避时间（含 jitter）。
func (a *Anthropic) backoff(attempt int) time.Duration {
	d := a.retryBase
	for i := 1; i < attempt; i++ {
		d *= 2
	}
	if d > a.retryMax {
		d = a.retryMax
	}
	return d + time.Duration(float64(d)*0.2*rand.Float64())
}

// detectMediaType 从 data URI 中提取媒体类型（如 "image/png"）。
func detectMediaType(dataURI string) string {
	// data:image/png;base64,...
	if strings.HasPrefix(dataURI, "data:") {
		rest := dataURI[5:]
		if idx := strings.Index(rest, ";"); idx > 0 {
			return rest[:idx]
		}
		if idx := strings.Index(rest, ","); idx > 0 {
			return rest[:idx]
		}
	}
	return "image/png"
}

// extractBase64Data 从 data URI 中提取 base64 数据部分。
func extractBase64Data(dataURI string) string {
	if strings.HasPrefix(dataURI, "data:") {
		if idx := strings.Index(dataURI, ","); idx >= 0 {
			return dataURI[idx+1:]
		}
	}
	return dataURI
}
