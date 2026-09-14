package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/jrmarcco/goaw/internal/schema"
)

var _ Tool = (*FileWriter)(nil)

type FileWriter struct {
	workspace string
}

func NewFileWriter(workspace string) *FileWriter {
	return &FileWriter{
		workspace: workspace,
	}
}

func (w *FileWriter) Name() string {
	return "file_writer"
}

func (w *FileWriter) Definition() schema.ToolDef {
	const propNamePath = "path"
	const propNameContent = "content"

	return schema.ToolDef{
		Name:        w.Name(),
		Description: "创建或覆盖指定路径的文件，请提供工作目录内的相对路径。",
		InputSchema: map[string]any{
			schema.KeyType: schema.TypeObject,
			schema.KeyProperties: map[string]any{
				propNamePath: map[string]any{
					schema.KeyType:        schema.TypeString,
					schema.KeyDescription: "待写入的文件相对路径，例如 cmd/main.go",
				},
				propNameContent: map[string]any{
					schema.KeyType:        schema.TypeString,
					schema.KeyDescription: "待写入的完整内容",
				},
			},
			schema.KeyRequired: []string{propNamePath, propNameContent},
		},
	}
}

func (w *FileWriter) Execute(_ context.Context, args json.RawMessage) (string, error) {
	var input fileWriteArgs
	if err := json.Unmarshal(args, &input); err != nil {
		return "", fmt.Errorf("解析参数失败: %w", err)
	}

	if !filepath.IsLocal(input.Path) {
		return "", fmt.Errorf("文件路径必须是工作区内的相对路径: %q", input.Path)
	}

	// 拼接完整路径 ( 限制在 workspace 下执行，防止模型修改系统级文件 )。
	fullPath := filepath.Join(w.workspace, input.Path)

	// 自动创建缺失的父目录。
	if err := os.MkdirAll(filepath.Dir(fullPath), dirPerm); err != nil {
		return "", fmt.Errorf("创建父目录失败: %w", err)
	}

	// 写入文件内容。
	err := os.WriteFile(fullPath, []byte(input.Content), filePerm)
	if err != nil {
		return "", fmt.Errorf("写入文件失败: %w", err)
	}

	return fmt.Sprintf("成功写入内容到文件: %s", input.Path), nil
}

type fileWriteArgs struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}
