// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

func wireToolName(name string) string {
	var builder strings.Builder
	for _, char := range name {
		if char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || char == '_' || char == '-' {
			builder.WriteRune(char)
			continue
		}
		fmt.Fprintf(&builder, "_x%x_", char)
	}
	return builder.String()
}

func nativeToolName(name string, definitions []ToolDefinition) string {
	for _, definition := range definitions {
		if wireToolName(definition.Name) == name {
			return definition.Name
		}
	}
	return name
}

type Provider string

const (
	ProviderOpenAICompatible Provider = "openai_compatible"
	ProviderGemini           Provider = "gemini"
	ProviderAnthropic        Provider = "anthropic"
)

type APIFormat string

const (
	APIFormatResponses       APIFormat = "responses"
	APIFormatChatCompletions APIFormat = "chat_completions"
)

// APIStyle is the WebUI-facing name retained by the newer configuration API.
// APIFormat remains the runtime field used by the restored provider adapters.
type APIStyle string

const (
	APIStyleResponses       APIStyle = "responses"
	APIStyleChatCompletions APIStyle = "chat_completions"
)

const (
	// DefaultContextWindowTokens 是模型名和目录都推断不出窗口时的兜底值。
	// 128K 是当前主流模型的下限（GPT-4o、DeepSeek、Qwen、GLM、Llama 3.1 起皆
	// 如此），按 16K 兜底等于把现代模型当成 2023 年的模型用。真的跑小窗口本地
	// 模型时，请求超限会被 ErrContextOverflow 识别并自动收缩重试，同时可以在
	// WebUI 的「模型上下文窗口」里填写实际值一劳永逸。
	DefaultContextWindowTokens int64 = 128000
	DefaultMaxContextTokens    int64 = 128000
	DefaultMaxOutputTokens     int64 = 1024
	minContextWindowTokens     int64 = 1024
)

type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

type Message struct {
	Role       Role          `json:"role"`
	Content    string        `json:"content"`
	Parts      []ContentPart `json:"parts,omitempty"`
	ToolCalls  []ToolCall    `json:"tool_calls,omitempty"`
	ToolCallID string        `json:"tool_call_id,omitempty"`
	ToolName   string        `json:"tool_name,omitempty"`
	ToolError  bool          `json:"tool_error,omitempty"`
	// ResponsesOutput preserves original Responses API output items, including
	// reasoning and encrypted continuation state required by the next request.
	ResponsesOutput []json.RawMessage `json:"-"`
	Priority        MessagePriority   `json:"-"`
	// ContextGroup keeps related optional messages atomic during token fitting.
	// It is local orchestration metadata and is never sent to providers.
	ContextGroup string `json:"-"`
	// CacheBreakpoint marks the last message of a stable prompt prefix.
	// Providers with explicit prompt caching place a cache breakpoint after this
	// message; providers with automatic prefix caching ignore it.
	CacheBreakpoint bool `json:"-"`
	// AtomicText marks a message whose text is a single semantic unit. A
	// compressed summary cut in the middle still reads as complete while having
	// lost its conclusions, entity relations or time bounds, so the budgeter
	// keeps such a message whole or drops it whole instead of trimming it. The
	// producer is responsible for shrinking it to fit beforehand.
	AtomicText bool `json:"-"`
}

type ToolDefinition struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters"`
	Strict      bool           `json:"strict,omitempty"`
}

type ToolCall struct {
	ID        string         `json:"id"`
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments,omitempty"`
}

type MessagePriority int

const (
	MessagePriorityDefault MessagePriority = 0
	MessagePriorityHistory MessagePriority = 20
	MessagePrioritySummary MessagePriority = 60
	MessagePriorityMemory  MessagePriority = 80
	// Recent history is more useful for conversational continuity than recalled
	// memory or an older summary, but remains expendable before system context.
	MessagePriorityRecentHistory MessagePriority = 100
	MessagePrioritySystem        MessagePriority = 120
	MessagePriorityPlugin        MessagePriority = 130
	MessagePriorityCurrent       MessagePriority = 140
)

