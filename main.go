package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"runtime"

	tea "charm.land/bubbletea/v2"
	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/middlewares/permission"
	"github.com/cloudwego/eino/adk/middlewares/summarization"
	"github.com/cloudwego/eino/adk/session"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

func main() {
	// trace start
	traceClose := traceStart()

	defer func() {
		if r := recover(); r != nil {
			log.Printf("panic: %v", r)
		}
		traceClose()
		logFile.Close()
	}()

	// 初始化（init.go 中的 init() 自动执行）

	// 创建并启动 turnLoop（非阻塞）
	initTurnLoop()

	// 启动 Bubble Tea TUI（阻塞）
	program = tea.NewProgram(newTeaModel())
	if _, err := program.Run(); err != nil {
		log.Fatal(err)
	}

	// 清理
	turnLoop.Stop()
	turnLoop.Wait()
}

var (
	turnLoop *adk.TurnLoop[chatItem, adk.AgenticMessage]
	program  *tea.Program

	homeDir      string // ~
	rootDir      string // ~/.einoclaw
	sessionDir   string // ~/.einoclaw/sessions
	memoryDir    string // ~/.einoclaw/memory
	sessionID    string
	cfg          *config
	baseModel    model.BaseModel[adk.AgenticMessage]
	agent        adk.TypedAgent[adk.AgenticMessage]
	sessionStore adk.SessionEventStore[adk.AgenticMessage]
	logFile      *os.File

	ctx = context.Background()
)

// chatItem 是 turnLoop 的泛型参数 T，表示一个聊天输入项
type chatItem struct {
	id          string
	query       string
	interruptId string
	action      permission.ResumeAction
}

// initTurnLoop 创建并启动 turnLoop。
// 在 /resume 切换 session 时可重复调用。
func initTurnLoop() {
	var err error
	sessionStore, err = session.NewFileStore[adk.AgenticMessage](sessionDir, nil)
	if err != nil {
		log.Fatal(err)
	}
	turnLoop = adk.NewTurnLoop(adk.TurnLoopConfig[chatItem, adk.AgenticMessage]{
		GenInput:  GenInput,
		GenResume: GenResume,
		PrepareAgent: func(ctx context.Context, loop *adk.TurnLoop[chatItem, adk.AgenticMessage], consumed []chatItem) (adk.TypedAgent[adk.AgenticMessage], error) {
			return agent, nil
		},
		OnAgentEvents: OnAgentEvents,
		InterruptMode: adk.TurnLoopInterruptWaitsForExplicitResume,
		SessionID:     sessionID,
		SessionStore:  sessionStore,
	})
	turnLoop.Run(context.Background())
}

// OnAgentEvents 是后端事件处理。解析事件类型，发送 TUI 能理解的特化消息。
func OnAgentEvents(ctx context.Context, tc *adk.TurnContext[chatItem, adk.AgenticMessage],
	events *adk.AsyncIterator[*adk.TypedAgentEvent[adk.AgenticMessage]]) error {

	for {
		event, ok := events.Next()
		if !ok {
			break
		}
		if err := event.Err; err != nil {
			return err
		}

		// 权限中断 / 摘要
		if event.Action != nil {
			handleAction(event.Action)
			continue
		}

		if event.Output == nil || event.Output.MessageOutput == nil {
			continue
		}

		mv := event.Output.MessageOutput
		if !mv.IsStreaming {
			if mv.Message != nil {
				for _, block := range mv.Message.ContentBlocks {
					if block.FunctionToolResult != nil {
						program.Send(toolResultMsg{
							Name:    block.FunctionToolResult.Name,
							Content: resultContent(block.FunctionToolResult),
						})
					}
				}
			}
			continue
		}

		// 流式消息：消费 stream
		accumulated := []adk.AgenticMessage{}
		stream := mv.MessageStream
		for {
			chunk, err := stream.Recv()
			if err != nil {
				if errors.Is(err, io.EOF) {
					break
				}
				log.Println("stream.Recv() error:", err)
				return err
			}
			accumulated = append(accumulated, chunk)
			for _, block := range chunk.ContentBlocks {
				if block.Reasoning != nil {
					if text := block.Reasoning.Text; text != "" {
						program.Send(aiThinkingChunkMsg{text: text})
					}
				}
				if block.AssistantGenText != nil {
					if text := block.AssistantGenText.Text; text != "" {
						program.Send(aiTextChunkMsg{text: text})
					}
				}
			}
			if chunk.ResponseMeta != nil && chunk.ResponseMeta.TokenUsage != nil {
				u := chunk.ResponseMeta.TokenUsage
				program.Send(tokenUsageMsg{
					promptTokens: u.PromptTokens,
					cachedTokens: u.PromptTokenDetails.CachedTokens,
				})
			}
		}
		// 提取工具调用
		message, err := schema.ConcatAgenticMessages(accumulated)
		if err != nil {
			log.Fatal(err)
		}
		var items []toolCallItem
		for _, block := range message.ContentBlocks {
			if block.FunctionToolCall != nil {
				items = append(items, toolCallItem{
					Name: block.FunctionToolCall.Name,
					Args: block.FunctionToolCall.Arguments,
				})
			}
		}
		if len(items) > 0 {
			program.Send(toolCallMsg{Items: items})
		}
	}
	return nil
}

