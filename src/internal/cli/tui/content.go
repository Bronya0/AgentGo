package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
)

// blockKind 标识一个内容块的类型。
type blockKind int

const (
	blockUser      blockKind = iota // 用户输入
	blockAssistant                  // 助手文本回复
	blockThinking                   // 推理内容（可折叠）
	blockTool                       // 工具调用 (ongoing 或完成)
	blockSystem                     // 系统消息（命令输出、错误、info）
	blockError                      // 错误
)

// block 是聊天流中的一个可渲染单元。
type block struct {
	kind blockKind

	// 通用文本内容
	text string

	// --- assistant block 专属 ---
	streaming bool // 是否仍在流式接收（用于选择渲染策略）

	// --- thinking block 专属 ---
	thinkingTokens    int
	thinkingCollapsed bool
	thinkingActive    bool // 是否正在流式接收推理
	thinkingFrame     int  // 动画帧（由外部 tick 递增）

	// --- tool block 专属 ---
	toolID     string
	toolName   string
	toolArgs   string
	toolOutput string
	toolDone   bool
	toolError  bool
}

// renderer 是 content 渲染器，持有 glamour 实例做 Markdown 渲染。
type renderer struct {
	glamour *glamour.TermRenderer
	theme   string
	width   int
}

func newRenderer(theme string, width int) *renderer {
	r := &renderer{theme: theme, width: width}
	r.rebuild()
	return r
}

func (r *renderer) rebuild() {
	w := r.width - 4
	if w < 40 {
		w = 40
	}
	tr, _ := glamour.NewTermRenderer(glamourOpt(r.theme), glamour.WithWordWrap(w))
	r.glamour = tr
}

func (r *renderer) setWidth(w int) {
	if w == r.width {
		return
	}
	r.width = w
	r.rebuild()
}

func (r *renderer) setTheme(theme string) {
	r.theme = theme
	r.rebuild()
}

func glamourOpt(theme string) glamour.TermRendererOption {
	switch strings.ToLower(theme) {
	case "dark":
		return glamour.WithStylePath("dark")
	case "light":
		return glamour.WithStylePath("light")
	case "notty":
		return glamour.WithStylePath("notty")
	case "dracula":
		return glamour.WithStylePath("dracula")
	case "ascii":
		return glamour.WithStylePath("ascii")
	default:
		return glamour.WithAutoStyle()
	}
}

// render 把一个 block 渲染为字符串（不含末尾换行）。
func (r *renderer) render(b block) string {
	switch b.kind {
	case blockUser:
		return r.renderUser(b.text)
	case blockAssistant:
		return r.renderAssistant(b)
	case blockThinking:
		return r.renderThinking(b)
	case blockTool:
		return r.renderTool(b)
	case blockError:
		return errorStyle.Render("✗ " + b.text)
	case blockSystem:
		return dimStyle.Render(b.text)
	}
	return b.text
}

func (r *renderer) renderUser(text string) string {
	prefix := userStyle.Render("❯ ")
	// 用户输入逐行加前缀
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	for i, ln := range lines {
		if i == 0 {
			lines[i] = prefix + ln
		} else {
			lines[i] = "  " + ln
		}
	}
	return strings.Join(lines, "\n")
}

func (r *renderer) renderAssistant(b block) string {
	text := strings.TrimRight(b.text, " \t")
	if strings.TrimSpace(text) == "" {
		return ""
	}
	if r.glamour == nil {
		return assistStyle.Render(text)
	}

	// 非流式：整段用 glamour 渲染
	if !b.streaming {
		if rendered, err := r.glamour.Render(text); err == nil && strings.TrimSpace(rendered) != "" {
			return strings.TrimRight(rendered, "\n")
		}
		return assistStyle.Render(text)
	}

	// 流式：把"完整段落"与"尾部不完整段落"分开处理
	complete, tail := splitStreaming(text)
	var out strings.Builder
	if strings.TrimSpace(complete) != "" {
		if rendered, err := r.glamour.Render(complete); err == nil && strings.TrimSpace(rendered) != "" {
			out.WriteString(strings.TrimRight(rendered, "\n"))
		} else {
			out.WriteString(assistStyle.Render(complete))
		}
	}
	if tail != "" {
		if out.Len() > 0 {
			out.WriteString("\n")
		}
		out.WriteString(assistStyle.Render(tail))
	}
	return out.String()
}

// splitStreaming 把已在流式的 markdown 文本切成"已闭合"和"未闭合尾部"两段。
// 启发式：
//   - 如果尾部有未闭合的代码 fence（单数个 ``` ），整个尾部视为未完成
//   - 否则最后一个段落（以空行为界）视为未完成
func splitStreaming(text string) (complete, tail string) {
	// 统计 ``` 数量，奇数则最后一个 fence 未闭合
	fenceCount := strings.Count(text, "```")
	if fenceCount%2 == 1 {
		// 找最后一个 ``` 的位置，整个尾部（含那段代码）作为 tail
		idx := strings.LastIndex(text, "```")
		return strings.TrimRight(text[:idx], "\n"), text[idx:]
	}
	// 找最后一个段落分隔（连续 \n\n）
	lastPara := strings.LastIndex(text, "\n\n")
	if lastPara < 0 {
		// 没有段落分隔 → 全部作为尾部
		return "", text
	}
	return text[:lastPara], strings.TrimLeft(text[lastPara:], "\n")
}

