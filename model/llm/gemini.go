// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package llm

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"strings"

	"google.golang.org/genai"
)

type geminiClient struct {
	cfg    ProviderConfig
	client *genai.Client
}

const maxGeminiOutputTokens = int64(1<<31 - 1)

// newGeminiClient 创建 Gemini provider 客户端。
func newGeminiClient(cfg ProviderConfig, httpClient *http.Client) (*geminiClient, error) {
	httpOptions := genai.HTTPOptions{}
	if cfg.BaseURL != "" {
		httpOptions.BaseURL = cfg.BaseURL
	}
	if cfg.Timeout > 0 {
		httpOptions.Timeout = &cfg.Timeout
	}

	// Gemini SDK 的 client 创建需要 context，但这里不做网络请求，用 background 即可。
	client, err := genai.NewClient(context.Background(), &genai.ClientConfig{
		APIKey:      cfg.APIKey,
		Backend:     genai.BackendGeminiAPI,
		HTTPClient:  httpClient,
		HTTPOptions: httpOptions,
	})
	if err != nil {
		return nil, err
	}

	return &geminiClient{
		cfg:    cfg,
		client: client,
	}, nil
}

// Generate 调用 Gemini 模型生成回复。
func (c *geminiClient) Generate(ctx context.Context, req GenerateRequest) (*GenerateResponse, error) {
	req = req.withDefaults(c.cfg)
	req = applyContextBudget(req, c.cfg)
	if err := validateGenerateRequest(req); err != nil {
		return nil, fmt.Errorf("llm: local request validation failed: %w", err)
	}

	system, messages := splitSystemPrompt(req.Messages)
	config := &genai.GenerateContentConfig{}
	if system != "" {
		// Gemini 把 system instruction 放在 GenerateContentConfig，而不是普通对话消息里。
		config.SystemInstruction = genai.NewContentFromText(system, genai.RoleUser)
	}
	if req.Temperature != nil {
		temperature := float32(*req.Temperature)
		config.Temperature = &temperature
	}
	if req.MaxOutputTokens > 0 {
		maxOutputTokens, err := geminiOutputTokenLimit(req.MaxOutputTokens)
		if err != nil {
			return nil, err
		}
		config.MaxOutputTokens = maxOutputTokens
	}
	config.Tools = geminiTools(req.Tools)

	resp, err := c.client.Models.GenerateContent(ctx, req.Model, geminiContents(messages, req.Tools), config)
	if err != nil {
		return nil, fmt.Errorf("llm: provider request failed: %w", err)
	}

	text := strings.TrimSpace(resp.Text())
	toolCalls := geminiToolCalls(resp, req.Tools)
	if text == "" && len(toolCalls) == 0 {
		return nil, fmt.Errorf("llm: gemini response has no text")
	}

	return &GenerateResponse{
		Provider:  ProviderGemini,
		Model:     req.Model,
		Text:      text,
		ToolCalls: toolCalls,
		Usage: Usage{
			InputTokens:  int64(resp.UsageMetadata.PromptTokenCount),
			OutputTokens: int64(resp.UsageMetadata.CandidatesTokenCount),
			TotalTokens:  int64(resp.UsageMetadata.TotalTokenCount),
		},
	}, nil
}

func (c *geminiClient) Stream(ctx context.Context, req GenerateRequest) (<-chan ChatEvent, error) {
	req = req.withDefaults(c.cfg)
	if err := validateGenerateRequest(req); err != nil {
		return nil, err
	}
	system, messages := splitSystemPrompt(req.Messages)
	config := &genai.GenerateContentConfig{}
	if system != "" {
		config.SystemInstruction = genai.NewContentFromText(system, genai.RoleUser)
	}
	if req.Temperature != nil {
		value := float32(*req.Temperature)
		config.Temperature = &value
	}
	if req.MaxOutputTokens > 0 {
		value, err := geminiOutputTokenLimit(req.MaxOutputTokens)
		if err != nil {
			return nil, err
		}
		config.MaxOutputTokens = value
	}
	config.Tools = geminiTools(req.Tools)
	iterator := c.client.Models.GenerateContentStream(ctx, req.Model, geminiContents(messages, req.Tools), config)
	out := make(chan ChatEvent, 4)
	go func() {
		defer close(out)
		var last Usage
		for response, err := range iterator {
			if err != nil {
				out <- ChatEvent{Type: ChatEventError, Error: err.Error()}
				return
			}
			text := response.Text()
			if text != "" {
				out <- ChatEvent{Type: ChatEventTextDelta, Text: text}
			}
			for _, functionCall := range response.FunctionCalls() {
				if functionCall == nil {
					continue
				}
				call := ToolCall{ID: functionCall.ID, Name: nativeToolName(functionCall.Name, req.Tools), Arguments: functionCall.Args}
				out <- ChatEvent{Type: ChatEventToolCall, ToolCall: &call}
			}
			last = Usage{InputTokens: int64(response.UsageMetadata.PromptTokenCount), OutputTokens: int64(response.UsageMetadata.CandidatesTokenCount), TotalTokens: int64(response.UsageMetadata.TotalTokenCount)}
		}
		out <- ChatEvent{Type: ChatEventUsage, Usage: &last}
		out <- ChatEvent{Type: ChatEventDone}
	}()
	return out, nil
}

