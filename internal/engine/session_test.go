package engine

import (
	"encoding/json"
	"math/rand"
	"strings"
	"testing"

	"github.com/jrmarcco/goaw/internal/schema"
)

// asciiContentLen 返回一段纯 ASCII 文本按估算规则折算后的 Token 数。
// 纯 ASCII 时 estimateTokens(text) == len(text)/4，方便在测试里推算预算。
func asciiContentLen(n int) string {
	if n%4 != 0 {
		panic("asciiContentLen expects a multiple of 4")
	}
	out := make([]byte, n)
	for i := range out {
		out[i] = 'a'
	}
	return string(out)
}

func TestGetWorkingMemory(t *testing.T) {
	t.Parallel()

	mk := func(role schema.Role, content string) schema.Message {
		return schema.Message{Role: role, Content: content}
	}

	user := func(content string) schema.Message { return mk(schema.RoleUser, content) }
	asst := func(content string) schema.Message { return mk(schema.RoleAssistant, content) }

	msgs := []schema.Message{
		user(asciiContentLen(40)),    // 10 content tokens
		asst(asciiContentLen(40)),    // 10
		user(asciiContentLen(400)),   // 100
		asst(asciiContentLen(4000)),  // 1000
		user(asciiContentLen(40000)), // 10000
	}

	t.Run("no limit returns all messages", func(t *testing.T) {
		t.Parallel()

		s := NewSession("s", "w")
		s.Append(msgs...)

		if got := s.GetWorkingMemory(0, 0); len(got) != len(msgs) {
			t.Fatalf("GetWorkingMemory(0, 0) returned %d messages, want %d", len(got), len(msgs))
		}
	})

	t.Run("message count limit", func(t *testing.T) {
		t.Parallel()

		s := NewSession("s", "w")
		s.Append(msgs...)

		got := s.GetWorkingMemory(2, 0)
		if len(got) != 2 {
			t.Fatalf("GetWorkingMemory(2, 0) returned %d messages, want 2", len(got))
		}
		if got[0].Content != msgs[3].Content || got[1].Content != msgs[4].Content {
			t.Fatal("GetWorkingMemory(2, 0) did not return the latest 2 messages")
		}
	})

	t.Run("token budget limit", func(t *testing.T) {
		t.Parallel()

		s := NewSession("s", "w")
		s.Append(msgs...)

		// 每条消息 4 tokens 固定开销:
		//   msgs[4] = 10004, msgs[3] = 1004, msgs[2] = 104。
		// 预算 11112 刚好装下 msgs[2..4]；再加 msgs[1] ( 14 ) 就超了。
		got := s.GetWorkingMemory(0, 10004+1004+104)
		if len(got) != 3 {
			t.Fatalf("GetWorkingMemory(0, 11112) returned %d messages, want 3", len(got))
		}
		if got[0].Content != msgs[2].Content {
			t.Fatal("GetWorkingMemory(0, 11112) did not drop the overflowing earliest message")
		}
	})

	t.Run("token budget always keeps the latest message", func(t *testing.T) {
		t.Parallel()

		s := NewSession("s", "w")
		s.Append(msgs...)

		got := s.GetWorkingMemory(0, 1)
		if len(got) != 1 || got[0].Content != msgs[4].Content {
			t.Fatalf("GetWorkingMemory(0, 1) = %v, want only the latest message", got)
		}
	})

	t.Run("both limits apply together", func(t *testing.T) {
		t.Parallel()

		s := NewSession("s", "w")
		s.Append(msgs...)

		// 条数上限允许 3 条 ( msgs[2..4] )，
		// 但 Token 预算 11100 装不下 msgs[2..4] ( 共 11112 )，只能装下 msgs[3..4]。
		got := s.GetWorkingMemory(3, 11100)
		if len(got) != 2 {
			t.Fatalf("GetWorkingMemory(3, 11100) returned %d messages, want 2", len(got))
		}
		if got[0].Content != msgs[3].Content {
			t.Fatal("GetWorkingMemory(3, 11100) should start at msgs[3]")
		}
	})

	t.Run("drops orphan tool call results at the head", func(t *testing.T) {
		t.Parallel()

		s := NewSession("s", "w")
		s.Append(
			user("early"),
			asst("call a tool"),
			schema.Message{Role: schema.RoleUser, Content: "tool output", ToolCallID: "tc-1"},
			user("latest"),
		)

		// 条数截取后第一条恰好是 ToolCallResult，必须被顺延丢弃。
		got := s.GetWorkingMemory(2, 0)
		if len(got) != 1 {
			t.Fatalf("GetWorkingMemory(2, 0) returned %d messages, want 1", len(got))
		}
		if got[0].ToolCallID != "" || got[0].Content != "latest" {
			t.Fatalf("GetWorkingMemory(2, 0) = %v, want the plain user message", got)
		}
	})

	t.Run("tool call args count toward the token budget", func(t *testing.T) {
		t.Parallel()

		s := NewSession("s", "w")
		s.Append(
			asst("old"),
			schema.Message{
				Role: schema.RoleAssistant,
				ToolCalls: []schema.ToolCall{
					{ID: "tc-1", Name: "file_reader", Args: json.RawMessage(`{"path":"/tmp/a.go"}`)},
				},
			},
		)

		// 最新一条: 4 ( 开销 ) + 0 ( 正文 ) + 2 ( name ) + 5 ( args ) = 11 tokens。
		// 预算 13 装得下最新一条但装不下两条 ( 11+4 )，验证 ToolCalls 计入了估算。
		got := s.GetWorkingMemory(0, 13)
		if len(got) != 1 || len(got[0].ToolCalls) != 1 {
			t.Fatalf("GetWorkingMemory(0, 13) = %v, want only the tool-calling message", got)
		}
	})
}

