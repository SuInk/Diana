package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/packages/param"
	"github.com/openai/openai-go/v3/responses"
	"github.com/openai/openai-go/v3/shared"
)

type openAICompatibleClient struct {
	cfg        ProviderConfig
	client     openai.Client
	httpClient *http.Client
}

// newOpenAICompatibleClient 创建 OpenAI-compatible provider 客户端。
func newOpenAICompatibleClient(cfg ProviderConfig, httpClient *http.Client) *openAICompatibleClient {
	opts := []option.RequestOption{
		option.WithAPIKey(cfg.APIKey),
		option.WithHTTPClient(httpClient),
	}
	for name, value := range cfg.NormalizedHeaders() {
		opts = append(opts, option.WithHeader(name, value))
	}
	if userAgent := cfg.UserAgentWithDefault(); userAgent != "" {
		opts = append(opts, option.WithHeader("User-Agent", userAgent))
	}
	if cfg.BaseURL != "" {
		opts = append(opts, option.WithBaseURL(cfg.BaseURL))
	}
	if cfg.Timeout > 0 {
		opts = append(opts, option.WithRequestTimeout(cfg.Timeout))
	}

	return &openAICompatibleClient{
		cfg:        cfg,
		client:     openai.NewClient(opts...),
		httpClient: httpClient,
	}
}

// Generate 调用 OpenAI-compatible 模型生成回复。
func (c *openAICompatibleClient) Generate(ctx context.Context, req GenerateRequest) (*GenerateResponse, error) {
	req = req.withDefaults(c.cfg)
	if err := validateGenerateRequest(req); err != nil {
		return nil, err
	}
	if c.cfg.APIStyle == APIStyleChatCompletions {
		return c.generateChatCompletion(ctx, req)
	}
	return c.generateResponse(ctx, req)
}

func (c *openAICompatibleClient) generateChatCompletion(ctx context.Context, req GenerateRequest) (*GenerateResponse, error) {
	type chatMessage struct {
		Role    Role `json:"role"`
		Content any  `json:"content"`
	}
	messages := make([]chatMessage, 0, len(req.Messages))
	for _, message := range req.Messages {
		content := any(message.Content)
		if len(message.Parts) > 0 {
			parts := make([]map[string]any, 0, len(message.Parts)+1)
			if strings.TrimSpace(message.Content) != "" {
				parts = append(parts, map[string]any{"type": "text", "text": message.Content})
			}
			for _, part := range message.Parts {
				switch part.Type {
				case ContentPartText:
					parts = append(parts, map[string]any{"type": "text", "text": part.Text})
				case ContentPartImageURL:
					parts = append(parts, map[string]any{
						"type":      "image_url",
						"image_url": map[string]string{"url": part.ImageURL, "detail": part.Detail},
					})
				}
			}
			content = parts
		}
		messages = append(messages, chatMessage{Role: message.Role, Content: content})
	}
	payload := map[string]any{"model": req.Model, "messages": messages}
	if req.Temperature != nil {
		payload["temperature"] = *req.Temperature
	}
	if req.MaxOutputTokens > 0 {
		payload["max_tokens"] = req.MaxOutputTokens
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	request, err := c.newOpenAIRequest(ctx, "chat/completions", body)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := c.httpClient.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		errBody, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		return nil, openAICompatibleError(fmt.Errorf("chat completions failed"), &openAIErrorCapture{
			statusCode: response.StatusCode,
			body:       string(errBody),
		})
	}
	var decoded struct {
		Model   string `json:"model"`
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int64 `json:"prompt_tokens"`
			CompletionTokens int64 `json:"completion_tokens"`
			TotalTokens      int64 `json:"total_tokens"`
		} `json:"usage"`
	}
	if err := json.NewDecoder(response.Body).Decode(&decoded); err != nil {
		return nil, err
	}
	if len(decoded.Choices) == 0 || strings.TrimSpace(decoded.Choices[0].Message.Content) == "" {
		return nil, errors.New("llm: openai-compatible chat completion output is empty")
	}
	return &GenerateResponse{
		Provider: ProviderOpenAICompatible,
		Model:    decoded.Model,
		Text:     strings.TrimSpace(decoded.Choices[0].Message.Content),
		Usage: Usage{
			InputTokens:  decoded.Usage.PromptTokens,
			OutputTokens: decoded.Usage.CompletionTokens,
			TotalTokens:  decoded.Usage.TotalTokens,
		},
	}, nil
}

