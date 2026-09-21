package engine

import "context"

// Reporter 定义了 Agent 引擎向外界输出信息的规范。
type Reporter interface {
	// OnThingking 当模型开始进行慢思考 ( Reasoning ) 时，调用该方法。
	OnThinking(ctx context.Context) error

	// OnToolCall 当模型执行工具调用时，调用该方法。
	OnToolCall(ctx context.Context, toolName, args string) error

	// OnToolCallResult 当模型返回工具执行的结果时，调用该方法。
	OnToolCallResult(ctx context.Context, toolName, result string, isError bool) error

	// OnMessage 当模型完成任务，向用户输出最终文本答案时，调用该方法。
	OnMessage(ctx context.Context, content string) error
}
