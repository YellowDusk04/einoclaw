package main

import (
	"encoding/json"
	"fmt"
	"log"
	"slices"
	"sort"
	"strconv"
	"strings"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/list"
	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/middlewares/filesystem"
	"github.com/cloudwego/eino/adk/middlewares/permission"
	"github.com/cloudwego/eino/adk/middlewares/summarization"
)

// tuiMode 当前界面模式
type tuiMode int

const (
	modeChat tuiMode = iota
	modePermission

	banner = `
███████╗██╗███╗   ██╗ ██████╗  ██████╗██╗      █████╗ ██╗    ██╗
██╔════╝██║████╗  ██║██╔═══██╗██╔════╝██║     ██╔══██╗██║    ██║
█████╗  ██║██╔██╗ ██║██║   ██║██║     ██║     ███████║██║ █╗ ██║
██╔══╝  ██║██║╚██╗██║██║   ██║██║     ██║     ██╔══██║██║███╗██║
███████╗██║██║ ╚████║╚██████╔╝╚██████╗███████╗██║  ██║╚███╔███╔╝
╚══════╝╚═╝╚═╝  ╚═══╝ ╚═════╝  ╚═════╝╚══════╝╚═╝  ╚═╝ ╚══╝╚══╝ 
                                                                `

	defaultToolResultLines = 10

	toolNameSkill = "skill"
)

// pendingMessage 已 push 但尚未进入框架处理管道的用户消息
type pendingMessage struct {
	id       string
	text     string
	consumed bool
}

// teaModel 是 Bubble Tea 的顶层 model
type teaModel struct {
	width  int
	height int

	chatList  *chatList
	inputArea textarea.Model
	helpArea  help.Model
	keyMap    appKeyMap
	focus     focusArea // 当前焦点区域

	// 抢占等待队列
	pendingMessages []*pendingMessage
	pendingExpanded bool

	// 权限审批
	mode                  tuiMode
	permissionCmd         string
	permissionInterruptID string
	permissionList        list.Model
	respondInput          textinput.Model
	respondMode           bool // 选中"拒绝并回复"后，输入回复文本

	// 模型信息
	modelIndex int
	modelName  string

	// token 用量
	promptTokens int
	cachedTokens int
}

type focusArea int

const (
	focusInput focusArea = iota
	focusChat
)

// appKeyMap 键盘映射
type appKeyMap struct {
	Up            key.Binding
	Down          key.Binding
	PageUp        key.Binding
	PageDown      key.Binding
	Top           key.Binding
	Bottom        key.Binding
	ToggleHelp    key.Binding
	TogglePending key.Binding
	Newline       key.Binding
	Send          key.Binding
	Quit          key.Binding
	FocusChat     key.Binding
}

func (k appKeyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.Up, k.Down, k.Newline, k.ToggleHelp, k.TogglePending, k.Send, k.Quit, k.FocusChat}
}

func (k appKeyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{k.Up, k.Down, k.PageUp, k.PageDown},
		{k.Top, k.Bottom, k.Send, k.Quit},
	}
}

