package tui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/bronya/mini-agent/internal/app"
	"github.com/bronya/mini-agent/internal/provider"
	"github.com/bronya/mini-agent/internal/runner"
	"github.com/bronya/mini-agent/internal/session"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// runPhase 标识本轮对话当前处于哪个阶段。
type runPhase int

const (
	phaseIdle     runPhase = iota
	phaseThinking          // 正在接收 reasoning
	phaseTool              // 正在执行工具
	phaseWriting           // 正在流式输出 assistant 文本
)

func (p runPhase) label() string {
	switch p {
	case phaseThinking:
		return "thinking"
	case phaseTool:
		return "running tool"
	case phaseWriting:
		return "writing"
	default:
		return "waiting"
	}
}

// Model 是整个 TUI 的状态。
type Model struct {
	app       *app.App
	ctx       context.Context
	sessionID string

	// UI 组件
	viewport viewport.Model
	textarea textarea.Model
	spinner  spinner.Model
	renderer *renderer

	// 尺寸
	width  int
	height int

	// 运行态
	engine       *engine
	blocks       []block // 聊天流
	running      bool    // LLM 正在响应
	runStartedAt time.Time // 本轮开始时间（用于标签 spinner）
	phase        runPhase // 当前阶段（thinking/writing/tool）
	lastUsage    provider.Usage
	sessionUsage provider.Usage // 本轮 session 累计
	liveOutTok   int            // 流式输出的估算 token（Provider 发 usage 前的占位）
	lastESC      time.Time      // 用于双击 ESC 取消
	autoFollow   bool           // 是否跟随最新内容滚动（用户手动上滚后置为 false）
	turnAnchor   int            // 本轮开始时 viewport 的 Y 偏移（用于输出结束后停留在首行）

	// 斜杠命令浮层
	slashOpen     bool
	slashFiltered []slashCommand
	slashCursor   int

	// 命令历史（上下键翻阅）
	history      []string
	historyIdx   int    // 当前指针：len(history)=未选中（正在编辑新消息）
	historyDraft string // 进入历史浏览前保存的草稿

	// 工具批准（阻塞在对话框）
	awaitingApproval bool
	approvalPrompt   approvalRequestMsg

	// 启动横幅
	bannerShown bool
}

// New 创建一个新的 TUI Model。
func New(ctx context.Context, a *app.App, sessionID string) Model {
	if sessionID == "" {
		sessionID = "default"
	}

	theme := a.Config.Render.Theme
	if theme == "" {
		theme = "auto"
	}

	ta := textarea.New()
	ta.Placeholder = "Ask anything · / for commands · Ctrl+J for newline"
	ta.Prompt = "❯ "
	ta.ShowLineNumbers = false
	ta.CharLimit = 0
	ta.SetHeight(3)
	ta.Focus()

	// 重绑 InsertNewline：只响应 Ctrl+J / Alt+Enter / Shift+Enter
	// Enter 单独留给"提交"动作（在 handleKey 里处理）
	ta.KeyMap.InsertNewline = key.NewBinding(
		key.WithKeys("ctrl+j", "alt+enter", "shift+enter"),
		key.WithHelp("shift+enter", "newline"),
	)

	// 自定义 textarea 的样式
	ta.FocusedStyle.Base = lipgloss.NewStyle()
	ta.FocusedStyle.Prompt = accentStyle
	ta.FocusedStyle.Text = assistStyle
	ta.FocusedStyle.CursorLine = lipgloss.NewStyle()
	ta.FocusedStyle.Placeholder = dimStyle
	ta.BlurredStyle.Base = lipgloss.NewStyle()
	ta.BlurredStyle.Prompt = dimStyle
	ta.BlurredStyle.Text = dimStyle

	vp := viewport.New(80, 20)
	vp.MouseWheelEnabled = true

	sp := spinner.New()
	sp.Spinner = spinner.Spinner{
		Frames: []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"},
		FPS:    time.Second / 12, // ~12fps，足够丝滑又不浪费
	}
	sp.Style = lipgloss.NewStyle().Foreground(colorSpinner)

	m := Model{
		app:       a,
		ctx:       ctx,
		sessionID: sessionID,
		viewport:  vp,
		textarea:  ta,
		spinner:   sp,
		renderer:  newRenderer(theme, 80),
	}
	m.engine = newEngine(a.Runner, a.Sessions)
	m.loadHistoryFromDisk()

	return m
}

// Approval 返回 TUI 使用的 approval 函数，供 app.Options 注入 Runner。
func (m Model) Approval() runner.ExecApprovalFn {
	return m.engine.approvalFn()
}

