package main

import (
	"strings"

	"charm.land/lipgloss/v2"
)

// aiStreamingState 封装 AI 流式渲染状态（仅 eventAI 使用，避免 chatEvent 膨胀）
type aiStreamingState struct {
	stream   *streamingMarkdown
	fullText strings.Builder
}

// eventType 聊天事件类型
type eventType int

const (
	eventUser          eventType = iota // 用户消息
	eventAI                             // AI 回复
	eventThinking                       // AI 思考过程 (ContentBlockTypeReasoning)
	eventToolCall                       // 工具调用
	eventToolResult                     // 工具结果
	eventSummarization                  // 摘要事件
)

// chatEvent 表示聊天区中的一个消息/事件
type chatEvent struct {
	typ eventType

	expanded   []string // 展开版行（所有类型都有）
	collapsed  []string // 折叠版行（仅有展开需求的类型设置）
	isExpanded bool

	rawText        strings.Builder // 流式思考原始文本
	streamingState *aiStreamingState
}

// visibleLines 返回当前可见的行数组
func (ev *chatEvent) visibleLines() []string {
	if ev.isExpanded || ev.collapsed == nil {
		return ev.expanded
	}
	return ev.collapsed
}

func newChatEvent(typ eventType, expanded, collapsed []string) *chatEvent {
	// 根据类型给第一行加前缀
	switch typ {
	case eventUser:
		prefix := lipgloss.NewStyle().Foreground(lipgloss.Color("63")).Bold(true).Render("┃ ")
		expanded[0] = prefix + expanded[0]
	case eventAI:
		prefix := lipgloss.NewStyle().Foreground(lipgloss.Color("14")).Render("● ")
		expanded[0] = prefix + expanded[0]
	}
	return &chatEvent{
		typ:        typ,
		expanded:   expanded,
		collapsed:  collapsed,
		isExpanded: typ != eventThinking && typ != eventSummarization && typ != eventToolResult,
	}
}

// chatList 管理聊天消息列表 + 虚拟滚动
type chatList struct {
	events     []*chatEvent
	yOffset    int // 当前滚动偏移（逻辑行号）
	width      int
	height     int
	totalLines int // 所有 events 的 lineCount 之和

	autoScroll bool // 是否自动滚到底部
}

func newChatList(width, height int) *chatList {
	return &chatList{
		width:      width,
		height:     height,
		autoScroll: true,
	}
}

// appendEvent 追加新事件
func (cl *chatList) appendEvent(ev *chatEvent) {
	// 非第一个 event 时，先加 1 行空行分隔
	if len(cl.events) > 0 {
		cl.totalLines++
	}
	cl.events = append(cl.events, ev)
	cl.totalLines += len(ev.visibleLines())
	if cl.autoScroll {
		cl.scrollToBottom()
	}
}

// toggleExpand 切换展开/折叠，直接互换缓存行。
func (cl *chatList) toggleExpand(idx int) {
	ev := cl.events[idx]
	oldLines := len(ev.visibleLines())
	ev.isExpanded = !ev.isExpanded
	cl.totalLines += len(ev.visibleLines()) - oldLines
}

// view 渲染可见区域，线性遍历定位起始 event
func (cl *chatList) view() string {
	if len(cl.events) == 0 {
		return ""
	}

	// 1. 线性遍历定位 yOffset 对应的起始 event，计入 event 间的空行分隔
	startIdx := 0
	lineInEvent := 0
	remaining := cl.yOffset
	for i, ev := range cl.events {
		lc := len(ev.visibleLines())
		if remaining < lc {
			startIdx = i
			lineInEvent = remaining
			remaining = 0
			break
		}
		remaining -= lc
		if i < len(cl.events)-1 {
			if remaining > 0 {
				remaining-- // 跳过 event 后的空行分隔
			} else {
				// remaining == 0 表示 yOffset 恰好落在空行上
				startIdx = i + 1
				lineInEvent = 0
				break
			}
		}
	}

	// 2. 只收集可见行，event 间用空行分隔
	var visibleLines []string
	remaining = cl.height
	for i := startIdx; i < len(cl.events) && remaining > 0; i++ {
		ev := cl.events[i]
		visible := ev.visibleLines()[lineInEvent:]
		if len(visible) > remaining {
			visible = visible[:remaining]
		}
		visibleLines = append(visibleLines, visible...)
		remaining -= len(visible)
		lineInEvent = 0

		// 非最后一个 event 且还有空间时，插入空行分隔
		if i < len(cl.events)-1 && remaining > 0 {
			visibleLines = append(visibleLines, "")
			remaining--
		}
	}

	// 3. 填充空白
	for remaining > 0 {
		visibleLines = append(visibleLines, "")
		remaining--
	}
	return strings.Join(visibleLines, "\n")
}

// scroll 操作
func (cl *chatList) scrollUp(n int) {
	cl.yOffset = max(0, cl.yOffset-n)
	cl.autoScroll = cl.yOffset >= cl.maxYOffset()
}

func (cl *chatList) scrollDown(n int) {
	cl.yOffset = min(cl.maxYOffset(), cl.yOffset+n)
	cl.autoScroll = cl.yOffset >= cl.maxYOffset()
}

func (cl *chatList) pageUp()   { cl.scrollUp(cl.height) }
func (cl *chatList) pageDown() { cl.scrollDown(cl.height) }
func (cl *chatList) scrollToTop() {
	cl.yOffset = 0
	cl.autoScroll = false
}
func (cl *chatList) scrollToBottom() {
	cl.yOffset = cl.maxYOffset()
	cl.autoScroll = true
}

func (cl *chatList) maxYOffset() int {
	return max(0, cl.totalLines-cl.height)
}

// eventAtRow 返回给定行号（相对于 chat 区域顶部 0 行）对应的 event 索引。
// 点击在空行分隔或超出范围时返回 -1。
func (cl *chatList) eventAtRow(row int) int {
	if row < 0 {
		return -1
	}
	absRow := cl.yOffset + row

	remaining := absRow
	for i, ev := range cl.events {
		lc := len(ev.visibleLines())
		if remaining < lc {
			return i
		}
		remaining -= lc
		if i < len(cl.events)-1 {
			if remaining == 0 {
				return -1 // 点击在空行分隔上
			}
			remaining-- // 跳过空行
		}
	}
	return -1 // 点击在 padding 区域
}

// expandable 返回该 event 是否可展开/折叠
func (ev *chatEvent) expandable() bool {
	return ev.collapsed != nil && ev.expanded != nil
}

// toggleEventAtRow 在给定行号切换该 event 的展开状态
func (cl *chatList) toggleEventAtRow(row int) bool {
	idx := cl.eventAtRow(row)
	if idx < 0 {
		return false
	}
	ev := cl.events[idx]
	if !ev.expandable() {
		return false
	}
	cl.toggleExpand(idx)
	return true
}