// generateResponse 使用 Responses API 生成回复。
func (c *openAICompatibleClient) generateResponse(ctx context.Context, req GenerateRequest) (*GenerateResponse, error) {
	system, messages := splitSystemPrompt(req.Messages)
	// Responses API 把 system prompt 放到 Instructions，普通对话放 InputItemList。
	params := responses.ResponseNewParams{
		Model: shared.ResponsesModel(req.Model),
		Input: responses.ResponseNewParamsInputUnion{
			OfInputItemList: openAIResponsesInput(messages),
		},
	}
	if system != "" {
		params.Instructions = param.NewOpt(system)
	}
	if req.Temperature != nil {
		params.Temperature = param.NewOpt(*req.Temperature)
	}
	if req.MaxOutputTokens > 0 {
		params.MaxOutputTokens = param.NewOpt(req.MaxOutputTokens)
	}

	resp, err, capture := c.newResponse(ctx, params)
	if err != nil {
		return nil, openAICompatibleError(err, capture)
	}
	text := strings.TrimSpace(resp.OutputText())
	if text == "" {
		return nil, fmt.Errorf("llm: openai-compatible responses output is empty")
	}

	return &GenerateResponse{
		Provider: ProviderOpenAICompatible,
		Model:    string(resp.Model),
		Text:     text,
		Usage: Usage{
			InputTokens:  resp.Usage.InputTokens,
			OutputTokens: resp.Usage.OutputTokens,
			TotalTokens:  resp.Usage.TotalTokens,
		},
	}, nil
}

// newResponse 执行 Responses API 请求并返回错误捕获器。
func (c *openAICompatibleClient) newResponse(ctx context.Context, params responses.ResponseNewParams) (*responses.Response, error, *openAIErrorCapture) {
	capture := &openAIErrorCapture{}
	body, err := json.Marshal(params)
	if err != nil {
		return nil, err, capture
	}
	req, err := c.newOpenAIRequest(ctx, "responses", body)
	if err != nil {
		return nil, err, capture
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err, capture
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		capture.statusCode = resp.StatusCode
		capture.body = string(errBody)
		return nil, fmt.Errorf("openai-compatible responses failed"), capture
	}
	contentType := strings.ToLower(resp.Header.Get("Content-Type"))
	if strings.Contains(contentType, "text/event-stream") {
		return decodeOpenAIResponseSSE(resp.Body, params.Model)
	}
	var out responses.Response
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err, capture
	}
	return &out, nil, capture
}

func (c *openAICompatibleClient) newOpenAIRequest(ctx context.Context, endpoint string, body []byte) (*http.Request, error) {
	baseURL := strings.TrimSpace(c.cfg.BaseURL)
	if baseURL == "" {
		baseURL = "https://api.openai.com/v1"
	}
	requestURL, err := joinOpenAICompatibleURL(baseURL, endpoint)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, requestURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
	for name, value := range normalizeHeaders(c.cfg.NormalizedHeaders()) {
		req.Header.Set(name, value)
	}
	if userAgent := c.cfg.UserAgentWithDefault(); strings.TrimSpace(userAgent) != "" {
		req.Header.Set("User-Agent", userAgent)
	}
	return req, nil
}

func decodeOpenAIResponseSSE(reader io.Reader, model shared.ResponsesModel) (*responses.Response, error, *openAIErrorCapture) {
	text, usage, err := decodeOpenAITextEventStream(reader)
	if err != nil {
		return nil, err, &openAIErrorCapture{}
	}
	if strings.TrimSpace(text) == "" {
		return nil, errors.New("llm: openai-compatible event stream output is empty"), &openAIErrorCapture{}
	}
	return &responses.Response{
		Model: model,
		Output: []responses.ResponseOutputItemUnion{{
			Type: "message",
			Role: "assistant",
			Content: []responses.ResponseOutputMessageContentUnion{{
				Type: "output_text",
				Text: text,
			}},
		}},
		Usage: responses.ResponseUsage{
			InputTokens:  usage.InputTokens,
			OutputTokens: usage.OutputTokens,
			TotalTokens:  usage.TotalTokens,
		},
	}, nil, &openAIErrorCapture{}
}

