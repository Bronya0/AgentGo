// Package plugin 提供基于 Hook 的插件扩展机制。
//
// 学习 OpenClaw 的 Hook 系统，在 agent 运行的关键节点允许外部代码介入：
//   - before_llm_call: 修改发送给 LLM 的消息
//   - after_llm_call:  观察 LLM 的响应
//   - before_tool_call: 修改工具参数或拦截
//   - after_tool_call:  观察工具结果
//   - on_message:       消息到达时的预处理
package plugin

import (
	"context"
	"log/slog"

	"github.com/bronya/mini-agent/internal/provider"
	"github.com/bronya/mini-agent/internal/tool"
)

// HookBeforeLLMCall 在 LLM 调用前触发，可修改消息列表。
type HookBeforeLLMCall func(ctx context.Context, messages []provider.Message) []provider.Message

// HookAfterLLMCall 在 LLM 调用后触发，可观察响应。
type HookAfterLLMCall func(ctx context.Context, msg *provider.Message)

// HookBeforeToolCall 在工具调用前触发，可修改参数。
type HookBeforeToolCall func(ctx context.Context, name string, args tool.Args) tool.Args

// HookAfterToolCall 在工具调用后触发。
type HookAfterToolCall func(ctx context.Context, name string, result tool.Result)

// HookOnMessage 在用户消息到达时触发（可过滤/变换）。
type HookOnMessage func(ctx context.Context, message string) string

// Hooks 管理所有注册的 hook 函数。
type Hooks struct {
	beforeLLMCall  []HookBeforeLLMCall
	afterLLMCall   []HookAfterLLMCall
	beforeToolCall []HookBeforeToolCall
	afterToolCall  []HookAfterToolCall
	onMessage      []HookOnMessage
}

// NewHooks 创建空的 Hooks 集合。
func NewHooks() *Hooks {
	return &Hooks{}
}

// OnBeforeLLMCall 注册一个 before_llm_call hook。
func (h *Hooks) OnBeforeLLMCall(fn HookBeforeLLMCall)   { h.beforeLLMCall = append(h.beforeLLMCall, fn) }
func (h *Hooks) OnAfterLLMCall(fn HookAfterLLMCall)     { h.afterLLMCall = append(h.afterLLMCall, fn) }
func (h *Hooks) OnBeforeToolCall(fn HookBeforeToolCall)  { h.beforeToolCall = append(h.beforeToolCall, fn) }
func (h *Hooks) OnAfterToolCall(fn HookAfterToolCall)    { h.afterToolCall = append(h.afterToolCall, fn) }
func (h *Hooks) OnMessage(fn HookOnMessage)              { h.onMessage = append(h.onMessage, fn) }

// BeforeLLMCall 执行所有 before_llm_call hook 链（含 panic 恢复）。
func (h *Hooks) BeforeLLMCall(ctx context.Context, messages []provider.Message) []provider.Message {
	for _, fn := range h.beforeLLMCall {
		func() {
			defer recoverHook("before_llm_call")
			messages = fn(ctx, messages)
		}()
	}
	return messages
}

// AfterLLMCall 执行所有 after_llm_call hook。
func (h *Hooks) AfterLLMCall(ctx context.Context, msg *provider.Message) {
	for _, fn := range h.afterLLMCall {
		func() {
			defer recoverHook("after_llm_call")
			fn(ctx, msg)
		}()
	}
}

// BeforeToolCall 执行所有 before_tool_call hook 链。
func (h *Hooks) BeforeToolCall(ctx context.Context, name string, args tool.Args) tool.Args {
	for _, fn := range h.beforeToolCall {
		func() {
			defer recoverHook("before_tool_call")
			args = fn(ctx, name, args)
		}()
	}
	return args
}

// AfterToolCall 执行所有 after_tool_call hook。
func (h *Hooks) AfterToolCall(ctx context.Context, name string, result tool.Result) {
	for _, fn := range h.afterToolCall {
		func() {
			defer recoverHook("after_tool_call")
			fn(ctx, name, result)
		}()
	}
}

// ProcessMessage 执行所有 on_message hook。
func (h *Hooks) ProcessMessage(ctx context.Context, message string) string {
	for _, fn := range h.onMessage {
		func() {
			defer recoverHook("on_message")
			message = fn(ctx, message)
		}()
	}
	return message
}

// recoverHook 捕获 hook 中的 panic，防止崩溃。
func recoverHook(hookName string) {
	if r := recover(); r != nil {
		slog.Error("hook panic recovered", "hook", hookName, "panic", r)
	}
}

// Plugin 是一个可注册的插件。
type Plugin interface {
	// Name 返回插件名称。
	Name() string
	// Register 将 hook 注册到 Hooks 集合中。
	Register(hooks *Hooks)
}

// Manager 管理插件的加载和注册。
type Manager struct {
	hooks   *Hooks
	plugins []Plugin
}

// NewManager 创建一个插件管理器。
func NewManager(hooks *Hooks) *Manager {
	return &Manager{hooks: hooks}
}

// Register 注册并激活一个插件。
func (m *Manager) Register(p Plugin) {
	p.Register(m.hooks)
	m.plugins = append(m.plugins, p)
	slog.Info("plugin registered", "plugin", p.Name())
}

// Plugins 返回已注册的插件列表。
func (m *Manager) Plugins() []Plugin { return m.plugins }