type ContentPartType string

const (
	ContentPartText       ContentPartType = "text"
	ContentPartImageURL   ContentPartType = "image_url"
	ContentPartInputAudio ContentPartType = "input_audio"
)

type ContentPart struct {
	Type        ContentPartType `json:"type"`
	Text        string          `json:"text,omitempty"`
	ImageURL    string          `json:"image_url,omitempty"`
	Detail      string          `json:"detail,omitempty"`
	AudioData   string          `json:"audio_data,omitempty"`
	AudioFormat string          `json:"audio_format,omitempty"`
}

type GenerateRequest struct {
	Model           string           `json:"model,omitempty"`
	Messages        []Message        `json:"messages"`
	Temperature     *float64         `json:"temperature,omitempty"`
	ReasoningEffort string           `json:"reasoning_effort,omitempty"`
	MaxOutputTokens int64            `json:"max_output_tokens,omitempty"`
	Tools           []ToolDefinition `json:"tools,omitempty"`
	// ToolChoice 强制本轮只能调用指定名字的工具。为空时保持供应商默认的自动
	// 选择；不支持强制选择的供应商按自动处理。
	ToolChoice string `json:"tool_choice,omitempty"`
	// MaxContextTokens 覆盖配置档的请求上下文上限。上游只在「上一次请求被供应商
	// 判为超出上下文」后重试时设置它，用来把请求收缩到更保守的预算；为 0 时按
	// 配置档取值。
	MaxContextTokens int64 `json:"-"`
}

type Usage struct {
	InputTokens  int64 `json:"input_tokens,omitempty"`
	OutputTokens int64 `json:"output_tokens,omitempty"`
	TotalTokens  int64 `json:"total_tokens,omitempty"`
	// CachedInputTokens 是 InputTokens 里命中供应商前缀缓存的部分（OpenAI 的
	// cached_tokens、Anthropic 的 cache_read_input_tokens、Gemini 的
	// cachedContentTokenCount）。系统提示词的稳定前缀是否真的被缓存，靠这个
	// 字段观察，而不是靠离线的字符串比较。
	CachedInputTokens int64 `json:"cached_input_tokens,omitempty"`
}

type GenerateResponse struct {
	Provider  Provider   `json:"provider"`
	Model     string     `json:"model,omitempty"`
	Text      string     `json:"text"`
	Usage     Usage      `json:"usage,omitempty"`
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
	// ResponsesOutput is internal continuation state and must not enter logs.
	ResponsesOutput []json.RawMessage `json:"-"`
}

type ImageGenerateRequest struct {
	Model  string `json:"model,omitempty"`
	Prompt string `json:"prompt"`
	Size   string `json:"size,omitempty"`
	N      int    `json:"n,omitempty"`
}

type ImageEditRequest struct {
	Model  string   `json:"model,omitempty"`
	Prompt string   `json:"prompt"`
	Images []string `json:"images"`
	Size   string   `json:"size,omitempty"`
	N      int      `json:"n,omitempty"`
}

type ImageGenerateResponse struct {
	Provider Provider `json:"provider"`
	Model    string   `json:"model,omitempty"`
	Images   []string `json:"images"`
}

