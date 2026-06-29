package main

import "github.com/cloudwego/eino/adk/middlewares/summarization"

// ackMsg 抢占等待队列 ack
type ackMsg struct {
	id string
}

// aiTextChunkMsg AI 流式输出的单个文本块
type aiTextChunkMsg struct {
	text string
}

// aiThinkingChunkMsg 流式思考文本块
type aiThinkingChunkMsg struct {
	text string
}

// toolCallMsg 流结束后 AI 发起的工具调用
type toolCallItem struct {
	Name string
	Args string
}

type toolCallMsg struct {
	Items []toolCallItem
}

// toolResultMsg 非流式事件中的工具结果
type toolResultMsg struct {
	Name    string
	Content string
}

// tokenUsageMsg token 用量更新
type tokenUsageMsg struct {
	promptTokens int
	cachedTokens int
}

// permissionAskMsg 后端请求权限审批
type permissionAskMsg struct {
	cmd       string
	interrupt string
}

// summarizationEventMsg 摘要事件
type summarizationEventMsg struct {
	actionType summarization.ActionType
	content    string // after 时有效
}