// newTeaModel 创建初始 model
func newTeaModel() teaModel {
	ta := textarea.New()
	ta.Placeholder = " Type your message... (Enter to send, Ctrl+j for newline)"
	ta.SetHeight(3)
	ta.ShowLineNumbers = false
	ta.CharLimit = 0

	// 基于默认暗色样式，逐一覆盖
	styles := textarea.DefaultDarkStyles()
	styles.Focused.Prompt = lipgloss.NewStyle().Foreground(lipgloss.Color("63"))
	styles.Blurred.Prompt = lipgloss.NewStyle().Foreground(lipgloss.Color("63"))
	styles.Focused.Text = lipgloss.NewStyle().Foreground(lipgloss.Color("15"))
	styles.Blurred.Text = lipgloss.NewStyle().Foreground(lipgloss.Color("15"))
	styles.Focused.Placeholder = lipgloss.NewStyle().Foreground(lipgloss.Color("243"))
	styles.Blurred.Placeholder = lipgloss.NewStyle().Foreground(lipgloss.Color("243"))
	styles.Cursor.Color = lipgloss.Color("63") // 紫色光标
	// 光标行不用黑色背景，保持透明
	styles.Focused.CursorLine = lipgloss.NewStyle()
	styles.Blurred.CursorLine = lipgloss.NewStyle()
	ta.SetStyles(styles)

	// 换行改为 Ctrl+J，Enter 留给发送
	ta.KeyMap.InsertNewline = key.NewBinding(key.WithKeys("ctrl+j"), key.WithHelp("ctrl+j", "newline"))

	ta.Focus()

	return teaModel{
		chatList:   newChatList(80, 5),
		inputArea:  ta,
		helpArea:   help.New(),
		focus:      focusInput,
		modelIndex: 0,
		modelName:  cfg.Models[0].ModelName,
		keyMap: appKeyMap{
			Up:         key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑/k", "up")),
			Down:       key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("↓/j", "down")),
			PageUp:     key.NewBinding(key.WithKeys("pgup", "ctrl+b"), key.WithHelp("pgup", "page up")),
			PageDown:   key.NewBinding(key.WithKeys("pgdown", "ctrl+f", " "), key.WithHelp("pgdn", "page down")),
			Top:        key.NewBinding(key.WithKeys("home", "g"), key.WithHelp("home", "top")),
			Bottom:     key.NewBinding(key.WithKeys("end", "G"), key.WithHelp("end", "bottom")),
			ToggleHelp:    key.NewBinding(key.WithKeys("ctrl+h"), key.WithHelp("ctrl+h", "help")),
			TogglePending: key.NewBinding(key.WithKeys("ctrl+p"), key.WithHelp("ctrl+p", "queue")),
			Newline:       key.NewBinding(key.WithKeys("ctrl+j"), key.WithHelp("ctrl+j", "newline")),
			Send:          key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "send")),
			Quit:       key.NewBinding(key.WithKeys("ctrl+c"), key.WithHelp("ctrl+c", "quit")),
			FocusChat:  key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "chat")),
		},
	}
}

// Init 初始化。Bubble Tea v2 中 Init 返回 Cmd (func() Msg)，
// textarea.Blink() 和 tea.RequestWindowSize 返回 Msg，需要包装为 Cmd。
func (m teaModel) Init() tea.Cmd {
	return tea.Batch(
		textarea.Blink,
		tea.RequestWindowSize,
	)
}

// Update 消息处理
func (m teaModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	case tea.KeyPressMsg:
		return m, m.handleKeyPress(msg)

	case tea.PasteMsg:
		if m.focus == focusInput {
			var cmd tea.Cmd
			m.inputArea, cmd = m.inputArea.Update(msg)
			return m, cmd
		}
		if m.mode == modePermission && m.respondMode {
			var cmd tea.Cmd
			m.respondInput, cmd = m.respondInput.Update(msg)
			return m, cmd
		}
		return m, nil

	case permissionAskMsg:
		m.mode = modePermission
		m.permissionCmd = msg.cmd
		m.permissionInterruptID = msg.interrupt
		m.respondMode = false
		m.initPermissionList()
		m.layout()
		m.focus = focusChat
		m.inputArea.Blur()
		return m, nil

	case summarizationEventMsg:
		if msg.actionType == summarization.ActionTypeBeforeSummarize {
			m.chatList.appendEvent(newChatEvent(eventSummarization, nil, []string{"[summarizing...]"}))
		} else {
			m.chatList.appendEvent(newChatEvent(eventSummarization,
				[]string{"[summarized]"},
				append([]string{"[summarized]"}, strings.Split(msg.content, "\n")...)))
		}
		return m, nil

	case aiTextChunkMsg:
		m.handleTextChunk(msg.text, m.chatList)
		return m, nil

	case aiThinkingChunkMsg:
		m.handleThinkingChunk(msg.text, m.chatList)
		return m, nil

	case toolCallMsg:
		m.handleToolCalls(msg)
		return m, nil

	case toolResultMsg:
		collapsed, expanded := renderToolResult(msg.Name, msg.Content)
		m.chatList.appendEvent(newChatEvent(eventToolResult, expanded, collapsed))
		return m, nil

	case tokenUsageMsg:
		m.promptTokens = msg.promptTokens
		m.cachedTokens = msg.cachedTokens
		m.layout()
		return m, nil

	case ackMsg:
		return m, m.handleAck(msg)

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.layout()
		return m, nil

	case tea.MouseMsg:
		return m, m.handleMouse(msg)
	}

	return m, nil
}

func (m *teaModel) handleMouse(msg tea.MouseMsg) tea.Cmd {
	switch msg := msg.(type) {
	case tea.MouseWheelMsg:
		switch msg.Button {
		case tea.MouseWheelUp:
			m.chatList.scrollUp(1)
		case tea.MouseWheelDown:
			m.chatList.scrollDown(1)
		}
	case tea.MouseClickMsg:
		if msg.Button == tea.MouseLeft {
			// 先判断是否点到 pending queue 区域（紧接在 chatView + 分隔行之后）
			pendingStart := m.chatList.height + 1
			pendingH := m.pendingQueueHeight()
			if pendingH > 0 && msg.Y >= pendingStart && msg.Y < pendingStart+pendingH {
				m.pendingExpanded = !m.pendingExpanded
				m.layout()
			} else {
				m.chatList.toggleEventAtRow(msg.Y)
			}
		}
	}
	return nil
}

