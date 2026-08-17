// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package llm

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// Protocol identifies the wire protocol independently from a vendor. OpenAI
// compatible gateways deliberately share one adapter and differ only here.
type Protocol string

const (
	ProtocolOpenAICompletions Protocol = "openai-completions"
	ProtocolOpenAIResponses   Protocol = "openai-responses"
	ProtocolAnthropicMessages Protocol = "anthropic-messages"
	ProtocolGemini            Protocol = "gemini"
)

type ProviderDefinition struct {
	ID       string            `json:"id"`
	Name     string            `json:"name"`
	Protocol Protocol          `json:"protocol"`
	BaseURL  string            `json:"baseUrl,omitempty"`
	APIKey   string            `json:"apiKey,omitempty"`
	Headers  map[string]string `json:"headers,omitempty"`
	Enabled  bool              `json:"enabled"`
}

type ModelDefinition struct {
	ID            string          `json:"id"`
	ProviderID    string          `json:"providerId"`
	ModelID       string          `json:"modelId"`
	Name          string          `json:"name"`
	ContextWindow int64           `json:"contextWindow,omitempty"`
	MaxTokens     int64           `json:"maxTokens,omitempty"`
	Capabilities  map[string]bool `json:"capabilities,omitempty"`
}

// AgentModelConfig is the new robot-facing selection. Profile and legacy
// ProviderConfig remain supported during migration.
type AgentModelConfig struct {
	ProviderID string `json:"providerId"`
	ModelID    string `json:"modelId"`
}

type ChatMessage struct {
	Role       Role          `json:"role"`
	Content    string        `json:"content,omitempty"`
	Parts      []ContentPart `json:"parts,omitempty"`
	ToolCalls  []ToolCall    `json:"toolCalls,omitempty"`
	ToolResult *ToolResult   `json:"toolResult,omitempty"`
}

type ChatRequest struct {
	Model           string           `json:"model,omitempty"`
	Messages        []ChatMessage    `json:"messages"`
	Temperature     *float64         `json:"temperature,omitempty"`
	ReasoningEffort string           `json:"reasoningEffort,omitempty"`
	MaxTokens       int64            `json:"maxTokens,omitempty"`
	Tools           []ToolDefinition `json:"tools,omitempty"`
}

type ChatEventType string

const (
	ChatEventTextDelta ChatEventType = "text_delta"
	ChatEventReasoning ChatEventType = "reasoning"
	ChatEventToolCall  ChatEventType = "tool_call"
	ChatEventUsage     ChatEventType = "usage"
	ChatEventDone      ChatEventType = "done"
	ChatEventError     ChatEventType = "error"
)

type ChatEvent struct {
	Type      ChatEventType `json:"type"`
	Text      string        `json:"text,omitempty"`
	Reasoning string        `json:"reasoning,omitempty"`
	ToolCall  *ToolCall     `json:"toolCall,omitempty"`
	Usage     *Usage        `json:"usage,omitempty"`
	Error     string        `json:"error,omitempty"`
}

type ToolResult struct {
	CallID  string `json:"callId"`
	Name    string `json:"name,omitempty"`
	Content string `json:"content,omitempty"`
}

// LLMAdapter is the vendor-neutral boundary consumed by the registry and
// future Runtime integration. Adapters own all wire-format conversion.
type LLMAdapter interface {
	Generate(context.Context, ModelDefinition, ChatRequest) (ChatResponse, error)
	Stream(context.Context, ModelDefinition, ChatRequest) (<-chan ChatEvent, error)
}

type ModelListerAdapter interface {
	ListModels(context.Context, ProviderDefinition) ([]ModelInfo, error)
}

type ChatResponse struct {
	Text      string     `json:"text,omitempty"`
	ToolCalls []ToolCall `json:"toolCalls,omitempty"`
	Usage     Usage      `json:"usage,omitempty"`
}

type ProviderRegistry struct {
	mu        sync.RWMutex
	providers map[string]ProviderDefinition
	models    map[string]ModelDefinition
	adapters  map[string]LLMAdapter
}

