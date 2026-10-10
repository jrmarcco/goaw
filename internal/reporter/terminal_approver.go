package reporter

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"

	"github.com/jrmarcco/goaw/internal/schema"
	"github.com/jrmarcco/goaw/internal/tools"
	"github.com/jrmarcco/jit/xbean/option"
)

var _ tools.Approver = (*TerminalApprover)(nil)

// TerminalApprover 终端人工审核器: 在终端展示待审核的工具调用并等待 stdin 输入。
// bufio.Reader 必须实例级持有 ( 预读缓冲会吞掉后续输入 );
// 并发工具调用可能同时触发审核提示，互斥锁串行化交互防止串台。
//
// 局限: stdin 阻塞读无法被 ctx 取消中断，只能依赖 ApprovalManager 的超时兜底;
// 可中断的人工交互由 FeishuApprover 等实现提供。
type TerminalApprover struct {
	mu    sync.Mutex
	stdin *bufio.Reader
}

// TerminalApproverWithStdin 替换输入源 ( 默认 os.Stdin )，用于测试。
func TerminalApproverWithStdin(r io.Reader) option.Opt[TerminalApprover] {
	return func(a *TerminalApprover) {
		a.stdin = bufio.NewReader(r)
	}
}

func NewTerminalApprover(opts ...option.Opt[TerminalApprover]) *TerminalApprover {
	a := &TerminalApprover{
		stdin: bufio.NewReader(os.Stdin),
	}

	option.Apply(a, opts...)
	return a
}

// RequestApproval 实现 tools.Approver，阻塞等待终端输入。
func (a *TerminalApprover) RequestApproval(_ context.Context, tc schema.ToolCall) tools.ApprovalResult {
	a.mu.Lock()
	defer a.mu.Unlock()

	fmt.Printf("\n⚠️  工具 [%s] 请求人工审核\n参数: %s\n", tc.Name, tc.Args)
	fmt.Print("允许执行? [y] 允许一次 / [a] 总是允许 / [回车或其他] 拒绝: ")

	line, err := a.stdin.ReadString('\n')
	if err != nil {
		return tools.ApprovalResult{
			Decision: tools.DecisionRejected,
			Reason:   "人工审核输入读取失败",
		}
	}

	switch strings.TrimSpace(line) {
	case "y":
		return tools.ApprovalResult{Decision: tools.DecisionApproved}
	case "a":
		return tools.ApprovalResult{Decision: tools.DecisionApproved, Remember: true}
	default:
		return tools.ApprovalResult{
			Decision: tools.DecisionRejected,
			Reason:   "人工审核选择拒绝执行该工具调用",
		}
	}
}