// handleKeyPress 统一处理所有按键事件，根据 key+focus 分发。
func (m *teaModel) handleKeyPress(msg tea.KeyPressMsg) tea.Cmd {
	// ── 全局快捷键（无视焦点） ──
	switch {
	case key.Matches(msg, m.keyMap.Quit):
		turnLoop.Stop(adk.WithImmediate())
		turnLoop.Wait()
		return tea.Quit

	case key.Matches(msg, m.keyMap.FocusChat):
		if m.focus == focusChat {
			m.focus = focusInput
			m.inputArea.Focus()
		} else {
			m.focus = focusChat
			m.inputArea.Blur()
		}
		return nil

	case key.Matches(msg, m.keyMap.ToggleHelp):
		m.helpArea.ShowAll = !m.helpArea.ShowAll
		m.layout()
		return nil

	case key.Matches(msg, m.keyMap.TogglePending):
		m.pendingExpanded = !m.pendingExpanded
		m.layout()
		return nil
	}

	// ── 权限模式 ──
	if m.mode == modePermission {
		return m.handlePermissionKey(msg)
	}

	// ── 按焦点分发 ──
	switch m.focus {
	case focusChat:
		return m.handleChatScrollKey(msg)

	case focusInput:
		return m.handleInputKey(msg)
	}
	return nil
}

func (m *teaModel) handleChatScrollKey(msg tea.KeyPressMsg) tea.Cmd {
	switch {
	case key.Matches(msg, m.keyMap.Up):
		m.chatList.scrollUp(1)
	case key.Matches(msg, m.keyMap.Down):
		m.chatList.scrollDown(1)
	case key.Matches(msg, m.keyMap.PageUp):
		m.chatList.pageUp()
	case key.Matches(msg, m.keyMap.PageDown):
		m.chatList.pageDown()
	case key.Matches(msg, m.keyMap.Top):
		m.chatList.scrollToTop()
	case key.Matches(msg, m.keyMap.Bottom):
		m.chatList.scrollToBottom()
	default:
		// 任意键回到输入框
		m.focus = focusInput
		m.inputArea.Focus()
	}
	return nil
}

func (m *teaModel) handleInputKey(msg tea.KeyPressMsg) tea.Cmd {
	// Enter → 发送或换行
	if key.Matches(msg, m.keyMap.Send) {
		text := strings.TrimSpace(m.inputArea.Value())
		if text != "" {
			m.inputArea.Reset()
			handled, cmd := m.handleCommand(text)
			if handled {
				return cmd
			}
			return m.sendUserMessage(text)
		}
	}
	// 其余按键委托给 textarea
	var cmd tea.Cmd
	m.inputArea, cmd = m.inputArea.Update(msg)
	return cmd
}

func (m *teaModel) handlePermissionKey(msg tea.KeyPressMsg) tea.Cmd {
	if m.respondMode {
		// respond 输入模式
		if key.Matches(msg, m.keyMap.Send) {
			text := strings.TrimSpace(m.respondInput.Value())
			turnLoop.Resume(chatItem{
				interruptId: m.permissionInterruptID,
				query:       text,
				action:      permission.ResumeActionRespond,
			})
			m.exitPermission()
		}
		var cmd tea.Cmd
		m.respondInput, cmd = m.respondInput.Update(msg)
		return cmd
	}

	// 列表模式 —— list 自己处理方向键，我们只拦截 enter
	if key.Matches(msg, m.keyMap.Send) {
		idx := m.permissionList.Index()
		switch idx {
		case 0:
			turnLoop.Resume(chatItem{
				interruptId: m.permissionInterruptID,
				action:      permission.ResumeActionApprove,
			})
			m.exitPermission()
		case 1:
			turnLoop.Resume(chatItem{
				interruptId: m.permissionInterruptID,
				action:      permission.ResumeActionReject,
			})
			m.exitPermission()
		case 2:
			m.respondMode = true
			m.respondInput.Focus()
			m.layout()
		}
		return nil
	}

	var cmd tea.Cmd
	m.permissionList, cmd = m.permissionList.Update(msg)
	return cmd
}

