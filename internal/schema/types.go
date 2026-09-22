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

// ToolDef 是工具的定义，描述了一个 LLM 可以调用的工具元信息 ( 让 LLM 理解工具 )。
type ToolDef struct {
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

// ToolCallRes 是工具调用的返回结果。
type ToolCallRes struct {
	ID      string `json:"id"`
	Output  string `json:"output"`
	IsError bool   `json:"isError,omitempty"`
}
