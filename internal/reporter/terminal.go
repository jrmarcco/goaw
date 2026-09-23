package reporter

import (
	"context"
	"fmt"

	"github.com/jrmarcco/goaw/internal/engine"
)

var _ engine.Reporter = (*TerminalReporter)(nil)

// TerminalReporter 是一个终端输出 reporter ( 主要用于调试 )。
type TerminalReporter struct{}

func NewTerminalReporter() *TerminalReporter {
	return &TerminalReporter{}
}

func (r *TerminalReporter) OnThinking(_ context.Context) error {
	fmt.Printf("\n模型正在思考中...\n")
	return nil
}

func (r *TerminalReporter) OnToolCall(_ context.Context, toolName, args string) error {
	fmt.Printf("\n模型正在执行工具调用: %s, 参数: %s\n", toolName, args)
	return nil
}

func (r *TerminalReporter) OnToolCallResult(_ context.Context, toolName, result string, isError bool) error {
	if isError {
		fmt.Printf("\n工具 [%s] 调用失败\n", toolName)
		if result != "" {
			fmt.Printf("错误信息: %s\n", result)
		}
	} else {
		fmt.Printf("\n工具 [%s] 调用成功\n", toolName)
	}
	return nil
}

func (r *TerminalReporter) OnMessage(_ context.Context, content string) error {
	if content != "" {
		fmt.Printf("\nAgent 引擎回复: \n%s\n\n", content)
	}
	return nil
}
