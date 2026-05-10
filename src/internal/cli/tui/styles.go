package tui

import "github.com/charmbracelet/lipgloss"

// 颜色取自 Claude Code 的配色：柔和的琥珀 + 灰蓝 + 低饱和度
var (
	colorAccent  = lipgloss.Color("#d79921") // 温暖琥珀（AgentGo 主色）
	colorUser    = lipgloss.Color("#83a598") // 青蓝（用户消息）
	colorAssist  = lipgloss.Color("#ebdbb2") // 米白（助手文本）
	colorTool    = lipgloss.Color("#b8bb26") // 橄榄绿（工具）
	colorThink   = lipgloss.Color("#928374") // 柔灰（思考）
	colorDim     = lipgloss.Color("#7c6f64") // 深灰（辅助信息）
	colorError   = lipgloss.Color("#fb4934") // 红（错误）
	colorOK      = lipgloss.Color("#a9e34b") // 亮一点的青柠绿（成功/工具完成）
	colorSpinner = lipgloss.Color("#b8e453") // spinner 专用的稍亮绿
	colorBorder  = lipgloss.Color("#504945") // 边框
	colorBorderH = lipgloss.Color("#d79921") // 高亮边框
)

var (
	accentStyle = lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	dimStyle    = lipgloss.NewStyle().Foreground(colorDim)
	userStyle   = lipgloss.NewStyle().Foreground(colorUser).Bold(true)
	assistStyle = lipgloss.NewStyle().Foreground(colorAssist)
	toolStyle   = lipgloss.NewStyle().Foreground(colorTool)
	thinkStyle  = lipgloss.NewStyle().Foreground(colorThink).Italic(true)
	errorStyle  = lipgloss.NewStyle().Foreground(colorError).Bold(true)
	okStyle     = lipgloss.NewStyle().Foreground(colorOK)

	headerStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colorBorderH).
			Padding(0, 1).
			Foreground(colorAccent)

	inputBoxStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colorBorder).
			Padding(0, 1)

	inputBoxFocusedStyle = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(colorBorderH).
				Padding(0, 1)

	statusBarStyle = lipgloss.NewStyle().
			Foreground(colorDim).
			Padding(0, 1)

	bannerStyle = lipgloss.NewStyle().
			Foreground(colorAccent).
			Bold(true)

	cardStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colorBorder).
			Padding(0, 1)

	slashPanelStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colorAccent).
			Padding(0, 1)

	slashSelectedStyle = lipgloss.NewStyle().
				Foreground(colorAccent).
				Bold(true)
)
