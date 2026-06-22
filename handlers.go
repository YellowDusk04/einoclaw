package main

import (
	"context"
	"fmt"
	"log"
	"path/filepath"
	"strings"

	"github.com/bytedance/sonic"
	"github.com/cloudwego/eino-ext/adk/backend/local"
	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/middlewares/automemory"
	"github.com/cloudwego/eino/adk/middlewares/filesystem"
	"github.com/cloudwego/eino/adk/middlewares/patchtoolcalls"
	"github.com/cloudwego/eino/adk/middlewares/permission"
	"github.com/cloudwego/eino/adk/middlewares/reduction"
	"github.com/cloudwego/eino/adk/middlewares/skill"
	"github.com/cloudwego/eino/adk/middlewares/summarization"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
	"github.com/pkoukk/tiktoken-go"
	"github.com/pterm/pterm"
)

func newLocal() *local.Local {
	backend, err := local.NewBackend(ctx, &local.Config{})
	if err != nil {
		log.Fatal(err)
	}
	return backend
}

func newTokenCounter() func(ctx context.Context, msg []adk.AgenticMessage, tools []*schema.ToolInfo) (int64, error) {
	enc, err := tiktoken.GetEncoding("cl100k_base")
	if err != nil {
		panic(err)
	}
	return func(ctx context.Context, msg []adk.AgenticMessage, tools []*schema.ToolInfo) (int64, error) {
		var count int64
		for _, m := range msg {
			tokens := enc.Encode(m.String(), nil, nil)
			for _, t := range tokens {
				count += int64(t)
			}
		}
		for _, tl := range tools {
			tl_ := *tl
			tl_.Extra = nil
			text, err := sonic.MarshalString(tl_)
			if err != nil {
				return 0, fmt.Errorf("failed to marshal tool info: %w", err)
			}
			tokens := enc.Encode(text, nil, nil)
			for _, t := range tokens {
				count += int64(t)
			}
		}
		return count, nil
	}
}

func newFilesystemHandler() adk.TypedChatModelAgentMiddleware[adk.AgenticMessage] {
	backend := newLocal()
	fcfg := cfg.Handlers.Filesystem
	handler, err := filesystem.NewTyped[adk.AgenticMessage](
		ctx,
		&filesystem.MiddlewareConfig{
			Backend: backend,
			Shell:   backend,
			LsToolConfig: &filesystem.ToolConfig{
				Disable: !fcfg.Ls,
			},
			ReadFileToolConfig: &filesystem.ToolConfig{
				Disable: !fcfg.ReadFile,
			},
			WriteFileToolConfig: &filesystem.ToolConfig{
				Disable: !fcfg.WriteFile,
			},
			GrepToolConfig: &filesystem.ToolConfig{
				Disable: !fcfg.Grep,
			},
			GlobToolConfig: &filesystem.ToolConfig{
				Disable: !fcfg.Glob,
			},
			EditFileToolConfig: &filesystem.ToolConfig{
				Disable: !fcfg.EditFile,
			},
			ExecuteToolConfig: &filesystem.ExecuteToolConfig{
				ToolConfig: filesystem.ToolConfig{
					Disable: !fcfg.Execute,
				},
				InputMode: filesystem.ExecuteToolInputModeRich,
			},
			UseMultiModalRead: true,
		},
	)
	if err != nil {
		log.Fatal(err)
	}
	return handler
}

func newPatchToolCallsHandler() adk.TypedChatModelAgentMiddleware[adk.AgenticMessage] {
	handler, err := patchtoolcalls.NewTyped[adk.AgenticMessage](
		ctx,
		&patchtoolcalls.Config{
			RemoveOrphanResults:    true,
			RemoveDuplicateResults: true,
			MarkSynthetic:          true,
		},
	)
	if err != nil {
		log.Fatal(err)
	}
	return handler
}

func newSummarizationHandler() adk.TypedChatModelAgentMiddleware[adk.AgenticMessage] {
	finalizer, err := summarization.NewTypedFinalizer[adk.AgenticMessage]().
		PreserveSkills(&summarization.PreserveSkillsConfig{}).
		Build()
	handler, err := summarization.NewTyped(
		ctx,
		&summarization.TypedConfig[adk.AgenticMessage]{
			Model: baseModel,
			Trigger: &summarization.TriggerCondition{
				ContextTokens: cfg.Handlers.Summarization.ContextTokens << 10, // k
			},
			EmitInternalEvents: true,
			TranscriptFilePath: filepath.Join(sessionDir, fmt.Sprintf("%s.evlog", sessionID)),
			Finalize:           finalizer,
		},
	)
	if err != nil {
		log.Fatal(err)
	}
	return handler
}

