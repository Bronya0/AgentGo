package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// View 是 bubbletea 的渲染入口，每次状态变化都会调用。
func (m Model) View() string {
	if m.width == 0 || m.height == 0 {
		return "initializing…"
	}

	// 1. 主体：viewport（滚动内容区）
	body := m.viewport.View()

	// 2. 斜杠命令浮层（在输入框上方）
	slash := ""
	if m.slashOpen {
		slash = renderSlashPanel(m.slashFiltered, m.slashCursor, m.width)
	}

	// 3. 输入框（高度固定，避免状态栏抖动）
	var input string
	boxStyle := inputBoxFocusedStyle.Width(m.width - 2)
	idleStyle := inputBoxStyle.Width(m.width - 2)

	switch {
	case m.awaitingApproval:
		// 审批时输入框替换为紧凑提示（仍占 textarea 高度以保持底部稳定）
		content := accentStyle.Render("⚠ ") +
			toolStyle.Render(m.approvalPrompt.toolName) + dimStyle.Render(" wants to run") + "\n" +
			"  " + trimOneline(m.approvalPrompt.command, m.width-6) + "\n" +
			dimStyle.Render("  [Enter] allow · [a] always · [Esc/n] deny")
		input = idleStyle.Render(content)
	case m.running:
		// 运行中：仍展示输入框，但失去焦点样式，避免"输入区变标签"造成布局抖动
		input = idleStyle.Render(m.textarea.View())
	default:
		input = boxStyle.Render(m.textarea.View())
	}

	// 4. 状态栏
	status := m.statusBar()

	// 组装（自上而下）
	parts := []string{body}
	if slash != "" {
		parts = append(parts, slash)
	}
	parts = append(parts, input, status)

	return lipgloss.JoinVertical(lipgloss.Left, parts...)
}

// statusBar 构造底部状态栏。
func (m Model) statusBar() string {
	model := "none"
	if m.app.Provider != nil {
		model = m.app.Provider.Model()
	}
	sess := m.app.Sessions.Get(m.sessionID)

	// 上下文百分比
	budget := m.app.Config.MaxContextTokens
	pct := 0
	if budget > 0 {
		pct = sess.TokenEstimate() * 100 / budget
	}
	ctxColor := colorDim
	switch {
	case pct >= 80:
		ctxColor = colorError
	case pct >= 60:
		ctxColor = colorAccent
	}
	ctxStr := lipgloss.NewStyle().Foreground(ctxColor).Render(fmt.Sprintf("ctx %d%%", pct))

	// 本轮 prompt/completion：运行中显示 live 估算，完成后显示真实 usage
	inTok := m.sessionUsage.PromptTokens + m.lastUsage.PromptTokens
	outTok := m.sessionUsage.CompletionTokens + m.lastUsage.CompletionTokens
	if m.running && m.lastUsage.CompletionTokens == 0 {
		outTok = m.sessionUsage.CompletionTokens + m.liveOutTok
	}
	liveMark := ""
	if m.running {
		liveMark = accentStyle.Render(" ●")
	}

	parts := []string{
		accentStyle.Render(model),
		dimStyle.Render(fmt.Sprintf("↑%s ↓%s%s",
			formatNum(inTok),
			formatNum(outTok),
			liveMark)),
		ctxStr,
		dimStyle.Render(fmt.Sprintf("msgs %d", len(sess.History()))),
		dimStyle.Render("session " + m.sessionID),
	}

	left := strings.Join(parts, dimStyle.Render(" · "))
	right := dimStyle.Render("? /help · ↑ history")

	// 左右对齐：若空间不足，优先保留右侧帮助文字，截断左侧
	leftW := lipgloss.Width(left)
	rightW := lipgloss.Width(right)
	avail := m.width - rightW - 2 - 1 // -2 padding, -1 min gap
	if leftW > avail && avail > 10 {
		left = lipgloss.NewStyle().MaxWidth(avail).Render(left)
		leftW = lipgloss.Width(left)
	}
	gap := m.width - leftW - rightW - 2
	if gap < 1 {
		gap = 1
	}
	return statusBarStyle.Render(left + strings.Repeat(" ", gap) + right)
}

// formatNum 把数字缩写为 1.2k 这种形式。
func formatNum(n int) string {
	if n < 1000 {
		return fmt.Sprintf("%d", n)
	}
	if n < 1_000_000 {
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	}
	return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
}

// renderSlashPanel 渲染斜杠命令浮层。
func renderSlashPanel(cmds []slashCommand, cursor, width int) string {
	if len(cmds) == 0 {
		return ""
	}
	max := 6
	if len(cmds) > max {
		// 以 cursor 为中心裁一个窗口
		start := cursor - max/2
		if start < 0 {
			start = 0
		}
		if start+max > len(cmds) {
			start = len(cmds) - max
		}
		cmds = cmds[start : start+max]
		cursor = cursor - start
	}

	var lines []string
	for i, c := range cmds {
		line := fmt.Sprintf("%-24s %s", c.Usage, dimStyle.Render(c.Desc))
		if i == cursor {
			line = slashSelectedStyle.Render("▸ " + line)
		} else {
			line = "  " + line
		}
		lines = append(lines, line)
	}
	body := strings.Join(lines, "\n")
	return slashPanelStyle.Width(width - 2).Render(body)
}

// renderApprovalBox 已由 View 内联渲染，这里保留以备未来扩展。
func renderApprovalBox(toolName, command string) string {
	return accentStyle.Render("⚠ ") + toolStyle.Render(toolName) + " " + command
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
