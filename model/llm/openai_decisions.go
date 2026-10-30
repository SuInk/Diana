// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strings"

	"github.com/SuInk/diana/model/netguard"
)

const openAIDecisionsDefaultModel = "gpt-6-luna"

type openAIDecisionsClient struct {
	cfg    ProviderConfig
	client *http.Client
}

func newOpenAIDecisionsClient(cfg ProviderConfig, client *http.Client) *openAIDecisionsClient {
	if client == nil {
		client = http.DefaultClient
	}
	return &openAIDecisionsClient{cfg: cfg, client: client}
}
func openAIDecisionsEndpoint(base string) string {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if base == "" {
		base = "https://api.openai.com/v1"
	}
	if strings.HasSuffix(base, "/decisions") {
		return base
	}
	if !strings.HasSuffix(base, "/v1") {
		base += "/v1"
	}
	return base + "/decisions"
}

type openAIDecisionQuestion struct {
	Type         string              `json:"type"`
	Name         string              `json:"name"`
	Instructions string              `json:"instructions"`
	Choices      []map[string]string `json:"choices,omitempty"`
	Levels       []map[string]string `json:"levels,omitempty"`
}
type openAIDecisionAnswer struct {
	Type        string   `json:"type"`
	Name        string   `json:"name"`
	Probability *float64 `json:"probability"`
	Choice      *string  `json:"choice"`
	Score       *float64 `json:"score"`
	Confidence  *float64 `json:"confidence"`
}

