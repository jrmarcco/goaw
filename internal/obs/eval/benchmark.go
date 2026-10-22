// Package eval 提供自动化 Harness Benchmark: 用例驱动的 Agent 任务评估。
package eval

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/jrmarcco/goaw/internal/engine"
	"github.com/jrmarcco/goaw/internal/logging"
	"github.com/jrmarcco/goaw/internal/obs"
	"github.com/jrmarcco/goaw/internal/provider"
	"github.com/jrmarcco/goaw/internal/schema"
	"github.com/jrmarcco/goaw/internal/tools"
)

const (
	// defaultModel 缺省模型标识 ( 与 cmd/main.go 保持一致 )。
	defaultModel = "glm-5.3"

	// defaultMaxAttempts 单用例修复轮上限 ( TestCase.MaxAttempts ≤0 时生效 )。
	defaultMaxAttempts = 3

	// defaultTokenBudget 单次尝试的 token 预算缺省值，
	// 环境变量 GOAW_TOKEN_BUDGET 覆盖 ( ≤0 关闭预算拦截，与 cmd/main.go 同口径 )。
	defaultTokenBudget = 1_000_000

	// validateFeedbackLimit 修复轮反馈消息中校验输出的截断上限 ( rune 数 )。
	validateFeedbackLimit = 4000

	// validateSummaryLimit 报告与错误摘要中校验输出的截断上限 ( rune 数 )。
	validateSummaryLimit = 500

	// runErrLimit 反馈消息中运行错误的截断上限 ( rune 数 )。
	runErrLimit = 512
)

// dirPerm 目录权限: rwxr-x---。
const dirPerm os.FileMode = 0o750

// TestCase 定义需要 Agent 去完成并验证的独立任务。
type TestCase struct {
	ID     string
	Name   string
	Prompt string

	SetupScript    string // Agent 运行前的初始化 bash 脚本 ( 用于初始化靶机代码 )
	ValidateScript string // Agent 运行结束后的校验 bash 脚本 ( exit 0 视为成功，空脚本直接通过 )

	MaxTurns    int // 单次尝试允许 Agent 执行的最大轮数 ( 超限强制收尾 )
	MaxAttempts int // 修复轮上限 ( 校验失败后带着反馈重试，≤0 用 defaultMaxAttempts )
}

// TokenUsage 是报告口径的 token 消耗 ( obs.CostSnapshot 的可序列化转写 )。
type TokenUsage struct {
	Calls               int   `json:"calls"`
	PromptTokens        int   `json:"promptTokens"`
	CompletionTokens    int   `json:"completionTokens"`
	CacheReadTokens     int   `json:"cacheReadTokens"`
	CacheCreationTokens int   `json:"cacheCreationTokens"`
	TotalTokens         int   `json:"totalTokens"`    // Prompt + Completion 合并口径
	BusyDurationMs      int64 `json:"busyDurationMs"` // 模型调用耗时累计 ( 不含工具执行 )
}

// add 就地累加一份消耗 ( 用例与套件两层聚合复用 )。
func (u *TokenUsage) add(other TokenUsage) {
	u.Calls += other.Calls
	u.PromptTokens += other.PromptTokens
	u.CompletionTokens += other.CompletionTokens
	u.CacheReadTokens += other.CacheReadTokens
	u.CacheCreationTokens += other.CacheCreationTokens
	u.TotalTokens += other.TotalTokens
	u.BusyDurationMs += other.BusyDurationMs
}

// newTokenUsage 把记账器快照转写为报告口径。
func newTokenUsage(s obs.CostSnapshot) TokenUsage {
	return TokenUsage{
		Calls:               s.Calls,
		PromptTokens:        s.PromptTokens,
		CompletionTokens:    s.CompletionTokens,
		CacheReadTokens:     s.CacheReadTokens,
		CacheCreationTokens: s.CacheCreationTokens,
		TotalTokens:         s.TotalTokens,
		BusyDurationMs:      s.BusyDuration.Milliseconds(),
	}
}

// AttemptRecord 记录一次尝试 ( 一次 Agent 运行 + 一次校验脚本 )。
type AttemptRecord struct {
	Attempt         int        `json:"attempt"`                   // 1-based 序号
	Passed          bool       `json:"passed"`                    // 校验脚本 exit 0
	ElapsedMs       int64      `json:"elapsedMs"`                 // 本次尝试耗时 ( 含校验 )
	Usage           TokenUsage `json:"usage"`                     // 本次尝试 token 消耗 ( 独立记账器快照 )
	RunErr          string     `json:"runErr,omitempty"`          // 引擎运行错误 ( 出错仍执行校验 )
	ValidateSummary string     `json:"validateSummary,omitempty"` // 校验输出摘要 ( 截断 )
}

