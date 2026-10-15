package eval

import (
	"context"
)

// TestCase 定义需要 Agent 去完成并验证的独立任务。
type TestCase struct {
	ID     string
	Name   string
	Prompt string

	SetupScript    string // Agent 运行前的初始化 bash 脚本 ( 用于初始化靶机代码 )
	ValidateScript string // Agent 运行结束后的校验 bash 脚本 ( exit 0 视为成功 )

	MaxTurns int // 允许 Agent 尝试的最大轮数 ( 超时视为失败 )
}

// TestResult 存放单词跑分结果。
type TestResult struct {
	TestCaseID string

	Passed     bool
	CastTokens int
	DurationMs int64

	ErrMsg string
}

type BenchmarkRunner struct {
	model string
}

func NewBenchmarkRunner(model string) *BenchmarkRunner {
	return &BenchmarkRunner{model: model}
}

//nolint:revive // 存根待实现: 自动化评测属后续计划，ctx 为届时签名预留。
func (r *BenchmarkRunner) RunSuite(ctx context.Context, tcs []TestCase) {
	// TODO: not implemented
	panic("not implemented")
}

//nolint:revive,unused // 存根待实现: 自动化评测属后续计划，签名与调用关系为届时预留。
func (r *BenchmarkRunner) runTest(ctx context.Context, tc TestCase) TestResult {
	// TODO: not implemented
	panic("not implemented")
}