func resultContent(r *schema.FunctionToolResult) string {
	for _, c := range r.Content {
		if c != nil && c.Text != nil {
			return c.Text.Text
		}
	}
	return ""
}

func handleAction(action *adk.AgentAction) {
	if action.Interrupted != nil {
		for _, ic := range action.Interrupted.InterruptContexts {
			if info, ok := ic.Info.(*permission.AskInfo); ok {
				program.Send(permissionAskMsg{
					cmd:       info.Summary,
					interrupt: ic.ID,
				})
				return
			}
		}
	}
	if action.CustomizedAction != nil {
		if ca, ok := action.CustomizedAction.(*summarization.TypedCustomizedAction[adk.AgenticMessage]); ok {
			content := ""
			if ca.Type == summarization.ActionTypeAfterSummarize && len(ca.After.Messages) > 0 {
				content = ca.After.Messages[len(ca.After.Messages)-1].String()
			}
			program.Send(summarizationEventMsg{actionType: ca.Type, content: content})
		}
	}
}

// GenInput 将用户输入转换为 agent input
func GenInput(ctx context.Context, loop *adk.TurnLoop[chatItem, adk.AgenticMessage], items []chatItem) (*adk.GenInputResult[chatItem, adk.AgenticMessage], error) {
	if len(items) == 0 {
		return nil, nil
	}
	// 通知前端消息已被消费
	for _, item := range items {
		if item.id != "" {
			program.Send(ackMsg{id: item.id})
		}
	}
	return &adk.GenInputResult[chatItem, adk.AgenticMessage]{
		Input: &adk.TypedAgentInput[adk.AgenticMessage]{
			Messages: []adk.AgenticMessage{
				schema.UserAgenticMessage(items[0].query),
			},
			EnableStreaming: true,
		},
		Consumed:  items[:1],
		Remaining: items[1:],
	}, nil
}

// GenResume 处理权限审批的恢复
func GenResume(ctx context.Context, loop *adk.TurnLoop[chatItem, adk.AgenticMessage], interruptedItems, unhandledItems, newItems []chatItem) (*adk.GenResumeResult[chatItem, adk.AgenticMessage], error) {
	if len(newItems) == 0 {
		return nil, nil
	}
	all := append(append([]chatItem(nil), interruptedItems...), unhandledItems...)
	resumeItem := newItems[0]
	return &adk.GenResumeResult[chatItem, *schema.AgenticMessage]{
		Consumed:  all[:1],
		Remaining: all[1:],
		Decision:  adk.TurnLoopResumeDecisionResume,
		ResumeParams: &adk.ResumeParams{
			Targets: map[string]any{
				resumeItem.interruptId: &permission.ResumeResponse{
					Action:  resumeItem.action,
					Message: resumeItem.query,
				},
			},
		},
	}, nil
}

// loadAgent 创建 agent（使用全局 baseModel）
func loadAgent() {
	var err error

	handlers := []adk.TypedChatModelAgentMiddleware[adk.AgenticMessage]{
		newToolCallErrorHandler(),
	}

	hcfg := cfg.Handlers
	if hcfg.PatchToolCalls.Enabled {
		handlers = append(handlers, newPatchToolCallsHandler())
	}
	if hcfg.Filesystem.Enabled {
		handlers = append(handlers, newFilesystemHandler())
	}
	if hcfg.Skill.Enabled {
		handlers = append(handlers, newSkillHandler())
	}
	if hcfg.Summarization.Enabled {
		handlers = append(handlers, newSummarizationHandler())
	}
	if hcfg.Reduction.Enabled {
		handlers = append(handlers, newReductionHandler())
	}
	if hcfg.AutoMemory.Enabled {
		handlers = append(handlers, newAutoMemmoryHandler())
	}
	if hcfg.Permission.Enabled {
		handlers = append(handlers, newPermissionHandler())
	}

	wd, _ := os.Getwd()
	userInfoPrompt := fmt.Sprintf(`
<runtime_info>
working_directory: %s
os: %s
</runtime_info>`, wd, runtime.GOOS)

	agent, err = adk.NewTypedChatModelAgent(ctx, &adk.TypedChatModelAgentConfig[adk.AgenticMessage]{
		Name:          "einoclaw",
		Description:   "a code agent which can do many things",
		Instruction:   "你是一个编程智能体, 你的名字叫做 einoclaw, 擅长解决编程问题。" + userInfoPrompt,
		Model:         baseModel,
		Handlers:      handlers,
		MaxIterations: 50,
	})
	if err != nil {
		log.Fatal(err)
	}
}

func loadModelAndAgent(index int) {
	loadModel(index)
	loadAgent()
}
