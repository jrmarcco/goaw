package eval

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/jrmarcco/goaw/internal/logging"
)

// reportFilePerm 报告文件权限: rw------- ( 内容含校验输出，收紧到属主可读写 )。
const reportFilePerm os.FileMode = 0o600

// SuiteReport 是整批评估的汇总报告 ( JSON 落盘的机器可读口径 )。
type SuiteReport struct {
	Model     string    `json:"model"`
	StartedAt time.Time `json:"startedAt"`
	EndedAt   time.Time `json:"endedAt"`
	ElapsedMs int64     `json:"elapsedMs"`

	Total  int `json:"total"`
	Passed int `json:"passed"`
	Failed int `json:"failed"`

	// 试错成本核心读数: 一次性成功与重试后才成功的分层。
	OneShotPassed     int     `json:"oneShotPassed"`     // 一次性成功数
	RepairPassed      int     `json:"repairPassed"`      // 重试后才成功数
	PassRate          float64 `json:"passRate"`          // 最终通过率
	OneShotRate       float64 `json:"oneShotRate"`       // 一次性成功率
	AvgAttemptsPassed float64 `json:"avgAttemptsPassed"` // 通过用例的平均尝试次数 ( 1.0 为满分 )

	TotalUsage      TokenUsage `json:"totalUsage"`      // 全部用例累计消耗
	WastedUsage     TokenUsage `json:"wastedUsage"`     // 失败尝试累计消耗 ( 试错成本 )
	WastedTokenRate float64    `json:"wastedTokenRate"` // Wasted/Total，试错成本指数

	Results []TestResult `json:"results"`
}

// buildSuiteReport 由逐用例结果计算汇总指标 ( 纯函数，便于单测 )。
func buildSuiteReport(model string, results []TestResult, startedAt, endedAt time.Time) SuiteReport {
	report := SuiteReport{
		Model:     model,
		StartedAt: startedAt,
		EndedAt:   endedAt,
		ElapsedMs: endedAt.Sub(startedAt).Milliseconds(),
		Results:   results,
	}

	attemptsSum := 0
	// TestResult 体积较大，按索引迭代避免逐元素值拷贝。
	for i := range results {
		res := &results[i]

		report.Total++
		if res.Passed {
			report.Passed++
			attemptsSum += res.Attempts
			if res.OneShot {
				report.OneShotPassed++
			}
		} else {
			report.Failed++
		}
		report.TotalUsage.add(res.TotalUsage)
		report.WastedUsage.add(res.WastedUsage)
	}

	report.RepairPassed = report.Passed - report.OneShotPassed

	if report.Total > 0 {
		report.PassRate = float64(report.Passed) / float64(report.Total)
		report.OneShotRate = float64(report.OneShotPassed) / float64(report.Total)
	}
	if report.Passed > 0 {
		report.AvgAttemptsPassed = float64(attemptsSum) / float64(report.Passed)
	}
	if report.TotalUsage.TotalTokens > 0 {
		report.WastedTokenRate = float64(report.WastedUsage.TotalTokens) / float64(report.TotalUsage.TotalTokens)
	}

	return report
}

// logSummary 输出套件级汇总指标。
func logSummary(report SuiteReport) {
	logging.For("benchmark").Info(
		"自动化 Harness Benchmark 评估结束",
		"model", report.Model,
		"total", report.Total,
		"passed", report.Passed,
		"failed", report.Failed,
		"one_shot_passed", report.OneShotPassed,
		"repair_passed", report.RepairPassed,
		"pass_rate", report.PassRate,
		"one_shot_rate", report.OneShotRate,
		"avg_attempts_passed", report.AvgAttemptsPassed,
		"total_tokens", report.TotalUsage.TotalTokens,
		"wasted_tokens", report.WastedUsage.TotalTokens,
		"wasted_token_rate", report.WastedTokenRate,
	)
}

// defaultReportPath 生成报告落盘路径: benchmarks/report-<时间戳>.json。
func defaultReportPath() string {
	return filepath.Join("benchmarks", fmt.Sprintf("report-%s.json", time.Now().Format("20060102-150405")))
}

// writeJSONReport 把报告序列化写入指定路径 ( 自动创建目录 )。
func writeJSONReport(report SuiteReport, path string) error {
	if err := os.MkdirAll(filepath.Dir(path), dirPerm); err != nil {
		return fmt.Errorf("创建报告目录 [%s] 失败: %w", filepath.Dir(path), err)
	}

	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化报告失败: %w", err)
	}

	if err := os.WriteFile(path, data, reportFilePerm); err != nil {
		return fmt.Errorf("写入报告 [%s] 失败: %w", path, err)
	}
	return nil
}
