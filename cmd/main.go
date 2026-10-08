package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/jrmarcco/goaw/internal/engine"
	"github.com/jrmarcco/goaw/internal/provider"
	"github.com/jrmarcco/goaw/internal/reporter"
	"github.com/jrmarcco/goaw/internal/schema"
	"github.com/jrmarcco/goaw/internal/tools"
	"go.uber.org/zap"
	"go.uber.org/zap/exp/zapslog"
)

func main() {
	// 通过命令行参数接收用户 prompt。
	// go run cmd/claw/main.go -prompt="我需要你搭建一个极简的 Go 语言 Web Server 项目。"
	prompt := flag.String("prompt", "", "提交给 Agent 执行的任务描述。")
	flag.Parse()

	if *prompt == "" {
		// TODO: 增加提示。
		os.Exit(1)
	}

	// 初始化 slog。
	logger, err := zap.NewDevelopment()
	if err != nil {
		log.Fatalf("初始化日志器失败: %v", err)
	}
	slog.SetDefault(slog.New(zapslog.NewHandler(logger.Core())))

	fmt.Println("🚀 欢迎使用 goaw!")

	workspace, _ := os.Getwd()
	// TODO: 测试用
	workspace = filepath.Join(workspace, "tmp")

	llmProvider, err := provider.NewAnthropicProvider("glm-5.3")
	if err != nil {
		log.Fatalf("创建模型提供者失败: %v", err)
	}

	// 挂载 4 个基础工具。
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

	// 会话管理器为进程级单例，由所有入口 ( 飞书机器人、终端 ) 共享，
	// 保证同一会话 ID 在任何入口都命中同一个会话。
	sessions := engine.NewSessionManager()

	go func() {
		bot, err := createFeishuBot(eng, sessions, workspace)
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

	// TODO: 测试用
	tr := reporter.NewTerminalReporter()
	sess := sessions.Get("test_web_server_session", workspace, true)

	log.Printf("\n>>> 🚀 收到指令: %s\n", *prompt)

	sess.Append(schema.Message{
		Role:    schema.RoleUser,
		Content: *prompt,
	})

	if err := eng.Run(context.Background(), sess, tr); err != nil {
		log.Fatalf("引擎运行崩溃: %v", err)
	}
}

func createFeishuBot(eng *engine.AgentEngine, sessions *engine.SessionManager, workspace string) (*reporter.FeishuBot, error) {
	appID := os.Getenv("FEISHU_APP_ID")
	appSecret := os.Getenv("FEISHU_APP_SECRET")

	bot, err := reporter.NewFeishuBot(appID, appSecret, workspace, eng, sessions)
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
