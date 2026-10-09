package main

import (
	"context"
	"fmt"
	"log"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/jrmarcco/goaw/internal/engine"
	"github.com/jrmarcco/goaw/internal/provider"
	"github.com/jrmarcco/goaw/internal/reporter"
	"github.com/jrmarcco/goaw/internal/tools"
)

func main() {
	// 初始化 slog。
	// logger, err := zap.NewDevelopment()
	// if err != nil {
	// 	log.Fatalf("初始化日志器失败: %v", err)
	// }
	// slog.SetDefault(slog.New(zapslog.NewHandler(logger.Core())))
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	})))

	// 通过命令行参数接收用户 prompt。
	// go run cmd/claw/main.go -prompt="我需要你搭建一个极简的 Go 语言 Web Server 项目。"
	// prompt := flag.String("prompt", "", "提交给 Agent 执行的任务描述。")
	// flag.Parse()

	// if *prompt == "" {
	// 	os.Exit(1)
	// }

	fmt.Println("🚀 欢迎使用 goaw!")

	workspace, _ := os.Getwd()
	// TODO: 测试用。
	workspace = filepath.Join(workspace, "tmp")

	// 凭证与端点通过环境变量注入，baseURL 作为可选参数传入。
	var llmOpts []provider.Opt
	if baseURL := os.Getenv("ANTHROPIC_BASE_URL"); baseURL != "" {
		llmOpts = append(llmOpts, provider.WithBaseURL(baseURL))
	}

	llmProvider, err := provider.NewAnthropicProvider(os.Getenv("ANTHROPIC_API_KEY"), "glm-5.3", llmOpts...)
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

	eng, err := engine.NewAgentEngine(llmProvider, toolRegistry, false)
	if err != nil {
		log.Fatalf("创建 Agent 引擎失败: %v", err)
	}

	// 会话管理器为进程级单例，由所有入口 ( 飞书机器人、终端 ) 共享，
	// 保证同一会话 ID 在任何入口都命中同一个会话。
	sessions := engine.NewSessionManager()

	// go func() {
	// 	bot, err := createFeishuBot(eng, sessions, workspace)
	// 	if err != nil {
	// 		slog.Error("创建飞书机器人失败", "error", err)
	// 		return
	// 	}
	// 	slog.Info("飞书机器人创建成功")

	// 	if err := startFeishuBot(bot); err != nil {
	// 		slog.Error("启动飞书机器人失败", "error", err)
	// 		return
	// 	}
	// 	slog.Info("飞书机器人启动成功")
	// }()

	bot, err := createFeishuBot(eng, sessions, workspace)
	if err != nil {
		slog.Error("创建飞书机器人失败", "error", err)
		return
	}
	slog.Info("飞书机器人创建成功")

	if err := startFeishuBot(bot); err != nil {
		slog.Error("启动飞书机器人失败", "error", err)
		return
	}
	slog.Info("飞书机器人启动成功")

	// TODO: 测试用。
	// tr := reporter.NewTerminalReporter()
	// sess := sessions.Get("test_web_server_session", workspace, false)

	// log.Printf("\n>>> 🚀 收到指令: %s\n", *prompt)

	// sess.Append(schema.Message{
	// 	Role:    schema.RoleUser,
	// 	Content: *prompt,
	// })

	// if err := eng.Run(context.Background(), sess, tr); err != nil {
	// 	log.Fatalf("引擎运行崩溃: %v", err)
	// }
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

	// 该 ctx 控制机器人客户端的整个运行生命周期,
	// 绑定进程信号,随 Ctrl+C 优雅退出,而非启动超时。
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	return bot.StartWithWebSocket(ctx, eventEncryptKey, verificationToken)
}