func (m *teaModel) exitPermission() {
	m.mode = modeChat
	m.focus = focusInput
	m.inputArea.Focus()
	m.layout()
}

// appendMessageBlocks 解析 message 的 ContentBlocks 并追加到 chatList
func (m *teaModel) appendMessageBlocks(message adk.AgenticMessage) {
	for _, block := range message.ContentBlocks {
		if block.UserInputText != nil {
			text := block.UserInputText.Text
			m.chatList.appendEvent(newChatEvent(eventUser, []string{text}, nil))
		}
		if block.Reasoning != nil {
			text := block.Reasoning.Text
			collapsed, expanded := renderThinking(text, m.chatList.width)
			ev := newChatEvent(eventThinking, expanded, collapsed)
			m.chatList.appendEvent(ev)
		}
		if block.AssistantGenText != nil {
			text := block.AssistantGenText.Text
			lines := renderMarkdown(text, m.chatList.width)
			m.chatList.appendEvent(newChatEvent(eventAI, lines, nil))
		}
		if block.FunctionToolResult != nil {
			r := block.FunctionToolResult
			name := r.Name
			content := resultContent(r)
			collapsed, expanded := renderToolResult(name, content)
			m.chatList.appendEvent(newChatEvent(eventToolResult, expanded, collapsed))
		}
		if block.FunctionToolCall != nil {
			c := block.FunctionToolCall
			m.chatList.appendEvent(newChatEvent(eventToolCall,
				[]string{renderToolCall(c.Name, c.Arguments)}, nil))
		}
	}
}

func (m *teaModel) handleThinkingChunk(text string, cl *chatList) {
	// 已有 thinking event → 追加内容并重新渲染
	if len(cl.events) > 0 && cl.events[len(cl.events)-1].typ == eventThinking {
		last := cl.events[len(cl.events)-1]
		last.rawText.WriteString(text)
		txt := last.rawText.String()
		oldCount := len(last.visibleLines())
		last.collapsed, last.expanded = renderThinking(txt, cl.width)
		cl.totalLines += len(last.visibleLines()) - oldCount
	} else {
		// 新 thinking：直接追加（thinking 总是在 AI 消息之前到达）
		collapsed, expanded := renderThinking(text, cl.width)
		ev := newChatEvent(eventThinking, expanded, collapsed)
		ev.rawText.WriteString(text)
		cl.appendEvent(ev)
	}
	if cl.autoScroll {
		cl.scrollToBottom()
	}
}

func (m *teaModel) handleTextChunk(text string, cl *chatList) {
	if len(cl.events) == 0 || cl.events[len(cl.events)-1].typ != eventAI {
		ev := newChatEvent(eventAI, renderMarkdown(text, cl.width), nil)
		ev.streamingState = &aiStreamingState{
			stream:   &streamingMarkdown{},
			fullText: strings.Builder{},
		}
		ev.streamingState.fullText.WriteString(text)
		cl.appendEvent(ev)
	} else {
		last := cl.events[len(cl.events)-1]
		ss := last.streamingState
		if ss == nil {
			log.Fatal("streaming AI event without streamingState")
		}
		ss.fullText.WriteString(text)
		full := ss.fullText.String()
		rendered := ss.stream.Render(full, cl.width-4)
		prefix := lipgloss.NewStyle().Foreground(lipgloss.Color("14")).Render("● ")
		oldLines := len(last.visibleLines())
		last.expanded = append([]string{prefix + rendered[0]}, rendered[1:]...)
		cl.totalLines += len(last.visibleLines()) - oldLines
		if cl.autoScroll {
			cl.scrollToBottom()
		}
	}
}

// handleToolCalls 流结束后渲染 AI 发起的工具调用
func (m *teaModel) handleToolCalls(msg toolCallMsg) {
	for _, item := range msg.Items {
		m.chatList.appendEvent(newChatEvent(eventToolCall, []string{renderToolCall(item.Name, item.Args)}, nil))
	}
}

// handleAck 处理抢占 ack
func (m *teaModel) handleAck(msg ackMsg) tea.Cmd {
	for _, pm := range m.pendingMessages {
		if pm.id == msg.id && !pm.consumed {
			pm.consumed = true
			m.chatList.appendEvent(newChatEvent(eventUser, []string{pm.text}, nil))
			break
		}
	}
	return nil
}