// ProviderRegistryDocument is the versioned on-disk representation. API keys
// remain inside the document for local operation, but are never returned by
// management snapshots.
type ProviderRegistryDocument struct {
	Version   int                  `json:"version"`
	Providers []ProviderDefinition `json:"providers"`
	Models    []ModelDefinition    `json:"models"`
}

func (r *ProviderRegistry) Document() ProviderRegistryDocument {
	r.mu.RLock()
	defer r.mu.RUnlock()
	providers := make([]ProviderDefinition, 0, len(r.providers))
	for _, provider := range r.providers {
		providers = append(providers, provider)
	}
	models := make([]ModelDefinition, 0, len(r.models))
	for _, model := range r.models {
		models = append(models, model)
	}
	sort.Slice(providers, func(i, j int) bool { return providers[i].ID < providers[j].ID })
	sort.Slice(models, func(i, j int) bool { return models[i].ID < models[j].ID })
	return ProviderRegistryDocument{Version: 1, Providers: providers, Models: models}
}

func RegistryFromDocument(document ProviderRegistryDocument) (*ProviderRegistry, error) {
	if document.Version != 1 {
		return nil, fmt.Errorf("llm: unsupported provider registry version %d", document.Version)
	}
	r := NewProviderRegistry()
	for _, provider := range document.Providers {
		cfg := ProviderConfig{APIKey: provider.APIKey, BaseURL: provider.BaseURL, Headers: provider.Headers}
		switch provider.Protocol {
		case ProtocolAnthropicMessages:
			cfg.Provider = ProviderAnthropic
		case ProtocolGemini:
			cfg.Provider = ProviderGemini
		case ProtocolOpenAICompletions:
			cfg.Provider, cfg.APIFormat = ProviderOpenAICompatible, APIFormatChatCompletions
		default:
			cfg.Provider, cfg.APIFormat = ProviderOpenAICompatible, APIFormatResponses
		}
		if err := r.RegisterProvider(provider, clientAdapter{cfg: cfg}); err != nil {
			return nil, err
		}
	}
	for _, model := range document.Models {
		if err := r.RegisterModel(model); err != nil {
			return nil, err
		}
	}
	return r, nil
}

func NewProviderRegistry() *ProviderRegistry {
	return &ProviderRegistry{providers: map[string]ProviderDefinition{}, models: map[string]ModelDefinition{}, adapters: map[string]LLMAdapter{}}
}

func (r *ProviderRegistry) RegisterProvider(provider ProviderDefinition, adapter LLMAdapter) error {
	provider.ID = strings.TrimSpace(provider.ID)
	provider.Name = strings.TrimSpace(provider.Name)
	provider.BaseURL = strings.TrimSpace(provider.BaseURL)
	if provider.ID == "" || provider.Protocol == "" {
		return fmt.Errorf("llm: provider id and protocol are required")
	}
	if adapter == nil {
		return fmt.Errorf("llm: adapter is required for provider %q", provider.ID)
	}
	if provider.Headers != nil {
		provider.Headers = normalizeHeaders(provider.Headers)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.providers[provider.ID] = provider
	r.adapters[provider.ID] = adapter
	return nil
}

func (r *ProviderRegistry) RegisterModel(model ModelDefinition) error {
	model.ID = strings.TrimSpace(model.ID)
	model.ProviderID = strings.TrimSpace(model.ProviderID)
	model.ModelID = strings.TrimSpace(model.ModelID)
	if model.ID == "" || model.ProviderID == "" || model.ModelID == "" {
		return fmt.Errorf("llm: model id, providerId and modelId are required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.providers[model.ProviderID]; !ok {
		return fmt.Errorf("llm: provider %q is not registered", model.ProviderID)
	}
	r.models[model.ID] = model
	return nil
}

func (r *ProviderRegistry) Provider(id string) (ProviderDefinition, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.providers[strings.TrimSpace(id)]
	return p, ok
}

func (r *ProviderRegistry) Model(id string) (ModelDefinition, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	m, ok := r.models[strings.TrimSpace(id)]
	return m, ok
}

func (r *ProviderRegistry) Models() []ModelDefinition {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]ModelDefinition, 0, len(r.models))
	for _, model := range r.models {
		out = append(out, model)
	}
	return out
}

