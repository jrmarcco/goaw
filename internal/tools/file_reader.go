package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/jrmarcco/goaw/internal/schema"
)

var _ Tool = (*FileReader)(nil)

type FileReader struct {
	workspace string
}

func NewFileReader(workspace string) *FileReader {
	return &FileReader{
		workspace: workspace,
	}
}

func (r *FileReader) Name() string {
	return "file_reader"
}

func (r *FileReader) Definition() schema.ToolDef {
	const propNamePath = "path"

	return schema.ToolDef{
		Name:        r.Name(),
		Description: "读取指定路径的文件内容，请提供工作目录内的相对路径。",
		InputSchema: map[string]any{
			schema.KeyType: schema.TypeObject,
			schema.KeyProperties: map[string]any{
				propNamePath: map[string]any{
					schema.KeyType:        schema.TypeString,
					schema.KeyDescription: "待读取的文件相对路径，例如 cmd/main.go",
				},
			},
			schema.KeyRequired: []string{propNamePath},
		},
	}
}

func (r *FileReader) Execute(_ context.Context, args json.RawMessage) (string, error) {
	var input fileReadArgs
	if err := json.Unmarshal(args, &input); err != nil {
		return "", fmt.Errorf("解析参数失败: %w", err)
	}

	if !filepath.IsLocal(input.Path) {
		return "", fmt.Errorf("文件路径必须是工作区内的相对路径: %q", input.Path)
	}

	root, err := os.OpenRoot(r.workspace)
	if err != nil {
		return "", fmt.Errorf("打开工作区失败: %w", err)
	}
	defer func() {
		if closeErr := root.Close(); closeErr != nil {
			slog.Error("[file reader] 关闭工作区失败", "error", closeErr)
		}
	}()

	file, err := root.Open(input.Path)
	if err != nil {
		return "", fmt.Errorf("打开文件失败: %w", err)
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil {
			slog.Error("[file reader] 关闭文件失败", "error", closeErr)
		}
	}()

	content, err := io.ReadAll(file)
	if err != nil {
		return "", fmt.Errorf("读取文件内容失败: %w", err)
	}

	const MaxLen = 8192
	if len(content) > MaxLen {
		fullPath := filepath.Join(r.workspace, input.Path)
		return fmt.Sprintf("%s\n\n...[文件内容过长，已截断至前 %d 字节]", fullPath, MaxLen), nil
	}

	return string(content), nil
}

type fileReadArgs struct {
	Path string `json:"path"`
}
