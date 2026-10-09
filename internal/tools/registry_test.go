package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"testing"

	"github.com/jrmarcco/goaw/internal/schema"
)

// stubTool 可编程返回结果的测试替身工具。
type stubTool struct {
	name    string
	output  string
	execErr error
}

// 测试替身工具与工具调用的固定标识。
const (
	stubToolName = "stub"
	stubCallID   = "call-1"
)

func (s *stubTool) Name() string { return s.name }

func (s *stubTool) Definition() schema.ToolDefinition {
	return schema.ToolDefinition{Name: s.name}
}

func (s *stubTool) Execute(_ context.Context, _ json.RawMessage) (string, error) {
	if s.execErr != nil {
		return "", s.execErr
	}
	return s.output, nil
}

func TestRegistryExecuteErrorCodeExtraction(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string

		// 工具替身的执行结果。
		execErr   error
		wantIsErr bool
		wantCode  schema.ToolErrorCode

		// 对 Output 的附加断言 ( 为空则跳过 )。
		wantOutputContains string
	}{
		{
			name:               "hard tool error carries code and token",
			execErr:            wrapSysError("打开文件失败", fmt.Errorf("open x.txt: %w", fs.ErrNotExist)),
			wantIsErr:          true,
			wantCode:           schema.ErrCodeFileNotFound,
			wantOutputContains: "[ERR_FILE_NOT_FOUND]",
		},
		{
			name: "soft tool error keeps output and clears IsError",
			execErr: &ToolError{
				Code: schema.ErrCodeCmdTimeout,
				Msg:  "sleep 100\n[Warning: 命令执行超时(30s)，已强制终止。]",
				Soft: true,
			},
			wantIsErr:          false,
			wantCode:           schema.ErrCodeCmdTimeout,
			wantOutputContains: "[Warning: 命令执行超时(30s)，已强制终止。]",
		},
		{
			name:               "wrapped tool error still extracted",
			execErr:            fmt.Errorf("文件内容替换失败: %w", newToolError(schema.ErrCodeEditAmbiguous, nil, "出现了 %d 次", 3)),
			wantIsErr:          true,
			wantCode:           schema.ErrCodeEditAmbiguous,
			wantOutputContains: "文件内容替换失败",
		},
		{
			name:      "foreign error falls back to ERR_UNKNOWN",
			execErr:   errors.New("something unexpected"),
			wantIsErr: true,
			wantCode:  schema.ErrCodeUnknown,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			registry := NewDefaultRegistry(&stubTool{name: stubToolName, output: "ok", execErr: tt.execErr})
			result := registry.Execute(context.Background(), schema.ToolCall{ID: stubCallID, Name: stubToolName})

			if result.IsError != tt.wantIsErr {
				t.Fatalf("IsError = %v, want %v", result.IsError, tt.wantIsErr)
			}
			if result.ErrorCode != tt.wantCode {
				t.Fatalf("ErrorCode = %q, want %q", result.ErrorCode, tt.wantCode)
			}
			if tt.wantOutputContains != "" && !strings.Contains(result.Output, tt.wantOutputContains) {
				t.Fatalf("Output = %q, want it to contain %q", result.Output, tt.wantOutputContains)
			}
		})
	}
}

func TestRegistryExecuteUnregisteredTool(t *testing.T) {
	t.Parallel()

	registry := NewDefaultRegistry()
	result := registry.Execute(context.Background(), schema.ToolCall{ID: stubCallID, Name: "hallucinated_tool"})

	if !result.IsError {
		t.Fatal("unregistered tool should mark IsError")
	}
	if result.ErrorCode != schema.ErrCodeToolNotFound {
		t.Fatalf("ErrorCode = %q, want %q", result.ErrorCode, schema.ErrCodeToolNotFound)
	}
}

func TestRegistryExecuteSuccessHasNoCode(t *testing.T) {
	t.Parallel()

	registry := NewDefaultRegistry(&stubTool{name: stubToolName, output: "done"})
	result := registry.Execute(context.Background(), schema.ToolCall{ID: stubCallID, Name: stubToolName})

	if result.IsError {
		t.Fatal("success should not mark IsError")
	}
	if result.ErrorCode != "" {
		t.Fatalf("success should carry no error code, got %q", result.ErrorCode)
	}
}