// Init 是 bubbletea 启动时调用的初始化命令。
func (m Model) Init() tea.Cmd {
	return tea.Batch(
		textarea.Blink,
		m.spinner.Tick,
		m.engine.listenApproval(),
	)
}

// Update 处理所有消息并返回新的 Model 与后续命令。
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.applyLayout()
		m.refreshViewport()
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)

	case streamChunkMsg:
		m.onStreamChunk(msg.chunk)
		m.refreshViewport()
		cmds = append(cmds, m.engine.listenChunk())

	case streamDoneMsg:
		m.running = false
		m.phase = phaseIdle
		m.finalizeThinking()
		m.finalizeAssistant()
		m.engine.reset()
		if msg.err != nil && !strings.Contains(msg.err.Error(), "context canceled") {
			m.appendBlock(block{kind: blockError, text: msg.err.Error()})
		}
		// 如果 provider 没发 usage（有些 Ollama 等不发），用估算值兜底
		if m.lastUsage.CompletionTokens == 0 && m.liveOutTok > 0 {
			m.lastUsage.CompletionTokens = m.liveOutTok
		}
		// 更新 session usage 累计
		m.sessionUsage.PromptTokens += m.lastUsage.PromptTokens
		m.sessionUsage.CompletionTokens += m.lastUsage.CompletionTokens
		m.sessionUsage.TotalTokens += m.lastUsage.TotalTokens
		m.liveOutTok = 0
		// 本轮完成后做一次文件快照（用于回滚）
		m.checkpointAfterTurn()
		m.refreshViewport()

	case approvalRequestMsg:
		m.awaitingApproval = true
		m.approvalPrompt = msg
		m.refreshViewport()
		cmds = append(cmds, m.engine.listenApproval())

	case spinner.TickMsg:
		if m.running {
			var cmd tea.Cmd
			m.spinner, cmd = m.spinner.Update(msg)
			cmds = append(cmds, cmd)
			// 推进 thinking 块的动画帧
			for i := range m.blocks {
				if m.blocks[i].kind == blockThinking && m.blocks[i].thinkingActive {
					m.blocks[i].thinkingFrame++
				}
			}
			m.refreshViewport()
		}
	}

	// 把消息也传给 viewport 和 textarea（例如鼠标滚轮、编辑按键）
	if !m.awaitingApproval {
		var vpCmd, taCmd tea.Cmd
		m.viewport, vpCmd = m.viewport.Update(msg)
		if !m.running {
			m.textarea, taCmd = m.textarea.Update(msg)
		}
		cmds = append(cmds, vpCmd, taCmd)
	}

	// 动态刷新斜杠命令浮层
	m.updateSlashPanel()

	return m, tea.Batch(cmds...)
}