func TestEstimateTokens(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		text string
		want int
	}{
		{"empty", "", 0},
		{"ascii under 4 chars", "abc", 0},
		{"ascii exactly 4 chars", "abcd", 1},
		{"ascii 100 chars", asciiContentLen(100), 25},
		{"cjk chars count 1 token each", "你好世界", 4},
		{"mixed", "abc你", 0 + 1},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			if got := estimateTokens(c.text); got != c.want {
				t.Fatalf("estimateTokens(%q) = %d, want %d", c.text, got, c.want)
			}
		})
	}
}

// TestGetWorkingMemoryBudgetInvariants 用固定种子的随机历史 + 随机预算验证截取不变量:
//   - 结果条数不超过 limit ( limit > 0 时 )。
//   - 结果预估 Token 总量不超过 maxTokens ( maxTokens > 0 时 )，
//     唯一例外是最后一条消息单独就超预算时允许只保留它 ( 避免工作记忆被截空 )。
//   - 结果必须是历史的连续后缀 ( 保证消息连续性 )。
func TestGetWorkingMemoryBudgetInvariants(t *testing.T) {
	t.Parallel()

	//nolint:gosec // 固定种子的确定性伪随机数，测试需要可复现。
	rng := rand.New(rand.NewSource(42))

	for iter := 0; iter < 500; iter++ {
		historyCnt := rng.Intn(20)
		msgs := make([]schema.Message, 0, historyCnt)
		for i := 0; i < historyCnt; i++ {
			msgs = append(msgs, schema.Message{
				Role:    schema.RoleUser,
				Content: strings.Repeat("a", rng.Intn(400)),
			})
		}

		s := NewSession("s", "w")
		s.Append(msgs...)

		limit := rng.Intn(12)
		maxTokens := rng.Intn(400)

		got := s.GetWorkingMemory(limit, maxTokens)

		if limit > 0 && len(got) > limit {
			t.Fatalf("iter %d: got %d messages, exceeds limit %d", iter, len(got), limit)
		}

		if maxTokens > 0 {
			used := 0
			for _, m := range got {
				used += estimateMessageTokens(m)
			}
			if used > maxTokens && len(got) > 1 {
				t.Fatalf("iter %d: used %d tokens, exceeds budget %d with %d messages", iter, used, maxTokens, len(got))
			}
		}

		if len(got) > 0 && got[len(got)-1].Content != msgs[len(msgs)-1].Content {
			t.Fatalf("iter %d: result is not a suffix of the history", iter)
		}
		for i := 1; i < len(got); i++ {
			prev := len(msgs) - len(got) + i - 1
			if got[i].Content != msgs[prev+1].Content {
				t.Fatalf("iter %d: result has a gap in the history", iter)
			}
		}
	}
}