// Generate routes a model through its provider adapter and rejects disabled
// or unknown selections before any network request is made.
func (r *ProviderRegistry) Generate(ctx context.Context, selection AgentModelConfig, req ChatRequest) (ChatResponse, error) {
	r.mu.RLock()
	model, modelOK := r.models[strings.TrimSpace(selection.ModelID)]
	provider, providerOK := r.providers[strings.TrimSpace(selection.ProviderID)]
	adapter := r.adapters[strings.TrimSpace(selection.ProviderID)]
	r.mu.RUnlock()
	if !providerOK || !modelOK || model.ProviderID != provider.ID {
		return ChatResponse{}, fmt.Errorf("llm: model selection %q/%q is not registered", selection.ProviderID, selection.ModelID)
	}
	if !provider.Enabled {
		return ChatResponse{}, fmt.Errorf("llm: provider %q is disabled", provider.ID)
	}
	return adapter.Generate(ctx, model, req)
}

// Stream routes provider-native deltas through the vendor-neutral event
// contract. Consumers never need to inspect SDK-specific stream types.
func (r *ProviderRegistry) Stream(ctx context.Context, selection AgentModelConfig, req ChatRequest) (<-chan ChatEvent, error) {
	r.mu.RLock()
	model, modelOK := r.models[strings.TrimSpace(selection.ModelID)]
	provider, providerOK := r.providers[strings.TrimSpace(selection.ProviderID)]
	adapter := r.adapters[strings.TrimSpace(selection.ProviderID)]
	r.mu.RUnlock()
	if !providerOK || !modelOK || model.ProviderID != provider.ID {
		return nil, fmt.Errorf("llm: model selection %q/%q is not registered", selection.ProviderID, selection.ModelID)
	}
	if !provider.Enabled {
		return nil, fmt.Errorf("llm: provider %q is disabled", provider.ID)
	}
	return adapter.Stream(ctx, model, req)
}

func (r *ProviderRegistry) ListModels(ctx context.Context, providerID string) ([]ModelInfo, error) {
	r.mu.RLock()
	provider, ok := r.providers[strings.TrimSpace(providerID)]
	adapter := r.adapters[strings.TrimSpace(providerID)]
	r.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("llm: provider %q is not registered", providerID)
	}
	lister, ok := adapter.(ModelListerAdapter)
	if !ok {
		return nil, fmt.Errorf("llm: provider %q does not support model listing", providerID)
	}
	return lister.ListModels(ctx, provider)
}

type clientAdapter struct{ cfg ProviderConfig }

func (a clientAdapter) ListModels(ctx context.Context, provider ProviderDefinition) ([]ModelInfo, error) {
	cfg := a.cfg
	cfg.BaseURL, cfg.APIKey, cfg.Headers = provider.BaseURL, provider.APIKey, provider.Headers
	return ListModels(ctx, cfg)
}

func (a clientAdapter) Generate(ctx context.Context, model ModelDefinition, req ChatRequest) (ChatResponse, error) {
	client, err := NewClient(a.cfgForModel(model))
	if err != nil {
		return ChatResponse{}, err
	}
	messages := make([]Message, 0, len(req.Messages))
	for _, message := range req.Messages {
		converted := Message{Role: message.Role, Content: message.Content, Parts: message.Parts, ToolCalls: message.ToolCalls}
		if message.ToolResult != nil {
			converted.ToolCallID, converted.ToolName, converted.Content = message.ToolResult.CallID, message.ToolResult.Name, message.ToolResult.Content
		}
		messages = append(messages, converted)
	}
	response, err := client.Generate(ctx, GenerateRequest{Model: model.ModelID, Messages: messages, Temperature: req.Temperature, ReasoningEffort: req.ReasoningEffort, MaxOutputTokens: req.MaxTokens, Tools: req.Tools})
	if err != nil {
		return ChatResponse{}, err
	}
	return ChatResponse{Text: response.Text, ToolCalls: response.ToolCalls, Usage: response.Usage}, nil
}