func (r *renderer) renderThinking(b block) string {
	// 动画图标
	frames := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
	icon := frames[b.thinkingFrame%len(frames)]
	tokStr := fmt.Sprintf("%d tokens", b.thinkingTokens)

	var head string
	if b.thinkingActive {
		head = renderPulse("✻ thinking", b.thinkingFrame) +
			"  " + lipgloss.NewStyle().Foreground(colorSpinner).Render(icon) +
			"  " + thinkStyle.Render(tokStr)
	} else {
		head = thinkStyle.Render("✻ thinking · " + tokStr)
	}

	if b.thinkingCollapsed {
		hint := "Tab to expand"
		if b.thinkingActive {
			hint = "Tab to expand · streaming"
		}
		return head + "  " + dimStyle.Render("("+hint+")")
	}
	head += "  " + dimStyle.Render("(Tab to collapse)")
	if b.text == "" {
		return head
	}
	lines := strings.Split(strings.TrimRight(b.text, "\n"), "\n")
	for i, ln := range lines {
		lines[i] = thinkStyle.Render("│ " + ln)
	}
	return head + "\n" + strings.Join(lines, "\n")
}

// renderPulse 渲染流动渐变的文字（用于 thinking 动画）。
func renderPulse(text string, frame int) string {
	// 一组柔和的琥珀色阶梯，让字符一个接一个亮起
	colors := []lipgloss.Color{
		lipgloss.Color("#504945"),
		lipgloss.Color("#7c6f64"),
		lipgloss.Color("#a89984"),
		lipgloss.Color("#d5c4a1"),
		lipgloss.Color("#ebdbb2"),
		lipgloss.Color("#fabd2f"),
		lipgloss.Color("#d79921"),
		lipgloss.Color("#b57614"),
	}
	var sb strings.Builder
	for i, ch := range text {
		c := colors[(i+frame)%len(colors)]
		sb.WriteString(lipgloss.NewStyle().Foreground(c).Bold(true).Render(string(ch)))
	}
	return sb.String()
}

func (r *renderer) renderTool(b block) string {
	marker := dimStyle.Render("◌")
	nameStyle := toolStyle
	if b.toolDone {
		if b.toolError {
			marker = errorStyle.Render("✗")
			nameStyle = errorStyle
		} else {
			marker = okStyle.Render("●")
		}
	}
	head := fmt.Sprintf("%s %s", marker, nameStyle.Render(b.toolName))
	args := trimOneline(b.toolArgs, 80)
	if args != "" {
		head += " " + dimStyle.Render(args)
	}
	if !b.toolDone || b.toolOutput == "" {
		return head
	}
	// 单行摘要化输出，最多 2 行；详细输出省略（用户可从 turn log 查）
	out := strings.TrimSpace(b.toolOutput)
	outLines := strings.Split(out, "\n")
	n := len(outLines)
	switch {
	case n == 0:
		return head
	case n == 1:
		return head + "\n  " + dimStyle.Render("└ "+trimOneline(outLines[0], 100))
	default:
		summary := trimOneline(outLines[0], 100)
		return head + "\n  " + dimStyle.Render(fmt.Sprintf("└ %s  (+%d lines)", summary, n-1))
	}
}

// trimOneline 把多行文本合并为一行并截断到 max。
func trimOneline(s string, max int) string {
	s = strings.ReplaceAll(strings.TrimSpace(s), "\n", " ")
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max]) + "…"
}

// renderBlocks 把一组 block 拼成完整视图内容。
func (r *renderer) renderBlocks(blocks []block) string {
	var sb strings.Builder
	for i, b := range blocks {
		if i > 0 {
			sb.WriteString("\n\n")
		}
		sb.WriteString(r.render(b))
	}
	return sb.String()
}

// renderBanner 渲染启动横幅。
func renderBanner(title, model, workspace, sessionID string, skills int) string {
	logo := bannerStyle.Render(strings.Join([]string{
		"  ___                 _    ___      ",
		" / _ \\  __ _ ___ _ __| |_ / __|___  ",
		"| |_| |/ _` / _ \\ '_ \\  _| (_ / _ \\ ",
		" \\__,_|\\__, \\___/ .__/\\__|\\___\\___/ ",
		"        |___/   |_|                  ",
	}, "\n"))
	info := lipgloss.JoinVertical(lipgloss.Left,
		dimStyle.Render("model:     ")+accentStyle.Render(model),
		dimStyle.Render("workspace: ")+workspace,
		dimStyle.Render("session:   ")+sessionID,
		dimStyle.Render(fmt.Sprintf("skills:    %d loaded", skills)),
	)
	tip := dimStyle.Render("/ commands · Ctrl+J newline · Tab toggle thinking · ↑ history · Ctrl+C exit")
	content := lipgloss.JoinVertical(lipgloss.Left, logo, "", info, "", tip)
	return headerStyle.Render(content)
}