type ProviderConfig struct {
	Provider Provider `json:"provider"`
	APIKey   string   `json:"api_key,omitempty"`
	// OAuthProvider 指向 model/llmauth 里某个已登录的提供商。填了它就用授权登录
	// 的令牌，此时 APIKey 可以留空——但填了也不浪费：续期失败时它是兜底。
	OAuthProvider       string            `json:"oauth_provider,omitempty"`
	BaseURL             string            `json:"base_url,omitempty"`
	APIFormat           APIFormat         `json:"api_format,omitempty"`
	APIStyle            APIStyle          `json:"api_style,omitempty"`
	Models              []ModelInfo       `json:"models,omitempty"`
	Model               string            `json:"model"`
	ImageModel          string            `json:"image_model,omitempty"`
	ImageBaseURL        string            `json:"image_base_url,omitempty"`
	ImageOrigin         string            `json:"image_origin,omitempty"`
	ImageTimeout        time.Duration     `json:"image_timeout,omitempty"`
	UserAgent           string            `json:"user_agent,omitempty"`
	Headers             map[string]string `json:"headers,omitempty"`
	Temperature         *float64          `json:"temperature,omitempty"`
	ReasoningEffort     string            `json:"reasoning_effort,omitempty"`
	ContextWindowTokens int64             `json:"context_window_tokens,omitempty"`
	MaxContextTokens    int64             `json:"max_context_tokens,omitempty"`
	MaxOutputTokens     int64             `json:"max_output_tokens,omitempty"`
	Timeout             time.Duration     `json:"timeout,omitempty"`
}

type ClientOption func(*clientOptions)

type clientOptions struct {
	httpClient  *http.Client
	credentials CredentialSource
}

// WithHTTPClient 注入自定义 HTTP client。
func WithHTTPClient(client *http.Client) ClientOption {
	return func(opts *clientOptions) {
		opts.httpClient = client
	}
}

type LLMClient interface {
	Generate(ctx context.Context, req GenerateRequest) (*GenerateResponse, error)
}

type ImageGenerator interface {
	GenerateImage(ctx context.Context, req ImageGenerateRequest) (*ImageGenerateResponse, error)
}

type ImageEditor interface {
	EditImage(ctx context.Context, req ImageEditRequest) (*ImageGenerateResponse, error)
}

var (
	ErrMissingAPIKey   = errors.New("llm: missing api key")
	ErrMissingModel    = errors.New("llm: missing model")
	ErrMissingMessages = errors.New("llm: missing messages")
)

// NewClient 根据 provider 配置创建 LLM 客户端。
func NewClient(cfg ProviderConfig, opts ...ClientOption) (LLMClient, error) {
	cfg = cfg.WithDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	options := clientOptions{
		httpClient: http.DefaultClient,
	}
	for _, opt := range opts {
		opt(&options)
	}
	// 凭据注入放在 HTTP 层，三家 provider 的 SDK 都不必知道 OAuth 的存在。
	options.httpClient = httpClientWithCredentials(options.httpClient, options.credentials)

	// 对外统一 LLMClient 接口，内部按 provider 分发到不同 SDK/HTTP 协议。
	switch cfg.Provider {
	case ProviderOpenAICompatible:
		return newOpenAICompatibleClient(cfg, options.httpClient), nil
	case ProviderGemini:
		return newGeminiClient(cfg, options.httpClient)
	case ProviderAnthropic:
		return newAnthropicClient(cfg, options.httpClient), nil
	default:
		return nil, fmt.Errorf("llm: unsupported provider %q", cfg.Provider)
	}
}

// GenerateImage 根据 provider 配置生成图片。
func GenerateImage(ctx context.Context, cfg ProviderConfig, req ImageGenerateRequest, opts ...ClientOption) (*ImageGenerateResponse, error) {
	client, err := NewClient(cfg, opts...)
	if err != nil {
		return nil, err
	}
	generator, ok := client.(ImageGenerator)
	if !ok {
		return nil, fmt.Errorf("llm: image generation is not supported for provider %q", cfg.Provider)
	}
	return generator.GenerateImage(ctx, req)
}

// EditImage 根据 provider 配置编辑图片。
func EditImage(ctx context.Context, cfg ProviderConfig, req ImageEditRequest, opts ...ClientOption) (*ImageGenerateResponse, error) {
	client, err := NewClient(cfg, opts...)
	if err != nil {
		return nil, err
	}
	editor, ok := client.(ImageEditor)
	if !ok {
		return nil, fmt.Errorf("llm: image editing is not supported for provider %q", cfg.Provider)
	}
	return editor.EditImage(ctx, req)
}