// handleKey 处理键盘事件。
func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// 批准对话框：Enter=允许 / a=总是允许 / Esc|n=拒绝
	if m.awaitingApproval {
		switch msg.String() {
		case "enter":
			m.approvalPrompt.respond <- true
			m.awaitingApproval = false
		case "a", "A":
			m.approvalPrompt.respond <- true
			m.awaitingApproval = false
		case "n", "N", "esc":
			m.approvalPrompt.respond <- false
			m.awaitingApproval = false
		case "ctrl+c":
			m.approvalPrompt.respond <- false
			m.awaitingApproval = false
			return m, tea.Quit
		}
		m.refreshViewport()
		return m, nil
	}

	switch msg.Type {
	case tea.KeyCtrlC:
		if m.running {
			m.engine.cancel()
			return m, nil
		}
		return m, tea.Quit

	case tea.KeyEsc:
		// 双击 ESC 取消当前任务
		now := time.Now()
		if now.Sub(m.lastESC) < 500*time.Millisecond {
			if m.running {
				m.engine.cancel()
			}
			m.lastESC = time.Time{}
		} else {
			m.lastESC = now
		}
		// 关闭斜杠浮层
		if m.slashOpen {
			m.slashOpen = false
		}
		return m, nil

	case tea.KeyTab:
		// 斜杠面板打开时：Tab 选中项填入输入框
		if m.slashOpen {
			return m.selectSlash(), nil
		}
		// 折叠/展开最后一个 thinking 块
		if idx := m.lastThinkingIdx(); idx >= 0 {
			m.blocks[idx].thinkingCollapsed = !m.blocks[idx].thinkingCollapsed
			m.refreshViewport()
			return m, nil
		}
	case tea.KeyUp:
		if m.slashOpen {
			if m.slashCursor > 0 {
				m.slashCursor--
			}
			return m, nil
		}
		// 命令历史：仅在输入为空或单行时激活，避免干扰多行编辑
		if m.canNavigateHistory() {
			m.historyPrev()
			return m, nil
		}
	case tea.KeyDown:
		if m.slashOpen {
			if m.slashCursor < len(m.slashFiltered)-1 {
				m.slashCursor++
			}
			return m, nil
		}
		if m.canNavigateHistory() {
			m.historyNext()
			return m, nil
		}
	case tea.KeyPgUp:
		m.viewport.ScrollUp(m.viewport.Height / 2)
		m.autoFollow = false
		return m, nil
	case tea.KeyPgDown:
		before := m.viewport.YOffset
		m.viewport.ScrollDown(m.viewport.Height / 2)
		if m.viewport.YOffset == before || m.viewport.AtBottom() {
			m.autoFollow = true
		}
		return m, nil
	case tea.KeyEnter:
		// Shift+Enter / Alt+Enter / Ctrl+J：textarea 通过自定义 KeyMap 处理换行
		// 裸 Enter：提交输入。斜杠面板不拦截 Enter（避免"输入命令后还要再按一次"的 bug）
		if msg.Alt {
			break
		}
		if m.running {
			return m, nil
		}
		input := strings.TrimSpace(m.textarea.Value())
		if input == "" {
			return m, nil
		}
		m.textarea.Reset()
		m.slashOpen = false
		m.historyAdd(input)
		return m.submit(input)
	}

	// 其余键交给 textarea
	var taCmd tea.Cmd
	m.textarea, taCmd = m.textarea.Update(msg)
	m.updateSlashPanel()
	return m, taCmd
}

// submit 处理用户输入：斜杠命令或发送给 Runner。
func (m Model) submit(input string) (tea.Model, tea.Cmd) {
	m.slashOpen = false

	if strings.HasPrefix(input, "/") {
		return m.handleSlashCommand(input)
	}

	// 追加用户块
	m.appendBlock(block{kind: blockUser, text: input})
	m.lastUsage = provider.Usage{}
	m.liveOutTok = 0
	m.running = true
	m.phase = phaseThinking
	m.runStartedAt = time.Now()
	m.autoFollow = true
	// checkpoint BeginTurn/SealTurn 现由 Runner.Run() 自动管理
	m.refreshViewport()
	// 记录本轮开始时 viewport 的行数位置（用于结束后停留在输出第一行）
	m.turnAnchor = m.contentLineCount()

	cmds := []tea.Cmd{
		m.engine.start(m.ctx, m.sessionID, input),
		m.spinner.Tick,
	}
	return m, tea.Batch(cmds...)
}

// onStreamChunk 处理来自 Runner 的单个流式事件。
func (m *Model) onStreamChunk(c runner.StreamChunk) {
	// Usage 累计（provider 最后才发，但收到就立即并入 session 累计）
	if c.Usage != nil {
		// 取较大值（有些 provider 在流中期也发增量，我们保留最终值）
		if c.Usage.PromptTokens > m.lastUsage.PromptTokens {
			m.lastUsage.PromptTokens = c.Usage.PromptTokens
		}
		if c.Usage.CompletionTokens > m.lastUsage.CompletionTokens {
			m.lastUsage.CompletionTokens = c.Usage.CompletionTokens
		}
		if c.Usage.TotalTokens > m.lastUsage.TotalTokens {
			m.lastUsage.TotalTokens = c.Usage.TotalTokens
		}
		return
	}

	switch c.Event {
	case runner.EventText:
		m.phase = phaseWriting
		// 文本开始 → 把所有 thinking 块标记为完成
		m.finalizeThinking()
		m.liveOutTok += estimateTokens(c.Text)
		// 合并到最后一个 assistant 块（并标记为流式中）
		if n := len(m.blocks); n > 0 && m.blocks[n-1].kind == blockAssistant {
			m.blocks[n-1].text += c.Text
			m.blocks[n-1].streaming = true
		} else {
			m.appendBlock(block{kind: blockAssistant, text: c.Text, streaming: true})
		}
	case runner.EventReasoning:
		m.phase = phaseThinking
		m.liveOutTok += estimateTokens(c.Reasoning)
		// 合并到最后一个 thinking 块（或新建一个）
		if n := len(m.blocks); n > 0 && m.blocks[n-1].kind == blockThinking {
			m.blocks[n-1].text += c.Reasoning
			m.blocks[n-1].thinkingTokens += estimateTokens(c.Reasoning)
			m.blocks[n-1].thinkingActive = true
		} else {
			m.appendBlock(block{
				kind:              blockThinking,
				text:              c.Reasoning,
				thinkingTokens:    estimateTokens(c.Reasoning),
				thinkingCollapsed: true, // 默认折叠
				thinkingActive:    true,
			})
		}
	case runner.EventToolStart:
		m.phase = phaseTool
		m.finalizeThinking()
		m.appendBlock(block{
			kind:     blockTool,
			toolID:   c.ToolID,
			toolName: c.ToolName,
			toolArgs: c.ToolArgs,
		})
	case runner.EventToolEnd:
		// 找到对应的 tool 块，标记完成
		for i := len(m.blocks) - 1; i >= 0; i-- {
			if m.blocks[i].kind == blockTool && m.blocks[i].toolID == c.ToolID && !m.blocks[i].toolDone {
				m.blocks[i].toolOutput = c.ToolOut
				m.blocks[i].toolDone = true
				m.blocks[i].toolError = strings.HasPrefix(strings.TrimSpace(c.ToolOut), "error:")
				break
			}
		}
	case runner.EventError:
		if c.Err != nil {
			m.appendBlock(block{kind: blockError, text: c.Err.Error()})
		}
	case runner.EventDone:
		// nothing extra; streamDoneMsg will handle state transition
	}
}