// TestResult 存放单用例的完整评估结果。
type TestResult struct {
	TestCaseID string `json:"testCaseId"`
	Name       string `json:"name"`

	Passed    bool  `json:"passed"`    // 任一尝试通过校验
	OneShot   bool  `json:"oneShot"`   // 首次尝试即通过 ( 试错成本核心读数 )
	Attempts  int   `json:"attempts"`  // 总尝试次数，1 = 一次性成功
	ElapsedMs int64 `json:"elapsedMs"` // 全部尝试累计耗时

	TotalUsage  TokenUsage `json:"totalUsage"`  // 全部尝试累计消耗
	WastedUsage TokenUsage `json:"wastedUsage"` // 失败尝试的累计消耗 ( 试错成本 )

	AttemptLog []AttemptRecord `json:"attemptLog"`
	ErrMsg     string          `json:"errMsg,omitempty"` // 基建错误 ( 沙箱/Setup/Provider 等，不计入 Attempts )
}

type BenchmarkRunner struct {
	model    string
	sessions *engine.SessionManager
}

// NewBenchmarkRunner 创建评估运行器; model 为空时回退缺省模型。
func NewBenchmarkRunner(model string) *BenchmarkRunner {
	if model == "" {
		model = defaultModel
	}
	return &BenchmarkRunner{
		model:    model,
		sessions: engine.NewSessionManager(),
	}
}

// RunSuite 串行执行全部用例，返回带汇总指标的评估报告，并落盘 JSON 报告。
func (r *BenchmarkRunner) RunSuite(ctx context.Context, tcs []TestCase) SuiteReport {
	logging.For("benchmark").Info("启动自动化 Harness Benchmark 评估", "model", r.model, "cases", len(tcs))

	tokenBudget, err := tokenBudgetFromEnv()
	if err != nil {
		// 配置错误不中断整批评估，回退缺省预算并显式告警。
		logging.For("benchmark").Warn("token 预算环境变量解析失败，回退缺省值", "error", err)
		tokenBudget = defaultTokenBudget
	}

	startedAt := time.Now()
	results := make([]TestResult, 0, len(tcs))

	for _, tc := range tcs {
		logging.For("benchmark").Info("正在执行用例", "id", tc.ID, "name", tc.Name)

		res := r.runTest(ctx, tc, tokenBudget)
		results = append(results, res)

		attrs := []any{
			"id", res.TestCaseID,
			"passed", res.Passed,
			"attempts", res.Attempts,
			"one_shot", res.OneShot,
			"total_tokens", res.TotalUsage.TotalTokens,
			"wasted_tokens", res.WastedUsage.TotalTokens,
			"elapsed_ms", res.ElapsedMs,
		}
		if res.ErrMsg != "" {
			attrs = append(attrs, "err_msg", res.ErrMsg)
		}
		logging.For("benchmark").Info("用例结束", attrs...)
	}

	report := buildSuiteReport(r.model, results, startedAt, time.Now())
	logSummary(report)

	if path := defaultReportPath(); writeJSONReport(report, path) != nil {
		// 落盘失败不影响评估结论，报告对象仍随返回值交付调用方。
		logging.For("benchmark").Warn("评估报告落盘失败", "path", path)
	} else {
		logging.For("benchmark").Info("评估报告已写入", "path", path)
	}

	return report
}