// sendUserMessage 发送用户消息，返回监听 ack 的 Cmd
func (m *teaModel) sendUserMessage(text string) tea.Cmd {
	id := newID()
	ok, _ := turnLoop.Push(
		chatItem{id: id, query: text},
		adk.WithPreempt[chatItem, adk.AgenticMessage](adk.AnySafePoint),
	)
	if !ok {
		log.Fatal("turnLoop.Push() return false")
	}

	// 统一放入等待队列，GenInput 消费时由 program.Send(ackMsg) 触发移入 chatList
	m.pendingMessages = append(m.pendingMessages, &pendingMessage{id: id, text: text})
	return nil
}

// layout 计算各区域尺寸
func (m *teaModel) layout() {
	helpStr := m.helpArea.View(m.keyMap)
	helpHeight := lipgloss.Height(helpStr)

	pendingHeight := m.pendingQueueHeight()
	statusHeight := 1

	// 底部区域高度：审批模式用 dialog 高度，普通模式用 3 行输入框
	inputHeight := 3
	if m.mode == modePermission {
		inputHeight = lipgloss.Height(m.renderPermissionDialog())
	}

	// View() 中部件间插入的空行分隔数
	separatorCount := 2
	if pendingHeight > 0 {
		separatorCount++
	}

	chatHeight := max(m.height-separatorCount-statusHeight-inputHeight-helpHeight-pendingHeight, 5)

	m.chatList.width = m.width
	m.chatList.height = chatHeight
	m.inputArea.SetWidth(m.width - 4)
	m.helpArea.SetWidth(m.width)
	if m.mode == modePermission {
		m.permissionList.SetSize(m.width-4, 5)
		m.respondInput.SetWidth(m.width - 8)
	}
}

func (m *teaModel) pendingQueueHeight() int {
	active := 0
	for _, pm := range m.pendingMessages {
		if !pm.consumed {
			active++
		}
	}
	if active == 0 {
		return 0
	}
	if m.pendingExpanded {
		return 1 + active
	}
	return 1
}

// View 渲染
func (m teaModel) View() tea.View {
	chatView := m.chatList.view()
	if chatView == "" && len(m.pendingMessages) == 0 {
		chatView = m.renderBanner()
	}
	pendingView := m.renderPendingQueue()
	statusBar := m.renderStatusBar()
	helpView := m.helpArea.View(m.keyMap)

	var bottom string
	if m.mode == modePermission {
		bottom = m.renderPermissionDialog()
	} else {
		bottom = m.inputArea.View()
	}

	// 部件间用空行分隔，组件内部不添加多余空行
	// 布局: chatView → "" → (pendingView → "") → (statusBar) → bottom → "" → helpView
	// statusBar 和 bottom 之间无空行
	var parts []string
	parts = append(parts, chatView)
	parts = append(parts, "")
	if pendingView != "" {
		parts = append(parts, pendingView)
		parts = append(parts, "")
	}
	parts = append(parts, statusBar, bottom, "", helpView)
	content := lipgloss.JoinVertical(lipgloss.Top, parts...)
	return tea.View{
		Content:   content,
		AltScreen: true,
		MouseMode: tea.MouseModeCellMotion,
	}
}

func (m *teaModel) renderPendingQueue() string {
	var active []*pendingMessage
	for _, pm := range m.pendingMessages {
		if !pm.consumed {
			active = append(active, pm)
		}
	}
	if len(active) == 0 {
		return ""
	}

	headerStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("214")).Bold(true)
	msgStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("250"))
	pipeStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("63")).Bold(true)
	indicatorStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("243"))

	if !m.pendingExpanded {
		return lipgloss.NewStyle().Padding(0, 0, 0, 2).Render(
			headerStyle.Render(fmt.Sprintf("Waiting... (%d)", len(active))) + "  " +
				indicatorStyle.Render("[▶ expand]"),
		)
	}

	var lines []string
	lines = append(lines, lipgloss.NewStyle().Padding(0, 0, 0, 2).Render(
		headerStyle.Render(fmt.Sprintf("Waiting... (%d)", len(active)))+"  "+
			indicatorStyle.Render("[▼ collapse]"),
	))
	for _, pm := range active {
		lines = append(lines, "  "+pipeStyle.Render("│")+" "+msgStyle.Render(pm.text))
	}
	return strings.Join(lines, "\n")
}

