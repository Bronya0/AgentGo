// Package provider 定义 LLM 提供者的核心接口与消息类型。
package provider

import (
	"context"
	"errors"
)

// Role 表示消息角色。
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// Message 是 LLM 对话中的一条消息。
type Message struct {
	Role         Role          `json:"role"`
	Content      string        `json:"content,omitempty"`
	ContentParts []ContentPart `json:"content_parts,omitempty"` // 多模态内容（图片+文本）
	Reasoning    string        `json:"reasoning,omitempty"`
	ToolCalls    []ToolCall    `json:"tool_calls,omitempty"`
	ToolCallID   string        `json:"tool_call_id,omitempty"` // RoleTool 时的关联 ID
}

// ContentPart 是多模态消息中的一个内容片段。
type ContentPart struct {
	Type     string `json:"type"`               // "text" 或 "image_url"
	Text     string `json:"text,omitempty"`      // type=text 时
	ImageURL string `json:"image_url,omitempty"` // type=image_url 时，base64 data URI 或 URL
}

// ToolCall 表示 LLM 请求的一次工具调用。
type ToolCall struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Arguments string `json:"arguments"` // JSON string
}

// ToolDefinition 描述一个可用工具，对应 OpenAI function calling 格式。
type ToolDefinition struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Parameters  any    `json:"parameters"` // JSON Schema
}

// StreamDelta 是流式输出的一个片段。
type StreamDelta struct {
	Text     string    // 文本增量
	Reasoning string   // 思考增量（若 provider 支持）
	ToolCall *ToolCall // 工具调用增量（ID 可能跨多个 delta 组装）
	Done     bool      // 流结束标记
}

// StreamHandler 接收流式输出。
type StreamHandler func(delta StreamDelta)

// Provider 是 LLM 提供者的核心接口。
type Provider interface {
	// ID 返回此 provider 的唯一标识符。
	ID() string
	// Model 返回当前使用的模型名称。
	Model() string
	// Chat 发起一次对话请求。
	// messages: 完整对话历史；tools: 可用工具列表；handler: 流式回调（可为 nil）。
	// 返回完整的 assistant 消息。
	Chat(ctx context.Context, messages []Message, tools []ToolDefinition, handler StreamHandler) (*Message, error)
}

// --- 错误类型 ---

// ErrFailover 表示应切换到下一个 provider（如 rate limit、过载等）。
var ErrFailover = errors.New("failover")

// FailoverError 包装了一个需要 failover 的底层错误。
type FailoverError struct {
	Reason string
	Cause  error
}

func (e *FailoverError) Error() string {
	if e.Cause != nil {
		return e.Reason + ": " + e.Cause.Error()
	}
	return e.Reason
}

// Unwrap 返回错误链，同时支持 errors.Is(err, ErrFailover) 和 errors.Is(err, cause)。
func (e *FailoverError) Unwrap() []error {
	if e.Cause != nil {
		return []error{ErrFailover, e.Cause}
	}
	return []error{ErrFailover}
}

// IsFailover 判断错误是否应触发 failover。
func IsFailover(err error) bool {
	return errors.Is(err, ErrFailover)
}

// NewFailoverError 创建一个 failover 错误。
func NewFailoverError(reason string, cause error) *FailoverError {
	return &FailoverError{Reason: reason, Cause: cause}
}
