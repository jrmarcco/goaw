package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/jrmarcco/goaw/internal/schema"
	"github.com/jrmarcco/jit/xbean/option"
)

var _ Tool = (*BashExecutor)(nil)

// BashExecutor 在工作区目录下执行 bash 命令的工具。
// 工作区路径不随构造绑定，执行时从 context 解析 ( 见 WithWorkspace )。
type BashExecutor struct {
	timeout time.Duration
}

func BashExecutorWithTimeout(timeout time.Duration) option.Opt[BashExecutor] {
	return func(e *BashExecutor) {
		e.timeout = timeout
	}
}

func NewBashExecutor(opts ...option.Opt[BashExecutor]) *BashExecutor {
	const defaultTimeout = 30 * time.Second // 默认超时时间30秒。
	bashExecutor := &BashExecutor{
		timeout: defaultTimeout,
	}

	if len(opts) > 0 {
		option.Apply(bashExecutor, opts...)
	}

	return bashExecutor
}

func (e *BashExecutor) Name() string {
	return "bash"
}

func (e *BashExecutor) Definition() schema.ToolDefinition {
	const propNameCommand = "command"

	return schema.ToolDefinition{
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
	workspace, err := WorkspaceFromContext(ctx)
	if err != nil {
		return "", err
	}

	var input bashArgs
	if err = json.Unmarshal(args, &input); err != nil {
		return "", fmt.Errorf("解析参数失败: %w", err)
	}

	timeoutCtx, cancel := context.WithTimeout(ctx, e.timeout)
	defer cancel()

	// 在 MacOS/Linux 上通过将命令包在 `bash -c`　中执行，以支持环境变量、管道、逻辑运算等复杂 Shell 特性。
	//nolint:gosec // G204: Bash Executor 的职责就是执行模型给出的任意命令，命令内容必然是外部输入。
	cmd := exec.CommandContext(timeoutCtx, "bash", "-c", input.Command)

	// 设置工作区目录，确保命令在工作区下执行。
	cmd.Dir = workspace

	// 执行并捕获 CombinedOutput ( stdout + stderr )。
	out, err := cmd.CombinedOutput()

	// 超时判定用哨兵错误而非文案，父上下文取消等其他原因按普通错误上抛。
	if ctxErr := timeoutCtx.Err(); ctxErr != nil {
		if errors.Is(ctxErr, context.DeadlineExceeded) {
			// 超时属于软失败：告警输出回传模型自纠，错误码供恢复层注入提示。
			return "", &ToolError{
				Code: schema.ErrCodeCmdTimeout,
				Msg:  fmt.Sprintf("%s\n[Warning: 命令执行超时(%s)，已强制终止。]", out, e.timeout),
				Soft: true,
			}
		}
		return "", ctxErr
	}

	// 错误回传 ( Self-Correction 自愈机制 )。
	//
	// 注意:
	// 当 bash 报错时绝对不能反悔 Go 的 error 阻断程序。
	// 必须把 err 和 output 一起返回给模型，让模型的自纠能力分析报错。
	// 因此命令失败统一标记为软失败 ( Soft )：Registry 不置 IsError，输出照常回传，
	// 仅通过错误码 ( 见 classifyCmdFailure ) 向恢复层提供精准的分类信号。
	if err != nil {
		return "", &ToolError{
			Code: classifyCmdFailure(err, string(out)),
			Msg:  fmt.Sprintf("命令执行失败: %v\n输出: \n%s", err, out),
			Soft: true,
		}
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

// bash 退出码契约 ( 与错误码分类的映射见 classifyCmdFailure )。
const (
	bashExitSyntaxError = 2   // 语法错误等 bash 内置错误 ( 语义宽泛，需结合 stderr 确认 )
	bashExitCmdNotFound = 127 // command not found
)

// classifyCmdFailure 对 bash 命令失败进行精准分类。
// 退出码 127 ( command not found ) 是 bash 的稳定契约信号；
// syntax error 只能依赖 stderr 文案匹配 ( 退出码 2 语义过于宽泛，无法区分 )，
// 这是错误分类链路中唯一保留的 best-effort 文本匹配，且匹配的是 shell 的固定输出。
func classifyCmdFailure(err error, combinedOut string) schema.ToolErrorCode {
	if exitErr, ok := errors.AsType[*exec.ExitError](err); ok {
		switch exitErr.ExitCode() {
		case bashExitCmdNotFound:
			return schema.ErrCodeCmdNotFound
		case bashExitSyntaxError:
			if strings.Contains(strings.ToLower(combinedOut), "syntax error") {
				return schema.ErrCodeCmdSyntaxError
			}
		}
	}
	return schema.ErrCodeCmdFailed
}

type bashArgs struct {
	Command string `json:"command"` // 要执行的命令。
}
