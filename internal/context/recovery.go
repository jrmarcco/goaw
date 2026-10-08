package context

import (
	"fmt"

	"github.com/jrmarcco/goaw/internal/schema"
)

// RecoveryManager 基于领域错误码的错误恢复管理器。
type RecoveryManager struct{}

func NewRecoveryManager() *RecoveryManager {
	return &RecoveryManager{}
}

// recoveryHints 错误码 → 自救提示的静态映射。
// 提示文案面向模型，指导其下一步自纠动作；文案可随意调整，不影响匹配逻辑。
var recoveryHints = map[schema.ToolErrorCode]string{
	schema.ErrCodeFileNotFound:     "路径似乎不正确。请不要凭空猜测，先使用 `bash` 执行 `ls -la` 或 `find . -name` 命令查找正确的目录结构和文件名。",
	schema.ErrCodePermissionDenied: "你没有权限操作该文件。请检查工作区限制，或者思考是否需要修改其他文件。",
	schema.ErrCodeEditNoMatch:      "你提供的 oldContent 与文件当前内容不一致，或者缺少必要的缩进。请先使用 `file_reader` 工具重新读取该文件，获取最新、准确的内容后，再重新发起编辑。",
	schema.ErrCodeEditAmbiguous:    "你的 oldContent 不够具体，命中了多个相同代码块。请在 oldContent 中增加上下相邻的几行代码，以确保替换的唯一性。",
	schema.ErrCodeCmdNotFound:      "系统未安装该命令。请先思考：是否有替代命令？或者你需要先编写脚本进行安装？",
	schema.ErrCodeCmdSyntaxError:   "Bash 语法错误。请检查引号转义或特殊字符，确保命令在终端中可以直接运行。",
	schema.ErrCodeCmdTimeout:       "该命令执行超时，已被强制结束。如果它是一个常驻服务 (如 server 或 watch)，请将其转入后台执行(例如使用 `nohup ... &`)，不要阻塞主线程。",
}

// AnalyzeAndInject 按领域错误码注入系统自救指南。
// 命中错误码时在原始输出后追加提示；未命中时原样返回，不打扰模型。
func (m *RecoveryManager) AnalyzeAndInject(toolName string, code schema.ToolErrorCode, rawOutput string) string {
	if code == schema.ErrCodeToolNotFound {
		// 未注册工具的提示需要工具名参与，单独构造。
		return injectHint(rawOutput, fmt.Sprintf("工具 %q 未注册，可能是名称拼写有误。请核对可用工具列表，使用确切的工具名称重新调用。", toolName))
	}

	hint, ok := recoveryHints[code]
	if !ok {
		return rawOutput
	}
	return injectHint(rawOutput, hint)
}

// injectHint 在原始输出后追加自救提示。
func injectHint(rawOutput, hint string) string {
	return fmt.Sprintf("%s\n\n[系统自救指南]: %s", rawOutput, hint)
}
