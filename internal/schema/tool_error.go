package schema

// ToolErrorCode 是工具层领域错误码。
// 错误码是跨层传输的稳定契约 ( 工具产生 → Registry 提取 → 恢复层查表 )，
// 取代对错误文案的字符串匹配: 文案面向模型可读、随时可调，错误码面向程序、只增不改语义。
type ToolErrorCode string

const (
	ErrCodeToolNotFound     ToolErrorCode = "ERR_TOOL_NOT_FOUND"    // 模型幻觉调用了未注册的工具
	ErrCodeInvalidArgs      ToolErrorCode = "ERR_INVALID_ARGS"      // 工具参数 JSON 解析失败
	ErrCodePathEscape       ToolErrorCode = "ERR_PATH_ESCAPE"       // 路径逃逸出工作区
	ErrCodeNoWorkspace      ToolErrorCode = "ERR_NO_WORKSPACE"      // 执行上下文缺少工作区
	ErrCodeFileNotFound     ToolErrorCode = "ERR_FILE_NOT_FOUND"    // POSIX ENOENT
	ErrCodePermissionDenied ToolErrorCode = "ERR_PERMISSION_DENIED" // POSIX EACCES / EPERM
	ErrCodeIOFailure        ToolErrorCode = "ERR_IO_FAILURE"        // 其他 OS / IO 错误兜底
	ErrCodeEditNoMatch      ToolErrorCode = "ERR_EDIT_NO_MATCH"     // oldContent 在文件中无匹配
	ErrCodeEditAmbiguous    ToolErrorCode = "ERR_EDIT_AMBIGUOUS"    // oldContent 在文件中多处匹配
	ErrCodeCmdTimeout       ToolErrorCode = "ERR_CMD_TIMEOUT"       // bash 命令执行超时被强制终止
	ErrCodeCmdNotFound      ToolErrorCode = "ERR_CMD_NOT_FOUND"     // bash 退出码 127 ( command not found )
	ErrCodeCmdSyntaxError   ToolErrorCode = "ERR_CMD_SYNTAX_ERROR"  // bash 语法错误 ( best-effort 文案匹配 )
	ErrCodeCmdFailed        ToolErrorCode = "ERR_CMD_FAILED"        // bash 其他非零退出码
	ErrCodeUnknown          ToolErrorCode = "ERR_UNKNOWN"           // 未分类的外来 error
)
