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
	"sync"
	"time"

	"github.com/bronya/mini-agent/internal/acl"
	"github.com/bronya/mini-agent/internal/plugin"
	"github.com/bronya/mini-agent/internal/provider"
	"github.com/bronya/mini-agent/internal/session"
	"github.com/bronya/mini-agent/internal/tool"
)

// StreamEvent 类型标记流式事件类别。
type StreamEvent int

const (
	EventText      StreamEvent = iota // 文本增量
	EventReasoning                    // 思考增量
	EventToolStart                    // 工具调用开始
	EventToolEnd                      // 工具调用结束
	EventDone                         // 完成
	EventError                        // 错误
)

// StreamChunk 是向调用者推送的事件。
type StreamChunk struct {
	Event    StreamEvent
	Text     string
	Reasoning string
	ToolID   string
	ToolName string
	ToolArgs string
	ToolOut  string
	Err      error
}

// ExecApprovalFn 是命令审批回调。返回 true 允许执行，false 拒绝。
// 当为 nil 时，所有非危险命令自动通过（危险命令仍被 checkCommandSafety 拦截）。
type ExecApprovalFn func(ctx context.Context, toolName string, args tool.Args) (bool, error)

// Runner 驱动 Agent 的核心循环。
type Runner struct {
	provider     provider.Provider
	tools        *tool.Registry
	hooks        *plugin.Hooks
	acl          *acl.Service // 可选，nil 表示不做工具级权限检查
	systemPrompt string
	maxTurns     int // 最大循环次数（防无限循环），默认 32
	maxTokens    int // 上下文 token 上限（超出时截断旧消息）
	execApproval ExecApprovalFn // 可选，命令审批回调
}

// Config 是 Runner 的配置。
type Config struct {
	Provider     provider.Provider
	Tools        *tool.Registry
	Hooks        *plugin.Hooks
	ACL          *acl.Service // 可选
	SystemPrompt string
	MaxTurns     int
	MaxTokens    int
	ExecApproval ExecApprovalFn // 可选，命令执行审批回调
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
		acl:          cfg.ACL,
		systemPrompt: cfg.SystemPrompt,
		maxTurns:     cfg.MaxTurns,
		maxTokens:    cfg.MaxTokens,
		execApproval: cfg.ExecApproval,
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
			if delta.Reasoning != "" {
				handler(StreamChunk{Event: EventReasoning, Reasoning: delta.Reasoning})
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
			handler(StreamChunk{Event: EventError, Err: fmt.Errorf("%s", errMsg)})
			return fmt.Errorf("%s", errMsg)
		}

		// 执行 tool calls（多个时并行执行）
		if len(assistantMsg.ToolCalls) == 1 {
			tc := assistantMsg.ToolCalls[0]
			result := r.executeTool(ctx, tc, handler)
			sess.Append(provider.Message{
				Role:       provider.RoleTool,
				ToolCallID: tc.ID,
				Content:    result.Content,
			})
		} else {
			type indexedResult struct {
				tc     provider.ToolCall
				result tool.Result
			}
			results := make([]indexedResult, len(assistantMsg.ToolCalls))
			var wg sync.WaitGroup
			var mu sync.Mutex // 保护 handler 回调

			for i, tc := range assistantMsg.ToolCalls {
				wg.Add(1)
				go func(idx int, call provider.ToolCall) {
					defer wg.Done()
					safeHandler := func(chunk StreamChunk) {
						mu.Lock()
						handler(chunk)
						mu.Unlock()
					}
					results[idx] = indexedResult{tc: call, result: r.executeTool(ctx, call, safeHandler)}
				}(i, tc)
			}
			wg.Wait()

			// 按原始顺序写入 session
			for _, tr := range results {
				sess.Append(provider.Message{
					Role:       provider.RoleTool,
					ToolCallID: tr.tc.ID,
					Content:    tr.result.Content,
				})
			}
		}

		// 上下文压缩：在工具执行后检查是否需要压缩
		r.maybeCompress(ctx, sess)
	}

	handler(StreamChunk{Event: EventError, Err: fmt.Errorf("max turns (%d) exceeded", r.maxTurns)})
	return fmt.Errorf("agent loop exceeded max turns (%d)", r.maxTurns)
}

