package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"

	icontext "github.com/jrmarcco/goaw/internal/context"
	"github.com/jrmarcco/goaw/internal/engine"
	"github.com/jrmarcco/goaw/internal/obs"
	"github.com/jrmarcco/goaw/internal/provider"
	"github.com/jrmarcco/goaw/internal/reporter"
	"github.com/jrmarcco/goaw/internal/schema"
	"github.com/jrmarcco/goaw/internal/tools"
)

const (
	// llmModel 模型标识 ( 记账账本标签与实际调用目标 )。
	llmModel = "glm-5.3"

	// defaultTokenBudget token 预算缺省值 ( prompt+completion 合并口径，进程级累计 )，
	// 可用环境变量 GOAW_TOKEN_BUDGET 覆盖，≤0 关闭拦截。
	defaultTokenBudget = 1_000_000
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
	prompt := flag.String("prompt", "", "提交给 Agent 执行的任务描述。")
	flag.Parse()

	if *prompt == "" {
		os.Exit(1)
	}

	if err := run(*prompt); err != nil {
		log.Fatalf("引擎运行崩溃: %v", err)
	}
}

// run 承载主流程并统一以 error 收尾:
// 退出语义 ( log.Fatalf ) 只留在 main，避免中途退出跳过本函数内注册的 defer ( 如 OTel 冲刷 )。
func run(prompt string) error {
	// 配置了 OTLP 端点时启用 OpenTelemetry 镜像 ( 逐 span 流式导出 )，退出前冲刷缓冲;
	// 未配置时为空操作，行为与纯自研追踪一致。
	otelShutdown, err := setupOTelFromEnv()
	if err != nil {
		return fmt.Errorf("装配 OpenTelemetry 失败: %w", err)
	}
	defer shutdownOTel(otelShutdown)

	fmt.Println("🚀 欢迎使用 goaw!")

	workspace, _ := os.Getwd()
	// TODO: 测试用。
	workspace = filepath.Join(workspace, "tmp")

	// 凭证与端点通过环境变量注入，baseURL 作为可选参数传入。
	var llmOpts []provider.Opt
	if baseURL := os.Getenv("ANTHROPIC_BASE_URL"); baseURL != "" {
		llmOpts = append(llmOpts, provider.WithBaseURL(baseURL))
	}

	llmProvider, err := provider.NewAnthropicProvider(os.Getenv("ANTHROPIC_API_KEY"), llmModel, llmOpts...)
	if err != nil {
		return fmt.Errorf("创建模型提供者失败: %w", err)
	}

	// CostTracker: 进程级 token 记账与预算拦截 ( 阈值 ≤0 关闭，耗尽后强制收尾 )。
	costTracker := obs.NewCostTracker(llmModel, tokenBudgetFromEnv())

	// 自研追踪树的导出器: 运行结束把整棵树以 JSON 打进日志流。
	traceExporter := obs.NewSlogExporter()

	// 挂载 4 个基础工具。
	toolRegistry := tools.NewDefaultRegistry(
		tools.NewFileReader(),
		tools.NewFileWriter(),
		tools.NewFileEditor(),
		tools.NewBashExecutor(),
	)

	// 只读工具自动放行，写/执行类工具进入终端人工审核。
	approvalManager := tools.NewApprovalManager(
		tools.ApprovalManagerWithApprover(reporter.NewTerminalApprover()),
	)
	toolRegistry.Use(approvalManager.Middleware())

	eng, err := engine.NewAgentEngine(
		llmProvider, toolRegistry, false,
		engine.AgentEngineWithCostObserver(costTracker),
		engine.AgentEngineWithTraceExporter(traceExporter),
	)
	if err != nil {
		return fmt.Errorf("创建 Agent 引擎失败: %w", err)
	}

	// Multi-Agent: SubAgent 按角色规格 ( SubAgentSpec ) 装配，角色决定 System Prompt 与工具集。
	subagentTool, err := newSubagentTool(llmProvider, costTracker, traceExporter)
	if err != nil {
		return fmt.Errorf("装配 SubAgent 工具失败: %w", err)
	}
	if err := toolRegistry.Register(subagentTool); err != nil {
		return fmt.Errorf("注册 SubAgent 工具失败: %w", err)
	}

	// 会话管理器为进程级单例，由所有入口 ( 飞书机器人、终端 ) 共享，
	// 保证同一会话 ID 在任何入口都命中同一个会话。
	sessions := engine.NewSessionManager()

	// go func() {
	// 	bot, err := createFeishuBot(eng, toolRegistry, sessions, workspace)
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

	// TODO: 测试用。
	tr := reporter.NewTerminalReporter()
	sess := sessions.Get("test_web_server_session", workspace, false)

	log.Printf("\n>>> 🚀 收到指令: %s\n", prompt)

	sess.Append(schema.Message{
		Role:    schema.RoleUser,
		Content: prompt,
	})

	if err := eng.Run(context.Background(), sess, tr); err != nil {
		return fmt.Errorf("引擎运行崩溃: %w", err)
	}
	return nil
}

