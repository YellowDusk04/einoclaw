package main

import (
	"context"
	"log"
	"time"

	ccb "github.com/cloudwego/eino-ext/callbacks/cozeloop"
	"github.com/cloudwego/eino/callbacks"
	"github.com/coze-dev/cozeloop-go"
)

func traceStart() func() {
	cozeCfg := cfg.CozeLoop
	if !cozeCfg.Enabled {
		return func() {}
	}

	client, err := cozeloop.NewClient(
		cozeloop.WithAPIToken(cozeCfg.APIToken),
		cozeloop.WithWorkspaceID(cozeCfg.WorkspaceID),
		cozeloop.WithTimeout(30*time.Second),
	)
	if err != nil {
		log.Fatalf("cozeloop.NewClient failed: %v", err)
	}
	callbacks.AppendGlobalHandlers(ccb.NewLoopHandler(client))
	return func() {
		time.Sleep(500 * time.Millisecond)
		client.Close(context.Background())
	}
}
