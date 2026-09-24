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
	// workspace = filepath.Join(workspace, "tmp")

	llmProvider, err := provider.NewAnthropicProvider("glm-5.3-flash")
	if err != nil {
		log.Fatalf("failed to create provider: %v", err)
	}

	toolRegistry := tools.NewDefaultRegistry(
		tools.NewFileReader(workspace),
		tools.NewFileWriter(workspace),
		tools.NewFileEditor(workspace),
		tools.NewBashExecutor(workspace),
	)

	eng, err := engine.NewAgentEngine(workspace, llmProvider, toolRegistry, true)
	if err != nil {
		log.Fatalf("failed to create agent engine: %v", err)
	}

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

	// prompt := `
	// 在当前目录下增加一个 ip.go 文件，文件内容如下：
	// 提供一个简单的获取当前 IP 地址的接口。
	// 写完之后，帮我把代码用 git 提交一下。 `
	// if err = eng.Run(context.Background(), prompt, reporter.NewTerminalReporter()); err != nil {
	// 	log.Fatalf("failed to run agent engine: %v", err)
	// }
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
