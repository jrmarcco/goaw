package main

import (
	"context"
	"fmt"
	"log"
	"log/slog"
	"os"

	"github.com/jrmarcco/goaw/internal/engine"
	"github.com/jrmarcco/goaw/internal/provider"
	"github.com/jrmarcco/goaw/internal/tools"
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

	r := tools.NewDefaultRegistry()

	fileReader := tools.NewFileReader(workDir)
	_ = r.Register(fileReader)

	ae, _ := engine.NewAgentEngine(workDir, p, r, false)

	prompt := "Read the code of cmd/main.go and tell me how many lines of code it has."
	if err := ae.Run(context.Background(), prompt); err != nil {
		log.Fatalf("engine crash: %v", err)
	}
}