func decodeOpenAITextEventStream(reader io.Reader) (string, Usage, error) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	var builder strings.Builder
	var usage Usage
	var eventName string
	var dataLines []string
	flush := func() error {
		if len(dataLines) == 0 {
			eventName = ""
			return nil
		}
		data := strings.TrimSpace(strings.Join(dataLines, "\n"))
		eventName = strings.TrimSpace(eventName)
		dataLines = nil
		if data == "" || data == "[DONE]" {
			return nil
		}
		delta, eventUsage, err := textFromOpenAIStreamEvent(eventName, []byte(data))
		if err != nil {
			return err
		}
		builder.WriteString(delta)
		if eventUsage.TotalTokens > 0 || eventUsage.InputTokens > 0 || eventUsage.OutputTokens > 0 {
			usage = eventUsage
		}
		eventName = ""
		return nil
	}
	for scanner.Scan() {
		line := strings.TrimRight(scanner.Text(), "\r")
		if line == "" {
			if err := flush(); err != nil {
				return "", Usage{}, err
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		if rest, ok := strings.CutPrefix(line, "event:"); ok {
			eventName = strings.TrimSpace(rest)
			continue
		}
		if rest, ok := strings.CutPrefix(line, "data:"); ok {
			dataLines = append(dataLines, strings.TrimSpace(rest))
		}
	}
	if err := scanner.Err(); err != nil {
		return "", Usage{}, err
	}
	if err := flush(); err != nil {
		return "", Usage{}, err
	}
	return strings.TrimSpace(builder.String()), usage, nil
}

func textFromOpenAIStreamEvent(eventName string, data []byte) (string, Usage, error) {
	var root map[string]any
	if err := json.Unmarshal(data, &root); err != nil {
		return "", Usage{}, fmt.Errorf("llm: decode openai-compatible event stream: %w", err)
	}
	if errMessage := streamErrorMessage(root); errMessage != "" {
		return "", Usage{}, errors.New(errMessage)
	}
	eventType := stringField(root, "type")
	if eventName == "" {
		eventName = eventType
	}
	usage := usageFromPayload(root["usage"])
	switch eventName {
	case "response.output_text.delta":
		return stringField(root, "delta"), usage, nil
	case "response.output_text.done":
		return "", usage, nil
	case "response.completed":
		return "", usageFromCompletedResponse(root), nil
	case "response.failed", "error":
		if msg := streamErrorMessage(root); msg != "" {
			return "", Usage{}, errors.New(msg)
		}
	}
	if choices, ok := root["choices"].([]any); ok {
		return chatCompletionDeltaText(choices), usage, nil
	}
	return stringField(root, "delta", "text", "output_text"), usage, nil
}

func streamErrorMessage(root map[string]any) string {
	if message := stringField(root, "message"); message != "" {
		return message
	}
	if errPayload, ok := root["error"].(map[string]any); ok {
		if message := stringField(errPayload, "message"); message != "" {
			return message
		}
	}
	if response, ok := root["response"].(map[string]any); ok {
		if errPayload, ok := response["error"].(map[string]any); ok {
			if message := stringField(errPayload, "message"); message != "" {
				return message
			}
		}
	}
	return ""
}

func usageFromCompletedResponse(root map[string]any) Usage {
	if response, ok := root["response"].(map[string]any); ok {
		if usage := usageFromPayload(response["usage"]); usage.TotalTokens > 0 || usage.InputTokens > 0 || usage.OutputTokens > 0 {
			return usage
		}
	}
	return usageFromPayload(root["usage"])
}

func usageFromPayload(payload any) Usage {
	values, ok := payload.(map[string]any)
	if !ok {
		return Usage{}
	}
	return Usage{
		InputTokens:  int64Field(values, "input_tokens", "prompt_tokens"),
		OutputTokens: int64Field(values, "output_tokens", "completion_tokens"),
		TotalTokens:  int64Field(values, "total_tokens"),
	}
}

func chatCompletionDeltaText(choices []any) string {
	var builder strings.Builder
	for _, item := range choices {
		choice, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if delta, ok := choice["delta"].(map[string]any); ok {
			builder.WriteString(stringField(delta, "content"))
		}
		if message, ok := choice["message"].(map[string]any); ok {
			builder.WriteString(stringField(message, "content"))
		}
		builder.WriteString(stringField(choice, "text"))
	}
	return builder.String()
}

// openAIResponsesInput 将通用消息转换为 Responses API 输入。
func openAIResponsesInput(messages []Message) responses.ResponseInputParam {
	out := make(responses.ResponseInputParam, 0, len(messages))
	for _, msg := range messages {
		content := openAIResponseContent(msg)
		if len(content) == 0 {
			out = append(out, responses.ResponseInputItemParamOfMessage(msg.Content, openAIResponseRole(msg.Role)))
			continue
		}
		out = append(out, responses.ResponseInputItemParamOfMessage(content, openAIResponseRole(msg.Role)))
	}
	return out
}

// openAIResponseContent 将多模态消息转换为 Responses API content list。
func openAIResponseContent(msg Message) responses.ResponseInputMessageContentListParam {
	if len(msg.Parts) == 0 {
		return nil
	}
	content := make(responses.ResponseInputMessageContentListParam, 0, len(msg.Parts)+1)
	hasText := false
	for _, part := range msg.Parts {
		switch part.Type {
		case ContentPartText:
			text := strings.TrimSpace(part.Text)
			if text == "" {
				continue
			}
			hasText = true
			content = append(content, responses.ResponseInputContentParamOfInputText(text))
		case ContentPartImageURL:
			imageURL := strings.TrimSpace(part.ImageURL)
			if imageURL == "" {
				continue
			}
			detail := openAIImageDetail(part.Detail)
			image := responses.ResponseInputContentParamOfInputImage(detail)
			image.OfInputImage.ImageURL = param.NewOpt(imageURL)
			content = append(content, image)
		}
	}
	if !hasText {
		if text := strings.TrimSpace(msg.Content); text != "" {
			content = append([]responses.ResponseInputContentUnionParam{responses.ResponseInputContentParamOfInputText(text)}, content...)
		}
	}
	return content
}

func openAIImageDetail(detail string) responses.ResponseInputImageDetail {
	switch strings.ToLower(strings.TrimSpace(detail)) {
	case "low":
		return responses.ResponseInputImageDetailLow
	case "high":
		return responses.ResponseInputImageDetailHigh
	case "original":
		return responses.ResponseInputImageDetailOriginal
	default:
		return responses.ResponseInputImageDetailAuto
	}
}

// openAIResponseRole 将通用角色转换为 Responses API 角色。
func openAIResponseRole(role Role) responses.EasyInputMessageRole {
	switch role {
	case RoleSystem:
		return responses.EasyInputMessageRoleSystem
	case RoleAssistant:
		return responses.EasyInputMessageRoleAssistant
	default:
		return responses.EasyInputMessageRoleUser
	}
}

type openAIErrorCapture struct {
	statusCode int
	body       string
}

// captureOpenAIErrorBody 创建捕获 OpenAI 错误响应体的中间件。
func captureOpenAIErrorBody(capture *openAIErrorCapture) option.Middleware {
	return func(req *http.Request, next option.MiddlewareNext) (*http.Response, error) {
		res, err := next(req)
		if res == nil || res.Body == nil || res.StatusCode < http.StatusBadRequest {
			return res, err
		}

		// SDK 解析错误前先复制 body，再放回去，既保留原 SDK 行为也能给用户更清楚的错误。
		body, readErr := io.ReadAll(res.Body)
		_ = res.Body.Close()
		res.Body = io.NopCloser(bytes.NewReader(body))
		if readErr == nil {
			capture.statusCode = res.StatusCode
			capture.body = string(body)
		}
		return res, err
	}
}

// openAIRequestOptions 构造 OpenAI 请求选项。
func openAIRequestOptions(userAgent string, headers map[string]string, capture *openAIErrorCapture) []option.RequestOption {
	opts := []option.RequestOption{option.WithMiddleware(captureOpenAIErrorBody(capture))}
	for name, value := range normalizeHeaders(headers) {
		opts = append(opts, option.WithHeader(name, value))
	}
	if strings.TrimSpace(userAgent) != "" {
		opts = append(opts, option.WithHeader("User-Agent", userAgent))
	}
	return opts
}

// openAICompatibleError 规范化 OpenAI-compatible 请求错误。
func openAICompatibleError(err error, capture *openAIErrorCapture) error {
	var apiErr *openai.Error
	if errors.As(err, &apiErr) {
		statusCode := apiErr.StatusCode
		if statusCode == 0 && apiErr.Response != nil {
			statusCode = apiErr.Response.StatusCode
		}
		body := strings.TrimSpace(apiErr.RawJSON())
		if body == "" && capture != nil {
			body = capture.body
		}
		// 聚合商错误格式差异很大，统一压成 status/code/type/message/body 便于前端展示。
		return fmt.Errorf("llm: openai-compatible request failed: %s", formatOpenAIStatusError(statusCode, apiErr.Code, apiErr.Type, apiErr.Message, body))
	}
	if capture != nil && capture.statusCode >= http.StatusBadRequest {
		return fmt.Errorf("llm: openai-compatible request failed: %s", formatOpenAIStatusError(capture.statusCode, "", "", "", capture.body))
	}
	return err
}

// formatOpenAIStatusError 格式化 OpenAI-compatible HTTP 错误。
func formatOpenAIStatusError(statusCode int, code, typ, message, body string) string {
	if looksLikeCloudflareBlock(body) {
		// Cloudflare HTML 页对普通用户没帮助，直接转成可读原因。
		return openAIStatusLabel(statusCode) + ": Cloudflare blocked the API request before it reached the upstream service"
	}
	body = compactErrorBody(body)
	if code == "" && typ == "" && message == "" {
		// 有些 SDK 错误没有字段，但 body 里有 {"error":{...}}，再尝试解析一次。
		code, typ, message = openAIErrorFieldsFromBody(body)
	}

	details := make([]string, 0, 4)
	if code != "" {
		details = append(details, "code="+code)
	}
	if typ != "" {
		details = append(details, "type="+typ)
	}
	if message != "" {
		details = append(details, "message="+message)
	}
	if body != "" && len(details) == 0 {
		details = append(details, "body="+body)
	}
	if len(details) == 0 {
		return openAIStatusLabel(statusCode)
	}
	return openAIStatusLabel(statusCode) + ": " + strings.Join(details, "; ")
}

// looksLikeCloudflareBlock 判断错误页面是否像 Cloudflare 拦截。
func looksLikeCloudflareBlock(body string) bool {
	lower := strings.ToLower(body)
	return strings.Contains(lower, "<title>attention required!") && strings.Contains(lower, "cloudflare")
}

// openAIStatusLabel 将 HTTP 状态码转换为可读标签。
func openAIStatusLabel(statusCode int) string {
	if statusCode <= 0 {
		return "request failed"
	}
	if text := http.StatusText(statusCode); text != "" {
		return fmt.Sprintf("%d %s", statusCode, text)
	}
	return fmt.Sprintf("%d", statusCode)
}

// compactErrorBody 压缩并截断上游错误响应体。
func compactErrorBody(body string) string {
	body = strings.Join(strings.Fields(strings.TrimSpace(body)), " ")
	const maxRunes = 1000
	runes := []rune(body)
	if len(runes) > maxRunes {
		return string(runes[:maxRunes]) + "..."
	}
	return body
}

// openAIErrorFieldsFromBody 从 JSON 错误响应中提取 code/type/message。
func openAIErrorFieldsFromBody(body string) (string, string, string) {
	var root struct {
		Code    string          `json:"code"`
		Type    string          `json:"type"`
		Message string          `json:"message"`
		Error   json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal([]byte(body), &root); err != nil {
		return "", "", ""
	}
	if len(root.Error) > 0 {
		var nested struct {
			Code    string `json:"code"`
			Type    string `json:"type"`
			Message string `json:"message"`
		}
		if err := json.Unmarshal(root.Error, &nested); err == nil {
			if root.Code == "" {
				root.Code = nested.Code
			}
			if root.Type == "" {
				root.Type = nested.Type
			}
			if root.Message == "" {
				root.Message = nested.Message
			}
		}
	}
	return root.Code, root.Type, root.Message
}