// Validate 校验 provider 配置是否可用于调用。
func (cfg ProviderConfig) Validate() error {
	// Validate 会先规整空白，避免前端输入带空格导致 provider/model 比较失败。
	cfg.Provider = Provider(strings.TrimSpace(string(cfg.Provider)))
	cfg.APIKey = strings.TrimSpace(cfg.APIKey)
	cfg.OAuthProvider = strings.ToLower(strings.TrimSpace(cfg.OAuthProvider))
	cfg.BaseURL = strings.TrimSpace(cfg.BaseURL)
	cfg.APIFormat = APIFormat(strings.TrimSpace(string(cfg.APIFormat)))
	cfg.APIStyle = APIStyle(strings.TrimSpace(string(cfg.APIStyle)))
	if cfg.APIStyle != "" {
		cfg.APIFormat = APIFormat(cfg.APIStyle)
	}
	cfg.Models = uniqueModels(cfg.Models)
	cfg.ImageBaseURL = strings.TrimSpace(cfg.ImageBaseURL)
	cfg.ImageOrigin = strings.TrimSpace(cfg.ImageOrigin)
	cfg.Model = strings.TrimSpace(cfg.Model)
	cfg.Headers = normalizeHeaders(cfg.Headers)
	cfg.ReasoningEffort = normalizeReasoningEffort(cfg.ReasoningEffort)
	if cfg.Provider == "" {
		return errors.New("llm: provider is required")
	}
	if !cfg.Provider.Supported() {
		return fmt.Errorf("llm: unsupported provider %q", cfg.Provider)
	}
	if cfg.Provider == ProviderOpenAICompatible {
		if cfg.APIFormat != "" && !cfg.APIFormat.Supported() {
			return fmt.Errorf("llm: unsupported api_format %q", cfg.APIFormat)
		}
	} else if cfg.APIFormat != "" {
		return fmt.Errorf("llm: api_format is only supported for provider %q", ProviderOpenAICompatible)
	}
	// 用 OAuth 登录的配置档没有 API Key 是正常状态，别在这里拦下来。
	if strings.TrimSpace(cfg.APIKey) == "" && strings.TrimSpace(cfg.OAuthProvider) == "" {
		return ErrMissingAPIKey
	}
	if strings.TrimSpace(cfg.Model) == "" {
		return ErrMissingModel
	}
	if err := validateBaseURL(cfg.BaseURL); err != nil {
		return err
	}
	if err := validateBaseURL(cfg.ImageBaseURL); err != nil {
		return fmt.Errorf("llm: invalid image_base_url: %w", err)
	}
	if err := validateImageOrigin(cfg.ImageOrigin); err != nil {
		return err
	}
	if cfg.ImageTimeout < 0 {
		return errors.New("llm: image_timeout must be greater than or equal to 0")
	}
	if cfg.MaxOutputTokens < 0 {
		return errors.New("llm: max_output_tokens must be greater than or equal to 0")
	}
	if cfg.ContextWindowTokens < 0 {
		return errors.New("llm: context_window_tokens must be greater than or equal to 0")
	}
	if cfg.MaxContextTokens < 0 {
		return errors.New("llm: max_context_tokens must be greater than or equal to 0")
	}
	if cfg.ContextWindowTokens > 0 && cfg.ContextWindowTokens < minContextWindowTokens {
		return fmt.Errorf("llm: context_window_tokens must be at least %d", minContextWindowTokens)
	}
	if cfg.MaxContextTokens > 0 && cfg.MaxContextTokens < minContextWindowTokens {
		return fmt.Errorf("llm: max_context_tokens must be at least %d", minContextWindowTokens)
	}
	if cfg.ContextWindowTokens > 0 && cfg.MaxContextTokens > cfg.ContextWindowTokens {
		return errors.New("llm: max_context_tokens cannot exceed context_window_tokens")
	}
	if cfg.MaxContextTokens > 0 && cfg.MaxOutputTokens >= cfg.MaxContextTokens {
		return errors.New("llm: max_output_tokens must be smaller than max_context_tokens")
	}
	if cfg.Temperature != nil && (*cfg.Temperature < 0 || *cfg.Temperature > 2) {
		return errors.New("llm: temperature must be between 0 and 2")
	}
	if err := validateReasoningEffort(cfg.ReasoningEffort); err != nil {
		return err
	}
	for name, value := range cfg.Headers {
		if !validHeaderName(name) {
			return fmt.Errorf("llm: invalid header name %q", name)
		}
		if strings.ContainsAny(value, "\r\n") {
			return fmt.Errorf("llm: invalid header value for %q", name)
		}
	}
	return nil
}

