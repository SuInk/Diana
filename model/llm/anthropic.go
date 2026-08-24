// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/anthropics/anthropic-sdk-go/packages/param"
)

const defaultAnthropicMaxTokens int64 = 1024

type anthropicClient struct {
	cfg    ProviderConfig
	client anthropic.Client
}

// newAnthropicClient 创建 Anthropic provider 客户端。
func newAnthropicClient(cfg ProviderConfig, httpClient *http.Client) *anthropicClient {
	opts := []option.RequestOption{
		// 禁用环境变量默认值，确保 WebUI 当前配置就是唯一生效来源。
		option.WithoutEnvironmentDefaults(),
		option.WithAPIKey(cfg.APIKey),
		option.WithHTTPClient(httpClient),
	}
	if cfg.BaseURL != "" {
		opts = append(opts, option.WithBaseURL(cfg.BaseURL))
	}
	if cfg.Timeout > 0 {
		opts = append(opts, option.WithRequestTimeout(cfg.Timeout))
	}

	return &anthropicClient{
		cfg:    cfg,
		client: anthropic.NewClient(opts...),
	}
}

// Generate 调用 Anthropic 模型生成回复。
func (c *anthropicClient) Generate(ctx context.Context, req GenerateRequest) (*GenerateResponse, error) {
	req = req.withDefaults(c.cfg)
	if messagesHaveInputAudio(req.Messages) {
		return nil, fmt.Errorf("llm: Anthropic provider does not support Diana input_audio messages")
	}
	if req.MaxOutputTokens == 0 {
		// Anthropic messages API 要求 MaxTokens，未配置时给一个保守默认值。
		req.MaxOutputTokens = defaultAnthropicMaxTokens
	}
	req = applyContextBudget(req, c.cfg)
	if err := validateGenerateRequest(req); err != nil {
		return nil, fmt.Errorf("llm: local request validation failed: %w", err)
	}

	system, messages := splitSystemPrompt(req.Messages)
	params := anthropic.MessageNewParams{
		Model:     anthropic.Model(req.Model),
		MaxTokens: req.MaxOutputTokens,
		Messages:  anthropicMessages(messages, req.Tools),
	}
	if system != "" {
		// Anthropic 的 system prompt 单独放在 System 字段，不能混进 Messages。
		// 工具定义和 system 是每轮都一样的稳定前缀，打上缓存断点后同一次 Agent
		// 循环里的后续请求按缓存命中计费。
		params.System = []anthropic.TextBlockParam{{Text: system, CacheControl: anthropic.NewCacheControlEphemeralParam()}}
	}
	if req.Temperature != nil {
		params.Temperature = param.NewOpt(*req.Temperature)
	}
	params.Tools = anthropicTools(req.Tools)
	params.ToolChoice = anthropicToolChoice(req)

	resp, err := c.client.Messages.New(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("llm: provider request failed: %w", err)
	}

	text := strings.TrimSpace(anthropicText(resp.Content))
	toolCalls := anthropicToolCalls(resp.Content, req.Tools)
	if text == "" && len(toolCalls) == 0 {
		return nil, fmt.Errorf("llm: anthropic response has no text")
	}

	return &GenerateResponse{
		Provider:  ProviderAnthropic,
		Model:     string(resp.Model),
		Text:      text,
		ToolCalls: toolCalls,
		Usage: Usage{
			InputTokens:       resp.Usage.InputTokens + resp.Usage.CacheReadInputTokens + resp.Usage.CacheCreationInputTokens,
			OutputTokens:      resp.Usage.OutputTokens,
			TotalTokens:       resp.Usage.InputTokens + resp.Usage.CacheReadInputTokens + resp.Usage.CacheCreationInputTokens + resp.Usage.OutputTokens,
			CachedInputTokens: resp.Usage.CacheReadInputTokens,
		},
	}, nil
}

