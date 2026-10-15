package logging

import (
	"bytes"
	"errors"
	"log/slog"
	"regexp"
	"strings"
	"testing"
)

// 测试常量 ( 避免魔数直书 )。
const (
	truncateLimit  = 5
	timeLineRegexp = `^\d{2}:\d{2}:\d{2}\.\d{3} `
)

// newTestLogger 构造携带组件标识的测试日志器。
func newTestLogger(buf *bytes.Buffer, level slog.Leveler, color bool) *slog.Logger {
	return slog.New(NewConsoleHandler(buf, level, color)).With(componentKey, "engine")
}

func TestConsoleHandlerSingleLine(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	logger := newTestLogger(&buf, slog.LevelInfo, false)

	logger.Info("运行开始", "session", "s1", "workspace", "/ws")
	logger.Debug("不应出现")

	line := buf.String()
	if matched, err := regexp.MatchString(timeLineRegexp, line); err != nil || !matched {
		t.Fatalf("行首应为毫秒时间戳，实际 %q", line)
	}
	if want := "INFO  engine     运行开始 session=s1 workspace=/ws"; !strings.Contains(line, want) {
		t.Fatalf("应包含紧凑单行字段，实际 %q", line)
	}
	if strings.Count(line, "\n") != 1 {
		t.Fatalf("应为单行输出，实际 %d 行", strings.Count(line, "\n"))
	}
	if strings.Contains(line, "不应出现") {
		t.Fatal("低于级别阈值的记录不应输出")
	}
	if strings.Contains(line, "\x1b[") {
		t.Fatal("未开颜色时不应包含 ANSI 转义码")
	}
}

func TestConsoleHandlerColor(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	logger := newTestLogger(&buf, slog.LevelInfo, true)
	logger.Info("运行开始")

	line := buf.String()
	if !strings.Contains(line, ansiBlue+"INFO ") {
		t.Fatalf("INFO 徽应着蓝色，实际 %q", line)
	}
	if !strings.Contains(line, ansiCyan+"engine") {
		t.Fatalf("组件列应着青色，实际 %q", line)
	}
	if !strings.Contains(line, ansiReset) {
		t.Fatalf("着色后应重置颜色，实际 %q", line)
	}
}

func TestConsoleHandlerValueQuoting(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	logger := newTestLogger(&buf, slog.LevelInfo, false)
	logger.Error("失败", "error", errors.New("boom"), "reason", "包含 空格")

	line := buf.String()
	if !strings.Contains(line, "error=boom") {
		t.Fatalf("error 值应渲染错误信息，实际 %q", line)
	}
	if !strings.Contains(line, `reason="包含 空格"`) {
		t.Fatalf("含空白的值应加引号，实际 %q", line)
	}
}

func TestTruncate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "未超限原样返回", in: "abc", want: "abc"},
		{name: "超限截断并标注剩余", in: "abcdefghij", want: "abcde…(+5)"},
		{name: "中文按 rune 计数", in: "中文字符测试", want: "中文字符测…(+1)"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := Truncate(tc.in, truncateLimit); got != tc.want {
				t.Fatalf("Truncate(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestTruncateNonPositiveLimit(t *testing.T) {
	t.Parallel()

	if got := Truncate("abcdef", 0); got != "abcdef" {
		t.Fatalf("limit<=0 应原样返回，实际 %q", got)
	}
}

func TestParseLevel(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want slog.Level
	}{
		{name: "空串回落 info", in: "", want: slog.LevelInfo},
		{name: "debug", in: "debug", want: slog.LevelDebug},
		{name: "大小写不敏感", in: " WARN ", want: slog.LevelWarn},
		{name: "error", in: "error", want: slog.LevelError},
		{name: "非法值回落 info", in: "bogus", want: slog.LevelInfo},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := ParseLevel(tc.in); got != tc.want {
				t.Fatalf("ParseLevel(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestForAttachesComponent(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	previous := slog.Default()
	t.Cleanup(func() {
		slog.SetDefault(previous)
	})
	slog.SetDefault(slog.New(NewConsoleHandler(&buf, slog.LevelInfo, false)))

	For("approval").Warn("审核拒绝", "tool", "bash")

	if line := buf.String(); !strings.Contains(line, "approval   审核拒绝 tool=bash") {
		t.Fatalf("For 应注入组件列，实际 %q", line)
	}
}