func (a clientAdapter) Stream(ctx context.Context, model ModelDefinition, req ChatRequest) (<-chan ChatEvent, error) {
	client, err := NewClient(a.cfgForModel(model))
	if err != nil {
		return nil, err
	}
	legacy := GenerateRequest{Model: model.ModelID, Messages: chatMessagesToLegacy(req.Messages), Temperature: req.Temperature, ReasoningEffort: req.ReasoningEffort, MaxOutputTokens: req.MaxTokens, Tools: req.Tools}
	if streamable, ok := client.(interface {
		Stream(context.Context, GenerateRequest) (<-chan ChatEvent, error)
	}); ok {
		return streamable.Stream(ctx, legacy)
	}
	out := make(chan ChatEvent, 2)
	go func() {
		defer close(out)
		response, err := a.Generate(ctx, model, req)
		if err != nil {
			out <- ChatEvent{Type: ChatEventError, Error: err.Error()}
			return
		}
		if response.Text != "" {
			out <- ChatEvent{Type: ChatEventTextDelta, Text: response.Text}
		}
		for _, call := range response.ToolCalls {
			call := call
			out <- ChatEvent{Type: ChatEventToolCall, ToolCall: &call}
		}
		usage := response.Usage
		out <- ChatEvent{Type: ChatEventUsage, Usage: &usage}
		out <- ChatEvent{Type: ChatEventDone}
	}()
	return out, nil
}

func chatMessagesToLegacy(messages []ChatMessage) []Message {
	out := make([]Message, 0, len(messages))
	for _, message := range messages {
		converted := Message{Role: message.Role, Content: message.Content, Parts: message.Parts, ToolCalls: message.ToolCalls}
		if message.ToolResult != nil {
			converted.ToolCallID, converted.ToolName, converted.Content = message.ToolResult.CallID, message.ToolResult.Name, message.ToolResult.Content
		}
		out = append(out, converted)
	}
	return out
}

func legacyMessagesToChat(messages []Message) []ChatMessage {
	out := make([]ChatMessage, 0, len(messages))
	for _, message := range messages {
		converted := ChatMessage{Role: message.Role, Content: message.Content, Parts: message.Parts, ToolCalls: message.ToolCalls}
		if message.Role == RoleTool {
			converted.ToolResult = &ToolResult{CallID: message.ToolCallID, Name: message.ToolName, Content: message.Content}
		}
		out = append(out, converted)
	}
	return out
}

func (a clientAdapter) cfgForModel(model ModelDefinition) ProviderConfig {
	cfg := a.cfg
	cfg.Model = model.ModelID
	return cfg
}

func protocolForConfig(cfg ProviderConfig) Protocol {
	switch cfg.Provider {
	case ProviderAnthropic:
		return ProtocolAnthropicMessages
	case ProviderGemini:
		return ProtocolGemini
	}
	if cfg.APIFormatWithDefault() == APIFormatChatCompletions {
		return ProtocolOpenAICompletions
	}
	return ProtocolOpenAIResponses
}

