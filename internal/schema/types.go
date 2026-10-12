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

	// ErrorCode 是随结果传输的领域错误码 ( 见 ToolErrorCode )。
	// 空值表示无错误；软失败 ( 如 bash 命令失败 ) 时 IsError 为 false 但 ErrorCode 非空，
	// 输出照常回传给模型自纠，恢复层仅依据错误码决定是否注入提示。
	ErrorCode ToolErrorCode `json:"errorCode,omitempty"`
}

// Usage 是一次模型调用的真实资源消耗。
// 各大模型 API 都会在 Response 的 Usage 字段中回传该数据，比本地字符估算精确得多。
// 尤其是本地根本统计不到的隐性消耗，如 System Prompt、工具定义的 Schema 等都会被计入 PromptTokens。
type Usage struct {
	// PromptTokens 本次请求消耗的提示词 Token 总数 ( 上下文窗口占用口径，已含缓存维度 )。
	PromptTokens int
	// CompletionTokens 本次请求生成的补全 Token 数。
	CompletionTokens int

	// 以下两者是 PromptTokens 的子集维度，仅供成本记账留痕
	// ( 缓存读/写在各家计费中是独立折扣/加价档，对账与未来加权计费依赖它们 ):
	CacheReadTokens     int // 缓存命中部分 ( 计费折扣档 )。
	CacheCreationTokens int // 缓存写入部分 ( 计费加价档 )。
}

// Generation 是一次模型生成的完整结果: 消息本体 + 真实 Token 消耗。
type Generation struct {
	Message Message
	Usage   Usage
}