func geminiOutputTokenLimit(value int64) (int32, error) {
	if value < 0 || value > maxGeminiOutputTokens {
		return 0, fmt.Errorf("llm: Gemini max_output_tokens must be between 0 and %d", maxGeminiOutputTokens)
	}
	return int32(value), nil
}

// geminiContents 将通用消息转换为 Gemini content。
func geminiContents(messages []Message, definitions []ToolDefinition) []*genai.Content {
	out := make([]*genai.Content, 0, len(messages))
	for _, msg := range messages {
		if msg.Role == RoleAssistant && len(msg.ToolCalls) > 0 {
			parts := make([]*genai.Part, 0, len(msg.ToolCalls))
			for _, call := range msg.ToolCalls {
				part := genai.NewPartFromFunctionCall(wireToolName(call.Name), call.Arguments)
				part.FunctionCall.ID = call.ID
				parts = append(parts, part)
			}
			out = append(out, &genai.Content{Role: genai.RoleModel, Parts: parts})
			continue
		}
		if msg.Role == RoleTool {
			part := genai.NewPartFromFunctionResponse(wireToolName(msg.ToolName), map[string]any{"output": msg.Content})
			part.FunctionResponse.ID = msg.ToolCallID
			out = append(out, &genai.Content{Role: genai.RoleUser, Parts: []*genai.Part{part}})
			continue
		}
		var role genai.Role = genai.RoleUser
		if msg.Role == RoleAssistant {
			// Gemini SDK 用 model 表示 assistant 历史消息。
			role = genai.RoleModel
		}
		out = append(out, geminiContent(msg, role))
	}
	return out
}

func geminiTools(definitions []ToolDefinition) []*genai.Tool {
	if len(definitions) == 0 {
		return nil
	}
	declarations := make([]*genai.FunctionDeclaration, 0, len(definitions))
	for _, definition := range definitions {
		declarations = append(declarations, &genai.FunctionDeclaration{
			Name: wireToolName(definition.Name), Description: definition.Description, ParametersJsonSchema: definition.Parameters,
		})
	}
	return []*genai.Tool{{FunctionDeclarations: declarations}}
}

func geminiToolCalls(response *genai.GenerateContentResponse, definitions []ToolDefinition) []ToolCall {
	raw := response.FunctionCalls()
	calls := make([]ToolCall, 0, len(raw))
	for _, call := range raw {
		if call == nil || strings.TrimSpace(call.Name) == "" {
			continue
		}
		calls = append(calls, ToolCall{ID: call.ID, Name: nativeToolName(call.Name, definitions), Arguments: call.Args})
	}
	return calls
}

func geminiContent(msg Message, role genai.Role) *genai.Content {
	if len(msg.Parts) == 0 {
		return genai.NewContentFromText(messageTextContent(msg), role)
	}

	parts := make([]*genai.Part, 0, len(msg.Parts)+1)
	hasText := false
	for _, part := range msg.Parts {
		switch part.Type {
		case ContentPartText:
			text := strings.TrimSpace(part.Text)
			if text == "" {
				continue
			}
			hasText = true
			parts = append(parts, genai.NewPartFromText(text))
		case ContentPartImageURL:
			input, ok := imageInputFromURL(part.ImageURL)
			if !ok {
				continue
			}
			if len(input.Data) > 0 {
				parts = append(parts, genai.NewPartFromBytes(input.Data, input.MediaType))
				continue
			}
			parts = append(parts, genai.NewPartFromURI(input.URL, input.MediaType))
		case ContentPartInputAudio:
			data, err := base64.StdEncoding.DecodeString(strings.TrimSpace(part.AudioData))
			if err != nil || len(data) == 0 {
				continue
			}
			parts = append(parts, genai.NewPartFromBytes(data, "audio/"+normalizedInputAudioFormat(part.AudioFormat)))
		}
	}
	if !hasText {
		if text := strings.TrimSpace(msg.Content); text != "" {
			parts = append([]*genai.Part{genai.NewPartFromText(text)}, parts...)
		}
	}
	if len(parts) == 0 {
		return genai.NewContentFromText(messageTextContent(msg), role)
	}
	return genai.NewContentFromParts(parts, role)
}
