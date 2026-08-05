package mock

import (
	"context"

	"github.com/jrmarcco/goaw/internal/schema"
	"github.com/jrmarcco/goaw/internal/tools"
)

var _ tools.Registry = (*mockRegistry)(nil)

type mockRegistry struct{}

func NewMockRegistry() *mockRegistry {
	return &mockRegistry{}
}

func (r *mockRegistry) GetAvailableTools() []schema.ToolDef {
	return nil
}

func (r *mockRegistry) Exec(_ context.Context, call schema.ToolCall) *schema.ToolCallRes {
	return &schema.ToolCallRes{
		ID:     call.ID,
		Output: "-rw-rw-r-- 1 jrmarcco jrmarcco 82 Aug  5 21:16 main.go",
	}
}
