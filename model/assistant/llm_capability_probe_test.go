package assistant

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/SuInk/diana/model/llm"
)

type probeRecordingClient struct {
	LLMProvider
	cfg    llm.ProviderConfig
	probed *[]llm.ProviderConfig
	mu     *sync.Mutex
}

func (c *probeRecordingClient) ProbeForcedToolChoice(context.Context) (llm.ProbeResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	*c.probed = append(*c.probed, c.cfg)
	return llm.ProbeResult{Provider: c.cfg.Provider, Model: c.cfg.Model, Usage: llm.Usage{InputTokens: 4, OutputTokens: 1}}, nil
}

type capabilityStoreStub struct {
	loaded []llm.DowngradeRecord
	saved  [][]llm.DowngradeRecord
}

func (s *capabilityStoreStub) LoadLLMParamDowngrades(context.Context) ([]llm.DowngradeRecord, error) {
	return s.loaded, nil
}

func (s *capabilityStoreStub) SaveLLMParamDowngrades(_ context.Context, records []llm.DowngradeRecord) error {
	s.saved = append(s.saved, records)
	return nil
}

func capabilityProbeRuntime(t *testing.T) (*Runtime, *[]llm.ProviderConfig, *capabilityStoreStub) {
	t.Helper()
	return capabilityProbeRuntimeWithToggle(t, true)
}

func capabilityProbeRuntimeWithToggle(t *testing.T, enabled bool) (*Runtime, *[]llm.ProviderConfig, *capabilityStoreStub) {
	t.Helper()
	store := &stubLLMProfileStore{set: llm.ProfileSet{Profiles: []llm.Profile{
		{ID: "one", Name: "A", Config: llm.ProviderConfig{Provider: llm.ProviderOpenAICompatible, APIKey: "k1", BaseURL: "https://one.invalid", Model: "chat-model"}},
		{ID: "two", Name: "B", Config: llm.ProviderConfig{Provider: llm.ProviderOpenAICompatible, APIKey: "k2", BaseURL: "https://two.invalid", Model: "image-model"}},
		{ID: "three", Name: "C", Config: llm.ProviderConfig{Provider: llm.ProviderOpenAICompatible, APIKey: "k3", BaseURL: "https://three.invalid", Model: "unused-model"}},
	}}}
	bot := BotConfig{ID: "a", OwnerID: "11", LLMCapabilityProbeEnabled: boolPointer(enabled), ModelRoles: map[string]ModelRole{
		"chat":   {ProfileID: "one", Model: "chat-model"},
		"intent": {ProfileID: "one", Model: "chat-model"},
		"image":  {ProfileID: "two", Model: "image-model"},
	}}.WithDefaults()
	r := NewRuntime(bot, nilChannel{}, NewPluginManager(), store, nil, nil, nil)
	r.SetProfiles(ProfileSet{Profiles: []BotConfig{bot}})
	probed := &[]llm.ProviderConfig{}
	mu := &sync.Mutex{}
	r.SetLLMProviderConfigFactory(func(cfg llm.ProviderConfig) (LLMProvider, error) {
		return &probeRecordingClient{LLMProvider: &capturingLLMProvider{reply: "ok"}, cfg: cfg, probed: probed, mu: mu}, nil
	})
	capabilityStore := &capabilityStoreStub{}
	r.SetLLMCapabilityStore(capabilityStore)
	return r, probed, capabilityStore
}

// 只探机器人角色真正绑着的档，按端点+模型去重，生图角色不发工具所以跳过。
func TestCapabilityProbeTargetsBoundNonImageProfiles(t *testing.T) {
	r, _, _ := capabilityProbeRuntime(t)
	targets := r.llmCapabilityProbeTargets()
	if len(targets) != 1 {
		t.Fatalf("targets=%#v, want only the chat/intent profile", targets)
	}
	if targets[0].BaseURL != "https://one.invalid" || targets[0].Model != "chat-model" {
		t.Fatalf("unexpected target: %#v", targets[0])
	}
}

// 探完一轮就把结论落盘，重启后不用重新学。
func TestCapabilityProbeRunsAndPersists(t *testing.T) {
	r, probed, store := capabilityProbeRuntime(t)
	r.probeLLMCapabilitiesWhenIdle(context.Background())
	if len(*probed) != 1 || (*probed)[0].Model != "chat-model" {
		t.Fatalf("probed=%#v", *probed)
	}
	if len(store.saved) != 1 {
		t.Fatalf("the probe round was not persisted: %#v", store.saved)
	}
}

// 探测默认关闭：没开开关的机器人一次请求都不该发。
func TestCapabilityProbeIsOffByDefault(t *testing.T) {
	enabled := BotConfig{ID: "a"}.WithDefaults().LLMCapabilityProbeEnabled
	if enabled == nil || *enabled {
		t.Fatalf("LLMCapabilityProbeEnabled default = %v, want false", enabled)
	}
	r, probed, store := capabilityProbeRuntimeWithToggle(t, false)
	if targets := r.llmCapabilityProbeTargets(); len(targets) != 0 {
		t.Fatalf("targets=%#v, want none while the toggle is off", targets)
	}
	r.probeLLMCapabilitiesWhenIdle(context.Background())
	if len(*probed) != 0 || len(store.saved) != 0 {
		t.Fatalf("a disabled probe still ran: probed=%#v saved=%#v", *probed, store.saved)
	}
}

// 落盘过的结论在启动时装回来。
func TestCapabilityProbeRestoresRecords(t *testing.T) {
	r, _, store := capabilityProbeRuntime(t)
	store.loaded = []llm.DowngradeRecord{{Key: "openai_compatible|https://one.invalid|chat-model", Field: "tool_choice", LearnedAt: time.Now()}}
	r.restoreLLMCapabilityRecords(context.Background())
	records := llm.DowngradeRecords()
	for _, record := range records {
		if record.Key == store.loaded[0].Key && record.Field == "tool_choice" {
			return
		}
	}
	t.Fatalf("the persisted conclusion was not restored: %#v", records)
}
