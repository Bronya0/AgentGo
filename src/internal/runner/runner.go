// Package runner 实现 Agent 核心运行循环。
//
// 循环流程：
//  1. 将用户消息追加到 session
//  2. 构建 system prompt（含 skill 注入）
//  3. 调用 LLM（provider.Chat）
//  4. 若 LLM 返回 tool_calls → 执行工具 → 追加 tool result → 回到 3
//  5. 若 LLM 仅返回文本 → 结束
package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/bronya/mini-agent/internal/plugin"
	"github.com/bronya/mini-agent/internal/provider"
	"github.com/bronya/mini-agent/internal/session"
	"github.com/bronya/mini-agent/internal/tool"
)

// StreamEvent 类型标记流式事件类别。
type StreamEvent int

const (
	EventText      StreamEvent = iota // 文本增量
	EventToolStart                    // 工具调用开始
	EventToolEnd                      // 工具调用结束
	EventDone                         // 完成
	EventError                        // 错误
)

// StreamChunk 是向调用者推送的事件。
type StreamChunk struct {
	Event    StreamEvent
	Text     string
	ToolName string
	ToolArgs string
	ToolOut  string
	Err      error
}

// Runner 驱动 Agent 的核心循环。
type Runner struct {
	provider     provider.Provider
	tools        *tool.Registry
	hooks        *plugin.Hooks
	systemPrompt string
	maxTurns     int // 最大循环次数（防无限循环），默认 32
	maxTokens    int // 上下文 token 上限（超出时截断旧消息）
}

// Config 是 Runner 的配置。
type Config struct {
	Provider     provider.Provider
	Tools        *tool.Registry
	Hooks        *plugin.Hooks
	SystemPrompt string
	MaxTurns     int
	MaxTokens    int
}

// New 创建一个 Runner。
func New(cfg Config) *Runner {
	if cfg.MaxTurns <= 0 {
		cfg.MaxTurns = 32
	}
	if cfg.MaxTokens <= 0 {
		cfg.MaxTokens = 100_000
	}
	if cfg.Hooks == nil {
		cfg.Hooks = plugin.NewHooks()
	}
	return &Runner{
		provider:     cfg.Provider,
		tools:        cfg.Tools,
		hooks:        cfg.Hooks,
		systemPrompt: cfg.SystemPrompt,
		maxTurns:     cfg.MaxTurns,
		maxTokens:    cfg.MaxTokens,
	}
}

// Run 执行一次完整的 agent 对话，从用户消息到最终回复。
// handler 接收流式事件（可为 nil 表示不需要流式）。
func (r *Runner) Run(ctx context.Context, sess *session.Session, userMessage string, handler func(StreamChunk)) error {
	if handler == nil {
		handler = func(StreamChunk) {}
	}

	// Hook: on_message（预处理用户消息）
	userMessage = r.hooks.ProcessMessage(ctx, userMessage)

	// 追加用户消息
	sess.Append(provider.Message{Role: provider.RoleUser, Content: userMessage})

	// 构建工具定义列表
	toolDefs := r.buildToolDefs()

	// 工具调用循环检测：记录最近调用模式
	loopDetector := newLoopDetector(5) // 同一模式重复 5 次视为循环

	for turn := 0; turn < r.maxTurns; turn++ {
		// 构建消息列表（含 system prompt）
		messages := r.buildMessages(sess)

		// Hook: before_llm_call
		messages = r.hooks.BeforeLLMCall(ctx, messages)

		// 调用 LLM
		var streamHandler provider.StreamHandler
		streamHandler = func(delta provider.StreamDelta) {
			if delta.Text != "" {
				handler(StreamChunk{Event: EventText, Text: delta.Text})
			}
		}

		assistantMsg, err := r.provider.Chat(ctx, messages, toolDefs, streamHandler)
		if err != nil {
			handler(StreamChunk{Event: EventError, Err: err})
			return fmt.Errorf("llm call (turn %d): %w", turn, err)
		}

		// 追加 assistant 消息到会话
		sess.Append(*assistantMsg)

		// Hook: after_llm_call
		r.hooks.AfterLLMCall(ctx, assistantMsg)

		// 若没有 tool calls，对话结束
		if len(assistantMsg.ToolCalls) == 0 {
			handler(StreamChunk{Event: EventDone})
			return nil
		}

		// 循环检测
		if loopDetector.check(assistantMsg.ToolCalls) {
			errMsg := "tool call loop detected, stopping agent"
			slog.Warn(errMsg)
			handler(StreamChunk{Event: EventError, Err: fmt.Errorf(errMsg)})
			return fmt.Errorf(errMsg)
		}

		// 执行每个 tool call
		for _, tc := range assistantMsg.ToolCalls {
			result := r.executeTool(ctx, tc, handler)
			sess.Append(provider.Message{
				Role:       provider.RoleTool,
				ToolCallID: tc.ID,
				Content:    result.Content,
			})
		}
	}

	handler(StreamChunk{Event: EventError, Err: fmt.Errorf("max turns (%d) exceeded", r.maxTurns)})
	return fmt.Errorf("agent loop exceeded max turns (%d)", r.maxTurns)
}

