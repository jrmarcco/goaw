package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/jrmarcco/goaw/internal/schema"
)

// readonlyFakeTool 固定输出的测试替身工具，验证非 bash 工具不受只读策略影响。
type readonlyFakeTool struct {
	executed bool
}

func (t *readonlyFakeTool) Name() string { return "ping" }

func (t *readonlyFakeTool) Definition() schema.ToolDefinition {
	return schema.ToolDefinition{Name: t.Name()}
}

func (t *readonlyFakeTool) Execute(_ context.Context, _ json.RawMessage) (string, error) {
	t.executed = true
	return "pong", nil
}

func TestCheckReadOnlyCommand(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		command string
		wantOK  bool
	}{
		// 放行: 白名单命令、管道、链式与引号内的操作符字面量。
		{"allow plain ls", "ls -la", true},
		{"allow grep recursive", `grep -rn "handler" .`, true},
		{"allow find by name", `find . -name "*.go"`, true},
		{"allow pipe", "cat main.go | head -5", true},
		{"allow chain", "ls && cat main.go", true},
		{"allow quoted redirect literal", `grep ">" README.md`, true},
		{"allow single quoted operator", `grep '>(x)' log.txt`, true},
		{"allow git log with flags", "git log --oneline -n 5", true},
		{"allow git blame", "git blame main.go", true},
		{"allow stat and tree", "du -sh . && stat main.go && tree", true},

		// 拒绝: 未授权命令。
		{"deny rm", "rm -rf build", false},
		{"deny unknown binary", "python3 script.py", false},
		{"deny pipe to xargs", "find . -name x | xargs rm", false},
		{"deny semicolon chain to rm", "grep x . ; rm y", false},

		// 拒绝: 写入与执行语义。
		{"deny redirect write", "echo x > file.txt", false},
		{"deny redirect append via cat", "cat a >> b", false},
		{"deny input redirect", "cat < a", false},
		{"deny command substitution", "cat $(which ls)", false},
		{"deny backtick substitution", "cat `which ls`", false},
		{"deny double quoted substitution", `cat "$(rm -rf x)"`, false},
		{"deny background", "grep x . &", false},
		{"deny subshell", "(ls)", false},
		{"deny backslash escape", `grep a\ b`, false},
		{"deny env prefix", "FOO=bar ls", false},

		// 拒绝: 命令级副作用选项与 git 配置注入。
		{"deny find delete", "find . -delete", false},
		{"deny find exec", `find . -exec rm {} \;`, false},
		{"deny git config injection", "git -c x=y log", false},
		{"deny git push", "git push origin main", false},
		{"deny git missing subcommand", "git", false},

		// 拒绝: 路径卫生与残缺语法。
		{"deny absolute path", "cat /etc/passwd", false},
		{"deny tilde path", "cat ~/.ssh/id_rsa", false},
		{"deny parent escape", "cat ../main.go", false},
		{"deny trailing separator", "cat a |", false},
		{"deny unclosed quote", `grep "unclosed`, false},
		{"deny blank", "   ", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			reason := checkReadOnlyCommand(tc.command)
			switch {
			case tc.wantOK && reason != "":
				t.Fatalf("checkReadOnlyCommand(%q) rejected: %s", tc.command, reason)
			case !tc.wantOK && reason == "":
				t.Fatalf("checkReadOnlyCommand(%q) should be rejected", tc.command)
			}
		})
	}
}

func TestReadOnlyBashMiddlewareViaRegistry(t *testing.T) {
	t.Parallel()

	fake := &readonlyFakeTool{}
	registry := NewDefaultRegistry(NewBashExecutor(), fake)
	registry.Use(NewReadOnlyBashMiddleware())

	// bash 破坏性命令在中间件层被拦截，不会抵达执行器。
	result := registry.Execute(context.Background(), schema.ToolCall{
		ID:   "tc-readonly-1",
		Name: bashToolName,
		Args: []byte(`{"command":"rm -rf build"}`),
	})
	if !result.IsError || result.ErrorCode != schema.ErrCodeCallIntercepted {
		t.Fatalf("Execute() = %+v, want an intercepted call", result)
	}

	// 非 bash 工具不受只读策略影响，正常放行执行。
	result = registry.Execute(context.Background(), schema.ToolCall{
		ID:   "tc-readonly-2",
		Name: fake.Name(),
		Args: []byte(`{}`),
	})
	if result.IsError || !fake.executed {
		t.Fatalf("Execute() = %+v, non-bash tool should pass through (executed=%v)", result, fake.executed)
	}

	// 放行的只读 bash 命令真实执行 ( 工作区内 echo 不在白名单，用 ls 验证 )。
	ctx := WithWorkspace(context.Background(), t.TempDir())
	result = registry.Execute(ctx, schema.ToolCall{
		ID:   "tc-readonly-3",
		Name: bashToolName,
		Args: []byte(`{"command":"ls"}`),
	})
	if result.IsError {
		t.Fatalf("Execute() readonly bash failed: %+v", result)
	}
}