// renderThinking 渲染思考内容为灰色背景块，自动换行，左右各留 2 列空白。
func renderThinking(content string, width int) (collapsed, expanded []string) {
	bg := lipgloss.NewStyle().Background(lipgloss.Color("235")).Foreground(lipgloss.Color("250"))
	indent := 2
	textWidth := width - indent - 2 // 左缩进 2，右边留白 2

	wrapText := func(s string) []string {
		var result []string
		var lineRunes []rune
		lineW := 0
		for para := range strings.SplitSeq(s, "\n") {
			if strings.TrimSpace(para) == "" {
				continue
			}
			for _, r := range para {
				rw := lipgloss.Width(string(r))
				if lineW+rw > textWidth && len(lineRunes) > 0 {
					result = append(result, string(lineRunes))
					lineRunes = lineRunes[:0]
					lineW = 0
				}
				lineRunes = append(lineRunes, r)
				lineW += rw
			}
		}
		if len(lineRunes) > 0 {
			result = append(result, string(lineRunes))
		}
		return result
	}

	renderLine := func(l string) string {
		lw := lipgloss.Width(l)
		padding := max(textWidth-lw, 0)
		return strings.Repeat(" ", indent) + bg.Render(l+strings.Repeat(" ", padding))
	}

	renderBlock := func(ls []string) []string {
		res := make([]string, len(ls))
		for i, l := range ls {
			res[i] = renderLine(l)
		}
		return res
	}

	lines := wrapText(content)
	n := len(lines)

	const defaultLines = 10
	if n <= defaultLines {
		block := renderBlock(lines)
		return block, block
	}
	// collapsed 取前 10 行，expanded 取全部
	return renderBlock(lines[:defaultLines]), renderBlock(lines)
}

// renderBanner 用渐变色渲染 banner（从青色到紫色）
func (m *teaModel) renderBanner() string {
	if m.width < 10 {
		return ""
	}
	bannerLines := strings.Split(strings.Trim(banner, "\n"), "\n")

	// 渐变：从青色 (14) 过渡到紫色 (63)
	colors := []string{"14", "39", "63", "99", "135", "171", "207"}
	var styled []string
	for i, line := range bannerLines {
		c := lipgloss.Color(colors[i])
		s := lipgloss.NewStyle().Foreground(c).Align(lipgloss.Center).Width(m.width).Render(line)
		styled = append(styled, s)
	}
	// 底部加一行提示
	hint := lipgloss.NewStyle().
		Foreground(lipgloss.Color("243")).
		Align(lipgloss.Center).
		Width(m.width).
		Render("Type a message to get started...")
	styled = append(styled, hint)
	return strings.Join(styled, "\n")
}

func (m *teaModel) renderStatusBar() string {
	ctxStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("14")).Bold(true)
	cacheStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("63")).Bold(true)
	modelStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("213")).Bold(true)
	return fmt.Sprintf("  Context %s  │  Cache %s  │  %s",
		ctxStyle.Render(formatNum(m.promptTokens)),
		cacheStyle.Render(formatNum(m.cachedTokens)),
		modelStyle.Render(m.modelName))
}

// plistItem 权限审批列表项
type plistItem struct {
	label string
	desc  string
}

func (i plistItem) FilterValue() string { return i.label }
func (i plistItem) Title() string       { return i.label }
func (i plistItem) Description() string { return i.desc }

func (m *teaModel) initPermissionList() {
	items := []list.Item{
		plistItem{label: "批准执行", desc: "Approve"},
		plistItem{label: "拒绝执行", desc: "Reject"},
		plistItem{label: "拒绝并回复", desc: "Respond with message"},
	}
	d := list.NewDefaultDelegate()
	d.ShowDescription = false
	d.SetHeight(1)
	d.SetSpacing(0)
	d.Styles = plistStyles()
	l := list.New(items, d, m.width-4, 5)
	l.SetShowTitle(true)
	l.Title = "权限审批 — " + m.permissionCmd
	l.Styles.TitleBar = lipgloss.NewStyle().Padding(0, 0, 1, 2)
	l.Styles.Title = lipgloss.NewStyle().Foreground(lipgloss.Color("14")).Bold(true)
	l.SetShowHelp(false)
	l.SetShowStatusBar(false)
	l.SetShowPagination(false)
	l.SetShowFilter(false)
	l.DisableQuitKeybindings()
	l.KeyMap.Quit.Unbind()
	l.KeyMap.ForceQuit.Unbind()
	m.permissionList = l

	// init respond input
	ti := textinput.New()
	ti.Placeholder = "输入回复理由…"
	ti.SetWidth(m.width - 8)
	ti.SetStyles(plistInputStyles())
	m.respondInput = ti
}

