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
	workDir string
}

func NewFileReader(workDir string) *FileReader {
	return &FileReader{
		workDir: workDir,
	}
}

func (r *FileReader) Name() string {
	return "file_reader"
}

func (r *FileReader) Definition() schema.ToolDef {
	return schema.ToolDef{
		Name:        r.Name(),
		Description: "读取指定路径的文件内容，请提供工作目录内的相对路径。",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "目标文件的相对路径，例如 cmd/main.go",
				},
			},
			"required": []string{"path"},
		},
	}
}

func (r *FileReader) Execute(_ context.Context, args json.RawMessage) (string, error) {
	var input fileReadArgs
	if err := json.Unmarshal(args, &input); err != nil {
		return "", fmt.Errorf("解析参数失败: %w", err)
	}

	if !filepath.IsLocal(input.Path) {
		return "", fmt.Errorf("文件路径必须是工作目录内的相对路径: %q", input.Path)
	}

	root, err := os.OpenRoot(r.workDir)
	if err != nil {
		return "", fmt.Errorf("打开工作目录失败: %w", err)
	}
	defer func() {
		if closeErr := root.Close(); closeErr != nil {
			slog.Error("[file reader] 关闭工作目录失败", "error", closeErr)
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
		fullPath := filepath.Join(r.workDir, input.Path)
		truncateMsg := fmt.Sprintf("%s\n\n...[文件内容过长，已截断至前 %d 字节]", fullPath, MaxLen)
		return truncateMsg, nil
	}

	return string(content), nil
}

type fileReadArgs struct {
	Path string `json:"path"`
}