// runTest 执行单个用例: 基建准备 + 同会话修复轮循环。
//
// 修复轮语义:
// 校验失败后把校验输出作为反馈注入同一会话，
// Agent 带着完整上下文继续修复，直至通过或次数耗尽;
// 由此区分「一次性成功」与「重试 N 次后成功」，并把失败尝试的消耗记入试错成本。
func (r *BenchmarkRunner) runTest(ctx context.Context, tc TestCase, tokenBudget int) TestResult {
	res := TestResult{TestCaseID: tc.ID, Name: tc.Name}

	workspace, err := prepareSandbox(ctx, tc)
	if err != nil {
		res.ErrMsg = err.Error()
		return res
	}

	llmProvider, err := newLLMProvider(r.model)
	if err != nil {
		res.ErrMsg = err.Error()
		return res
	}

	toolRegistry := newBenchToolRegistry()
	runOpts := attemptRunOpts(tc)
	maxAttempts := attemptLimit(tc)

	// 同一用例的修复轮共享会话 ( 历史跨 Run 累积 );
	// 会话 ID 带时间戳隔离，避免同进程多次 RunSuite 相互污染。
	session := r.sessions.Get(fmt.Sprintf("%s_%d", tc.ID, time.Now().Unix()), workspace, false)

	message := tc.Prompt
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if ctxErr := ctx.Err(); ctxErr != nil {
			res.ErrMsg = fmt.Sprintf("上下文已取消: %s", ctxErr.Error())
			break
		}

		// 每次尝试独立的记账器与引擎: 快照即本次尝试的真实消耗，无需差值运算。
		eng, tracker, engErr := newAttemptEngine(llmProvider, toolRegistry, r.model, tokenBudget)
		if engErr != nil {
			res.ErrMsg = engErr.Error()
			return res
		}

		// 首次尝试注入任务描述，修复轮注入上一次的校验反馈。
		session.Append(schema.Message{Role: schema.RoleUser, Content: message})

		record, validateOut := executeAttempt(ctx, eng, tracker, session, tc, attempt, runOpts)

		res.Attempts = attempt
		res.ElapsedMs += record.ElapsedMs
		res.TotalUsage.add(record.Usage)
		res.AttemptLog = append(res.AttemptLog, record)

		logging.For("benchmark").Debug(
			"尝试结束",
			"id", tc.ID,
			"attempt", attempt,
			"passed", record.Passed,
			"tokens", record.Usage.TotalTokens,
		)

		if record.Passed {
			res.Passed = true
			res.OneShot = attempt == 1
			break
		}

		// 失败尝试的消耗计入试错成本。
		res.WastedUsage.add(record.Usage)
		message = buildRepairFeedback(attempt, validateOut, record.RunErr)
	}

	fillFailureReason(&res)
	return res
}

// prepareSandbox 创建沙箱目录并执行 Setup 脚本 ( 可选 )。
func prepareSandbox(ctx context.Context, tc TestCase) (string, error) {
	// 沙箱目录即 Session 工作区，本身必须存在，否则后续脚本与工具执行都会因 chdir 失败。
	base, _ := os.Getwd()
	workspace := filepath.Join(base, fmt.Sprintf("workspaces/%s_%d", tc.ID, time.Now().Unix()))
	if err := os.MkdirAll(workspace, dirPerm); err != nil {
		return "", fmt.Errorf("创建沙箱目录 [%s] 失败: %w", workspace, err)
	}

	if tc.SetupScript == "" {
		return workspace, nil
	}

	//nolint:gosec // G204: Setup 脚本由基准用例作者显式定义，属受信输入，Harness 的职责就是执行它。
	cmd := exec.CommandContext(ctx, "bash", "-c", tc.SetupScript)
	cmd.Dir = workspace
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("setup 脚本执行失败: %w", err)
	}

	return workspace, nil
}

// newLLMProvider 从环境变量装配模型提供者。
func newLLMProvider(model string) (provider.LLMProvider, error) {
	var llmOpts []provider.Opt
	if baseURL := os.Getenv("ANTHROPIC_BASE_URL"); baseURL != "" {
		llmOpts = append(llmOpts, provider.WithBaseURL(baseURL))
	}
	return provider.NewAnthropicProvider(os.Getenv("ANTHROPIC_API_KEY"), model, llmOpts...)
}

// newBenchToolRegistry 装配基准评估用的工具集。
func newBenchToolRegistry() *tools.DefaultRegistry {
	return tools.NewDefaultRegistry(
		tools.NewFileReader(),
		tools.NewFileWriter(),
		tools.NewFileEditor(),
		tools.NewBashExecutor(),
	)
}

// newAttemptEngine 为单次尝试装配独立的记账器与引擎。
func newAttemptEngine(
	llmProvider provider.LLMProvider,
	registry tools.Registry,
	model string,
	tokenBudget int,
) (*engine.AgentEngine, *obs.CostTracker, error) {
	costTracker := obs.NewCostTracker(model, tokenBudget)

	eng, err := engine.NewAgentEngine(
		llmProvider, registry, false,
		engine.AgentEngineWithCostObserver(costTracker),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("创建 Agent 引擎失败: %w", err)
	}
	return eng, costTracker, nil
}