// handleSlashCommand 处理斜杠命令。
func (m Model) handleSlashCommand(input string) (tea.Model, tea.Cmd) {
	fields := strings.Fields(input)
	if len(fields) == 0 {
		return m, nil
	}
	cmd := fields[0]
	args := fields[1:]

	appendInfo := func(text string) {
		m.appendBlock(block{kind: blockSystem, text: text})
	}

	switch cmd {
	case "/exit", "/quit":
		return m, tea.Quit

	case "/help":
		var sb strings.Builder
		sb.WriteString(accentStyle.Render("Available commands:") + "\n")
		for _, c := range slashCommands {
			sb.WriteString(fmt.Sprintf("  %-28s %s\n", c.Usage, dimStyle.Render(c.Desc)))
		}
		appendInfo(sb.String())

	case "/clear":
		sess := m.app.Sessions.Get(m.sessionID)
		sess.Reset()
		_ = sess.Save()
		m.blocks = nil
		m.lastUsage = provider.Usage{}
		m.sessionUsage = provider.Usage{}
		appendInfo(okStyle.Render("session cleared"))

	case "/model":
		if m.app.Provider == nil {
			appendInfo(errorStyle.Render("no provider configured, use /config"))
		} else {
			appendInfo(fmt.Sprintf("provider: %s\nmodel:    %s", m.app.Provider.ID(), m.app.Provider.Model()))
		}

	case "/status":
		appendInfo(m.renderStatusDetail())

	case "/context":
		appendInfo(m.renderContext())

	case "/tools":
		tools := m.app.Tools.List()
		names := make([]string, 0, len(tools))
		for _, t := range tools {
			names = append(names, t.Name)
		}
		sort.Strings(names)
		appendInfo(strings.Join(names, "\n"))

	case "/skill":
		if len(m.app.Skills) == 0 {
			appendInfo(dimStyle.Render("no skills loaded"))
		} else {
			var sb strings.Builder
			for _, s := range m.app.Skills {
				flag := ""
				if s.Metadata.Always {
					flag = " " + accentStyle.Render("[always]")
				}
				sb.WriteString(okStyle.Render("●") + " " + s.Metadata.Name + flag + "\n")
				if s.Metadata.Description != "" {
					sb.WriteString("  " + dimStyle.Render(s.Metadata.Description) + "\n")
				}
			}
			appendInfo(strings.TrimRight(sb.String(), "\n"))
		}

	case "/sessions":
		ids := m.app.Sessions.ListIDs()
		sort.Strings(ids)
		if len(ids) == 0 {
			appendInfo(dimStyle.Render("no sessions"))
		} else {
			var sb strings.Builder
			for _, id := range ids {
				marker := "  "
				if id == m.sessionID {
					marker = okStyle.Render("* ")
				}
				s := m.app.Sessions.Get(id)
				sb.WriteString(fmt.Sprintf("%s%-20s msgs=%d tokens≈%d\n", marker, id, len(s.History()), s.TokenEstimate()))
			}
			appendInfo(strings.TrimRight(sb.String(), "\n"))
		}

	case "/session":
		if len(args) == 0 {
			appendInfo(errorStyle.Render("usage: /session <id>"))
		} else {
			m.sessionID = args[0]
			m.app.Sessions.Get(m.sessionID)
			m.lastUsage = provider.Usage{}
			m.sessionUsage = provider.Usage{}
			m.blocks = nil
			appendInfo(okStyle.Render("switched to session " + m.sessionID))
		}

	case "/theme":
		if len(args) == 0 {
			appendInfo("current theme: " + accentStyle.Render(m.renderer.theme) +
				"\navailable: auto, dark, light, dracula, ascii, notty")
		} else {
			m.renderer.setTheme(args[0])
			m.app.Config.Render.Theme = args[0]
			if err := m.app.SaveConfig(); err != nil {
				appendInfo(errorStyle.Render("warning: " + err.Error()))
			} else {
				appendInfo(okStyle.Render("theme set to " + args[0]))
			}
			m.refreshViewport()
		}

	case "/export":
		format := session.FormatMarkdown
		if len(args) > 0 && strings.EqualFold(args[0], "json") {
			format = session.FormatJSON
		}
		sess := m.app.Sessions.Get(m.sessionID)
		out, err := sess.Export(format)
		if err != nil {
			appendInfo(errorStyle.Render("export: " + err.Error()))
		} else if len(args) < 2 {
			appendInfo(out)
		} else {
			path := args[1]
			if !filepath.IsAbs(path) {
				path = filepath.Join(m.app.Workspace, path)
			}
			if err := writeFileSafe(path, out); err != nil {
				appendInfo(errorStyle.Render("write: " + err.Error()))
			} else {
				appendInfo(okStyle.Render("exported to " + path))
			}
		}

	case "/config":
		// 用 /theme / /model 等更简洁，这里只实现 set 形式
		if len(args) >= 3 && args[0] == "set" {
			if out := m.configSet(args[1], args[2]); out != "" {
				appendInfo(out)
			}
		} else {
			appendInfo("usage: /config set <key> <value>\nkeys: provider.type, provider.base_url, provider.api_key, provider.model, render.theme")
		}

	case "/rollback":
		appendInfo(m.handleRollback(args))

	default:
		appendInfo(errorStyle.Render("unknown command: " + cmd))
	}
	m.refreshViewport()
	return m, nil
}

