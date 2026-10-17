package eval

import (
	"strings"
	"testing"
	"time"

	"github.com/jrmarcco/goaw/internal/obs"
)

// shortText 截断测试用的短样本文本。
const shortText = "hello"

func TestTruncate(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		in    string
		limit int
		want  string
	}{
		{"短字符串原样返回", shortText, 10, shortText},
		{"恰好等于上限原样返回", shortText, 5, shortText},
		{"超限截断并追加标记", shortText + " world", 5, shortText + "...( 已截断 )"},
		{"多字节字符按 rune 截断", "你好世界你好世界", 4, "你好世界...( 已截断 )"},
		{"非正上限返回空", shortText, 0, ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := truncate(tc.in, tc.limit); got != tc.want {
				t.Errorf("truncate(%q, %d) = %q, want %q", tc.in, tc.limit, got, tc.want)
			}
		})
	}
}

func TestNewTokenUsage(t *testing.T) {
	t.Parallel()

	snap := obs.CostSnapshot{
		Calls:               3,
		PromptTokens:        10,
		CompletionTokens:    5,
		CacheReadTokens:     2,
		CacheCreationTokens: 1,
		TotalTokens:         15,
		BusyDuration:        1500 * time.Millisecond,
	}

	u := newTokenUsage(snap)
	if u.Calls != 3 || u.PromptTokens != 10 || u.CompletionTokens != 5 ||
		u.CacheReadTokens != 2 || u.CacheCreationTokens != 1 || u.TotalTokens != 15 {
		t.Errorf("token 维度转写错误: %+v", u)
	}
	if u.BusyDurationMs != 1500 {
		t.Errorf("BusyDurationMs = %d, want 1500", u.BusyDurationMs)
	}
}

func TestBuildRepairFeedback(t *testing.T) {
	t.Parallel()

	t.Run("包含校验输出与运行错误", func(t *testing.T) {
		t.Parallel()

		fb := buildRepairFeedback(2, "assert failed", "engine boom")
		for _, want := range []string{"第 2 次尝试未通过校验", "assert failed", "engine boom"} {
			if !strings.Contains(fb, want) {
				t.Errorf("反馈消息缺少关键信息 %q: %s", want, fb)
			}
		}
	})

	t.Run("无运行错误时不包含错误段", func(t *testing.T) {
		t.Parallel()

		fb := buildRepairFeedback(1, "assert failed", "")
		if strings.Contains(fb, "运行过程报错") {
			t.Errorf("无运行错误时不应包含错误段: %s", fb)
		}
	})
}

// suiteFixtures 返回混合场景的用例结果与时间边界 ( 每次调用产生独立副本 )。
func suiteFixtures() (oneShot, repaired, failed TestResult, started, ended time.Time) {
	started = time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	ended = started.Add(time.Minute)

	oneShot = TestResult{
		TestCaseID: "case-1", Passed: true, OneShot: true, Attempts: 1,
		TotalUsage: TokenUsage{Calls: 2, PromptTokens: 60, CompletionTokens: 40, TotalTokens: 100},
	}
	repaired = TestResult{
		TestCaseID: "case-2", Passed: true, OneShot: false, Attempts: 3,
		TotalUsage:  TokenUsage{Calls: 10, PromptTokens: 300, CompletionTokens: 200, TotalTokens: 500},
		WastedUsage: TokenUsage{Calls: 6, PromptTokens: 180, CompletionTokens: 120, TotalTokens: 300},
	}
	failed = TestResult{
		TestCaseID: "case-3", Passed: false, Attempts: 3, ErrMsg: "3 次尝试均未通过校验",
		TotalUsage:  TokenUsage{Calls: 8, TotalTokens: 400},
		WastedUsage: TokenUsage{Calls: 8, TotalTokens: 400},
	}
	return oneShot, repaired, failed, started, ended
}

func TestBuildSuiteReportMixed(t *testing.T) {
	t.Parallel()

	oneShot, repaired, failed, started, ended := suiteFixtures()
	report := buildSuiteReport("m", []TestResult{oneShot, repaired, failed}, started, ended)

	if report.Total != 3 || report.Passed != 2 || report.Failed != 1 {
		t.Errorf("计数错误: total=%d passed=%d failed=%d", report.Total, report.Passed, report.Failed)
	}
	if report.OneShotPassed != 1 || report.RepairPassed != 1 {
		t.Errorf("试错分层错误: one_shot=%d repair=%d", report.OneShotPassed, report.RepairPassed)
	}
	if report.PassRate != 2.0/3.0 {
		t.Errorf("PassRate = %v, want %v", report.PassRate, 2.0/3.0)
	}
	if report.OneShotRate != 1.0/3.0 {
		t.Errorf("OneShotRate = %v, want %v", report.OneShotRate, 1.0/3.0)
	}
	// 通过用例的平均尝试次数: ( 1 + 3 ) / 2。
	if report.AvgAttemptsPassed != 2.0 {
		t.Errorf("AvgAttemptsPassed = %v, want 2", report.AvgAttemptsPassed)
	}
	if report.TotalUsage.TotalTokens != 1000 || report.WastedUsage.TotalTokens != 700 {
		t.Errorf("token 聚合错误: total=%d wasted=%d", report.TotalUsage.TotalTokens, report.WastedUsage.TotalTokens)
	}
	if report.WastedTokenRate != 0.7 {
		t.Errorf("WastedTokenRate = %v, want 0.7", report.WastedTokenRate)
	}
	if report.ElapsedMs != 60000 {
		t.Errorf("ElapsedMs = %d, want 60000", report.ElapsedMs)
	}
}

func TestBuildSuiteReportEmpty(t *testing.T) {
	t.Parallel()

	_, _, _, started, ended := suiteFixtures()
	report := buildSuiteReport("m", nil, started, ended)
	if report.Total != 0 || report.PassRate != 0 || report.OneShotRate != 0 ||
		report.AvgAttemptsPassed != 0 || report.WastedTokenRate != 0 {
		t.Errorf("空结果应全零: %+v", report)
	}
}

func TestBuildSuiteReportAllOneShot(t *testing.T) {
	t.Parallel()

	oneShot, _, _, started, ended := suiteFixtures()
	report := buildSuiteReport("m", []TestResult{oneShot, oneShot}, started, ended)
	if report.PassRate != 1 || report.OneShotRate != 1 || report.RepairPassed != 0 || report.WastedTokenRate != 0 {
		t.Errorf("全一次性通过的指标错误: %+v", report)
	}
}