func plistStyles() list.DefaultItemStyles {
	// 从零构建，不用默认背景色
	s := list.DefaultItemStyles{
		SelectedTitle: lipgloss.NewStyle().
			Border(lipgloss.NormalBorder(), false, false, false, true).
			BorderForeground(lipgloss.Color("63")).
			Foreground(lipgloss.Color("63")).
			Padding(0, 0, 0, 1),
		NormalTitle: lipgloss.NewStyle().
			Foreground(lipgloss.Color("15")).
			Padding(0, 0, 0, 2),
		DimmedTitle: lipgloss.NewStyle().
			Foreground(lipgloss.Color("243")).
			Padding(0, 0, 0, 2),
	}
	return s
}

func plistInputStyles() textinput.Styles {
	s := textinput.DefaultDarkStyles()
	s.Focused.Prompt = lipgloss.NewStyle().Foreground(lipgloss.Color("63"))
	s.Blurred.Prompt = lipgloss.NewStyle().Foreground(lipgloss.Color("63"))
	s.Focused.Text = lipgloss.NewStyle().Foreground(lipgloss.Color("15"))
	s.Blurred.Text = lipgloss.NewStyle().Foreground(lipgloss.Color("15"))
	s.Focused.Placeholder = lipgloss.NewStyle().Foreground(lipgloss.Color("243"))
	s.Blurred.Placeholder = lipgloss.NewStyle().Foreground(lipgloss.Color("243"))
	s.Cursor.Color = lipgloss.Color("63")
	return s
}

func (m *teaModel) renderPermissionDialog() string {
	if m.respondMode {
		return m.respondInput.View()
	}
	return m.permissionList.View()
}

// handleCommand 处理斜杠命令，返回 (已处理, 后续 Cmd)
func (m *teaModel) handleCommand(text string) (bool, tea.Cmd) {
	if !strings.HasPrefix(text, "/") {
		return false, nil
	}
	parts := strings.SplitN(text[1:], " ", 2)
	cmd := parts[0]

	switch cmd {
	case "exit":
		turnLoop.Stop()
		turnLoop.Wait()
		return true, tea.Quit

	case "model":
		if len(parts) > 1 {
			if idx, err := strconv.Atoi(parts[1]); err == nil && idx >= 0 && idx < len(cfg.Models) {
				loadModelAndAgent(idx)
				m.modelIndex = idx
				m.modelName = cfg.Models[idx].ModelName
			}
		}
		return true, nil

	case "resume":
		if len(parts) > 1 {
			m.resumeSession(parts[1])
		}
		return true, nil

	default:
		return false, nil
	}
}

// resumeSession 恢复历史 session
func (m *teaModel) resumeSession(newID string) {
	sessionID = newID
	turnLoop.Stop()
	turnLoop.Wait()
	initTurnLoop()
	result, err := sessionStore.LoadEvents(ctx, newID,
		&adk.LoadSessionEventsRequest{
			Limit: 0,
			Kinds: []adk.SessionEventKind{adk.SessionEventMessage},
		})
	if err != nil {
		log.Fatalf("load events error: %v\n", err)
	}
	// 直接在 cmd 中重建 chatList，无需中转消息
	m.chatList = newChatList(m.width, m.chatList.height)
	for _, ev := range result.Events {
		if ev.Message != nil {
			m.appendMessageBlocks(ev.Message)
		}
	}
}

var idCounter int

func newID() string {
	idCounter++
	return fmt.Sprintf("msg-%d", idCounter)
}

func formatNum(n int) string {
	if n < 1024 {
		return fmt.Sprintf("%d", n)
	}
	return fmt.Sprintf("%.1fK", float64(n)/1024)
}

var (
	noNeedToCollapseTools = []string{
		filesystem.ToolNameLs,
		filesystem.ToolNameReadFile,
		filesystem.ToolNameWriteFile,
		filesystem.ToolNameEditFile,
		filesystem.ToolNameGlob,
		filesystem.ToolNameGrep,
		filesystem.ToolNameExecute,
		toolNameSkill,
	}
)

// renderToolResult 渲染工具结果为 [collapsed, expanded] 行数组
func renderToolResult(name, content string) (collapsed, expanded []string) {
	headStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(toolColor(name))).Bold(true)
	contentStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("250"))
	lines := strings.Split(content, "\n")
	head := headStyle.Render("  " + toolLabel(name))

	styledLines := make([]string, len(lines))
	for i, l := range lines {
		styledLines[i] = contentStyle.Render("    " + l)
	}

	if len(lines) <= defaultToolResultLines {
		// 内容简短，不需要折叠
		result := []string{head}
		result = append(result, styledLines...)
		return result, result
	}

	// 折叠版：前 10 行 + 展开提示
	collapsed = []string{head}
	collapsed = append(collapsed, styledLines[:defaultToolResultLines]...)
	hidden := len(lines) - defaultToolResultLines
	collapsed = append(collapsed, contentStyle.Render(fmt.Sprintf("  ▼ 展开 (%d more lines)", hidden)))

	// 展开版：全部内容 + 折叠提示
	expanded = []string{head}
	expanded = append(expanded, styledLines...)
	expanded = append(expanded, contentStyle.Render("  ▲ 折叠"))
	return
}

