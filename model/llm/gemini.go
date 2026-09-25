// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package llm

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"iter"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"google.golang.org/genai"
)

type geminiClient struct {
	cfg    ProviderConfig
	client *genai.Client
	// httpClient 供改图时下载源图用：genai 只收字节，拿 URL 得自己取。
	httpClient *http.Client
}

const maxGeminiOutputTokens = int64(1<<31 - 1)

// geminiOAuthPlaceholderKey 是只靠 OAuth 的配置档建 genai 客户端时的占位 API Key。
const geminiOAuthPlaceholderKey = "oauth-placeholder"

// newGeminiClient 创建 Gemini provider 客户端。
func newGeminiClient(cfg ProviderConfig, httpClient *http.Client) (*geminiClient, error) {
	httpOptions := genai.HTTPOptions{}
	if cfg.BaseURL != "" {
		httpOptions.BaseURL = normalizeGeminiBaseURL(cfg.BaseURL)
	}
	if cfg.Timeout > 0 {
		httpOptions.Timeout = &cfg.Timeout
	}

	// Gemini SDK 的 client 创建需要 context，但这里不做网络请求，用 background 即可。
	apiKey := cfg.APIKey
	if strings.TrimSpace(apiKey) == "" && oauthOnlyTransport(httpClient) {
		// genai 建客户端时没有 API Key 直接报错，可只靠 OAuth 的配置档本来就没有。
		// 给个占位值让它建起来：真正的令牌在传输层注入，占位的 x-goog-api-key 也在
		// 那里摘掉（见 credentialTransport.requireOAuth）；OAuth 取不到凭据时请求根本
		// 不会发出去。没接上 OAuth 凭据的调用不给占位值，照旧报缺 API Key。
		apiKey = geminiOAuthPlaceholderKey
	}
	client, err := genai.NewClient(context.Background(), &genai.ClientConfig{
		APIKey:      apiKey,
		Backend:     genai.BackendGeminiAPI,
		HTTPClient:  withGeminiStreamReadErrors(httpClient),
		HTTPOptions: httpOptions,
	})
	if err != nil {
		return nil, err
	}

	return &geminiClient{
		cfg:        cfg,
		client:     client,
		httpClient: httpClient,
	}, nil
}

func normalizeGeminiBaseURL(raw string) string {
	base := strings.TrimRight(strings.TrimSpace(raw), "/")
	for _, suffix := range []string{"/v1beta/models", "/v1beta"} {
		if strings.HasSuffix(strings.ToLower(base), suffix) {
			base = strings.TrimRight(base[:len(base)-len(suffix)], "/")
			break
		}
	}
	return base
}

// Generate 调用 Gemini 模型生成回复。
func (c *geminiClient) Generate(ctx context.Context, req GenerateRequest) (result *GenerateResponse, resultErr error) {
	defer func() {
		if result != nil {
			result.ContinuationScope = continuationScope(c.cfg, req.Model)
		}
	}()
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
	req = c.cfg.withImplicitMaxOutputTokens(ProviderGemini, req, false)
	implicitLimit, err := setGeminiOutputTokenLimit(config, req)
	if err != nil {
		return nil, err
	}
	config.Tools = geminiTools(req.Tools)
	config.ToolConfig = geminiToolConfig(req)

	contents := geminiContents(messages, req.Tools)
	resp, err := c.client.Models.GenerateContent(ctx, req.Model, contents, config)
	if err != nil && implicitLimit && isGeminiOutputLimitRejection(err) {
		config.MaxOutputTokens = 0
		resp, err = c.client.Models.GenerateContent(ctx, req.Model, contents, config)
	}
	if err != nil {
		return nil, fmt.Errorf("llm: provider request failed: %w", err)
	}
	if resp == nil {
		return nil, fmt.Errorf("llm: gemini returned an empty response")
	}
	if err := geminiContentBlock(resp); err != nil {
		return nil, err
	}

	text := strings.TrimSpace(resp.Text())
	toolCalls := geminiToolCalls(resp, req.Tools)
	if text == "" && len(toolCalls) == 0 {
		return nil, geminiEmptyOutputError(resp, config.MaxOutputTokens)
	}

	return &GenerateResponse{
		Provider:  ProviderGemini,
		Model:     req.Model,
		Text:      text,
		ToolCalls: toolCalls,
		Usage:     geminiUsage(resp),
	}, nil
}