// configSet 处理 /config set key value。
func (m *Model) configSet(key, value string) string {
	cfg := m.app.Config
	switch key {
	case "provider.type":
		if value != "openai" && value != "anthropic" {
			return errorStyle.Render("type must be 'openai' or 'anthropic'")
		}
		cfg.Provider.Type = value
	case "provider.base_url":
		cfg.Provider.BaseURL = value
	case "provider.api_key":
		cfg.Provider.APIKey = value
	case "provider.model":
		cfg.Provider.Model = value
	case "render.theme":
		m.renderer.setTheme(value)
		cfg.Render.Theme = value
		if err := m.app.SaveConfig(); err != nil {
			return errorStyle.Render("save: " + err.Error())
		}
		return okStyle.Render("render.theme = " + value)
	default:
		return errorStyle.Render("unknown key: " + key)
	}

	if cfg.Provider.Model == "" {
		return errorStyle.Render("provider.model is required")
	}
	if err := m.app.RebuildProvider(); err != nil {
		return errorStyle.Render("rebuild: " + err.Error())
	}
	if err := m.app.SaveConfig(); err != nil {
		return errorStyle.Render("save: " + err.Error())
	}
	return okStyle.Render(key + " = " + value)
}

// --- 辅助 ---

func (m *Model) appendBlock(b block) {
	m.blocks = append(m.blocks, b)
}

func (m *Model) lastThinkingIdx() int {
	for i := len(m.blocks) - 1; i >= 0; i-- {
		if m.blocks[i].kind == blockThinking {
			return i
		}
	}
	return -1
}

// finalizeThinking 把所有活跃的 thinking 块标记为完成。
func (m *Model) finalizeThinking() {
	for i := range m.blocks {
		if m.blocks[i].kind == blockThinking && m.blocks[i].thinkingActive {
			m.blocks[i].thinkingActive = false
		}
	}
}

// finalizeAssistant 把所有流式中的 assistant 块标记为完成（切换到全量 glamour 渲染）。
func (m *Model) finalizeAssistant() {
	for i := range m.blocks {
		if m.blocks[i].kind == blockAssistant && m.blocks[i].streaming {
			m.blocks[i].streaming = false
		}
	}
}

