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

// FileWriter 在工作区内创建或覆盖文件的工具。
// 工作区路径不随构造绑定，执行时从 context 解析 ( 见 WithWorkspace )。
type FileWriter struct{}

func NewFileWriter() *FileWriter {
	return &FileWriter{}
}

func (w *FileWriter) Name() string {
	return "file_writer"
}

func (w *FileWriter) Definition() schema.ToolDefinition {
	const propNamePath = "path"
	const propNameContent = "content"

	return schema.ToolDefinition{
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

func (w *FileWriter) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	workspace, err := WorkspaceFromContext(ctx)
	if err != nil {
		return "", err
	}

	var input fileWriteArgs
	if err = json.Unmarshal(args, &input); err != nil {
		return "", newToolError(schema.ErrCodeInvalidArgs, err, "解析参数失败")
	}

	if !filepath.IsLocal(input.Path) {
		return "", newToolError(schema.ErrCodePathEscape, nil, "文件路径必须是工作区内的相对路径: %q", input.Path)
	}

	// 拼接完整路径 ( 限制在 workspace 下执行，防止模型修改系统级文件 )。
	fullpath := filepath.Join(workspace, input.Path)

	// 自动创建缺失的父目录。
	if err = os.MkdirAll(filepath.Dir(fullpath), dirPerm); err != nil {
		return "", wrapSysError("创建父目录失败", err)
	}

	// 写入文件内容。
	err = os.WriteFile(fullpath, []byte(input.Content), filePerm)
	if err != nil {
		return "", wrapSysError("写入文件失败", err)
	}

	return fmt.Sprintf("成功写入内容到文件: %s", input.Path), nil
}

type fileWriteArgs struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}
