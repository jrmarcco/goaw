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

	workspace, _ := os.Getwd()

	llmProvider, err := provider.NewOpenAIV3Provider("glm-4.5-air")
	if err != nil {
		log.Fatalf("failed to create provider: %v", err)
	}

	toolRegistry := tools.NewDefaultRegistry()

	_ = toolRegistry.Register(tools.NewFileReader(workspace))
	_ = toolRegistry.Register(tools.NewFileWriter(workspace))
	_ = toolRegistry.Register(tools.NewFileEditor(workspace))
	_ = toolRegistry.Register(tools.NewBashExecutor(workspace))

	eng, _ := engine.NewAgentEngine(workspace, llmProvider, toolRegistry, true)

	prompt := `
	当前目录下有 a.txt, b.txt, c.txt 三个文件。
	为了节省时间，请你同时一次性读取这三个文件，并将它们的内容综合起来，告诉我它们分别记录了什么领域的信息。
	`
	if err := eng.Run(context.Background(), prompt); err != nil {
		log.Fatalf("engine crash: %v", err)
	}
}
