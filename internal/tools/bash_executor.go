package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"time"

	"github.com/jrmarcco/goaw/internal/schema"
	"github.com/jrmarcco/jit/xbean/option"
)

var _ Tool = (*BashExecutor)(nil)

type BashExecutor struct {
	workspace string
	timeout   time.Duration
}

func BashExecutorWithTimeout(timeout time.Duration) option.Opt[BashExecutor] {
	return func(e *BashExecutor) {
		e.timeout = timeout
	}
}

func NewBashExecutor(workspace string, opts ...option.Opt[BashExecutor]) *BashExecutor {
	const defaultTimeout = 30 * time.Second // 默认超时时间30秒。
	bashExecutor := &BashExecutor{
		workspace: workspace,
		timeout:   defaultTimeout,
	}

	if len(opts) > 0 {
		option.Apply(bashExecutor, opts...)
	}

	return bashExecutor
}

func (e *BashExecutor) Name() string {
	return "bash"
}

func (e *BashExecutor) Definition() schema.ToolDef {
	const propNameCommand = "command"

	return schema.ToolDef{
		Name:        e.Name(),
		Description: "在当前工作区执行 bash 命令，支持链式命令(如 &&)。返回标准输出(stdout)。",
		InputSchema: map[string]any{
			schema.KeyType: schema.TypeObject,
			schema.KeyProperties: map[string]any{
				propNameCommand: map[string]any{
					schema.KeyType:        schema.TypeString,
					schema.KeyDescription: "待执行的 bash 命令，例如 ls -alh",
				},
			},
			schema.KeyRequired: []string{propNameCommand},
		},
	}
}

func (e *BashExecutor) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var input bashArgs
	if err := json.Unmarshal(args, &input); err != nil {
		return "", fmt.Errorf("解析参数失败: %w", err)
	}

	timeoutCtx, cancel := context.WithTimeout(ctx, e.timeout)
	defer cancel()

	// 在 MacOS/Linux 上通过将命令包在 `bash -c`　中执行，以支持环境变量、管道、逻辑运算等复杂 Shell 特性。

	//nolint:gosec // G204: Bash Executor的职责就是执行大模型给出的任意命令，命令内容必然是外部输入。
	cmd := exec.CommandContext(timeoutCtx, "bash", "-c", input.Command)

	// 设置工作区目录，确保命令在工作区下执行。
	cmd.Dir = e.workspace

	// 执行并捕获 CombinedOutput ( stdout + stderr )。
	out, err := cmd.CombinedOutput()

	if timeoutCtx.Err() != nil {
		// 命令执行超时，返回告警给大模型。
		return fmt.Sprintf("%s\n[Warning: 命令执行超时(%s)，已强制终止。]", out, e.timeout), nil
	}

	// 错误回传 ( Self-Correction 自愈机制 )。
	// 注意:
	//  当 bash 报错时绝对不能反悔 Go 的 error 阻断程序。
	//  必须把 err 和 output 一起返回给大模型，让大模型的自纠能力分析报错。
	if err != nil {
		return fmt.Sprintf("命令执行失败: %v\n输出: \n%s", err, out), nil
	}

	if len(out) == 0 {
		return "命令执行成功，终端无输出。", nil
	}

	const maxLen = 8192
	if len(out) > maxLen {
		return fmt.Sprintf("%s\n\n...[终端输出过长，已截断至前 %d 字节]", out[:maxLen], maxLen), nil
	}
	return string(out), nil
}

type bashArgs struct {
	Command string `json:"command"` // 要执行的命令。
}
