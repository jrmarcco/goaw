package engine

import (
	"testing"

	"github.com/jrmarcco/goaw/internal/schema"
)

// testToolCallID1 测试用 ToolCall 标识。
const testToolCallID1 = "tc-1"

// testToolCallID2 测试用 ToolCall 标识。
const testToolCallID2 = "tc-2"

func TestSessionRunGuard(t *testing.T) {
	t.Parallel()

	s := NewSession("s", "w")

	if s.compactor == nil {
		t.Fatal("NewSession() did not initialize the compactor")
	}

	if !s.TryStartRun() {
		t.Fatal("TryStartRun() = false on an idle session")
	}
	if s.TryStartRun() {
		t.Fatal("TryStartRun() = true while a run is in progress")
	}

	s.EndRun()
	if !s.TryStartRun() {
		t.Fatal("TryStartRun() = false after EndRun()")
	}
}

func TestGetWorkingMemory(t *testing.T) {
	t.Parallel()

	mk := func(role schema.Role, content string) schema.Message {
		return schema.Message{Role: role, Content: content}
	}

	user := func(content string) schema.Message { return mk(schema.RoleUser, content) }
	asst := func(content string) schema.Message { return mk(schema.RoleAssistant, content) }

	msgs := []schema.Message{
		user("user-1"),
		asst("assistant-1"),
		user("user-2"),
		asst("assistant-2"),
		user("user-3"),
	}

	t.Run("no limit returns all messages", func(t *testing.T) {
		t.Parallel()

		s := NewSession("s", "w")
		s.Append(msgs...)

		if got := s.GetWorkingMemory(0); len(got) != len(msgs) {
			t.Fatalf("GetWorkingMemory(0) returned %d messages, want %d", len(got), len(msgs))
		}
	})

	t.Run("limit larger than history returns all messages", func(t *testing.T) {
		t.Parallel()

		s := NewSession("s", "w")
		s.Append(msgs...)

		if got := s.GetWorkingMemory(len(msgs) + 1); len(got) != len(msgs) {
			t.Fatalf("GetWorkingMemory(%d) returned %d messages, want %d", len(msgs)+1, len(got), len(msgs))
		}
	})

	t.Run("message count limit", func(t *testing.T) {
		t.Parallel()

		s := NewSession("s", "w")
		s.Append(msgs...)

		// limit=3 的截取点恰好落在 User 消息上，无需回退，严格返回 3 条。
		got := s.GetWorkingMemory(3)
		if len(got) != 3 {
			t.Fatalf("GetWorkingMemory(3) returned %d messages, want 3", len(got))
		}
		if got[0].Content != msgs[2].Content || got[2].Content != msgs[4].Content {
			t.Fatal("GetWorkingMemory(3) did not return the latest 3 messages")
		}
	})

	t.Run("repairs orphaned tool call results by widening the window", func(t *testing.T) {
		t.Parallel()

		s := NewSession("s", "w")
		s.Append(
			user("early"),
			asst("call a tool"),
			schema.Message{Role: schema.RoleUser, Content: "tool output", ToolCallID: testToolCallID1},
			user("latest"),
		)

		// 条数截取后第一条恰好是 ToolCallResult，
		// 起点回退补回发出 ToolCall 的父消息与前置用户消息，而不是丢弃孤儿。
		got := s.GetWorkingMemory(2)
		if len(got) != 4 {
			t.Fatalf("GetWorkingMemory(2) returned %d messages, want 4 (widened)", len(got))
		}
		if got[0].Content != "early" || got[0].ToolCallID != "" {
			t.Fatalf("GetWorkingMemory(2) head = %+v, want the plain user message", got[0])
		}
		if got[2].ToolCallID != "tc-1" {
			t.Fatal("the tool call result must stay in the window together with its parent")
		}
	})

	t.Run("repairs leading assistant head", func(t *testing.T) {
		t.Parallel()

		s := NewSession("s", "w")
		s.Append(
			user("early"),
			asst("reply"),
			user("ask"),
			schema.Message{
				Role: schema.RoleAssistant,
				ToolCalls: []schema.ToolCall{
					{ID: testToolCallID1, Name: "bash", Args: []byte(`{}`)},
				},
			},
			schema.Message{Role: schema.RoleUser, Content: "output", ToolCallID: testToolCallID1},
		)

		// limit=2 截取后窗口为 [assistant(tool_calls), tool_result]，
		// 开头的 Assistant 消息违反"对话以 user 开头"约束，回退到前一条用户消息。
		got := s.GetWorkingMemory(2)
		if len(got) != 3 {
			t.Fatalf("GetWorkingMemory(2) returned %d messages, want 3 (widened)", len(got))
		}
		if got[0].Role != schema.RoleUser || got[0].Content != "ask" || got[0].ToolCallID != "" {
			t.Fatalf("GetWorkingMemory(2) head = %+v, want the plain user message %q", got[0], "ask")
		}
		if len(got[1].ToolCalls) != 1 || got[2].ToolCallID != "tc-1" {
			t.Fatal("the tool call and its result must stay together in the window")
		}
	})

	t.Run("all tool result window never returns empty", func(t *testing.T) {
		t.Parallel()

		s := NewSession("s", "w")
		s.Append(
			user("early"),
			schema.Message{
				Role: schema.RoleAssistant,
				ToolCalls: []schema.ToolCall{
					{ID: testToolCallID1, Name: "bash", Args: []byte(`{}`)},
					{ID: testToolCallID2, Name: "file_reader", Args: []byte(`{}`)},
				},
			},
			schema.Message{Role: schema.RoleUser, Content: "out-1", ToolCallID: testToolCallID1},
			schema.Message{Role: schema.RoleUser, Content: "out-2", ToolCallID: testToolCallID2},
		)

		// limit=2 的窗口全是 ToolCallResult，回退到开场用户消息，窗口不为空。
		got := s.GetWorkingMemory(2)
		if len(got) != 4 {
			t.Fatalf("GetWorkingMemory(2) returned %d messages, want 4 (widened)", len(got))
		}
		if got[0].Content != "early" || got[0].ToolCallID != "" {
			t.Fatalf("window head = %+v, want the opening plain user message", got[0])
		}
	})
}