func (c *geminiClient) Stream(ctx context.Context, req GenerateRequest) (streamEvents <-chan ChatEvent, resultErr error) {
	defer func() {
		if resultErr == nil && streamEvents != nil {
			streamEvents = scopeContinuationEvents(ctx, streamEvents, continuationScope(c.cfg, req.Model))
		}
	}()
	req = applyContextBudget(req.withDefaults(c.cfg), c.cfg)
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
	req = c.cfg.withImplicitMaxOutputTokens(ProviderGemini, req, false)
	implicitLimit, err := setGeminiOutputTokenLimit(config, req)
	if err != nil {
		return nil, err
	}
	config.Tools = geminiTools(req.Tools)
	config.ToolConfig = geminiToolConfig(req)
	contents := geminiContents(messages, req.Tools)
	out := make(chan ChatEvent, 4)
	go func() {
		defer close(out)
		defer recoverChatStreamPanic(ctx, out, "gemini")
		var last Usage
		finished, emitted := false, false
		for response, err := range c.streamContent(ctx, req.Model, contents, config, implicitLimit) {
			if err != nil {
				sendChatEvent(ctx, out, ChatEvent{Type: ChatEventError, Error: err.Error()})
				return
			}
			if response == nil {
				sendChatEvent(ctx, out, ChatEvent{Type: ChatEventError, Error: "llm: gemini returned an empty stream response"})
				return
			}
			if err := geminiContentBlock(response); err != nil {
				sendChatEvent(ctx, out, ChatEvent{Type: ChatEventError, Error: err.Error(), ErrorCode: err.Reason, ErrorCause: err})
				return
			}
			if len(response.Candidates) > 0 && response.Candidates[0] != nil {
				reason := response.Candidates[0].FinishReason
				if reason == genai.FinishReasonStop {
					finished = true
				} else if reason != "" {
					if !emitted && !geminiHasOutput(response, req.Tools) {
						err := geminiEmptyOutputError(response, config.MaxOutputTokens)
						sendChatEvent(ctx, out, ChatEvent{Type: ChatEventError, Error: err.Error(), ErrorCause: err})
						return
					}
					sendChatEvent(ctx, out, ChatEvent{Type: ChatEventError, Error: "llm: incomplete gemini stream: " + string(reason)})
					return
				}
			}
			if len(response.Candidates) > 0 && response.Candidates[0] != nil && response.Candidates[0].Content != nil {
				for _, part := range response.Candidates[0].Content.Parts {
					if part == nil || part.Text == "" {
						continue
					}
					event := ChatEvent{Type: ChatEventTextDelta, Text: part.Text}
					if part.Thought {
						event = ChatEvent{Type: ChatEventReasoning, Reasoning: part.Text}
					} else {
						emitted = true
					}
					if !sendChatEvent(ctx, out, event) {
						return
					}
				}
			}
			for _, call := range geminiToolCalls(response, req.Tools) {
				emitted = true
				if !sendChatEvent(ctx, out, ChatEvent{Type: ChatEventToolCall, ToolCall: &call}) {
					return
				}
			}

			if response.UsageMetadata != nil {
				last = geminiUsage(response)
			}
		}
		if !finished {
			sendChatEvent(ctx, out, ChatEvent{Type: ChatEventError, Error: "llm: gemini stream ended before completion"})
			return
		}
		sendChatEvent(ctx, out, ChatEvent{Type: ChatEventUsage, Usage: &last})
		sendChatEvent(ctx, out, ChatEvent{Type: ChatEventDone, Response: &GenerateResponse{Provider: ProviderGemini, Model: req.Model}})
	}()
	return out, nil
}

// streamContent 打开流。代发的默认上限被拒时，400 会在第一个响应之前回来，此时
// 还没有任何输出，去掉上限重开一次即可，调用方看到的仍是一条完整的流。
func (c *geminiClient) streamContent(ctx context.Context, model string, contents []*genai.Content, config *genai.GenerateContentConfig, implicitLimit bool) iter.Seq2[*genai.GenerateContentResponse, error] {
	return func(yield func(*genai.GenerateContentResponse, error) bool) {
		first := true
		for response, err := range c.streamContentOnce(ctx, model, contents, config) {
			if first && err != nil && implicitLimit && isGeminiOutputLimitRejection(err) {
				config.MaxOutputTokens = 0
				for response, err := range c.streamContentOnce(ctx, model, contents, config) {
					if !yield(response, err) {
						return
					}
				}
				return
			}
			first = false
			if !yield(response, err) {
				return
			}
		}
	}
}

