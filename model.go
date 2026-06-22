package main

import (
	"log"

	"github.com/cloudwego/eino-ext/components/model/agenticark"
	"github.com/cloudwego/eino-ext/components/model/agenticopenai"
	"github.com/cloudwego/eino-ext/components/model/agenticqwen"
	"github.com/cloudwego/eino-ext/components/model/agenticdeepseek"
)

type ModelProvider string

const (
	ModelProviderQwen     ModelProvider = "qwen"
	ModelProviderOpenAI   ModelProvider = "openai"
	ModelProviderArk      ModelProvider = "ark"
	ModelProviderDeepseek ModelProvider = "deepseek"
)

func loadModel(index int) {
	if index < 0 || index >= len(cfg.Models) {
		log.Fatalf("invalid model index: %d", index)
	}
	modelconfig := cfg.Models[index]

	var err error
	switch modelconfig.Provider {
	case ModelProviderQwen:
		baseModel, err = agenticqwen.New(ctx, &agenticqwen.Config{
			APIKey:         modelconfig.APIKey,
			Model:          modelconfig.ModelID,
			BaseURL:        modelconfig.BaseURL,
			EnableThinking: &modelconfig.EnableThinking,
		})

	case ModelProviderOpenAI:
		baseModel, err = agenticopenai.NewResponsesModel(ctx, &agenticopenai.ResponsesConfig{
			APIKey:          modelconfig.APIKey,
			Model:           modelconfig.ModelID,
			BaseURL:         modelconfig.BaseURL,
			EnableAutoCache: true,
		})

	case ModelProviderArk:
		baseModel, err = agenticark.New(ctx, &agenticark.Config{
			APIKey:          modelconfig.APIKey,
			Model:           modelconfig.ModelID,
			BaseURL:         modelconfig.BaseURL,
			EnableAutoCache: true,
		})
	case ModelProviderDeepseek:
		baseModel, err = agenticdeepseek.New(ctx, &agenticdeepseek.Config{
			APIKey:          modelconfig.APIKey,
			Model:           modelconfig.ModelID,
			BaseURL:         modelconfig.BaseURL,
		})

	default:
		log.Fatalf("unsupported model provider: %s", modelconfig.Provider)
	}
	if err != nil {
		log.Fatal(err)
	}
}
