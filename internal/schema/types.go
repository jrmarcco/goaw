package schema

import "encoding/json"

// Role 是消息的角色。
type Role string

const (
	RoleUser      Role = "user"      // 用户输入 / 工具执行的返回结果 ( Observation )
	RoleSystem    Role = "system"    // 系统提示词
	RoleAssistant Role = "assistant" // LLM 的输出 ( 包含推理和工具调用 )
)

// Message 是上下文中传递的单条消息。
type Message struct {
	Role    Role   `json:"role"`
	Content string `json:"content"`

	ToolCalls  []ToolCall `json:"toolCalls,omitempty"`
	ToolCallID string     `json:"toolCallId,omitempty"`
}

// ToolDefinition 是工具的定义，描述了一个 LLM 可以调用的工具元信息 ( 让 LLM 理解工具 )。
type ToolDefinition struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	InputSchema any    `json:"inputSchema"` // 对应 JSON Schema
}

// ToolCall 是 LLM 请求调用的某个工具。
type ToolCall struct {
	ID   string          `json:"id"`
	Name string          `json:"name"`
	Args json.RawMessage `json:"args"` // 对应工具的输入参数 ( RawMessage 的解析交给具体的工具 )
}

// ToolCallResult 是工具调用的返回结果。
type ToolCallResult struct {
	ID      string `json:"id"`
	Output  string `json:"output"`
	IsError bool   `json:"isError,omitempty"`
}

// Usage 是一次模型调用的真实资源消耗。
// 各大模型 API 都会在 Response 的 Usage 字段中回传该数据，
// 比本地字符估算精确得多 ( 尤其是本地根本统计不到的隐性消耗，
// 如 System Prompt、工具定义的 Schema 等都会被计入 PromptTokens )。
type Usage struct {
	PromptTokens     int // 本次请求消耗的提示词 Token 总数。
	CompletionTokens int // 本次请求生成的补全 Token 数。
}

// Generation 是一次模型生成的完整结果: 消息本体 + 真实 Token 消耗。
type Generation struct {
	Message Message
	Usage   Usage
}
