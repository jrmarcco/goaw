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
	tools []schema.ToolDef,
) (*schema.Message, error) {
	if len(tools) == 0 {
		return &schema.Message{
			Role:    schema.RoleAssistant,
			Content: "[Thinking phase] The goal is to check the file list in the current directory. I need to first use the bash tool to run the `ls` command to see what's in the current directory, and then decide what to do next.",
		}, nil
	}

	p.turn++

	if p.turn == 1 {
		return &schema.Message{
			Role:    schema.RoleAssistant,
			Content: "I'm going to carry out the steps I've planned.",
			ToolCalls: []schema.ToolCall{
				{ID: "bash:ls_la", Name: "bash", Args: []byte(`{"command": "ls -la"}`)},
			},
		}, nil
	}

	return &schema.Message{
		Role:    schema.RoleAssistant,
		Content: "Base on the resuls of the tool call, the task was successfully completed.",
	}, nil
}