func (c *anthropicClient) Stream(ctx context.Context, req GenerateRequest) (<-chan ChatEvent, error) {
	req = req.withDefaults(c.cfg)
	if req.MaxOutputTokens == 0 {
		req.MaxOutputTokens = defaultAnthropicMaxTokens
	}
	system, messages := splitSystemPrompt(req.Messages)
	params := anthropic.MessageNewParams{Model: anthropic.Model(req.Model), MaxTokens: req.MaxOutputTokens, Messages: anthropicMessages(messages, req.Tools), Tools: anthropicTools(req.Tools)}
	if system != "" {
		params.System = []anthropic.TextBlockParam{{Text: system, CacheControl: anthropic.NewCacheControlEphemeralParam()}}
	}
	if req.Temperature != nil {
		params.Temperature = param.NewOpt(*req.Temperature)
	}
	params.ToolChoice = anthropicToolChoice(req)
	stream := c.client.Messages.NewStreaming(ctx, params)
	out := make(chan ChatEvent, 4)
	go func() {
		defer close(out)
		var usage Usage
		var activeTools = map[int64]*ToolCall{}
		var toolArguments = map[int64]string{}
		for stream.Next() {
			event := stream.Current()
			switch event.Type {
			case "content_block_start":
				if event.ContentBlock.Type == "tool_use" {
					call := ToolCall{ID: event.ContentBlock.ID, Name: nativeToolName(event.ContentBlock.Name, req.Tools), Arguments: map[string]any{}}
					activeTools[event.Index] = &call
				}
			case "content_block_delta":
				if event.Delta.Text != "" {
					out <- ChatEvent{Type: ChatEventTextDelta, Text: event.Delta.Text}
				}
				if event.Delta.Thinking != "" {
					out <- ChatEvent{Type: ChatEventReasoning, Reasoning: event.Delta.Thinking}
				}
				if event.Delta.PartialJSON != "" {
					toolArguments[event.Index] += event.Delta.PartialJSON
				}
			case "content_block_stop":
				if call := activeTools[event.Index]; call != nil {
					if raw := strings.TrimSpace(toolArguments[event.Index]); raw != "" {
						if err := json.Unmarshal([]byte(raw), &call.Arguments); err != nil {
							out <- ChatEvent{Type: ChatEventError, Error: fmt.Sprintf("llm: invalid tool arguments: %v", err)}
							return
						}
					}
					out <- ChatEvent{Type: ChatEventToolCall, ToolCall: call}
				}
			case "message_delta":
				usage.OutputTokens = event.Usage.OutputTokens
			}
		}
		if err := stream.Err(); err != nil {
			out <- ChatEvent{Type: ChatEventError, Error: err.Error()}
			return
		}
		out <- ChatEvent{Type: ChatEventUsage, Usage: &usage}
		out <- ChatEvent{Type: ChatEventDone}
	}()
	return out, nil
}

func messagesHaveInputAudio(messages []Message) bool {
	for _, message := range messages {
		for _, part := range message.Parts {
			if part.Type == ContentPartInputAudio && strings.TrimSpace(part.AudioData) != "" {
				return true
			}
		}
	}
	return false
}

// anthropicMessages 将通用消息转换为 Anthropic messages。
func anthropicMessages(messages []Message, definitions []ToolDefinition) []anthropic.MessageParam {
	out := make([]anthropic.MessageParam, 0, len(messages))
	for _, msg := range messages {
		blocks := anthropicContentBlocks(msg)
		if msg.Role == RoleAssistant && len(msg.ToolCalls) > 0 {
			blocks = blocks[:0]
			for _, call := range msg.ToolCalls {
				blocks = append(blocks, anthropic.NewToolUseBlock(call.ID, call.Arguments, wireToolName(call.Name)))
			}
		}
		if msg.Role == RoleTool {
			blocks = []anthropic.ContentBlockParamUnion{anthropic.NewToolResultBlock(msg.ToolCallID, msg.Content, false)}
		}
		if msg.CacheBreakpoint && len(blocks) > 0 {
			if control := blocks[len(blocks)-1].GetCacheControl(); control != nil {
				*control = anthropic.NewCacheControlEphemeralParam()
			}
		}
		if msg.Role == RoleAssistant {
			out = append(out, anthropic.NewAssistantMessage(blocks...))
			continue
		}
		out = append(out, anthropic.NewUserMessage(blocks...))
	}
	return out
}

