package context

import (
	"strings"
	"testing"

	"github.com/jrmarcco/goaw/internal/schema"
)

func TestAnalyzeAndInject(t *testing.T) {
	t.Parallel()

	m := NewRecoveryManager()

	t.Run("known codes inject hints", func(t *testing.T) {
		t.Parallel()

		codes := []schema.ToolErrorCode{
			schema.ErrCodeFileNotFound,
			schema.ErrCodePermissionDenied,
			schema.ErrCodeEditNoMatch,
			schema.ErrCodeEditAmbiguous,
			schema.ErrCodeCmdNotFound,
			schema.ErrCodeCmdSyntaxError,
			schema.ErrCodeCmdTimeout,
		}
		for _, code := range codes {
			got := m.AnalyzeAndInject("any_tool", code, "raw output")
			if got == "raw output" {
				t.Fatalf("code %q should inject a hint, got raw output unchanged", code)
			}
			if !strings.Contains(got, "raw output") || !strings.Contains(got, "[系统自救指南]") {
				t.Fatalf("code %q injected = %q, want original output plus hint", code, got)
			}
		}
	})

	t.Run("tool not found hint mentions tool name", func(t *testing.T) {
		t.Parallel()

		got := m.AnalyzeAndInject("hallucinated_tool", schema.ErrCodeToolNotFound, "工具 hallucinated_tool 未注册")
		if !strings.Contains(got, "hallucinated_tool") || !strings.Contains(got, "[系统自救指南]") {
			t.Fatalf("injected = %q, want hint containing the tool name", got)
		}
	})

	t.Run("unknown and empty codes pass through unchanged", func(t *testing.T) {
		t.Parallel()

		for _, code := range []schema.ToolErrorCode{schema.ErrCodeUnknown, schema.ErrCodeIOFailure, schema.ErrCodeCmdFailed, ""} {
			if got := m.AnalyzeAndInject("any_tool", code, "raw output"); got != "raw output" {
				t.Fatalf("code %q should pass through unchanged, got %q", code, got)
			}
		}
	})
}
