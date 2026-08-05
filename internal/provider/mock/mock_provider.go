package mock

import (
	"context"

	"github.com/jrmarcco/goaw/internal/provider"
	"github.com/jrmarcco/goaw/internal/schema"
)

var _ provider.LLMProvider = (*MockProvider)(nil)

type MockProvider struct {
	turn int
}

func NewMockProvider() *MockProvider {
	return &MockProvider{}
}

func (p *MockProvider) Generate(
	_ context.Context,
	_ []schema.Message,
	_ []schema.ToolDef,
) (*schema.Message, error) {
	p.turn++

	if p.turn == 1 {
		return &schema.Message{
			Role:    schema.RoleAssistant,
			Content: "Let me see what files are in the current directory.",
			ToolCalls: []schema.ToolCall{
				{ID: "bash:ls_la", Name: "bash", Args: []byte(`{"command": "ls -la"}`)},
			},
		}, nil
	}

	return &schema.Message{
		Role:    schema.RoleAssistant,
		Content: "I have checked the file list, mission accomplished.",
	}, nil
}