// tokenBudgetFromEnv 解析 token 预算。
// 环境变量 GOAW_TOKEN_BUDGET 覆盖缺省值，≤0 表示关闭拦截。
func tokenBudgetFromEnv() int {
	raw := os.Getenv("GOAW_TOKEN_BUDGET")
	if raw == "" {
		return defaultTokenBudget
	}
	parsed, err := strconv.Atoi(raw)
	if err != nil {
		log.Fatalf("GOAW_TOKEN_BUDGET 必须是整数: %v", err)
	}
	return parsed
}

// newSubagentTool 装配 Multi-Agent 的 SubAgent 工具。
//
// Explorer 是只读信息收集角色:
// 注册表不含 spawn_subagent ( 天然无递归 )，
// bash 经白名单中间件硬性约束为只读，不挂审核中间件 ( 只读由构造保证，审核留给主 Agent 的写路径 )。
func newSubagentTool(
	llmProvider provider.LLMProvider,
	costTracker *obs.CostTracker,
	traceExporter obs.Exporter,
) (*tools.SubagentTool, error) {
	subRegistry := tools.NewDefaultRegistry(
		tools.NewFileReader(),
		tools.NewBashExecutor(),
	)
	subRegistry.Use(tools.NewReadOnlyBashMiddleware())

	subRunner, err := engine.NewSubAgentRunner(
		llmProvider, false,
		engine.SubAgentRunnerWithCostObserver(costTracker),
		engine.SubAgentRunnerWithTraceExporter(traceExporter),
	)
	if err != nil {
		return nil, fmt.Errorf("创建 SubAgent 运行器失败: %w", err)
	}

	// explorerMaxTurns Explorer 角色的轮次预算。
	const explorerMaxTurns = 30

	explorerSpec := tools.SubAgentSpec{
		Name:         "explorer",
		Description:  "只读信息收集专员，在当前工作区内检索代码、日志与配置，返回带证据出处的调研结论。适用于检索、调研与总结，不适用于需要修改文件的任务。",
		SystemPrompt: icontext.ExplorerRolePrompt,
		MaxTurns:     explorerMaxTurns,
		Registry:     subRegistry,
	}

	return tools.NewSubagentTool(subRunner, []tools.SubAgentSpec{explorerSpec}, nil)
}

// setupOTelFromEnv 检测 OTLP 端点环境变量并装配 OpenTelemetry 镜像管线。
// 未配置时返回空操作，调用方无需判空。
func setupOTelFromEnv() (func(context.Context) error, error) {
	if os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") == "" {
		return func(context.Context) error { return nil }, nil
	}
	return obs.SetupOTel(context.Background())
}

// shutdownOTel 冲刷 OTel 缓冲区中的已结束跨度。
func shutdownOTel(shutdown func(context.Context) error) {
	if err := shutdown(context.Background()); err != nil {
		slog.Error("[trace] otel 冲刷失败", "error", err)
	}
}

//nolint:unused // 测试调试阶段。
func createFeishuBot(eng *engine.AgentEngine, registry *tools.DefaultRegistry, sessions *engine.SessionManager, workspace string) (*reporter.FeishuBot, error) {
	appID := os.Getenv("FEISHU_APP_ID")
	appSecret := os.Getenv("FEISHU_APP_SECRET")

	bot, err := reporter.NewFeishuBot(appID, appSecret, workspace, eng, sessions)
	if err != nil {
		return nil, err
	}

	// 飞书入口运行时，人工审核经飞书口令交互完成。
	// 注意: 与终端审核器互斥，启用飞书入口时应移除 main 中的终端审核器注册，
	// 否则先注册的中间件会先阻塞等待交互。
	registry.Use(
		tools.NewApprovalManager(
			tools.ApprovalManagerWithApprover(bot.Approver()),
		).Middleware(),
	)

	return bot, nil
}

//nolint:unused // 测试调试阶段。
func startFeishuBot(bot *reporter.FeishuBot) error {
	eventEncryptKey := os.Getenv("FEISHU_EVENT_ENCRYPT_KEY")
	verificationToken := os.Getenv("FEISHU_VERIFICATION_TOKEN")

	// 该 ctx 控制机器人客户端的整个运行生命周期,
	// 绑定进程信号,随 Ctrl+C 优雅退出,而非启动超时。
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	return bot.StartWithWebSocket(ctx, eventEncryptKey, verificationToken)
}
