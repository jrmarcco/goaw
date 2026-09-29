package engine

import (
	"testing"

	"github.com/jrmarcco/goaw/internal/schema"
)

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

		got := s.GetWorkingMemory(2)
		if len(got) != 2 {
			t.Fatalf("GetWorkingMemory(2) returned %d messages, want 2", len(got))
		}
		if got[0].Content != msgs[3].Content || got[1].Content != msgs[4].Content {
			t.Fatal("GetWorkingMemory(2) did not return the latest 2 messages")
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
		got := s.GetWorkingMemory(2)
		if len(got) != 1 {
			t.Fatalf("GetWorkingMemory(2) returned %d messages, want 1", len(got))
		}
		if got[0].ToolCallID != "" || got[0].Content != "latest" {
			t.Fatalf("GetWorkingMemory(2) = %v, want the plain user message", got)
		}
	})
}
