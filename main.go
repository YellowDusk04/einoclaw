package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/middlewares/permission"
	"github.com/cloudwego/eino/adk/middlewares/summarization"
	"github.com/cloudwego/eino/adk/session"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/compose"
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
		close(sigwinch)
		close(decisionChannel)
	}()

	// run turn loop, non block
	loadTurnLoopAndRun()

	// run tui loop, interact with you in the terminal, block
	runTUILoop()
}

type (
	chatItem struct {
		query       string
		interruptId string
		action      permission.ResumeAction
	}
)

var (
	homeDir    string // ~
	rootDir    string // ~/.einoclaw
	sessionDir string // ~/.einoclaw/sessions
	memoryDir  string // ~/.einoclaw/memory
	sessionID  string
	cfg        *config
	baseModel  model.BaseModel[adk.AgenticMessage]
	agent      adk.TypedAgent[adk.AgenticMessage]
	turnLoop   *adk.TurnLoop[chatItem, adk.AgenticMessage]
	logFile    *os.File
)

func loadAgent() {
	var err error

	handler := []adk.TypedChatModelAgentMiddleware[adk.AgenticMessage]{
		newToolCallErrorHandler(),
	}

	hcfg := cfg.Handlers
	if hcfg.PatchToolCalls.Enabled {
		handler = append(handler, newPatchToolCallsHandler())
	}
	if hcfg.Filesystem.Enabled {
		handler = append(handler, newFilesystemHandler())
	}
	if hcfg.Skill.Enabled {
		handler = append(handler, newSkillHandler())
	}
	if hcfg.Summarization.Enabled {
		handler = append(handler, newSummarizationHandler())
	}
	if hcfg.Reduction.Enabled {
		handler = append(handler, newReductionHandler())
	}
	if hcfg.AutoMemory.Enabled {
		handler = append(handler, newAutoMemmoryHandler())
	}
	if hcfg.Permission.Enabled {
		handler = append(handler, newPermissionHandler())
	}

	workdir, _ := os.Getwd()

	agent, err = adk.NewTypedChatModelAgent(ctx, &adk.TypedChatModelAgentConfig[adk.AgenticMessage]{
		Name:        "einoclaw",
		Description: "a code agent which can do many things",
		Instruction: "你是一个编程智能体, 你的名字叫做 einoclaw, 擅长解决编程问题。你当前的工作目录是: " + workdir,
		Model:       baseModel,
		Handlers:    handler,
		ToolsConfig: adk.ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				UnknownToolsHandler: func(ctx context.Context, name, input string) (string, error) {
					return fmt.Sprintf("unknown tool: %s", name), nil
				},
			},
		},
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

func loadTurnLoopAndRun() {
	sessionStore, err := session.NewFileStore[adk.AgenticMessage](
		sessionDir,
		nil,
	)
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

	turnLoop.Run(ctx)
}

func GenInput(ctx context.Context, loop *adk.TurnLoop[chatItem, adk.AgenticMessage], items []chatItem) (*adk.GenInputResult[chatItem, adk.AgenticMessage], error) {
	if len(items) == 0 {
		return nil, nil
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
					Action: resumeItem.action,
				},
			},
		},
	}, nil
}

func OnAgentEvents(ctx context.Context, tc *adk.TurnContext[chatItem, adk.AgenticMessage], events *adk.AsyncIterator[*adk.TypedAgentEvent[adk.AgenticMessage]]) error {
	isFirst := true
	for {
		event, ok := events.Next()
		if !ok {
			break
		}
		if err := event.Err; err != nil {
			return err
		}
		if isFirst {
			isFirst = false
		} else {
			mu.Lock()
			addChatLine("")
			mu.Unlock()
		}
		if event.Action != nil {
			if event.Action.CustomizedAction != nil {
				// display summarization event
				if action, ok := event.Action.CustomizedAction.(*summarization.TypedCustomizedAction[adk.AgenticMessage]); ok {
					renderSummarization(action)
				}
			} else if event.Action.Interrupted != nil {
				// handler interrupt, which casued by permission.Middleware to realize 'human in the loop'
				for _, interruptCtx := range event.Action.Interrupted.InterruptContexts {
					if interruptCtx.Info != nil {
						if info, ok := interruptCtx.Info.(*permission.AskInfo); ok {
							return turnLoop.Resume(chatItem{
								interruptId: interruptCtx.ID,
								action:      promptUserForPermission(info.Summary), // block here to get user's permission decision
							})
						}
					}
				}
				log.Fatal("interrupted info not found or unsupported")
			}
			continue
		}
		if event.Output == nil || event.Output.MessageOutput == nil {
			continue
		}
		mv := event.Output.MessageOutput
		if mv.IsStreaming {
			isFirstTextChunk := true
			stream := mv.MessageStream
			accumalatedMessages := []adk.AgenticMessage{}
			for {
				chunk, err := stream.Recv()
				if err != nil {
					if errors.Is(err, io.EOF) {
						break
					}
					log.Fatal(err)
				}
				accumalatedMessages = append(accumalatedMessages, chunk)
				for _, block := range chunk.ContentBlocks {
					if block.AssistantGenText != nil {
						if text := block.AssistantGenText.Text; text != "" {
							mu.Lock()
							if isFirstTextChunk {
								isFirstTextChunk = false
								addChatLine("\n⏺ ")
							}
							appendToChat(block.AssistantGenText.Text)
							mu.Unlock()
						}
					}
				}
				if chunk.ResponseMeta != nil && chunk.ResponseMeta.TokenUsage != nil {
					usage := chunk.ResponseMeta.TokenUsage
					if usage.PromptTokens > 0 {
						promptTokens = usage.PromptTokens
					}
					if usage.PromptTokenDetails.CachedTokens > 0 {
						cachedTokens = usage.PromptTokenDetails.CachedTokens
					}
					mu.Lock()
					renderTokenUsage()
					mu.Unlock()
				}
			}
			message, err := schema.ConcatAgenticMessages(accumalatedMessages)
			if err != nil {
				log.Fatal(err)
			}
			renderToolCall(message)
			renderToolResult(message)
		} else {
			renderToolResult(mv.Message)
		}
	}
	return nil
}
