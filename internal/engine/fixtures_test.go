package engine

// 跨测试文件共享的测试夹具常量集中于此。
// 仅被单个测试文件使用的常量请留在各自文件内，避免本文件沦为杂物堆。
const (
	// testToolCallID1 测试用 ToolCall 标识。
	testToolCallID1 = "tc-1"

	// testToolCallID2 测试用 ToolCall 标识。
	testToolCallID2 = "tc-2"

	// testToolName 测试用工具名。
	testToolName = "bash"
)
