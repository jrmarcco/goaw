package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/jrmarcco/goaw/internal/schema"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/shared"
)

var _ LLMProvider = (*OpenAIProvider)(nil)

type OpenAIProvider struct {
	client openai.Client
	model  string
}

func NewOpenAIV3Provider(model string) (*OpenAIProvider, error) {
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		return nil, errors.New("OPENAI_API_KEY is not set")
	}

	baseURL := os.Getenv("OPENAI_BASE_URL")
	if baseURL != "" {
		return &OpenAIProvider{
			client: openai.NewClient(option.WithAPIKey(apiKey), option.WithBaseURL(baseURL)),
			model:  model,
		}, nil
	}

	return &OpenAIProvider{
		client: openai.NewClient(option.WithAPIKey(apiKey)),
		model:  model,
	}, nil
}

func (p *OpenAIProvider) Generate(ctx context.Context, msgs []schema.Message, availableTools []schema.ToolDefinition) (*schema.Message, error) {
	openaiMsgs := p.transContextMessage(msgs)

	params := openai.ChatCompletionNewParams{
		Model:    p.model,
		Messages: openaiMsgs,
	}

	if len(availableTools) > 0 {
		params.Tools = p.transToolDef(availableTools)
	}

	resp, err := p.client.Chat.Completions.New(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("failed to generate response: %w", err)
	}

	if len(resp.Choices) == 0 {
		return nil, fmt.Errorf("no response from server")
	}

	choice := resp.Choices[0].Message
	res := &schema.Message{
		Role:    schema.RoleAssistant,
		Content: choice.Content,
	}

	for i := range choice.ToolCalls {
		tc := &choice.ToolCalls[i]
		if tc.Type == "function" {
			res.ToolCalls = append(res.ToolCalls, schema.ToolCall{
				ID:   tc.ID,
				Name: tc.Function.Name,
				Args: []byte(tc.Function.Arguments),
			})
		}
	}

	return res, nil
}

// transToolMessage 翻译上下文消息。
func (p *OpenAIProvider) transContextMessage(msgs []schema.Message) []openai.ChatCompletionMessageParamUnion {
	openaiMsgs := make([]openai.ChatCompletionMessageParamUnion, 0, len(msgs))

	for _, msg := range msgs {
		switch msg.Role {
		case schema.RoleSystem:
			openaiMsgs = append(openaiMsgs, openai.SystemMessage(msg.Content))

		case schema.RoleUser:
			if msg.ToolCallID != "" {
				openaiMsgs = append(openaiMsgs, openai.ToolMessage(msg.Content, msg.ToolCallID))
			} else {
				openaiMsgs = append(openaiMsgs, openai.UserMessage(msg.Content))
			}

		case schema.RoleAssistant:
			astParam := openai.ChatCompletionAssistantMessageParam{}

			if msg.Content != "" {
				astParam.Content = openai.ChatCompletionAssistantMessageParamContentUnion{
					OfString: openai.String(msg.Content),
				}
			}

			// 如果历史包含 ToolCalls 必须原样放回，以维系模型的逻辑链
			if len(msg.ToolCalls) > 0 {
				toolCalls := make([]openai.ChatCompletionMessageToolCallUnionParam, 0, len(msg.ToolCalls))

				for _, tc := range msg.ToolCalls {
					// OfFunction 对应 GetFunction()，字段类型严格要求指针。
					toolCalls = append(toolCalls, openai.ChatCompletionMessageToolCallUnionParam{
						OfFunction: &openai.ChatCompletionMessageFunctionToolCallParam{
							ID:   tc.ID,
							Type: "function",
							Function: openai.ChatCompletionMessageFunctionToolCallFunctionParam{
								Name:      tc.Name,
								Arguments: string(tc.Args),
							},
						},
					})
				}

				astParam.ToolCalls = toolCalls
			}

			openaiMsgs = append(openaiMsgs, openai.ChatCompletionMessageParamUnion{
				OfAssistant: &astParam,
			})
		}
	}

	return openaiMsgs
}

// transToolDef 翻译工具定义。
func (p *OpenAIProvider) transToolDef(toolDefs []schema.ToolDefinition) []openai.ChatCompletionToolUnionParam {
	openaiTools := make([]openai.ChatCompletionToolUnionParam, 0, len(toolDefs))

	for i := range toolDefs {
		td := &toolDefs[i]
		var params shared.FunctionParameters

		if m, ok := td.InputSchema.(map[string]any); ok {
			params = shared.FunctionParameters(m)
		} else {
			b, _ := json.Marshal(td.InputSchema)
			_ = json.Unmarshal(b, &params)
		}

		openaiTools = append(openaiTools, openai.ChatCompletionFunctionTool(
			shared.FunctionDefinitionParam{
				Name:        td.Name,
				Description: openai.String(td.Description),
				Parameters:  params,
			},
		))
	}

	return openaiTools
}