// toolLabel 返回工具的人类可读标签
func toolLabel(name string) string {
	switch name {
	case filesystem.ToolNameLs:
		return "List"
	case filesystem.ToolNameReadFile:
		return "Read"
	case filesystem.ToolNameWriteFile:
		return "Write"
	case filesystem.ToolNameEditFile:
		return "Edit"
	case filesystem.ToolNameGlob:
		return "Search"
	case filesystem.ToolNameGrep:
		return "Grep"
	case filesystem.ToolNameExecute:
		return "Run"
	case toolNameSkill:
		return "Use skill"
	default:
		return "Result: " + name
	}
}

// toolColor 返回工具名称对应的亮色 ANSI 色号
func toolColor(name string) string {
	switch name {
	case filesystem.ToolNameLs:
		return "14" // 亮青色
	case filesystem.ToolNameReadFile:
		return "39" // 亮蓝色
	case filesystem.ToolNameWriteFile:
		return "11" // 亮黄色
	case filesystem.ToolNameEditFile:
		return "13" // 亮品红
	case filesystem.ToolNameGlob:
		return "43" // 海绿色
	case filesystem.ToolNameGrep:
		return "9" // 亮红色
	case filesystem.ToolNameExecute:
		return "208" // 亮橙色
	case toolNameSkill:
		return "63" // 亮紫色
	default:
		return "40" // 绿色（兜底）
	}
}

// renderToolCall 渲染工具调用
func renderToolCall(name, args string) string {
	nameStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(toolColor(name))).Bold(true)
	argsStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("250"))

	var m map[string]any
	if err := json.Unmarshal([]byte(args), &m); err != nil {
		log.Fatalf("renderToolCall: failed to parse args for tool %q: %v\n  raw: %s", name, err, args)
	}

	label, param := formatToolCall(name, m)
	if !slices.Contains(noNeedToCollapseTools, name) && len([]rune(param)) > 40 {
		param = string([]rune(param)[:40]) + "..."
	}
	return fmt.Sprintf("  %s %s", nameStyle.Render(label), argsStyle.Render(param))
}

// formatToolCall 根据工具名称返回人类可读的标签和参数
func formatToolCall(name string, m map[string]any) (label, param string) {
	label = toolLabel(name)
	switch name {
	case filesystem.ToolNameLs:
		if v, ok := m["path"]; ok {
			param = fmt.Sprint(v)
		}
	case filesystem.ToolNameReadFile:
		if v, ok := m["file_path"]; ok {
			s := fmt.Sprint(v)
			start := 1
			if off, ok := m["offset"]; ok {
				if f, isFloat := off.(float64); isFloat && f > 1 {
					start = int(f)
				}
			}
			end := "end"
			if lim, ok := m["limit"]; ok {
				if f, isFloat := lim.(float64); isFloat && f > 0 {
					end = fmt.Sprint(start + int(f) - 1)
				}
			}
			s += fmt.Sprintf("  (L%d-%s)", start, end)
			param = s
		}
	case filesystem.ToolNameWriteFile:
		if v, ok := m["file_path"]; ok {
			param = fmt.Sprint(v)
		}
	case filesystem.ToolNameEditFile:
		if v, ok := m["file_path"]; ok {
			param = fmt.Sprint(v)
		}
	case filesystem.ToolNameGlob:
		if v, ok := m["pattern"]; ok {
			param = fmt.Sprint(v)
		}
	case filesystem.ToolNameGrep:
		if v, ok := m["pattern"]; ok {
			param = fmt.Sprint(v)
		}
	case filesystem.ToolNameExecute:
		if v, ok := m["command"]; ok {
			param = fmt.Sprint(v)
		}
	case toolNameSkill:
		if v, ok := m["skill"]; ok {
			param = fmt.Sprint(v)
		}
	default:
		// 未知工具：用 key=value 格式兜底
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var pairs []string
		for _, k := range keys {
			pairs = append(pairs, fmt.Sprintf("%s=%v", k, m[k]))
		}
		param = strings.Join(pairs, " ")
	}
	if param == "" {
		label = name
	}
	return
}
