package tools

import (
	"context"
	"testing"
)

func TestWorkspaceContext(t *testing.T) {
	t.Parallel()

	t.Run("missing workspace returns error", func(t *testing.T) {
		t.Parallel()

		if _, err := WorkspaceFromContext(context.Background()); err == nil {
			t.Fatal("WorkspaceFromContext() on a bare context should return an error")
		}
	})

	t.Run("round trip", func(t *testing.T) {
		t.Parallel()

		const workspace = "/tmp/some-workspace"
		ctx := WithWorkspace(context.Background(), workspace)

		got, err := WorkspaceFromContext(ctx)
		if err != nil {
			t.Fatalf("WorkspaceFromContext() error = %v", err)
		}
		if got != workspace {
			t.Fatalf("WorkspaceFromContext() = %q, want %q", got, workspace)
		}
	})
}