func (c *openAIDecisionsClient) Generate(ctx context.Context, req GenerateRequest) (*GenerateResponse, error) {
	req = req.withDefaults(c.cfg)
	if len(req.Tools) > 0 || req.Decision == nil || len(req.Decision.Questions) == 0 {
		return nil, ErrDecisionRequired
	}
	if err := req.Decision.Validate(); err != nil {
		return nil, err
	}
	if len(req.Decision.Questions) > 400 {
		return nil, fmt.Errorf("llm: Decisions supports at most 400 questions")
	}
	if req.Model == "" {
		req.Model = openAIDecisionsDefaultModel
	}
	if strings.TrimSpace(c.cfg.APIKey) == "" && c.cfg.OAuthProvider == "" {
		return nil, ErrMissingAPIKey
	}
	input, err := openAIDecisionInput(req.Messages)
	if err != nil {
		return nil, err
	}
	questions := make([]openAIDecisionQuestion, 0, len(req.Decision.Questions))
	for _, q := range req.Decision.Questions {
		wire := openAIDecisionQuestion{Name: q.Key, Instructions: q.Instructions, Type: string(q.Kind)}
		switch q.Kind {
		case DecisionNoul:
			wire.Type = "predicate"
			if q.TrueCriteria != "" {
				wire.Instructions += "\n成立条件：" + q.TrueCriteria
			}
			if q.FalseCriteria != "" {
				wire.Instructions += "\n不成立条件：" + q.FalseCriteria
			}
		case DecisionChoice:
			for _, option := range q.Options {
				wire.Choices = append(wire.Choices, map[string]string{"value": option.Value, "description": option.Description})
			}
		case DecisionScore:
			for _, level := range q.Levels {
				wire.Levels = append(wire.Levels, map[string]string{"label": level})
			}
		}
		questions = append(questions, wire)
	}
	payload := map[string]any{"model": req.Model, "input": input, "questions": questions}
	if req.PromptCacheKey != "" {
		payload["prompt_cache_key"] = req.PromptCacheKey
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, openAIDecisionsEndpoint(c.cfg.BaseURL), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	if c.cfg.UserAgent != "" {
		request.Header.Set("User-Agent", c.cfg.UserAgent)
	}
	if c.cfg.APIKey != "" {
		request.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
	}
	for name, value := range c.cfg.Headers {
		request.Header.Set(name, value)
	}
	response, err := c.client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("llm: provider request failed: %w", err)
	}
	defer response.Body.Close()
	raw, err := netguard.ReadResponseBody(response, 1<<20)
	if err != nil {
		return nil, fmt.Errorf("llm: provider response read failed: %w", err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("llm: provider request failed with status %d: %s", response.StatusCode, strings.TrimSpace(string(raw)))
	}
	var decoded struct {
		Model   string                 `json:"model"`
		Answers []openAIDecisionAnswer `json:"answers"`
		Usage   struct {
			InputTokens        int64 `json:"input_tokens"`
			OutputTokens       int64 `json:"output_tokens"`
			InputTokensDetails struct {
				CachedTokens int64 `json:"cached_tokens"`
			} `json:"input_tokens_details"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, fmt.Errorf("llm: invalid Decisions response: %w", err)
	}
	expected := make(map[string]DecisionQuestion, len(req.Decision.Questions))
	for _, q := range req.Decision.Questions {
		expected[q.Key] = q
	}
	answers := make(map[string]DecisionAnswer, len(decoded.Answers))
	for _, a := range decoded.Answers {
		q, ok := expected[a.Name]
		if !ok {
			return nil, fmt.Errorf("llm: unexpected decision answer %q", a.Name)
		}
		if _, exists := answers[a.Name]; exists {
			return nil, fmt.Errorf("llm: duplicate decision answer %q", a.Name)
		}
		if a.Type == "refusal" {
			return nil, fmt.Errorf("llm: Decisions refused question %q", a.Name)
		}
		answer := DecisionAnswer{Kind: q.Kind}
		valid := false
		switch q.Kind {
		case DecisionNoul:
			valid = a.Type == "predicate" && decisionNumberInRange(a.Probability, 0, 1)
			if valid {
				answer.Noul = *a.Probability
			}
		case DecisionChoice:
			if a.Type == "choice" && a.Choice != nil && decisionNumberInRange(a.Confidence, 0, 1) {
				for _, option := range q.Options {
					if option.Value == *a.Choice {
						valid = true
					}
				}
				answer.Choice, answer.Confidence = *a.Choice, *a.Confidence
			}
		case DecisionScore:
			valid = a.Type == "score" && decisionNumberInRange(a.Score, 0, float64(len(q.Levels)-1)) && decisionNumberInRange(a.Confidence, 0, 1)
			if valid {
				answer.Score, answer.Confidence = *a.Score, *a.Confidence
			}
		}
		if !valid {
			return nil, fmt.Errorf("llm: invalid decision answer %q (%s)", a.Name, a.Type)
		}
		answers[a.Name] = answer
	}
	text, err := req.Decision.RenderDecisionAnswers(answers)
	if err != nil {
		return nil, err
	}
	model := decoded.Model
	if model == "" {
		model = req.Model
	}
	usage := Usage{InputTokens: decoded.Usage.InputTokens, OutputTokens: decoded.Usage.OutputTokens, CachedInputTokens: decoded.Usage.InputTokensDetails.CachedTokens}
	usage.TotalTokens = usage.InputTokens + usage.OutputTokens
	return &GenerateResponse{Provider: ProviderOpenAIDecisions, Model: model, Text: text, Usage: usage, ContinuationScope: continuationScope(c.cfg, req.Model)}, nil
}

func decisionNumberInRange(value *float64, min, max float64) bool {
	return value != nil && !math.IsNaN(*value) && !math.IsInf(*value, 0) && *value >= min && *value <= max
}

// Preserve system guidance and image evidence; never silently discard unsupported input.
func openAIDecisionInput(messages []Message) ([]map[string]any, error) {
	input := make([]map[string]any, 0, len(messages))
	for _, msg := range messages {
		if len(msg.ToolCalls) > 0 || msg.Role == RoleTool {
			return nil, fmt.Errorf("llm: Decisions adapter does not support tool history")
		}
		role := string(msg.Role)
		if role != "user" && role != "system" && role != "developer" && role != "assistant" {
			return nil, fmt.Errorf("llm: unsupported Decisions role %q", role)
		}
		textType := "input_text"
		if role == "assistant" {
			textType = "output_text"
		}
		parts := []map[string]any{}
		if text := messageTextContent(msg); text != "" {
			parts = append(parts, map[string]any{"type": textType, "text": text})
		}
		for _, part := range msg.Parts {
			switch part.Type {
			case ContentPartText:
			case ContentPartImageURL:
				if role != "user" {
					return nil, fmt.Errorf("llm: Decisions images require user messages")
				}
				image := map[string]any{"type": "input_image", "image_url": part.ImageURL}
				if part.Detail != "" {
					image["detail"] = part.Detail
				}
				parts = append(parts, image)
			default:
				return nil, fmt.Errorf("llm: unsupported Decisions content %q", part.Type)
			}
		}
		if len(parts) > 0 {
			input = append(input, map[string]any{"role": role, "content": parts})
		}
	}
	if len(input) == 0 {
		return nil, ErrMissingMessages
	}
	return input, nil
}