// updateSlashPanel 根据当前输入刷新斜杠命令浮层。
func (m *Model) updateSlashPanel() {
	input := m.textarea.Value()
	// 只在单行、以 / 开头、且尚未输入到参数阶段时激活
	trimmed := strings.TrimSpace(input)
	if !strings.HasPrefix(trimmed, "/") || strings.Contains(input, "\n") {
		m.slashOpen = false
		m.slashCursor = 0
		return
	}
	// 输入里有空格 → 用户已经在输入参数，关闭浮层
	if strings.Contains(trimmed, " ") {
		m.slashOpen = false
		m.slashCursor = 0
		return
	}
	m.slashFiltered = filterCommands(trimmed)
	if len(m.slashFiltered) > 0 {
		m.slashOpen = true
		if m.slashCursor >= len(m.slashFiltered) {
			m.slashCursor = 0
		}
		return
	}
	m.slashOpen = false
	m.slashCursor = 0
}

// selectSlash 从浮层选中一个命令填入输入框。
func (m Model) selectSlash() Model {
	if !m.slashOpen || len(m.slashFiltered) == 0 {
		return m
	}
	selected := m.slashFiltered[m.slashCursor].Name
	m.textarea.SetValue(selected + " ")
	m.textarea.CursorEnd()
	m.slashOpen = false
	return m
}

// applyLayout 按窗口尺寸重新排版。
func (m *Model) applyLayout() {
	if m.width < 20 || m.height < 10 {
		return
	}

	taHeight := 3
	// 输入框 Width(m.width-2) 减去 border(2) + padding(2) = 内容区 m.width-6
	m.textarea.SetWidth(m.width - 6)
	m.textarea.SetHeight(taHeight)

	// 布局：viewport + input(taHeight+border 2) + status(1)
	// JoinVertical 不添加额外间距行
	vpHeight := m.height - taHeight - 2 /*input border*/ - 1 /*status*/
	if vpHeight < 5 {
		vpHeight = 5
	}
	m.viewport.Width = m.width
	m.viewport.Height = vpHeight
	m.renderer.setWidth(m.width - 2)
}

// refreshViewport 重新生成 viewport 内容。
func (m *Model) refreshViewport() {
	content := m.buildViewportContent()
	m.viewport.SetContent(content)

	// 滚动策略：
	//   - 运行中且 autoFollow：跟随最新内容
	//   - 运行结束：停留在本轮第一行输出（turnAnchor）
	//   - 用户手动滚动：不打扰
	switch {
	case m.running && m.autoFollow:
		m.viewport.GotoBottom()
	case !m.running && m.turnAnchor > 0 && m.autoFollow:
		// 只在刚结束时设置一次，之后 autoFollow 还原为 true 以便滚动条继续工作
		m.viewport.SetYOffset(m.turnAnchor - 1)
		m.turnAnchor = 0
	}
}

// renderStatusDetail 生成 /status 详细输出。
func (m Model) renderStatusDetail() string {
	sess := m.app.Sessions.Get(m.sessionID)
	model := "none"
	if m.app.Provider != nil {
		model = m.app.Provider.Model()
	}
	return fmt.Sprintf("model:     %s\nsession:   %s\nmessages:  %d\ncontext:   %d/%d tokens\nlast call: ↑%d ↓%d\nsession:   ↑%d ↓%d",
		accentStyle.Render(model),
		m.sessionID,
		len(sess.History()),
		sess.TokenEstimate(), m.app.Config.MaxContextTokens,
		m.lastUsage.PromptTokens, m.lastUsage.CompletionTokens,
		m.sessionUsage.PromptTokens, m.sessionUsage.CompletionTokens,
	)
}