// ValidateChannel validates provider credentials and transport settings while
// allowing the model to be assigned by a bot profile later.
func (cfg ProviderConfig) ValidateChannel() error {
	probe := cfg
	if strings.TrimSpace(probe.Model) == "" {
		probe.Model = "channel-placeholder"
	}
	return probe.Validate()
}

// Supported 判断 provider 是否被当前项目支持。
func (provider Provider) Supported() bool {
	switch provider {
	case ProviderOpenAICompatible, ProviderGemini, ProviderAnthropic:
		return true
	default:
		return false
	}
}

// Supported 判断 OpenAI-compatible 文本 API 格式是否受支持。
func (format APIFormat) Supported() bool {
	switch format {
	case APIFormatResponses, APIFormatChatCompletions:
		return true
	default:
		return false
	}
}

// WithDefaults 补齐 provider 配置默认值。
func (cfg ProviderConfig) WithDefaults() ProviderConfig {
	// WithDefaults 只补配置默认值，不校验密钥；这样 WebUI 可以展示未填 key 的草稿配置。
	cfg.OAuthProvider = strings.ToLower(strings.TrimSpace(cfg.OAuthProvider))
	cfg.Provider = Provider(strings.TrimSpace(string(cfg.Provider)))
	cfg.APIKey = strings.TrimSpace(cfg.APIKey)
	cfg.BaseURL = strings.TrimSpace(cfg.BaseURL)
	cfg.APIFormat = APIFormat(strings.TrimSpace(string(cfg.APIFormat)))
	cfg.APIStyle = APIStyle(strings.TrimSpace(string(cfg.APIStyle)))
	if cfg.APIStyle != "" {
		cfg.APIFormat = APIFormat(cfg.APIStyle)
	} else if cfg.APIFormat != "" {
		cfg.APIStyle = APIStyle(cfg.APIFormat)
	}
	cfg.Models = uniqueModels(cfg.Models)
	cfg.ImageBaseURL = strings.TrimSpace(cfg.ImageBaseURL)
	cfg.ImageOrigin = strings.TrimSpace(cfg.ImageOrigin)
	cfg.Model = strings.TrimSpace(cfg.Model)
	cfg.ImageModel = strings.TrimSpace(cfg.ImageModel)
	cfg.UserAgent = strings.TrimSpace(cfg.UserAgent)
	cfg.ReasoningEffort = normalizeReasoningEffort(cfg.ReasoningEffort)
	cfg.Headers = normalizeHeaders(cfg.Headers)
	if cfg.Provider == ProviderOpenAICompatible {
		if cfg.APIFormat == "" {
			cfg.APIFormat = APIFormatResponses
		}
		cfg.APIStyle = APIStyle(cfg.APIFormat)
	} else {
		// Provider 切换会复用当前配置对象，OpenAI 专用格式不能跟到 Gemini/Anthropic。
		cfg.APIFormat = ""
		cfg.APIStyle = ""
	}
	if cfg.Model == "" {
		cfg.Model = DefaultModel(cfg.Provider)
	}
	if cfg.ImageModel == "" {
		cfg.ImageModel = DefaultImageModel(cfg.Provider)
	}
	if cfg.UserAgent == "" {
		cfg.UserAgent = DefaultUserAgent(cfg.Provider)
	}
	// ContextWindowTokens 和 MaxContextTokens 一律保持原样，0 就是 0。
	//
	// 以前这里会把推断结果写进字段，于是「推断值」和「用户设的值」在结构体里长得
	// 一模一样：配置一落库就把某一刻的猜测固定成了设置，换模型不跟着变，界面回显
	// 出来还像是用户自己填的（「模型默认填了 400k」就是这么来的）。窗口是模型的
	// 属性，不是这套配置的属性——改由 ContextWindowTokensWithDefault 在读取时
	// 按当前模型现算。
	return cfg
}

