package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/jrmarcco/goaw/internal/schema"
)

var _ Tool = (*FileReader)(nil)

// FileReader 读取工作区内文件的工具。
// 工作区路径不随构造绑定，执行时从 context 解析 ( 见 WithWorkspace )。
type FileReader struct{}

func NewFileReader() *FileReader {
	return &FileReader{}
}

func (r *FileReader) Name() string {
	return "file_reader"
}

func (r *FileReader) Definition() schema.ToolDefinition {
	const propNamePath = "path"

	return schema.ToolDefinition{
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

func (r *FileReader) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	workspace, err := WorkspaceFromContext(ctx)
	if err != nil {
		return "", err
	}

	var input fileReadArgs
	if err = json.Unmarshal(args, &input); err != nil {
		return "", newToolError(schema.ErrCodeInvalidArgs, err, "解析参数失败")
	}

	if !filepath.IsLocal(input.Path) {
		return "", newToolError(schema.ErrCodePathEscape, nil, "文件路径必须是工作区内的相对路径: %q", input.Path)
	}

	root, err := os.OpenRoot(workspace)
	if err != nil {
		return "", wrapSysError("打开工作区失败", err)
	}
	defer func() {
		_ = root.Close()
	}()

	file, err := root.Open(input.Path)
	if err != nil {
		return "", wrapSysError("打开文件失败", err)
	}
	defer func() {
		_ = file.Close()
	}()

	content, err := io.ReadAll(file)
	if err != nil {
		return "", wrapSysError("读取文件内容失败", err)
	}

	const MaxLen = 8192
	if len(content) > MaxLen {
		fullPath := filepath.Join(workspace, input.Path)
		return fmt.Sprintf("%s\n\n...[文件内容过长，已截断至前 %d 字节]", fullPath, MaxLen), nil
	}

	return string(content), nil
}

type fileReadArgs struct {
	Path string `json:"path"`
}