// streamContentOnce 发一次流式请求，把读流时断掉的原因交回调用方。
//
// genai 读流遇到读错误（连接断开、请求被取消）不会交给调用方：它在迭代器末尾用标准库
// log 打一行「Error <err>」，然后当作流正常结束。线上日志里那些没头没尾的
// 「Error context canceled」就是这么来的——看不出是哪次请求，而调用方主动放弃的请求
// 本来也不该记成错误；调用方这边只看到「stream ended before completion」，不知道为什么。
// withGeminiStreamReadErrors 在传输层把读错误记下、对 SDK 报 io.EOF，SDK 就不再打那
// 行日志；这里读完再带上模型名交回去。取消仍然是 errors.Is(err, context.Canceled)，
// 上层照处理取消的老路走，不会被当成上游故障。
func (c *geminiClient) streamContentOnce(ctx context.Context, model string, contents []*genai.Content, config *genai.GenerateContentConfig) iter.Seq2[*genai.GenerateContentResponse, error] {
	return func(yield func(*genai.GenerateContentResponse, error) bool) {
		recorder := &geminiStreamReadRecorder{}
		streamCtx := context.WithValue(ctx, geminiStreamReadRecorderKey{}, recorder)
		for response, err := range c.client.Models.GenerateContentStream(streamCtx, model, contents, config) {
			if err != nil {
				// 读断在一行中间时，SDK 会拿半行去解析、报「invalid stream chunk」；
				// 真正的原因是读错误，换成它。
				if readErr := recorder.err(); readErr != nil {
					err = geminiStreamReadFailure(model, readErr)
				}
				yield(nil, err)
				return
			}
			if !yield(response, nil) {
				return
			}
		}
		if readErr := recorder.err(); readErr != nil {
			yield(nil, geminiStreamReadFailure(model, readErr))
		}
	}
}

func geminiStreamReadFailure(model string, err error) error {
	return fmt.Errorf("llm: gemini stream read failed (model=%s): %w", model, err)
}

// geminiStreamReadRecorderKey 把一次流式请求的 recorder 挂在请求 ctx 上，传输层按它
// 认出哪些响应体要接管：没挂的（非流式调用、改图下载）原样放行。
type geminiStreamReadRecorderKey struct{}

// geminiStreamReadRecorder 记下流式响应体第一次读失败的原因。
type geminiStreamReadRecorder struct {
	mu    sync.Mutex
	first error
}

func (r *geminiStreamReadRecorder) record(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.first == nil {
		r.first = err
	}
}

func (r *geminiStreamReadRecorder) err() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.first
}

// withGeminiStreamReadErrors 给 genai 用的 HTTP 客户端套一层传输，见 streamContentOnce。
// 传进来的客户端（凭据注入等）原样保留在底下，只是多包一层；nil 时和 genai 的缺省一样
// 用标准传输。
func withGeminiStreamReadErrors(client *http.Client) *http.Client {
	wrapped := &http.Client{}
	if client != nil {
		*wrapped = *client
	}
	base := wrapped.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	wrapped.Transport = geminiStreamTransport{base: base}
	return wrapped
}

type geminiStreamTransport struct {
	base http.RoundTripper
}

func (t geminiStreamTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(req)
	if err != nil || resp == nil || resp.Body == nil {
		return resp, err
	}
	recorder, _ := req.Context().Value(geminiStreamReadRecorderKey{}).(*geminiStreamReadRecorder)
	// 非 2xx 的响应体是 SDK 拿去拼 APIError 的，读错了就让它照常报出来。
	if recorder == nil || resp.StatusCode < 200 || resp.StatusCode > 299 {
		return resp, nil
	}
	resp.Body = &geminiStreamBody{ReadCloser: resp.Body, recorder: recorder}
	return resp, nil
}

type geminiStreamBody struct {
	io.ReadCloser
	recorder *geminiStreamReadRecorder
}

func (b *geminiStreamBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if err != nil && !errors.Is(err, io.EOF) {
		b.recorder.record(err)
		return n, io.EOF
	}
	return n, err
}

// setGeminiOutputTokenLimit 写入输出上限，返回这个值是不是代填的。
func setGeminiOutputTokenLimit(config *genai.GenerateContentConfig, req GenerateRequest) (bool, error) {
	if req.MaxOutputTokens <= 0 {
		return false, nil
	}
	value, err := geminiOutputTokenLimit(req.MaxOutputTokens)
	if err != nil {
		return false, err
	}
	config.MaxOutputTokens = value
	return req.implicitMaxOutputTokens, nil
}

