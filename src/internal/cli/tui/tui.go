// Package tui 提供基于 Bubble Tea 的 Agent CLI 界面。
//
// 布局（模仿 Claude Code）：
//
//	┌ AgentGo ──────────────────────────────┐   （启动横幅，仅首屏）
//	│ model: ...                            │
//	└───────────────────────────────────────┘
//	❯ user input
//	● tool_name  args…
//	  └ output…
//	💭 thinking · 1234 tokens (Tab to expand)
//	<assistant markdown here>
//	[斜杠命令浮层，输入 / 时激活]
//	╭ input ─────────────────────────────────╮
//	│ ❯ cursor                               │
//	╰────────────────────────────────────────╯
//	 model · ↑1.2k ↓567 · ctx 12% · msgs 4 · session default       ? /help
package tui

import (
	"context"
	"os"

	"github.com/bronya/mini-agent/internal/app"
	tea "github.com/charmbracelet/bubbletea"
)

// Run 启动 TUI 并阻塞，直到用户退出。
func Run(ctx context.Context, a *app.App, sessionID string) error {
	m := New(ctx, a, sessionID)

	// 把 TUI 的 approval 实现注入 Runner
	a.SetExecApproval(m.Approval())

	p := tea.NewProgram(m,
		tea.WithContext(ctx),
		tea.WithAltScreen(),
		tea.WithMouseCellMotion(),
	)

	_, err := p.Run()
	return err
}

// writeFileSafe 写文件（被 /export 使用）。
func writeFileSafe(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o600)
}
