package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/jrmarcco/goaw/internal/schema"
)

var _ LLMProvider = (*AnthropicProvider)(nil)

type AnthropicProvider struct {
	model  string
	client anthropic.Client
}

func NewAnthropicProvider(model string) (*AnthropicProvider, error) {
	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if apiKey == "" {
		return nil, errors.New("ANTHROPIC_API_KEY is not set")
	}

	baseURL := os.Getenv("ANTHROPIC_BASE_URL")
	if baseURL != "" {
		return &AnthropicProvider{
			model:  model,
			client: anthropic.NewClient(option.WithAPIKey(apiKey), option.WithBaseURL(baseURL)),
		}, nil
	}

	return &AnthropicProvider{
		model:  model,
		client: anthropic.NewClient(option.WithAPIKey(apiKey)),
	}, nil
}

func (p *AnthropicProvider) Generate(ctx context.Context, msgs []schema.Message, availableTools []schema.ToolDefinition) (*schema.Message, error) {
	// 1.解析上下文消息。
	systemPrompt, anthropicMsgs := p.transContextMessage(msgs)

	// 2.转换工具 Schema。
	anthropicTools := p.transToolDef(availableTools)

	// 3.构建请求。
	const maxTokens = 8192
	params := anthropic.MessageNewParams{
		Model:     p.model,
		MaxTokens: maxTokens,
		Messages:  anthropicMsgs,
	}

	if systemPrompt != "" {
		params.System = []anthropic.TextBlockParam{
			{Text: systemPrompt},
		}
	}

	if len(anthropicTools) > 0 {
		params.Tools = anthropicTools
	}

	resp, err := p.client.Messages.New(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("failed to generate response: %w", err)
	}

	// 4.解析响应信息。
	res := &schema.Message{
		Role: schema.RoleAssistant,
	}

	for i := range resp.Content {
		block := &resp.Content[i]

		switch block.Type {
		case "text":
			res.Content += block.Text

		case "tool_use":
			res.ToolCalls = append(res.ToolCalls, schema.ToolCall{
				ID:   block.ID,
				Name: block.Name,
				Args: block.Input,
			})
		}
	}
	return res, nil
}

func (p *AnthropicProvider) transContextMessage(msgs []schema.Message) (string, []anthropic.MessageParam) {
	var systemPrompt string
	var anthropicMsgs []anthropic.MessageParam

	for _, msg := range msgs {
		switch msg.Role {
		case schema.RoleSystem:
			systemPrompt = msg.Content

		case schema.RoleUser:
			if msg.ToolCallID == "" {
				anthropicMsgs = append(anthropicMsgs, anthropic.NewUserMessage(
					anthropic.NewTextBlock(msg.Content),
				))
				continue
			}

			anthropicMsgs = append(anthropicMsgs, anthropic.NewUserMessage(
				anthropic.NewToolResultBlock(msg.ToolCallID, msg.Content, false),
			))

		case schema.RoleAssistant:
			var blocks []anthropic.ContentBlockParamUnion
			if msg.Content != "" {
				blocks = append(blocks, anthropic.NewTextBlock(msg.Content))
			}

			for _, tc := range msg.ToolCalls {
				var inputMap map[string]any
				_ = json.Unmarshal(tc.Args, &inputMap)

				blocks = append(blocks, anthropic.ContentBlockParamUnion{
					OfToolUse: &anthropic.ToolUseBlockParam{
						ID:    tc.ID,
						Name:  tc.Name,
						Input: inputMap,
					},
				})
			}

			if len(blocks) > 0 {
				anthropicMsgs = append(anthropicMsgs, anthropic.NewAssistantMessage(blocks...))
			}
		}
	}

	return systemPrompt, anthropicMsgs
}

func (p *AnthropicProvider) transToolDef(toolDefs []schema.ToolDefinition) []anthropic.ToolUnionParam {
	tools := make([]anthropic.ToolUnionParam, 0, len(toolDefs))

	for _, toolDef := range toolDefs {
		// ToolInputSchemaParam 是结构体，需要通过 Properties 字段精准填充。
		var properties map[string]any
		var required []string

		if m, ok := toolDef.InputSchema.(map[string]any); ok {
			if p, ok := m["properties"].(map[string]any); ok {
				properties = p
			}

			if r, ok := m["required"].([]string); ok {
				required = r
			}
		}

		toolParam := anthropic.ToolParam{
			Name:        toolDef.Name,
			Description: anthropic.String(toolDef.Description),
			InputSchema: anthropic.ToolInputSchemaParam{
				Properties: properties,
				Required:   required,
			},
		}
		tools = append(tools, anthropic.ToolUnionParam{OfTool: &toolParam})
	}
	return tools
}