// isGeminiOutputLimitRejection 认出「上限超出该模型范围」的 400。上限更低的老模型
// 会这样拒绝代填的值，这时去掉字段重发，退回由服务端决定的缺省行为。
func isGeminiOutputLimitRejection(err error) bool {
	var apiErr genai.APIError
	if !errors.As(err, &apiErr) || apiErr.Code != http.StatusBadRequest {
		return false
	}
	text := strings.ToLower(apiErr.Message)
	for _, marker := range []string{"max_output_tokens", "maxoutputtokens", "max output tokens"} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

func geminiContentBlock(response *genai.GenerateContentResponse) *ContentBlockedError {
	if feedback := response.PromptFeedback; feedback != nil {
		switch feedback.BlockReason {
		case genai.BlockedReasonSafety, genai.BlockedReasonBlocklist, genai.BlockedReasonProhibitedContent, genai.BlockedReasonImageSafety:
			return &ContentBlockedError{Provider: ProviderGemini, Stage: "prompt", Reason: string(feedback.BlockReason), Message: feedback.BlockReasonMessage}
		}
	}
	for _, candidate := range response.Candidates {
		if candidate == nil {
			continue
		}
		switch candidate.FinishReason {
		case genai.FinishReasonSafety, genai.FinishReasonBlocklist, genai.FinishReasonProhibitedContent, genai.FinishReasonSPII, genai.FinishReasonImageSafety, genai.FinishReasonImageProhibitedContent:
			return &ContentBlockedError{Provider: ProviderGemini, Stage: "candidate", Reason: string(candidate.FinishReason)}
		}
	}
	return nil
}

// geminiEmptyOutputError 解释一次既没有正文也没有工具调用的结果。最常见的是
// MAX_TOKENS：模型把整份文件写进工具参数，写到一半撞上输出上限，Gemini 会把残缺的
// 调用整段丢掉，只剩一个空 text part。诊断里带上本次实际发出的上限（unset 表示
// 代填值被拒后去掉了字段，由网关决定），才看得出该调大哪边。
// 截断和 OpenAI 那边用同一个哨兵：同一模型原样重发还会截在同一处，交给降级链。
func geminiEmptyOutputError(response *genai.GenerateContentResponse, maxOutputTokens int32) error {
	reason := "none"
	if len(response.Candidates) > 0 && response.Candidates[0] != nil {
		if value := strings.TrimSpace(string(response.Candidates[0].FinishReason)); value != "" {
			reason = value
		}
	}
	limit := "unset"
	if maxOutputTokens > 0 {
		limit = strconv.FormatInt(int64(maxOutputTokens), 10)
	}
	usage := geminiUsage(response)
	diagnostics := fmt.Sprintf("(finish_reason=%s max_output_tokens=%s usage={input_tokens:%d output_tokens:%d})",
		reason, limit, usage.InputTokens, usage.OutputTokens)
	if reason == string(genai.FinishReasonMaxTokens) {
		return fmt.Errorf("llm: gemini output hit the max output token limit before any text or complete tool call %s: %w", diagnostics, ErrCompletionTruncatedNoText)
	}
	return fmt.Errorf("llm: gemini response has no text %s", diagnostics)
}

func geminiHasOutput(response *genai.GenerateContentResponse, definitions []ToolDefinition) bool {
	if len(geminiToolCalls(response, definitions)) > 0 {
		return true
	}
	if len(response.Candidates) == 0 || response.Candidates[0] == nil || response.Candidates[0].Content == nil {
		return false
	}
	for _, part := range response.Candidates[0].Content.Parts {
		if part != nil && !part.Thought && part.Text != "" {
			return true
		}
	}
	return false
}

func geminiUsage(response *genai.GenerateContentResponse) Usage {
	if response == nil || response.UsageMetadata == nil {
		return Usage{}
	}
	metadata := response.UsageMetadata
	return Usage{
		InputTokens:       int64(metadata.PromptTokenCount),
		OutputTokens:      int64(metadata.CandidatesTokenCount),
		TotalTokens:       int64(metadata.TotalTokenCount),
		CachedInputTokens: int64(metadata.CachedContentTokenCount),
	}
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
				part.ThoughtSignature = call.ThoughtSignature
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
		if msg.Role == RoleSystem {
			// 历史之后的补充指令：Gemini 只有开头的 systemInstruction，这里落成
			// 带标记的 user 文本，保持位置不动（理由见 splitSystemPrompt）。
			msg = inlineSystemMessage(msg)
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

// geminiToolConfig 在指定 ToolChoice 时把函数调用模式收紧到该工具，Gemini 因此
// 只能返回这一个函数调用；为空时保持默认的自动选择。
func geminiToolConfig(req GenerateRequest) *genai.ToolConfig {
	name := strings.TrimSpace(req.ToolChoice)
	if len(req.Tools) == 0 || name == "" {
		return nil
	}
	return &genai.ToolConfig{FunctionCallingConfig: &genai.FunctionCallingConfig{
		Mode:                 genai.FunctionCallingConfigModeAny,
		AllowedFunctionNames: []string{wireToolName(name)},
	}}
}

func geminiToolCalls(response *genai.GenerateContentResponse, definitions []ToolDefinition) []ToolCall {
	var calls []ToolCall
	if len(response.Candidates) == 0 || response.Candidates[0] == nil || response.Candidates[0].Content == nil {
		return calls
	}
	for _, part := range response.Candidates[0].Content.Parts {
		if part == nil || part.FunctionCall == nil {
			continue
		}
		call := part.FunctionCall
		if strings.TrimSpace(call.Name) == "" {
			continue
		}
		calls = append(calls, ToolCall{ID: call.ID, Name: nativeToolName(call.Name, definitions), Arguments: call.Args, ThoughtSignature: part.ThoughtSignature})
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
