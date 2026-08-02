package llm

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Provider string

const (
	ProviderOpenAICompatible Provider = "openai_compatible"
	ProviderGemini           Provider = "gemini"
	ProviderAnthropic        Provider = "anthropic"
)

type APIStyle string

const (
	APIStyleResponses       APIStyle = "responses"
	APIStyleChatCompletions APIStyle = "chat_completions"
)

type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

type Message struct {
	Role    Role          `json:"role"`
	Content string        `json:"content"`
	Parts   []ContentPart `json:"parts,omitempty"`
}

type ContentPartType string

const (
	ContentPartText     ContentPartType = "text"
	ContentPartImageURL ContentPartType = "image_url"
)

type ContentPart struct {
	Type     ContentPartType `json:"type"`
	Text     string          `json:"text,omitempty"`
	ImageURL string          `json:"image_url,omitempty"`
	Detail   string          `json:"detail,omitempty"`
}

type GenerateRequest struct {
	Model           string    `json:"model,omitempty"`
	Messages        []Message `json:"messages"`
	Temperature     *float64  `json:"temperature,omitempty"`
	MaxOutputTokens int64     `json:"max_output_tokens,omitempty"`
}

type Usage struct {
	InputTokens  int64 `json:"input_tokens,omitempty"`
	OutputTokens int64 `json:"output_tokens,omitempty"`
	TotalTokens  int64 `json:"total_tokens,omitempty"`
}

type GenerateResponse struct {
	Provider Provider `json:"provider"`
	Model    string   `json:"model,omitempty"`
	Text     string   `json:"text"`
	Usage    Usage    `json:"usage,omitempty"`
}

type ProviderConfig struct {
	Provider        Provider          `json:"provider"`
	APIStyle        APIStyle          `json:"api_style,omitempty"`
	APIKey          string            `json:"api_key,omitempty"`
	BaseURL         string            `json:"base_url,omitempty"`
	Models          []ModelInfo       `json:"models,omitempty"`
	Model           string            `json:"model"`
	ImageModel      string            `json:"image_model,omitempty"`
	UserAgent       string            `json:"user_agent,omitempty"`
	Headers         map[string]string `json:"headers,omitempty"`
	Temperature     *float64          `json:"temperature,omitempty"`
	MaxOutputTokens int64             `json:"max_output_tokens,omitempty"`
	Timeout         time.Duration     `json:"timeout,omitempty"`
}

type ClientOption func(*clientOptions)

type clientOptions struct {
	httpClient *http.Client
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

// ValidateChannel 校验渠道级配置（provider/key/地址）；模型允许留空，
// 由机器人配置按用途分配后在调用前补齐。
func (cfg ProviderConfig) ValidateChannel() error {
	probe := cfg
	if strings.TrimSpace(probe.Model) == "" {
		// 用占位模型跳过必填校验，其余字段（地址/密钥/参数）仍完整检查。
		probe.Model = "channel-placeholder"
	}
	return probe.Validate()
}

// Validate 校验 provider 配置是否可用于调用。
func (cfg ProviderConfig) Validate() error {
	// Validate 会先规整空白，避免前端输入带空格导致 provider/model 比较失败。
	cfg.Provider = Provider(strings.TrimSpace(string(cfg.Provider)))
	cfg.APIStyle = APIStyle(strings.TrimSpace(string(cfg.APIStyle)))
	if cfg.Provider == ProviderOpenAICompatible && cfg.APIStyle == "" {
		cfg.APIStyle = APIStyleResponses
	}
	cfg.APIKey = strings.TrimSpace(cfg.APIKey)
	cfg.BaseURL = strings.TrimSpace(cfg.BaseURL)
	cfg.Models = uniqueModels(cfg.Models)
	cfg.Model = strings.TrimSpace(cfg.Model)
	cfg.Headers = normalizeHeaders(cfg.Headers)
	if cfg.Provider == "" {
		return errors.New("llm: provider is required")
	}
	if !cfg.Provider.Supported() {
		return fmt.Errorf("llm: unsupported provider %q", cfg.Provider)
	}
	if cfg.Provider == ProviderOpenAICompatible && cfg.APIStyle != APIStyleResponses && cfg.APIStyle != APIStyleChatCompletions {
		return fmt.Errorf("llm: unsupported api style %q", cfg.APIStyle)
	}
	if strings.TrimSpace(cfg.APIKey) == "" {
		return ErrMissingAPIKey
	}
	if strings.TrimSpace(cfg.Model) == "" {
		return ErrMissingModel
	}
	if err := validateBaseURL(cfg.BaseURL); err != nil {
		return err
	}
	if cfg.MaxOutputTokens < 0 {
		return errors.New("llm: max_output_tokens must be greater than or equal to 0")
	}
	if cfg.Temperature != nil && (*cfg.Temperature < 0 || *cfg.Temperature > 2) {
		return errors.New("llm: temperature must be between 0 and 2")
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

// Supported 判断 provider 是否被当前项目支持。
func (provider Provider) Supported() bool {
	switch provider {
	case ProviderOpenAICompatible, ProviderGemini, ProviderAnthropic:
		return true
	default:
		return false
	}
}

// WithDefaults 补齐 provider 配置默认值。
func (cfg ProviderConfig) WithDefaults() ProviderConfig {
	// WithDefaults 只补配置默认值，不校验密钥；这样 WebUI 可以展示未填 key 的草稿配置。
	cfg.Provider = Provider(strings.TrimSpace(string(cfg.Provider)))
	cfg.APIStyle = APIStyle(strings.TrimSpace(string(cfg.APIStyle)))
	cfg.APIKey = strings.TrimSpace(cfg.APIKey)
	cfg.BaseURL = strings.TrimSpace(cfg.BaseURL)
	cfg.Models = uniqueModels(cfg.Models)
	cfg.Model = strings.TrimSpace(cfg.Model)
	cfg.ImageModel = strings.TrimSpace(cfg.ImageModel)
	cfg.UserAgent = strings.TrimSpace(cfg.UserAgent)
	cfg.Headers = normalizeHeaders(cfg.Headers)
	if cfg.Provider == ProviderOpenAICompatible && cfg.APIStyle == "" {
		cfg.APIStyle = APIStyleResponses
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
	return cfg
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

// DefaultImageModel 返回 provider 对应的默认图片模型。
func DefaultImageModel(provider Provider) string {
	switch provider {
	case ProviderOpenAICompatible:
		return "gpt-image-1"
	case ProviderGemini:
		return "imagen-4.0-generate-001"
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
	for i, msg := range req.Messages {
		if msg.Role == "" {
			return fmt.Errorf("llm: messages[%d].role is required", i)
		}
		if !messageHasContent(msg) {
			return fmt.Errorf("llm: messages[%d].content is required", i)
		}
	}
	return nil
}

func messageHasContent(msg Message) bool {
	if strings.TrimSpace(msg.Content) != "" {
		return true
	}
	for _, part := range msg.Parts {
		switch part.Type {
		case ContentPartText:
			if strings.TrimSpace(part.Text) != "" {
				return true
			}
		case ContentPartImageURL:
			if strings.TrimSpace(part.ImageURL) != "" {
				return true
			}
		}
	}
	return false
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
			if strings.TrimSpace(part.ImageURL) != "" {
				parts = append(parts, "[图片]")
			}
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
