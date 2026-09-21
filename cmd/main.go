package main

import (
	"context"
	"fmt"
	"log"
	"log/slog"
	"os"
	"time"

	"github.com/jrmarcco/goaw/internal/engine"
	"github.com/jrmarcco/goaw/internal/provider"
	"github.com/jrmarcco/goaw/internal/reporter"
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

	go func() {
		bot, err := createFeishuBot(eng)
		if err != nil {
			slog.Error("failed to create feishu bot", "error", err)
			return
		}
		slog.Info("feishu bot created successfully")

		err = startFeishuBot(bot)
		if err != nil {
			slog.Error("failed to start feishu bot", "error", err)
			return
		}
		slog.Info("feishu bot started successfully")
	}()
}

func createFeishuBot(eng *engine.AgentEngine) (*reporter.FeishuBot, error) {
	appID := os.Getenv("FEISHU_APP_ID")
	appSecret := os.Getenv("FEISHU_APP_SECRET")

	bot, err := reporter.NewFeishuBot(appID, appSecret, eng)
	if err != nil {
		return nil, err
	}

	return bot, nil
}

func startFeishuBot(bot *reporter.FeishuBot) error {
	eventEncryptKey := os.Getenv("FEISHU_EVENT_ENCRYPT_KEY")
	verificationToken := os.Getenv("FEISHU_VERIFICATION_TOKEN")

	const botStartTimeout = 10 * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), botStartTimeout)
	defer cancel()

	if err := bot.StartWithWebSocket(ctx, eventEncryptKey, verificationToken); err != nil {
		return err
	}
	return nil
}
