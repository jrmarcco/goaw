package main

import (
	"context"
	"fmt"
	"log"
	"log/slog"
	"os"

	"github.com/jrmarcco/goaw/internal/engine"
	"github.com/jrmarcco/goaw/internal/provider"
	mockregistry "github.com/jrmarcco/goaw/internal/tools/mock"
	"go.uber.org/zap"
	"go.uber.org/zap/exp/zapslog"
)

func main() {
	// 初始化 slog。
	logger, err := zap.NewDevelopment()
	if err != nil {
		log.Fatalf("failed to create logger: %v", err)
	}
	slog.SetDefault(slog.New(zapslog.NewHandler(logger.Core())))

	fmt.Println("🚀 Welcome to goaw!")

	workDir, _ := os.Getwd()

	p, err := provider.NewOpenAIV3Provider("glm-4.5-air")
	if err != nil {
		log.Fatalf("failed to create provider: %v", err)
	}

	ae, _ := engine.NewAgentEngine(workDir, p, mockregistry.NewMockRegistry(), true)

	prompt := "I'd like to cycling in XIamen. Can you tell me if the weather is be suitable?"
	if err := ae.Run(context.Background(), prompt); err != nil {
		log.Fatalf("engine crash: %v", err)
	}
}