func anthropicTools(definitions []ToolDefinition) []anthropic.ToolUnionParam {
	tools := make([]anthropic.ToolUnionParam, 0, len(definitions))
	for _, definition := range strictToolDefinitions(definitions) {
		schema := anthropic.ToolInputSchemaParam{ExtraFields: definition.Parameters}
		tool := anthropic.ToolUnionParamOfTool(schema, wireToolName(definition.Name))
		tool.OfTool.Description = param.NewOpt(definition.Description)
		tool.OfTool.Strict = param.NewOpt(definition.Strict)
		tools = append(tools, tool)
	}
	if len(tools) > 0 {
		// 工具定义排在缓存层级最前面，最后一个工具上的断点覆盖整个工具块。
		tools[len(tools)-1].OfTool.CacheControl = anthropic.NewCacheControlEphemeralParam()
	}
	return tools
}

// anthropicToolChoice 把统一的 ToolChoice 语义翻译成 Anthropic 参数：空值保持
// auto，指定工具名时强制本轮只调用该工具。
func anthropicToolChoice(req GenerateRequest) anthropic.ToolChoiceUnionParam {
	if len(req.Tools) == 0 {
		return anthropic.ToolChoiceUnionParam{}
	}
	if name := strings.TrimSpace(req.ToolChoice); name != "" {
		return anthropic.ToolChoiceUnionParam{OfTool: &anthropic.ToolChoiceToolParam{
			Name:                   wireToolName(name),
			DisableParallelToolUse: param.NewOpt(true),
		}}
	}
	return anthropic.ToolChoiceUnionParam{OfAuto: &anthropic.ToolChoiceAutoParam{DisableParallelToolUse: param.NewOpt(true)}}
}

func anthropicToolCalls(content []anthropic.ContentBlockUnion, definitions []ToolDefinition) []ToolCall {
	var calls []ToolCall
	for _, block := range content {
		if block.Type != "tool_use" || strings.TrimSpace(block.Name) == "" {
			continue
		}
		arguments := map[string]any{}
		_ = json.Unmarshal(block.Input, &arguments)
		calls = append(calls, ToolCall{ID: block.ID, Name: nativeToolName(block.Name, definitions), Arguments: arguments})
	}
	return calls
}

func anthropicContentBlocks(msg Message) []anthropic.ContentBlockParamUnion {
	if len(msg.Parts) == 0 {
		return []anthropic.ContentBlockParamUnion{anthropic.NewTextBlock(messageTextContent(msg))}
	}

	blocks := make([]anthropic.ContentBlockParamUnion, 0, len(msg.Parts)+1)
	hasText := false
	for _, part := range msg.Parts {
		switch part.Type {
		case ContentPartText:
			text := strings.TrimSpace(part.Text)
			if text == "" {
				continue
			}
			hasText = true
			blocks = append(blocks, anthropic.NewTextBlock(text))
		case ContentPartImageURL:
			input, ok := imageInputFromURL(part.ImageURL)
			if !ok {
				continue
			}
			if input.EncodedData != "" {
				blocks = append(blocks, anthropic.NewImageBlockBase64(input.MediaType, input.EncodedData))
				continue
			}
			blocks = append(blocks, anthropic.NewImageBlock(anthropic.URLImageSourceParam{URL: input.URL}))
		}
	}
	if !hasText {
		if text := strings.TrimSpace(msg.Content); text != "" {
			blocks = append([]anthropic.ContentBlockParamUnion{anthropic.NewTextBlock(text)}, blocks...)
		}
	}
	if len(blocks) == 0 {
		return []anthropic.ContentBlockParamUnion{anthropic.NewTextBlock(messageTextContent(msg))}
	}
	return blocks
}

// anthropicText 从 Anthropic 响应块中拼接文本。
func anthropicText(blocks []anthropic.ContentBlockUnion) string {
	parts := make([]string, 0, len(blocks))
	for _, block := range blocks {
		if block.Type == "text" && block.Text != "" {
			// 当前只消费文本块，工具/图片等块先忽略，避免把结构化内容直接拼给聊天通道。
			parts = append(parts, block.Text)
		}
	}
	return strings.Join(parts, "\n")
}
