// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

const (
	typeSafeDefaultBaseURL = "https://api.typesafe.ai"
	typeSafeDecisionPath   = "/v1/systemone"
	typeSafeDefaultModel   = "jev-latest"
)

// ErrDecisionRequired 表示这次调用要生成文本，而当前供应商只会做结构化判断。
// 上层据此给出「把这个用途单独绑到对话模型」的提示，而不是把空文本当成模型输出。
var ErrDecisionRequired = errors.New("llm: this provider only answers structured decisions")

// typeSafeClient 接 TypeSafe 的 System One 模型（Jev）。它不生成文本：输入一段
// state 和若干道有类型的题目，返回带概率的答案。判断类用途（要不要回话、算不算
// 在跟机器人说话）本来问的就是这些，换过来省掉一次对话模型调用和一轮 JSON 解析。
type typeSafeClient struct {
	cfg    ProviderConfig
	client *http.Client
}

func newTypeSafeClient(cfg ProviderConfig, httpClient *http.Client) *typeSafeClient {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &typeSafeClient{cfg: cfg, client: httpClient}
}

type typeSafeQuestion struct {
	Type         string `json:"type"`
	Instructions any    `json:"instructions"`
	Criteria     any    `json:"criteria,omitempty"`
}

type typeSafeRequest struct {
	Model     string                      `json:"model"`
	State     any                         `json:"state"`
	Questions map[string]typeSafeQuestion `json:"questions"`
}

type typeSafeAnswer struct {
	Type       string  `json:"type"`
	Noul       float64 `json:"noul"`
	Choice     string  `json:"choice"`
	Score      float64 `json:"score"`
	Confidence float64 `json:"confidence"`
}

type typeSafeResponse struct {
	Model   string                    `json:"model"`
	Answers map[string]typeSafeAnswer `json:"answers"`
	Usage   struct {
		InputTokens  int64 `json:"input_tokens"`
		OutputTokens int64 `json:"output_tokens"`
	} `json:"usage"`
}

func (c *typeSafeClient) Generate(ctx context.Context, req GenerateRequest) (*GenerateResponse, error) {
	req = req.withDefaults(c.cfg)
	if strings.TrimSpace(req.Model) == "" {
		req.Model = typeSafeDefaultModel
	}
	if len(req.Tools) > 0 {
		return nil, fmt.Errorf("%w: tool calls are not available", ErrDecisionRequired)
	}
	if req.Decision == nil || len(req.Decision.Questions) == 0 {
		return nil, ErrDecisionRequired
	}
	if err := req.Decision.Validate(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(c.cfg.APIKey) == "" && strings.TrimSpace(c.cfg.OAuthProvider) == "" {
		return nil, ErrMissingAPIKey
	}

	guidance, chat := splitSystemPrompt(req.Messages)
	payload := typeSafeRequest{
		Model:     req.Model,
		State:     typeSafeState(chat),
		Questions: map[string]typeSafeQuestion{},
	}
	for _, question := range req.Decision.Questions {
		payload.Questions[question.Key] = typeSafeQuestion{
			Type:         string(question.Kind),
			Instructions: typeSafeInstructions(question, guidance),
			Criteria:     typeSafeCriteria(question),
		}
	}

	decoded, err := c.post(ctx, payload)
	if err != nil {
		return nil, err
	}
	answers := make(map[string]DecisionAnswer, len(decoded.Answers))
	for key, answer := range decoded.Answers {
		answers[key] = DecisionAnswer{
			Kind:       DecisionKind(answer.Type),
			Noul:       answer.Noul,
			Choice:     answer.Choice,
			Score:      answer.Score,
			Confidence: answer.Confidence,
		}
	}
	text, err := req.Decision.RenderDecisionAnswers(answers)
	if err != nil {
		return nil, fmt.Errorf("%w (answered: %s)", err, strings.Join(decisionQuestionKeys(answers), ", "))
	}
	model := strings.TrimSpace(decoded.Model)
	if model == "" {
		model = req.Model
	}
	usage := Usage{InputTokens: decoded.Usage.InputTokens, OutputTokens: decoded.Usage.OutputTokens}
	usage.TotalTokens = usage.InputTokens + usage.OutputTokens
	return &GenerateResponse{
		Provider:          ProviderTypeSafe,
		Model:             model,
		Text:              text,
		Usage:             usage,
		ContinuationScope: continuationScope(c.cfg, req.Model),
	}, nil
}

func (c *typeSafeClient) post(ctx context.Context, payload typeSafeRequest) (*typeSafeResponse, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	endpoint := strings.TrimRight(strings.TrimSpace(c.cfg.BaseURL), "/")
	if endpoint == "" {
		endpoint = typeSafeDefaultBaseURL
	}
	if !strings.HasSuffix(endpoint, typeSafeDecisionPath) {
		endpoint += typeSafeDecisionPath
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	if key := strings.TrimSpace(c.cfg.APIKey); key != "" {
		request.Header.Set("Authorization", "Bearer "+key)
	}
	for name, value := range c.cfg.Headers {
		request.Header.Set(name, value)
	}
	response, err := c.client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("llm: provider request failed: %w", err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("llm: provider response read failed: %w", err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("llm: provider request failed with status %d: %s", response.StatusCode, strings.TrimSpace(string(raw)))
	}
	var decoded typeSafeResponse
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, fmt.Errorf("llm: provider response is not valid JSON: %w", err)
	}
	if len(decoded.Answers) == 0 {
		return nil, fmt.Errorf("llm: provider returned no answers")
	}
	return &decoded, nil
}

// typeSafeInstructions 把上层的系统提示词接在题目后面。判断模型没有 system 角色，
// 而这段提示词里有用户自己配的判据，丢掉等于改了判断口径。
func typeSafeInstructions(question DecisionQuestion, guidance string) any {
	instructions := strings.TrimSpace(question.Instructions)
	guidance = strings.TrimSpace(guidance)
	if guidance == "" {
		return instructions
	}
	if instructions == "" {
		return guidance
	}
	return []string{instructions, guidance}
}

func typeSafeCriteria(question DecisionQuestion) any {
	switch question.Kind {
	case DecisionNoul:
		criteria := map[string]string{}
		if text := strings.TrimSpace(question.TrueCriteria); text != "" {
			criteria["true"] = text
		}
		if text := strings.TrimSpace(question.FalseCriteria); text != "" {
			criteria["false"] = text
		}
		if len(criteria) == 0 {
			return nil
		}
		return criteria
	case DecisionChoice:
		criteria := map[string]any{}
		for _, option := range question.Options {
			if description := strings.TrimSpace(option.Description); description != "" {
				criteria[option.Value] = description
				continue
			}
			criteria[option.Value] = nil
		}
		return criteria
	case DecisionScore:
		return question.Levels
	}
	return nil
}

// typeSafeState 把对话摊平成纯文本。Jev 只吃文本，图片在这里变成一条计数标记：
// 上游的候选里本来就带着图片数量，留个标记比悄悄当成没有图片诚实。
func typeSafeState(messages []Message) []string {
	state := make([]string, 0, len(messages))
	for _, message := range messages {
		text := messageTextContent(message)
		if images := messageImageCount(message); images > 0 {
			marker := fmt.Sprintf("[图片 ×%d，判断模型看不到画面]", images)
			if text == "" {
				text = marker
			} else {
				text += "\n" + marker
			}
		}
		if text == "" {
			continue
		}
		state = append(state, string(message.Role)+"："+text)
	}
	return state
}

func messageImageCount(message Message) int {
	count := 0
	for _, part := range message.Parts {
		if part.Type == ContentPartImageURL {
			count++
		}
	}
	return count
}