// renderContext 生成 /context 输出。
func (m Model) renderContext() string {
	bd := m.app.GetContextBreakdown()
	sess := m.app.Sessions.Get(m.sessionID)
	var sb strings.Builder
	sb.WriteString(accentStyle.Render("System Prompt Breakdown") + "\n")
	sb.WriteString(fmt.Sprintf("  base prompt:  ~%d tokens\n", bd.BasePrompt))
	line := fmt.Sprintf("  skills:       ~%d tokens", bd.Skills)
	if len(bd.SkillNames) > 0 {
		line += dimStyle.Render("  ("+strings.Join(bd.SkillNames, ", ")+")")
	}
	sb.WriteString(line + "\n")
	line = fmt.Sprintf("  bootstrap:    ~%d tokens", bd.Bootstrap)
	if len(bd.BootstrapFile) > 0 {
		line += dimStyle.Render("  ("+strings.Join(bd.BootstrapFile, ", ")+")")
	}
	sb.WriteString(line + "\n")
	if bd.Extra > 0 {
		sb.WriteString(fmt.Sprintf("  extra:        ~%d tokens\n", bd.Extra))
	}
	sb.WriteString(dimStyle.Render("  ─────────────────────") + "\n")
	sb.WriteString(fmt.Sprintf("  system total: ~%d tokens\n\n", bd.Total))

	sb.WriteString(accentStyle.Render("Session") + "\n")
	sb.WriteString(fmt.Sprintf("  messages:     %d\n", len(sess.History())))
	sb.WriteString(fmt.Sprintf("  tokens:       ~%d\n", sess.TokenEstimate()))
	sb.WriteString(fmt.Sprintf("  budget:       %d\n", m.app.Config.MaxContextTokens))
	usage := float64(sess.TokenEstimate()+bd.Total) / float64(m.app.Config.MaxContextTokens) * 100
	sb.WriteString(fmt.Sprintf("  usage:        %.1f%%", usage))
	return sb.String()
}

func estimateTokens(s string) int {
	if s == "" {
		return 0
	}
	t := (len([]rune(s)) + 3) / 4
	if t < 1 {
		return 1
	}
	return t
}

// contentLineCount 返回当前 viewport 内容的行数（用作锚点）。
func (m *Model) contentLineCount() int {
	content := m.buildViewportContent()
	return strings.Count(content, "\n") + 1
}

// buildViewportContent 纯生成内容字符串，不改 viewport 状态。
func (m *Model) buildViewportContent() string {
	var sb strings.Builder
	if !m.bannerShown && len(m.blocks) == 0 {
		model := "none"
		if m.app.Provider != nil {
			model = m.app.Provider.Model()
		}
		sb.WriteString(renderBanner("AgentGo", model, m.app.Workspace, m.sessionID, len(m.app.Skills)))
		sb.WriteString("\n\n")
	}
	if len(m.blocks) > 0 {
		sb.WriteString(m.renderer.renderBlocks(m.blocks))
		sb.WriteString("\n")
	}
	// 运行中指示器：紧贴最后一个 block 下方（通常是 user 提问），放在模型回复上方
	// 仅当"当前没有正在流式的 thinking/assistant 块"时显示，避免与 thinking 块动画重复
	if m.running && !m.hasActiveStreaming() {
		sb.WriteString("\n")
		sb.WriteString(m.inlineRunningIndicator())
		sb.WriteString("\n")
	}
	return sb.String()
}

// hasActiveStreaming 判断聊天流尾部是否已经有一个"正在流动"的块（thinking 或 assistant 流式中）。
// 有的话，就不额外渲染 inline 指示器，避免视觉重复。
func (m *Model) hasActiveStreaming() bool {
	if len(m.blocks) == 0 {
		return false
	}
	last := m.blocks[len(m.blocks)-1]
	switch {
	case last.kind == blockThinking && last.thinkingActive:
		return true
	case last.kind == blockAssistant && last.streaming:
		return true
	case last.kind == blockTool && !last.toolDone:
		// 工具执行中，tool 块本身就是指示器（◌ 标记），不再叠加
		return true
	}
	return false
}

// inlineRunningIndicator 生成内容流里的 "⠙ thinking / running tool / writing · N tokens" 指示行。
func (m *Model) inlineRunningIndicator() string {
	frames := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
	frame := int(time.Since(m.runStartedAt).Milliseconds()/80) % len(frames)
	icon := lipgloss.NewStyle().Foreground(colorSpinner).Render(frames[frame])

	phase := m.phase
	if phase == phaseIdle {
		phase = phaseThinking
	}
	// phase 文字用渐变 pulse，营造"正在思考"的活感
	label := renderPulse(phase.label(), frame)

	parts := []string{icon, label}
	if m.liveOutTok > 0 {
		parts = append(parts, dimStyle.Render(fmt.Sprintf("· %s tokens", formatNum(m.liveOutTok))))
	}
	parts = append(parts, dimStyle.Render("· esc esc to cancel"))
	return strings.Join(parts, "  ")
}

// runningLabel 已由 inlineRunningIndicator 取代（指示行移到内容流里）。
// 保留为空函数避免其他地方引用报错。
func (m Model) runningLabel() string { return "" }

// checkpointAfterTurn — 已废弃，SealTurn 现由 Runner.Run() 自动管理。
// 保留为空函数避免其他地方引用报错。
func (m *Model) checkpointAfterTurn() {}