// MaxContextTokensWithDefault 返回不超过模型窗口的请求总 token 预算。
// 用户没单独设上限时就是整个窗口；设了也不能超过窗口。
func (cfg ProviderConfig) MaxContextTokensWithDefault() int64 {
	cfg = cfg.WithDefaults()
	window := cfg.ContextWindowTokensWithDefault()
	if cfg.MaxContextTokens <= 0 || cfg.MaxContextTokens > window {
		return window
	}
	return cfg.MaxContextTokens
}

// APIFormatWithDefault 返回 OpenAI-compatible 文本 API 格式。
func (cfg ProviderConfig) APIFormatWithDefault() APIFormat {
	if format := APIFormat(strings.TrimSpace(string(cfg.APIFormat))); format != "" {
		return format
	}
	if cfg.Provider == ProviderOpenAICompatible {
		return APIFormatResponses
	}
	return ""
}

// NormalizedHeaders 返回规整后的自定义 HTTP headers。
func (cfg ProviderConfig) NormalizedHeaders() map[string]string {
	return normalizeHeaders(cfg.Headers)
}

// normalizeHeaders 去掉空键值并规整 header 名称。
func normalizeHeaders(headers map[string]string) map[string]string {
	if len(headers) == 0 {
		return nil
	}
	out := make(map[string]string, len(headers))
	for name, value := range headers {
		name = http.CanonicalHeaderKey(strings.TrimSpace(name))
		value = strings.TrimSpace(value)
		if name == "" || value == "" {
			continue
		}
		out[name] = value
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// validHeaderName 保守校验 HTTP header 名称，避免换行、冒号等破坏请求结构。
func validHeaderName(name string) bool {
	name = strings.TrimSpace(name)
	if name == "" {
		return false
	}
	for _, r := range name {
		if r <= 32 || r >= 127 {
			return false
		}
		switch r {
		case '(', ')', '<', '>', '@', ',', ';', ':', '\\', '"', '/', '[', ']', '?', '=', '{', '}':
			return false
		}
	}
	return true
}

// validateBaseURL 校验自定义 BaseURL。
func validateBaseURL(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		// 空 BaseURL 表示使用各 provider SDK 默认地址。
		return nil
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return fmt.Errorf("llm: invalid base_url %q", raw)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("llm: base_url scheme must be http or https")
	}
	return nil
}

func validateImageOrigin(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	host, port, err := net.SplitHostPort(raw)
	if err != nil || strings.TrimSpace(host) == "" {
		return fmt.Errorf("llm: image_origin must use host:port format")
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 1 || portNumber > 65535 {
		return fmt.Errorf("llm: image_origin port must be between 1 and 65535")
	}
	return nil
}

// DefaultImageModel 返回 provider 对应的默认图片模型。
func DefaultImageModel(provider Provider) string {
	switch provider {
	case ProviderOpenAICompatible:
		return "gpt-image-2"
	case ProviderGemini:
		return "gemini-3-pro-image"
	default:
		return ""
	}
}

// DefaultUserAgent 返回 provider 对应的默认 User-Agent。
func DefaultUserAgent(provider Provider) string {
	switch provider {
	case ProviderOpenAICompatible:
		return DefaultOpenAICompatibleUserAgent
	default:
		return ""
	}
}

// ImageModelWithDefault 返回图片模型配置或默认值。
func (cfg ProviderConfig) ImageModelWithDefault() string {
	if strings.TrimSpace(cfg.ImageModel) != "" {
		return cfg.ImageModel
	}
	return DefaultImageModel(cfg.Provider)
}

func (cfg ProviderConfig) ImageBaseURLWithDefault() string {
	if strings.TrimSpace(cfg.ImageBaseURL) != "" {
		return cfg.ImageBaseURL
	}
	return cfg.BaseURL
}

func (cfg ProviderConfig) ImageTimeoutWithDefault() time.Duration {
	if cfg.ImageTimeout > 0 {
		return cfg.ImageTimeout
	}
	return cfg.Timeout
}

// UserAgentWithDefault 返回 User-Agent 配置或默认值。
func (cfg ProviderConfig) UserAgentWithDefault() string {
	if strings.TrimSpace(cfg.UserAgent) != "" {
		return cfg.UserAgent
	}
	return DefaultUserAgent(cfg.Provider)
}

// withDefaults 用 provider 配置补齐生成请求。
func (req GenerateRequest) withDefaults(cfg ProviderConfig) GenerateRequest {
	if strings.TrimSpace(req.Model) == "" {
		req.Model = cfg.Model
	}
	if req.Temperature == nil {
		req.Temperature = cfg.Temperature
	}
	if strings.TrimSpace(req.ReasoningEffort) == "" {
		req.ReasoningEffort = cfg.ReasoningEffort
	}
	req.ReasoningEffort = normalizeReasoningEffort(req.ReasoningEffort)
	if req.MaxOutputTokens == 0 {
		// 0 表示调用方没覆盖，沿用 provider config；负数会在 Validate 阶段拒绝。
		req.MaxOutputTokens = cfg.MaxOutputTokens
	}
	return req
}

// validateGenerateRequest 校验通用生成请求。
func validateGenerateRequest(req GenerateRequest) error {
	if strings.TrimSpace(req.Model) == "" {
		return ErrMissingModel
	}
	if len(req.Messages) == 0 {
		return ErrMissingMessages
	}
	if err := validateReasoningEffort(req.ReasoningEffort); err != nil {
		return err
	}
	for i, msg := range req.Messages {
		if msg.Role == "" {
			return fmt.Errorf("llm: messages[%d].role is required", i)
		}
	}
	return nil
}

func normalizeReasoningEffort(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func validateReasoningEffort(value string) error {
	switch normalizeReasoningEffort(value) {
	case "", "none", "minimal", "low", "medium", "high", "xhigh", "max", "ultra":
		return nil
	default:
		return fmt.Errorf("llm: unsupported reasoning_effort %q", value)
	}
}

func messageTextContent(msg Message) string {
	if text := strings.TrimSpace(msg.Content); text != "" {
		return text
	}
	parts := make([]string, 0, len(msg.Parts))
	for _, part := range msg.Parts {
		switch part.Type {
		case ContentPartText:
			if text := strings.TrimSpace(part.Text); text != "" {
				parts = append(parts, text)
			}
		case ContentPartImageURL:
			// Provider adapters carry image parts separately. A text placeholder
			// would falsely imply that an image was available in text-only paths.
		case ContentPartInputAudio:
			// Audio is carried as a real multimodal part. Never replace it with a
			// text placeholder that could invite the model to guess voice traits.
		}
	}
	return strings.TrimSpace(strings.Join(parts, "\n"))
}

// splitSystemPrompt 将 system 消息和普通对话消息拆开。
func splitSystemPrompt(messages []Message) (string, []Message) {
	// Gemini/Anthropic/OpenAI Responses 对 system prompt 的位置要求不同，这里统一拆出来。
	var system []string
	chat := make([]Message, 0, len(messages))
	for _, msg := range messages {
		if msg.Role == RoleSystem {
			system = append(system, messageTextContent(msg))
			continue
		}
		chat = append(chat, msg)
	}
	return strings.Join(system, "\n\n"), chat
}