func newReductionHandler() adk.TypedChatModelAgentMiddleware[adk.AgenticMessage] {
	rcfg := cfg.Handlers.Reduction
	handler, err := reduction.NewTyped(
		ctx,
		&reduction.TypedConfig[adk.AgenticMessage]{
			Backend:           newLocal(),
			RootDir:           rootDir,
			MaxLengthForTrunc: rcfg.MaxLengthForTrunc << 10,        // k
			MaxTokensForClear: int64(rcfg.MaxTokensForClear) << 10, // k
			TokenCounter:      newTokenCounter(),
			TruncExcludeTools: []string{"skill"},
			ClearExcludeTools: []string{"skill"},
		},
	)
	if err != nil {
		log.Fatal(err)
	}
	return handler
}

func newAutoMemmoryHandler() adk.TypedChatModelAgentMiddleware[adk.AgenticMessage] {
	handler, err := automemory.New(
		ctx,
		&automemory.Config[adk.AgenticMessage]{
			MemoryStores: []automemory.MemoryStore{
				{
					Path: memoryDir,
					Name: "local",
				},
			},
			MemoryBackend: newLocal(),
			Model:         baseModel,
			Write: &automemory.WriteConfig[adk.AgenticMessage]{
				Mode: automemory.WriteModeAsync,
				HandleExtractionIterator: func(ctx context.Context, iter *adk.AsyncIterator[*adk.TypedAgentEvent[adk.AgenticMessage]]) error {
					addChatLine(pterm.LightMagenta("saving memory..."))
					for {
						ev, ok := iter.Next()
						if !ok {
							addChatLine(pterm.LightMagenta("memory saved"))
							return nil
						}
						if ev == nil {
							continue
						}
						if ev.Err != nil {
							return ev.Err
						}
					}
				},
			},
			Coordination: &automemory.CoordinationConfig[adk.AgenticMessage]{
				SessionIDFunc: func(ctx context.Context, state *adk.TypedChatModelAgentState[adk.AgenticMessage]) (string, error) {
					return sessionID, nil
				},
			},
		},
	)
	if err != nil {
		log.Fatal(err)
	}
	return handler
}

type executeArgs struct {
	Command string `json:"command"`
}

func newPermissionHandler() adk.TypedChatModelAgentMiddleware[adk.AgenticMessage] {
	return permission.NewTyped[adk.AgenticMessage](
		func(ctx context.Context, tCtx *adk.ToolContext, args *schema.ToolArgument) (*permission.GateCheckResult, error) {
			if tCtx.Name == filesystem.ToolNameExecute {
				var execArgs executeArgs
				if err := sonic.UnmarshalString(args.Text, &execArgs); err != nil {
					return nil, err
				}
				decision, message := permission.GateAllow, ""
				for _, cmd := range cfg.Handlers.Permission.BlackList {
					if strings.HasPrefix(execArgs.Command, cmd) {
						decision = permission.GateAsk
						message = execArgs.Command
						break
					}
				}
				return &permission.GateCheckResult{
					Decision: decision,
					Message:  message,
				}, nil
			}
			return &permission.GateCheckResult{
				Decision: permission.GateAllow,
			}, nil
		},
	)
}

func newSkillHandler() adk.TypedChatModelAgentMiddleware[adk.AgenticMessage] {
	dir := cfg.Handlers.Skill.Dir
	if dir == "" {
		dir = filepath.Join(homeDir, ".agents", "skills")
	}
	backend, err := skill.NewBackendFromFilesystem(ctx, &skill.BackendFromFilesystemConfig{
		BaseDir: dir,
		Backend: newLocal(),
	})
	if err != nil {
		log.Fatal(err)
	}
	handler, err := skill.NewTyped(
		ctx,
		&skill.TypedConfig[adk.AgenticMessage]{
			Backend: backend,
		},
	)
	if err != nil {
		log.Fatal(err)
	}
	return handler
}

func newToolCallErrorHandler() adk.TypedChatModelAgentMiddleware[adk.AgenticMessage] {
	return &toolCallErrorWrapper{}
}

type toolCallErrorWrapper struct {
	adk.TypedBaseChatModelAgentMiddleware[adk.AgenticMessage]
}

func (b *toolCallErrorWrapper) WrapInvokableToolCall(_ context.Context, endpoint adk.InvokableToolCallEndpoint, _ *adk.ToolContext) (adk.InvokableToolCallEndpoint, error) {
	return func(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
		output, err := endpoint(ctx, argumentsInJSON)
		if err != nil {
			// ignore interrupt error
			if _, ok := compose.IsInterruptRerunError(err); ok {
				return "", err
			}
			return "tool call error: " + err.Error(), nil
		}
		return output, nil
	}, nil
}
