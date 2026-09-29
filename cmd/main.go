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
		log.Fatalf("初始化日志器失败: %v", err)
	}
	slog.SetDefault(slog.New(zapslog.NewHandler(logger.Core())))

	fmt.Println("🚀 欢迎使用 goaw!")

	workspace, _ := os.Getwd()
	// workspace = filepath.Join(workspace, "tmp")

	llmProvider, err := provider.NewAnthropicProvider("glm-5.3-flash")
	if err != nil {
		log.Fatalf("创建模型提供者失败: %v", err)
	}

	toolRegistry := tools.NewDefaultRegistry(
		tools.NewFileReader(),
		tools.NewFileWriter(),
		tools.NewFileEditor(),
		tools.NewBashExecutor(),
	)

	eng, err := engine.NewAgentEngine(llmProvider, toolRegistry, true)
	if err != nil {
		log.Fatalf("创建 Agent 引擎失败: %v", err)
	}

	go func() {
		bot, err := createFeishuBot(eng, workspace)
		if err != nil {
			slog.Error("创建飞书机器人失败", "error", err)
			return
		}
		slog.Info("飞书机器人创建成功")

		err = startFeishuBot(bot)
		if err != nil {
			slog.Error("启动飞书机器人失败", "error", err)
			return
		}
		slog.Info("飞书机器人启动成功")
	}()

	// prompt := `
	// 在当前目录下增加一个 ip.go 文件，文件内容如下：
	// 提供一个简单的获取当前 IP 地址的接口。
	// 写完之后，帮我把代码用 git 提交一下。 `
	// sess := engine.NewSession("terminal", workspace)
	// if err = eng.Run(context.Background(), sess, prompt, reporter.NewTerminalReporter()); err != nil {
	// 	log.Fatalf("运行 Agent 引擎失败: %v", err)
	// }
}

func createFeishuBot(eng *engine.AgentEngine, workspace string) (*reporter.FeishuBot, error) {
	appID := os.Getenv("FEISHU_APP_ID")
	appSecret := os.Getenv("FEISHU_APP_SECRET")

	bot, err := reporter.NewFeishuBot(appID, appSecret, workspace, eng)
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
