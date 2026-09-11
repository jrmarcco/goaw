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
	return []schema.ToolDef{
		{
			Name:        "get_weather",
			Description: "Get the weather of current city",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"city": map[string]any{
						"type": "string",
					},
				},
				"required": []string{"city"},
			},
		},
	}
}

func (r *mockRegistry) Exec(_ context.Context, call schema.ToolCall) *schema.ToolCallRes {
	return &schema.ToolCallRes{
		ID:     call.ID,
		Output: "API response: Today's weather is sunny with a temperature of 20°C.",
		Error:  "",
	}
}
