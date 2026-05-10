package cli

import (
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/bronya/mini-agent/internal/provider"
	"github.com/bronya/mini-agent/internal/runner"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
)

type Renderer struct {
	out              io.Writer
	assistant        strings.Builder
	reasoningTokens  int
	thinkingFrame    int
	reasoningActive  bool
	usage            provider.Usage
	markdown         *glamour.TermRenderer
	theme            string
	lastPreviewLines int
	lastPreviewTime  time.Time
	previewActive    bool
}

var (
	accentStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("81")).Bold(true)
	dimStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	errorStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("196")).Bold(true)
	cardStyle   = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("62")).Padding(0, 1)
	okStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
)

// glamourTheme 将配置中的 theme 名称转换为 glamour 选项。
func glamourTheme(theme string) glamour.TermRendererOption {
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

func NewRenderer(out io.Writer) *Renderer {
	tr, _ := glamour.NewTermRenderer(glamour.WithAutoStyle(), glamour.WithWordWrap(100))
	return &Renderer{out: out, markdown: tr, theme: "auto"}
}

func NewRendererWithTheme(out io.Writer, theme string) *Renderer {
	tr, _ := glamour.NewTermRenderer(glamourTheme(theme), glamour.WithWordWrap(100))
	return &Renderer{out: out, markdown: tr, theme: theme}
}

// SetTheme 动态切换 Markdown 渲染主题。
// 支持: auto, dark, light, notty, dracula, ascii
func (r *Renderer) SetTheme(theme string) {
	tr, err := glamour.NewTermRenderer(glamourTheme(theme), glamour.WithWordWrap(100))
	if err != nil {
		return
	}
	r.markdown = tr
	r.theme = theme
}

// Theme 返回当前主题名称。
func (r *Renderer) Theme() string { return r.theme }

func (r *Renderer) Header(title string, rows ...string) {
	content := accentStyle.Render(title)
	if len(rows) > 0 {
		content += "\n" + dimStyle.Render(strings.Join(rows, "\n"))
	}
	fmt.Fprintln(r.out, cardStyle.Render(content))
}

func (r *Renderer) Handle(chunk runner.StreamChunk) {
	if chunk.Usage != nil {
		r.usage.PromptTokens += chunk.Usage.PromptTokens
		r.usage.CompletionTokens += chunk.Usage.CompletionTokens
		r.usage.TotalTokens += chunk.Usage.TotalTokens
		return
	}

	switch chunk.Event {
	case runner.EventText:
		r.finishReasoningLine()
		r.assistant.WriteString(chunk.Text)
		r.maybePreview()
	case runner.EventReasoning:
		r.reasoningTokens += estimateTokens(chunk.Reasoning)
		r.printThinkingWave()
	case runner.EventToolStart:
		r.finishReasoningLine()
		fmt.Fprintf(r.out, "%s %s  %s\n",
			dimStyle.Render("◌"),
			accentStyle.Render(chunk.ToolName),
			dimStyle.Render(trim(chunk.ToolArgs, 120)))
	case runner.EventToolEnd:
		fmt.Fprintf(r.out, "%s %s  %s\n",
			okStyle.Render("●"),
			accentStyle.Render(chunk.ToolName),
			dimStyle.Render(trim(chunk.ToolOut, 200)))
	case runner.EventError:
		r.finishReasoningLine()
		fmt.Fprintf(r.out, "\n%s\n", errorStyle.Render("error: "+chunk.Err.Error()))
	case runner.EventDone:
		r.finishReasoningLine()
		fmt.Fprintln(r.out)
	}
}

func (r *Renderer) RenderFinal() {
	text := strings.TrimSpace(r.assistant.String())
	if text == "" {
		return
	}
	r.clearPreview()
	fmt.Fprintln(r.out)
	if r.markdown != nil {
		segments := splitMarkdown(text)
		allRendered := true
		for _, seg := range segments {
			if rendered, err := r.markdown.Render(seg); err == nil && strings.TrimSpace(rendered) != "" {
				fmt.Fprint(r.out, rendered)
			} else {
				allRendered = false
				break
			}
		}
		if allRendered {
			fmt.Fprintln(r.out)
			return
		}
	}
	fmt.Fprintln(r.out, text)
}

func (r *Renderer) Usage() provider.Usage { return r.usage }

func (r *Renderer) Reset() {
	r.assistant.Reset()
	r.reasoningTokens = 0
	r.thinkingFrame = 0
	r.reasoningActive = false
	r.usage = provider.Usage{}
	r.lastPreviewLines = 0
	r.lastPreviewTime = time.Time{}
	r.previewActive = false
}

func (r *Renderer) printThinkingWave() {
	r.reasoningActive = true
	r.thinkingFrame++
	fmt.Fprintf(r.out, "\r%s %s\033[K", renderWave("thinking", r.thinkingFrame), dimStyle.Render(fmt.Sprintf("%d tokens", r.reasoningTokens)))
}

func (r *Renderer) finishReasoningLine() {
	if !r.reasoningActive {
		return
	}
	fmt.Fprintf(r.out, "\r%s %s\033[K\n", renderWave("thinking", r.thinkingFrame), dimStyle.Render(fmt.Sprintf("%d tokens", r.reasoningTokens)))
	r.reasoningActive = false
}

func (r *Renderer) clearPreview() {
	if r.previewActive && r.lastPreviewLines > 0 {
		fmt.Fprintf(r.out, "\033[%dA", r.lastPreviewLines)
		for i := 0; i < r.lastPreviewLines; i++ {
			fmt.Fprint(r.out, "\033[2K")
			if i < r.lastPreviewLines-1 {
				fmt.Fprint(r.out, "\n")
			}
		}
		fmt.Fprintf(r.out, "\033[%dA", r.lastPreviewLines-1)
		r.previewActive = false
		r.lastPreviewLines = 0
	}
}

func (r *Renderer) maybePreview() {
	if r.markdown == nil {
		return
	}
	if time.Since(r.lastPreviewTime) < 200*time.Millisecond {
		return
	}
	text := strings.TrimSpace(r.assistant.String())
	if text == "" {
		return
	}

	var buf strings.Builder
	segments := splitMarkdown(text)
	allRendered := true
	for _, seg := range segments {
		if rendered, err := r.markdown.Render(seg); err == nil && strings.TrimSpace(rendered) != "" {
			buf.WriteString(rendered)
		} else {
			allRendered = false
			break
		}
	}
	if !allRendered {
		buf.Reset()
		buf.WriteString(text)
	}

	rendered := buf.String()
	newLines := strings.Count(rendered, "\n")
	if rendered != "" && !strings.HasSuffix(rendered, "\n") {
		newLines++
	}

	if r.previewActive && r.lastPreviewLines > 0 {
		fmt.Fprintf(r.out, "\033[%dA", r.lastPreviewLines)
	}

	fmt.Fprint(r.out, rendered)
	if rendered != "" && !strings.HasSuffix(rendered, "\n") {
		fmt.Fprintln(r.out)
	}

	if r.previewActive && newLines < r.lastPreviewLines {
		remain := r.lastPreviewLines - newLines
		for i := 0; i < remain; i++ {
			fmt.Fprint(r.out, "\033[2K")
			if i < remain-1 {
				fmt.Fprint(r.out, "\n")
			}
		}
		fmt.Fprintf(r.out, "\033[%dA", remain)
	}

	r.lastPreviewLines = newLines
	r.lastPreviewTime = time.Now()
	r.previewActive = true
}

func renderWave(text string, frame int) string {
	colors := []string{"63", "69", "75", "81", "87", "123", "87", "81", "75", "69"}
	var sb strings.Builder
	for i, ch := range text {
		color := colors[(i+frame)%len(colors)]
		style := lipgloss.NewStyle().Foreground(lipgloss.Color(color)).Bold((i+frame)%3 == 0)
		sb.WriteString(style.Render(string(ch)))
	}
	dots := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
	sb.WriteString(" ")
	sb.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color(colors[frame%len(colors)])).Render(dots[frame%len(dots)]))
	return sb.String()
}

func estimateTokens(s string) int {
	if s == "" {
		return 0
	}
	tokens := (utf8.RuneCountInString(s) + 3) / 4
	if tokens < 1 {
		return 1
	}
	return tokens
}

func splitMarkdown(text string) []string {
	var segments []string
	var buf strings.Builder
	inFence := false

	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			inFence = !inFence
		}

		if !inFence && trimmed == "" && buf.Len() > 0 {
			segments = append(segments, buf.String())
			buf.Reset()
			continue
		}

		buf.WriteString(line)
		buf.WriteByte('\n')
	}

	if buf.Len() > 0 {
		segments = append(segments, buf.String())
	}

	if len(segments) == 0 {
		return []string{text}
	}
	return segments
}

func trim(s string, max int) string {
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}