// executeTool 执行单个工具调用（含 panic 恢复）。
func (r *Runner) executeTool(ctx context.Context, tc provider.ToolCall, handler func(StreamChunk)) (result tool.Result) {
	handler(StreamChunk{Event: EventToolStart, ToolID: tc.ID, ToolName: tc.Name, ToolArgs: tc.Arguments})

	// panic 恢复
	defer func() {
		if rv := recover(); rv != nil {
			result = tool.Errf("tool %q panicked: %v", tc.Name, rv)
			slog.Error("tool panic recovered", "tool", tc.Name, "panic", rv)
		}
		handler(StreamChunk{Event: EventToolEnd, ToolID: tc.ID, ToolName: tc.Name, ToolOut: result.Content})
	}()

	t, ok := r.tools.Get(tc.Name)
	if !ok {
		result = tool.Errf("unknown tool: %s", tc.Name)
		return result
	}

	// ACL 工具级权限检查
	if r.acl != nil {
		if u, ok := acl.GetUser(ctx); ok {
			if !r.acl.CanUseTool(u.Platform, u.UserID, tc.Name) {
				result = tool.Errf("permission denied: you are not allowed to use tool %q", tc.Name)
				return result
			}
		}
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

	// Exec 审批：对 run_command 类工具，若配置了审批回调则请求确认
	if r.execApproval != nil && isExecTool(tc.Name) {
		approved, approvalErr := r.execApproval(ctx, tc.Name, args)
		if approvalErr != nil {
			result = tool.Errf("exec approval error: %v", approvalErr)
			return result
		}
		if !approved {
			result = tool.Errf("command execution was denied by approval policy")
			return result
		}
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
	if len(result.Content) > maxResultChars && !strings.HasPrefix(result.Content, "[IMAGE_VISION]") {
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

// buildMessages 构建发送给 LLM 的消息列表，含 system prompt、压缩摘要和上下文截断。
// 同时处理 [IMAGE_VISION] 标记，将图片 data URI 转为多模态消息。
func (r *Runner) buildMessages(sess *session.Session) []provider.Message {
	history := sess.History()
	summary := sess.GetSummary()

	var messages []provider.Message

	// system prompt
	if r.systemPrompt != "" {
		messages = append(messages, provider.Message{
			Role:    provider.RoleSystem,
			Content: r.systemPrompt,
		})
	}

	// 压缩摘要（来自更早的被压缩对话）
	if summary != "" {
		messages = append(messages, provider.Message{
			Role:    provider.RoleSystem,
			Content: "[Earlier conversation summary]\n" + summary,
		})
	}

	// 上下文截断 + 图片 vision 转换
	trimmed := r.trimHistory(history)
	for i, msg := range trimmed {
		if msg.Role == provider.RoleTool && strings.HasPrefix(msg.Content, "[IMAGE_VISION]") {
			question, dataURI := parseVisionMarker(msg.Content)
			// 将 tool result 替换为简短文本，然后在后续插入一条 user 消息携带图片
			trimmed[i].Content = fmt.Sprintf("[Image provided for analysis. Question: %s]", question)
			// 查找是否可以追加 user vision 消息
			if dataURI != "" {
				// 在当前消息序列末尾追加 vision 请求
				messages = append(messages, trimmed[:i+1]...)
				messages = append(messages, provider.Message{
					Role: provider.RoleUser,
					ContentParts: []provider.ContentPart{
						{Type: "text", Text: question},
						{Type: "image_url", ImageURL: dataURI},
					},
				})
				messages = append(messages, trimmed[i+1:]...)
				return messages
			}
		}
	}

	messages = append(messages, trimmed...)
	return messages
}

// parseVisionMarker 解析 [IMAGE_VISION] 标记。
func parseVisionMarker(content string) (question, dataURI string) {
	lines := strings.SplitN(content, "\n", 4)
	for _, line := range lines {
		if strings.HasPrefix(line, "question=") {
			question = strings.TrimPrefix(line, "question=")
		}
		if strings.HasPrefix(line, "data_uri=") {
			dataURI = strings.TrimPrefix(line, "data_uri=")
		}
	}
	if question == "" {
		question = "Describe this image."
	}
	return
}

// maybeCompress 在上下文接近 token 上限时，通过 LLM 对较早的消息进行摘要压缩。
// 参考 OpenClaw compaction：按 token 份额分块摘要，保留关键标识符和活跃任务状态。
func (r *Runner) maybeCompress(ctx context.Context, sess *session.Session) {
	estimate := sess.TokenEstimate()
	threshold := r.maxTokens * 70 / 100
	if estimate < threshold {
		return
	}

	history := sess.History()
	// 找到安全的压缩截断点：不破坏 tool_use / tool_result 配对
	cutIdx := findCompactionCut(history)
	if cutIdx < 4 {
		return // 消息太少，不值得压缩
	}

	// 安全超时：compaction 不能阻塞主流程太久
	compactCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	// 构建摘要请求
	var sb strings.Builder
	sb.WriteString(compactionInstruction)
	sb.WriteString("\n\n---\n\nConversation to summarize:\n\n")
	for _, m := range history[:cutIdx] {
		content := m.Content
		if len(content) > 800 {
			content = content[:800] + "...[truncated]"
		}
		if m.Role == provider.RoleTool {
			// 工具结果只保留前 200 字符在摘要请求中
			if len(content) > 200 {
				content = content[:200] + "...[truncated tool output]"
			}
			fmt.Fprintf(&sb, "[tool_result id=%s]: %s\n", m.ToolCallID, content)
		} else if len(m.ToolCalls) > 0 {
			fmt.Fprintf(&sb, "[%s]: %s\n", m.Role, content)
			for _, tc := range m.ToolCalls {
				fmt.Fprintf(&sb, "  → tool_call: %s(%s)\n", tc.Name, truncateStr(tc.Arguments, 150))
			}
		} else {
			fmt.Fprintf(&sb, "[%s]: %s\n", m.Role, content)
		}
	}

	sumMsg, err := r.provider.Chat(compactCtx, []provider.Message{
		{Role: provider.RoleSystem, Content: "You are a conversation summarizer. Produce a concise but thorough summary. Respond in the same language the user used."},
		{Role: provider.RoleUser, Content: sb.String()},
	}, nil, nil)
	if err != nil {
		slog.Warn("context compression failed", "err", err)
		return
	}

	sess.Compress(sumMsg.Content, cutIdx)
	slog.Info("context compressed", "dropped", cutIdx, "remaining", len(history)-cutIdx, "summary_len", len(sumMsg.Content))
}

// compactionInstruction 是 LLM 摘要的详细指令，参考 OpenClaw 的保留策略。
const compactionInstruction = `Summarize the following conversation concisely. You MUST preserve:
- Active tasks and their status (in-progress, blocked, pending, completed count like "5/17 done")
- The last user request and current processing state
- Key decisions and reasoning behind them
- All file paths, code snippets, variable names, and function names mentioned
- All identifiers: UUIDs, hashes, tokens, hostnames, IPs, URLs, branch names, commit SHAs
- TODO items, open questions, constraints, and blockers
- Tool call results that provided important data or changed state (file edits, command outputs)

Do NOT include:
- Redundant greetings or filler
- Tool call results that were just informational queries with no lasting impact
- Repeated attempts that ended in error before the final successful one`

// findCompactionCut 找到安全的压缩截断索引。
// 不能从 assistant(tool_calls) 和对应的 tool result 中间截断。
func findCompactionCut(history []provider.Message) int {
	half := len(history) / 2
	if half < 4 {
		return half
	}
	// 从 half 向前搜索安全截断点
	for i := half; i > 2; i-- {
		msg := history[i]
		// 不在 tool result 处截断（需要保留对应的 assistant tool_call）
		if msg.Role == provider.RoleTool {
			continue
		}
		// 不在带 tool_calls 的 assistant 消息处截断（结果在后面）
		if msg.Role == provider.RoleAssistant && len(msg.ToolCalls) > 0 {
			continue
		}
		return i
	}
	return half // fallback
}

func truncateStr(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
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

// isExecTool 判断工具是否为命令执行类（需要审批）。
func isExecTool(name string) bool {
	switch name {
	case "run_command", "run_command_sandboxed":
		return true
	default:
		return false
	}
}