// handleRollback 处理 /rollback 命令。
func (m *Model) handleRollback(args []string) string {
	if m.app.Checkpoint == nil {
		return errorStyle.Render("checkpoint not available")
	}

	turns := m.app.Checkpoint.List()

	if len(args) == 0 || args[0] == "list" {
		if len(turns) == 0 {
			return dimStyle.Render("no checkpoints yet — make a file change first")
		}
		var sb strings.Builder
		sb.WriteString(accentStyle.Render("Available checkpoints (newest first):") + "\n\n")
		for i, t := range turns {
			age := time.Since(t.CreatedAt).Round(time.Second)
			files := fmt.Sprintf("%d file", len(t.Entries))
			if len(t.Entries) != 1 {
				files += "s"
			}
			idx := fmt.Sprintf("[%d]", i+1)
			sb.WriteString(fmt.Sprintf("  %s %s · %s · %s ago\n",
				accentStyle.Render(idx),
				okStyle.Render(t.ID),
				dimStyle.Render(files),
				dimStyle.Render(age.String())))
			sb.WriteString("      " + dimStyle.Render("› "+t.UserInput) + "\n")
		}
		sb.WriteString("\n")
		sb.WriteString(dimStyle.Render("Usage: /rollback <index>    (e.g. /rollback 1 reverts the most recent turn)"))
		return sb.String()
	}

	// 解析索引或 turn-id
	target := args[0]
	var turnID string
	if idx, err := strconv.Atoi(target); err == nil {
		if idx < 1 || idx > len(turns) {
			return errorStyle.Render(fmt.Sprintf("invalid index %d (have %d checkpoints)", idx, len(turns)))
		}
		turnID = turns[idx-1].ID
	} else {
		turnID = target
	}

	n, err := m.app.Checkpoint.Rollback(turnID)
	if err != nil {
		return errorStyle.Render("rollback failed: " + err.Error())
	}
	return okStyle.Render(fmt.Sprintf("✓ rolled back %d file(s) to state before turn %s", n, turnID))
}

// canNavigateHistory 判断当前是否可以用 ↑/↓ 浏览历史。
// 策略：输入框只有一行、且未在斜杠浮层模式下。
func (m *Model) canNavigateHistory() bool {
	if m.slashOpen || m.running || m.awaitingApproval {
		return false
	}
	if len(m.history) == 0 {
		return false
	}
	val := m.textarea.Value()
	// 多行输入时禁用，避免干扰 textarea 的内部光标上下移动
	return !strings.Contains(val, "\n")
}

// historyAdd 追加一条历史（去重 + 去连续重复）。
func (m *Model) historyAdd(input string) {
	if input == "" {
		return
	}
	if n := len(m.history); n > 0 && m.history[n-1] == input {
		// 与上一条相同则跳过
	} else {
		m.history = append(m.history, input)
		m.saveHistoryToDisk(input)
	}
	// 限制历史长度
	const maxHistory = 500
	if len(m.history) > maxHistory {
		m.history = m.history[len(m.history)-maxHistory:]
	}
	m.historyIdx = len(m.history)
	m.historyDraft = ""
}

// historyPrev 显示上一条历史。
func (m *Model) historyPrev() {
	if m.historyIdx == len(m.history) {
		m.historyDraft = m.textarea.Value()
	}
	if m.historyIdx > 0 {
		m.historyIdx--
		m.textarea.SetValue(m.history[m.historyIdx])
		m.textarea.CursorEnd()
	}
}

// historyNext 显示下一条历史；若到底则恢复草稿。
func (m *Model) historyNext() {
	if m.historyIdx >= len(m.history) {
		return
	}
	m.historyIdx++
	if m.historyIdx == len(m.history) {
		m.textarea.SetValue(m.historyDraft)
	} else {
		m.textarea.SetValue(m.history[m.historyIdx])
	}
	m.textarea.CursorEnd()
}

// loadHistoryFromDisk 从 .agent/.cli_history 读取历史记录。
func (m *Model) loadHistoryFromDisk() {
	path := filepath.Join(m.app.Workspace, ".agent", ".cli_history")
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	for _, ln := range lines {
		ln = strings.TrimSpace(ln)
		if ln == "" {
			continue
		}
		m.history = append(m.history, ln)
	}
	m.historyIdx = len(m.history)
}

// saveHistoryToDisk 追加一条历史到磁盘。
func (m *Model) saveHistoryToDisk(input string) {
	path := filepath.Join(m.app.Workspace, ".agent", ".cli_history")
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.WriteString(input + "\n")
}