// executeTool 执行单个工具调用（含 panic 恢复）。
func (r *Runner) executeTool(ctx context.Context, tc provider.ToolCall, handler func(StreamChunk)) (result tool.Result) {
	handler(StreamChunk{Event: EventToolStart, ToolName: tc.Name, ToolArgs: tc.Arguments})

	// panic 恢复
	defer func() {
		if rv := recover(); rv != nil {
			result = tool.Errf("tool %q panicked: %v", tc.Name, rv)
			slog.Error("tool panic recovered", "tool", tc.Name, "panic", rv)
		}
		handler(StreamChunk{Event: EventToolEnd, ToolName: tc.Name, ToolOut: result.Content})
	}()

	t, ok := r.tools.Get(tc.Name)
	if !ok {
		result = tool.Errf("unknown tool: %s", tc.Name)
		return result
	}

	// 解析参数
	var args tool.Args
	if tc.Arguments != "" {
		if err := json.Unmarshal([]byte(tc.Arguments), &args); err != nil {
			result = tool.Errf("invalid tool arguments: %v", err)
			return result
		}
	}
	if args == nil {
		args = make(tool.Args)
	}

	// Hook: before_tool_call
	args = r.hooks.BeforeToolCall(ctx, tc.Name, args)

	// 执行工具（带超时保护）
	toolCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	result = t.Execute(toolCtx, args)

	// Hook: after_tool_call
	r.hooks.AfterToolCall(ctx, tc.Name, result)

	// 防止 tool result 过大
	const maxResultChars = 32 * 1024
	if len(result.Content) > maxResultChars {
		result.Content = result.Content[:maxResultChars] + "\n...[truncated]"
	}

	slog.Debug("tool executed", "tool", tc.Name, "error", result.IsError)
	return result
}

// buildToolDefs 将注册表中的工具转换为 provider.ToolDefinition。
func (r *Runner) buildToolDefs() []provider.ToolDefinition {
	tools := r.tools.List()
	defs := make([]provider.ToolDefinition, 0, len(tools))
	for _, t := range tools {
		defs = append(defs, provider.ToolDefinition{
			Name:        t.Name,
			Description: t.Description,
			Parameters:  t.Parameters,
		})
	}
	return defs
}

// buildMessages 构建发送给 LLM 的消息列表，含 system prompt 和上下文截断。
func (r *Runner) buildMessages(sess *session.Session) []provider.Message {
	history := sess.History()

	var messages []provider.Message

	// system prompt
	if r.systemPrompt != "" {
		messages = append(messages, provider.Message{
			Role:    provider.RoleSystem,
			Content: r.systemPrompt,
		})
	}

	// 上下文截断：若估算 token 超限，保留最近的消息
	messages = append(messages, r.trimHistory(history)...)
	return messages
}

// trimHistory 在 token 预算内保留尽可能多的最近消息。
func (r *Runner) trimHistory(history []provider.Message) []provider.Message {
	if len(history) == 0 {
		return history
	}

	// 计算每条消息的 char 数
	msgChars := make([]int, len(history))
	totalChars := 0
	for i, m := range history {
		c := len(m.Content)
		for _, tc := range m.ToolCalls {
			c += len(tc.Arguments)
		}
		msgChars[i] = c
		totalChars += c
	}

	// 估算 token（chars/4 启发式）
	if totalChars/4 <= r.maxTokens {
		return history
	}

	// 从后往前累积，超出预算时停止
	budget := r.maxTokens * 4 // 转回 chars
	used := 0
	start := len(history) // 保留的起始 index

	for i := len(history) - 1; i >= 0; i-- {
		if used+msgChars[i] > budget && start < len(history) {
			break
		}
		start = i
		used += msgChars[i]
	}

	kept := make([]provider.Message, 0, 1+len(history)-start)

	// 在截断消息前加一条提示
	if start > 0 {
		kept = append(kept, provider.Message{
			Role:    provider.RoleSystem,
			Content: fmt.Sprintf("[Context truncated: showing last %d of %d messages]", len(history)-start, len(history)),
		})
	}

	kept = append(kept, history[start:]...)
	return kept
}

// BuildSystemPrompt 组装系统提示词。
// 类似 OpenClaw 的分区块构建方式。
func BuildSystemPrompt(base string, skillsSection string, extraSections ...string) string {
	var sb strings.Builder
	sb.WriteString(base)

	if skillsSection != "" {
		sb.WriteString("\n\n## Available Skills\n\n")
		sb.WriteString(skillsSection)
	}

	for _, section := range extraSections {
		if section != "" {
			sb.WriteString("\n\n")
			sb.WriteString(section)
		}
	}

	return sb.String()
}

// --- 循环检测 ---

// loopDetector 检测 agent 是否陷入重复的工具调用循环。
type loopDetector struct {
	threshold int
	recent    []string // 最近的调用签名
}

func newLoopDetector(threshold int) *loopDetector {
	return &loopDetector{threshold: threshold}
}

// check 检查当前 tool calls 是否构成循环。
func (ld *loopDetector) check(calls []provider.ToolCall) bool {
	// 生成本轮签名
	sig := toolCallSignature(calls)
	ld.recent = append(ld.recent, sig)

	// 保留最近 threshold*2 条
	if len(ld.recent) > ld.threshold*2 {
		ld.recent = ld.recent[len(ld.recent)-ld.threshold*2:]
	}

	// 检查最近 threshold 次是否都相同
	if len(ld.recent) < ld.threshold {
		return false
	}
	tail := ld.recent[len(ld.recent)-ld.threshold:]
	for _, s := range tail {
		if s != sig {
			return false
		}
	}
	return true
}

func toolCallSignature(calls []provider.ToolCall) string {
	parts := make([]string, len(calls))
	for i, c := range calls {
		parts[i] = c.Name + ":" + c.Arguments
	}
	return strings.Join(parts, "|")
}
