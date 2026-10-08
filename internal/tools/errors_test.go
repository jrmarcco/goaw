package tools

import (
	"errors"
	"fmt"
	"io/fs"
	"testing"

	"github.com/jrmarcco/goaw/internal/schema"
)

func TestWrapSysError(t *testing.T) {
	t.Parallel()

	// 构造带包装链的 OS 哨兵错误，模拟工具内部 fmt.Errorf("%w") 后的真实形态。
	tests := []struct {
		name string
		err  error
		want schema.ToolErrorCode
	}{
		{"enoent wrapped", fmt.Errorf("打开文件失败: %w", fs.ErrNotExist), schema.ErrCodeFileNotFound},
		{"permission wrapped", fmt.Errorf("写入文件失败: %w", fs.ErrPermission), schema.ErrCodePermissionDenied},
		{"other io error", fmt.Errorf("读取文件内容失败: %w", fs.ErrClosed), schema.ErrCodeIOFailure},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			toolErr := wrapSysError("操作失败", tt.err)
			if toolErr.Code != tt.want {
				t.Fatalf("wrapSysError() code = %q, want %q", toolErr.Code, tt.want)
			}
			if !errors.Is(toolErr, tt.err) {
				t.Fatalf("wrapSysError() should preserve the cause chain")
			}
		})
	}
}

func TestToolErrorUnwrap(t *testing.T) {
	t.Parallel()

	cause := fmt.Errorf("解析参数失败: %w", fs.ErrNotExist)
	wrapped := fmt.Errorf("文件内容替换失败: %w", newToolError(schema.ErrCodeEditNoMatch, cause, "未找到匹配"))

	// ToolError 埋在双层 %w 包装下，errors.As 仍应能提取。
	var toolErr *ToolError
	if !errors.As(wrapped, &toolErr) {
		t.Fatal("errors.As() should extract *ToolError through %w chains")
	}
	if toolErr.Code != schema.ErrCodeEditNoMatch {
		t.Fatalf("extracted code = %q, want %q", toolErr.Code, schema.ErrCodeEditNoMatch)
	}

	// 错误链应同时穿透到最底层的 OS 哨兵。
	if !errors.Is(wrapped, fs.ErrNotExist) {
		t.Fatal("errors.Is() should reach the underlying sentinel through ToolError")
	}
}

func TestToolErrorErrorString(t *testing.T) {
	t.Parallel()

	t.Run("with cause", func(t *testing.T) {
		t.Parallel()

		err := newToolError(schema.ErrCodeInvalidArgs, errors.New("unexpected end of JSON"), "解析参数失败")
		want := "[ERR_INVALID_ARGS] 解析参数失败: unexpected end of JSON"
		if err.Error() != want {
			t.Fatalf("Error() = %q, want %q", err.Error(), want)
		}
	})

	t.Run("without cause", func(t *testing.T) {
		t.Parallel()

		err := newToolError(schema.ErrCodeEditNoMatch, nil, "未找到匹配 oldContent 的文本内容")
		want := "[ERR_EDIT_NO_MATCH] 未找到匹配 oldContent 的文本内容"
		if err.Error() != want {
			t.Fatalf("Error() = %q, want %q", err.Error(), want)
		}
	})
}
