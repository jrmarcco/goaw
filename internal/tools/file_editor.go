package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/jrmarcco/goaw/internal/schema"
)

var _ Tool = (*FileEditor)(nil)

type FileEditor struct {
	workspace string
}

func NewFileEditor(workspace string) *FileEditor {
	return &FileEditor{
		workspace: workspace,
	}
}

func (e *FileEditor) Name() string {
	return "file_editor"
}

func (e *FileEditor) Definition() schema.ToolDef {
	const propNamePath = "path"
	const propNameOldContent = "old_content"
	const propNameNewContent = "new_content"

	return schema.ToolDef{
		Name:        e.Name(),
		Description: "对指定文件进行局部内容（字符串）替换，比重写整个文件更安全、更快速。需要提供足够的 old_content 上下文以确保匹配的唯一性。",
		InputSchema: map[string]any{
			schema.KeyType: schema.TypeObject,
			schema.KeyProperties: map[string]any{
				propNamePath: map[string]any{
					schema.KeyType:        schema.TypeString,
					schema.KeyDescription: "待修改的文件相对路径，例如 cmd/main.go",
				},
				propNameOldContent: map[string]any{
					schema.KeyType:        schema.TypeString,
					schema.KeyDescription: "文件中原有的内容文本，必须包含足够的上下文",
				},
				propNameNewContent: map[string]any{
					schema.KeyType:        schema.TypeString,
					schema.KeyDescription: "新的内容文本，用于替换旧内容。",
				},
			},
			schema.KeyRequired: []string{propNamePath, propNameOldContent, propNameNewContent},
		},
	}
}

func (e *FileEditor) Execute(_ context.Context, args json.RawMessage) (string, error) {
	var input fileEditArgs
	if err := json.Unmarshal(args, &input); err != nil {
		return "", fmt.Errorf("解析参数失败: %w", err)
	}

	if !filepath.IsLocal(input.Path) {
		return "", fmt.Errorf("文件路径必须是工作区内的相对路径: %q", input.Path)
	}

	root, err := os.OpenRoot(e.workspace)
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

	// 读取文件内容。
	contentBytes, err := io.ReadAll(file)
	if err != nil {
		return "", fmt.Errorf("读取文件内容失败: %w", err)
	}
	oriContent := string(contentBytes)

	// 执行四级容错替换算法。
	newContent, err := e.fuzzyReplace(oriContent, input.OldContent, input.NewContent)
	if err != nil {
		return "", fmt.Errorf("文件内容替换失败: %w", err)
	}

	// 写入新内容。
	if err := os.WriteFile(filepath.Join(e.workspace, input.Path), []byte(newContent), filePerm); err != nil {
		return "", fmt.Errorf("写入文件失败: %w", err)
	}

	return fmt.Sprintf("✅ 成功修改文件: %s", input.Path), nil
}

// fuzzyReplace 四级容错降级替换算法。
// 四级容错:
//
//	L1: 最快最安全的精确匹配。
//	L2: 解决不同操作系统 ( Windows vs Unix ) 换行符导致的幻觉。
//	L3: 忽略整个代码块首尾的多余空行。
//	L4: ** ( 核心容错 ) ** 将 oldContent 和原始文件都按行切分，去掉每一行的首尾空格 ( 消除缩进差异 )，然后再进行比对。
func (e *FileEditor) fuzzyReplace(oriContent, oldContent, newContent string) (string, error) {
	// L1: 精确匹配。
	cnt := strings.Count(oriContent, oldContent)
	if cnt == 1 {
		return strings.Replace(oriContent, oldContent, newContent, 1), nil
	}
	if cnt > 1 {
		return "", fmt.Errorf("old_content 在原始文件中出现了 %d 次，无法确定唯一匹配", cnt)
	}

	// L2: 统一换行符 ( \r\n 转为 \n )。
	normalizedContent := strings.ReplaceAll(oriContent, "\r\n", "\n")
	normalizedOld := strings.ReplaceAll(oldContent, "\r\n", "\n")

	cnt = strings.Count(normalizedContent, normalizedOld)
	if cnt == 1 {
		return strings.Replace(normalizedContent, normalizedOld, newContent, 1), nil
	}

	// L3: Trim Space 匹配。
	trimedOld := strings.TrimSpace(normalizedOld)
	if trimedOld != "" {
		cnt = strings.Count(normalizedContent, trimedOld)
		if cnt == 1 {
			// 只替换被 TrimSpace 后的内容，直接试用 newContent 替换会破坏原本的缩进。
			return strings.Replace(normalizedContent, trimedOld, newContent, 1), nil
		}
	}

	// L4: 核心容错 ( 消除大模型遗漏缩进的幻觉 )。
	// 逐行去缩进匹配。
	return e.lineByLineReplace(normalizedContent, normalizedOld, newContent)
}

// lineByLineReplace 逐行替换内容。
// 将文本按行切割，去除首位空白后进行滑动窗口匹配。
func (e *FileEditor) lineByLineReplace(_, _, _ string) (string, error) {
	// TODO: not implemented
	panic("not implemented")
}

type fileEditArgs struct {
	Path       string `json:"path"`
	OldContent string `json:"oldContent"`
	NewContent string `json:"newContent"`
}