// NewProviderRegistryFromProfiles migrates legacy profiles without changing
// their persisted JSON. Each profile becomes one provider and its selected
// model; listed models are also registered under the same provider.
func NewProviderRegistryFromProfiles(set ProfileSet) (*ProviderRegistry, AgentModelConfig, error) {
	r := NewProviderRegistry()
	set = set.WithDefaults()
	var active AgentModelConfig
	for _, profile := range set.Profiles {
		cfg := profile.Config.WithDefaults()
		providerID := strings.TrimSpace(profile.ID)
		if providerID == "" {
			continue
		}
		provider := ProviderDefinition{ID: providerID, Name: profile.Name, Protocol: protocolForConfig(cfg), BaseURL: cfg.BaseURL, APIKey: cfg.APIKey, Headers: cfg.Headers, Enabled: true}
		if provider.Name == "" {
			provider.Name = providerID
		}
		adapter := clientAdapter{cfg: cfg}
		if err := r.RegisterProvider(provider, adapter); err != nil {
			return nil, AgentModelConfig{}, err
		}
		modelIDs := make([]string, 0, len(cfg.Models)+1)
		if cfg.Model != "" {
			modelIDs = append(modelIDs, cfg.Model)
		}
		for _, info := range cfg.Models {
			if info.ID != "" {
				modelIDs = append(modelIDs, info.ID)
			}
		}
		seen := map[string]bool{}
		for _, modelID := range modelIDs {
			if seen[modelID] {
				continue
			}
			seen[modelID] = true
			model := ModelDefinition{ID: providerID + ":" + modelID, ProviderID: providerID, ModelID: modelID, Name: modelID, ContextWindow: cfg.MaxContextTokensWithDefault(), MaxTokens: cfg.MaxOutputTokens}
			if err := r.RegisterModel(model); err != nil {
				return nil, AgentModelConfig{}, err
			}
			if profile.ID == set.ActiveID && modelID == cfg.Model {
				active = AgentModelConfig{ProviderID: providerID, ModelID: model.ID}
			}
		}
	}
	if active.ProviderID == "" {
		for _, profile := range set.Profiles {
			if profile.ID == set.ActiveID && profile.Config.Model != "" {
				active = AgentModelConfig{ProviderID: profile.ID, ModelID: profile.ID + ":" + profile.Config.Model}
				break
			}
		}
	}
	return r, active, nil
}

// PublicProvider returns a secret-free provider view for management APIs.
func (r *ProviderRegistry) PublicProvider(id string) (ProviderDefinition, bool) {
	p, ok := r.Provider(id)
	p.APIKey = ""
	return p, ok
}

// RegistryClient is the compatibility bridge used by the existing Agent
// loop. It keeps GenerateRequest/GenerateResponse stable while routing through
// the new vendor-neutral registry.
type RegistryClient struct {
	Registry  *ProviderRegistry
	Selection AgentModelConfig
}

func (c RegistryClient) Stream(ctx context.Context, req GenerateRequest) (<-chan ChatEvent, error) {
	if c.Registry == nil {
		return nil, fmt.Errorf("llm: provider registry is not configured")
	}
	return c.Registry.Stream(ctx, c.Selection, ChatRequest{Model: req.Model, Messages: legacyMessagesToChat(req.Messages), Temperature: req.Temperature, ReasoningEffort: req.ReasoningEffort, MaxTokens: req.MaxOutputTokens, Tools: req.Tools})
}

func (c RegistryClient) Generate(ctx context.Context, req GenerateRequest) (*GenerateResponse, error) {
	if c.Registry == nil {
		return nil, fmt.Errorf("llm: provider registry is not configured")
	}
	messages := make([]ChatMessage, 0, len(req.Messages))
	for _, message := range req.Messages {
		converted := ChatMessage{Role: message.Role, Content: message.Content, Parts: message.Parts, ToolCalls: message.ToolCalls}
		if message.Role == RoleTool {
			converted.ToolResult = &ToolResult{CallID: message.ToolCallID, Name: message.ToolName, Content: message.Content}
		}
		messages = append(messages, converted)
	}
	response, err := c.Registry.Generate(ctx, c.Selection, ChatRequest{Model: req.Model, Messages: messages, Temperature: req.Temperature, ReasoningEffort: req.ReasoningEffort, MaxTokens: req.MaxOutputTokens, Tools: req.Tools})
	if err != nil {
		return nil, err
	}
	return &GenerateResponse{Text: response.Text, ToolCalls: response.ToolCalls, Usage: response.Usage}, nil
}