// executeAttempt 执行一次「Agent 运行 + 校验」，产出尝试记录与校验原始输出 ( 供修复反馈拼接 )。
func executeAttempt(
	ctx context.Context,
	eng *engine.AgentEngine,
	tracker *obs.CostTracker,
	sess *engine.Session,
	tc TestCase,
	attempt int,
	runOpts []engine.RunOpt,
) (record AttemptRecord, validateOut string) {
	startedAt := time.Now()

	// 传入空的 reporter 屏蔽普通日志 ( 防止刷屏 )。
	runErr := eng.Run(ctx, sess, nil, runOpts...)

	// 引擎报错不拦截校验: 工作区状态是唯一事实，Agent 可能在报错前已完成工作。
	validateOut, validateErr := runValidate(ctx, sess.Workspace, tc.ValidateScript)

	record = AttemptRecord{
		Attempt:   attempt,
		ElapsedMs: time.Since(startedAt).Milliseconds(),
	}
	if runErr != nil {
		record.RunErr = runErr.Error()
	}
	record.ValidateSummary = truncate(validateOut, validateSummaryLimit)
	record.Passed = validateErr == nil
	record.Usage = newTokenUsage(tracker.Snapshot())
	return record, validateOut
}

// attemptRunOpts 组装单次尝试的运行选项 ( 轮次预算，≤0 不设限 )。
func attemptRunOpts(tc TestCase) []engine.RunOpt {
	if tc.MaxTurns <= 0 {
		return nil
	}
	return []engine.RunOpt{engine.WithMaxTurns(tc.MaxTurns)}
}

// attemptLimit 解析修复轮上限 ( ≤0 用缺省值 )。
func attemptLimit(tc TestCase) int {
	if tc.MaxAttempts <= 0 {
		return defaultMaxAttempts
	}
	return tc.MaxAttempts
}

// fillFailureReason 补充未通过用例的错误摘要 ( 取最后一次尝试的信号 )。
func fillFailureReason(res *TestResult) {
	if res.Passed || res.ErrMsg != "" || len(res.AttemptLog) == 0 {
		return
	}

	last := res.AttemptLog[len(res.AttemptLog)-1]
	res.ErrMsg = fmt.Sprintf("%d 次尝试均未通过校验", res.Attempts)
	if last.RunErr != "" {
		res.ErrMsg += fmt.Sprintf("; 最后一次运行错误: %s", truncate(last.RunErr, validateSummaryLimit))
	}
	if last.ValidateSummary != "" {
		res.ErrMsg += fmt.Sprintf("; 最后一次校验输出: %s", last.ValidateSummary)
	}
}

// runValidate 在沙箱内执行校验脚本，返回合并输出; 空脚本直接通过 ( exit 0 )。
func runValidate(ctx context.Context, workspace, script string) (string, error) {
	//nolint:gosec // G204: 校验脚本由基准用例作者显式定义，属受信输入，Harness 的职责就是执行它。
	cmd := exec.CommandContext(ctx, "bash", "-c", script)
	cmd.Dir = workspace

	out, err := cmd.CombinedOutput()
	return string(out), err
}

// buildRepairFeedback 组装修复轮注入的校验反馈消息。
func buildRepairFeedback(attempt int, validateOut, runErr string) string {
	var b strings.Builder

	fmt.Fprintf(&b, "[Benchmark 校验反馈] 第 %d 次尝试未通过校验", attempt)
	if runErr != "" {
		fmt.Fprintf(&b, "，且运行过程报错: %s", truncate(runErr, runErrLimit))
	}
	b.WriteString("。校验脚本输出如下 ( 可能截断 ):\n")
	b.WriteString(truncate(validateOut, validateFeedbackLimit))
	b.WriteString(`
	请根据以上反馈定位并修复问题，使校验脚本能够通过;
	不要重做整个任务，只修复未通过的部分。
	`)

	return b.String()
}

// truncate 按 rune 截断字符串，超限时追加截断标记 ( 避免切坏多字节字符 )。
func truncate(s string, limit int) string {
	if limit <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= limit {
		return s
	}
	return string(r[:limit]) + "...( 已截断 )"
}

// tokenBudgetFromEnv 解析单次尝试的 token 预算。
// 环境变量 GOAW_TOKEN_BUDGET 覆盖缺省值，≤0 表示关闭预算拦截。
func tokenBudgetFromEnv() (int, error) {
	raw := os.Getenv("GOAW_TOKEN_BUDGET")
	if raw == "" {
		return defaultTokenBudget, nil
	}
	parsed, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("GOAW_TOKEN_BUDGET 必须是整数: %w", err)
	}
	return parsed, nil
}
