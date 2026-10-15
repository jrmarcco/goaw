package reporter

import (
	"context"
	"fmt"

	"github.com/jrmarcco/goaw/internal/engine"
	"github.com/jrmarcco/goaw/internal/logging"
)

var _ engine.Reporter = (*TerminalReporter)(nil)

// UX 输出截断上限与符号前缀。
const (
	// uxArgsLimit 工具参数与错误信息的展示上限。
	uxArgsLimit = 256

	// uxResultLimit 工具结果的展示上限。
	uxResultLimit = 1000
)

// TerminalReporter 是面向终端用户的输出 reporter ( UX 输出，不走 slog )。
type TerminalReporter struct{}

func NewTerminalReporter() *TerminalReporter {
	return &TerminalReporter{}
}

func (r *TerminalReporter) OnThinking(_ context.Context) error {
	fmt.Println("▸ 思考中")
	return nil
}

func (r *TerminalReporter) OnToolCall(_ context.Context, toolName, args string) error {
	fmt.Printf("▸ 调用工具 %s 参数 %s\n", toolName, logging.Truncate(args, uxArgsLimit))
	return nil
}

func (r *TerminalReporter) OnToolCallResult(_ context.Context, toolName, result string, isError bool) error {
	if isError {
		fmt.Printf("✘ 工具失败 %s\n", toolName)
		if result != "" {
			fmt.Printf("  错误信息 %s\n", logging.Truncate(result, uxResultLimit))
		}
		return nil
	}
	fmt.Printf("✔ 工具完成 %s\n", toolName)
	return nil
}

func (r *TerminalReporter) OnMessage(_ context.Context, content string) error {
	if content != "" {
		fmt.Printf("\n回复\n%s\n\n", content)
	}
	return nil
}
