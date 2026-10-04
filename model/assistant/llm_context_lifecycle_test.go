package assistant

import (
	"context"
	"testing"

	"github.com/SuInk/diana/model/llm"
)

// 后台入口不能把调用方传来的 context 当成跨事件句柄：即使它已经带着另一台
// 机器人的用量状态，显式传入的当前事件也必须重新决定 profile 和模型角色。
func TestProactiveRouterRestoresEventProfileContext(t *testing.T) {
	store := &stubLLMProfileStore{set: llm.ProfileSet{Profiles: []llm.Profile{
		{ID: "provider-a", Group: llm.GroupChat, Config: llm.ProviderConfig{Provider: llm.ProviderOpenAICompatible, Model: "model-a"}},
		{ID: "provider-b", Group: llm.GroupChat, Config: llm.ProviderConfig{Provider: llm.ProviderOpenAICompatible, Model: "model-b"}},
	}}}
	botA := BotConfig{ID: "bot-a", ModelRoles: map[string]ModelRole{"chat": {ProfileID: "provider-a", Model: "model-a"}}}.WithDefaults()
	botB := BotConfig{ID: "bot-b", ModelRoles: map[string]ModelRole{"chat": {ProfileID: "provider-b", Model: "model-b"}}}.WithDefaults()
	runtime := NewRuntime(botA, nilChannel{}, NewPluginManager(), store, nil, nil, nil)
	runtime.SetProfiles(ProfileSet{Profiles: []BotConfig{botA, botB}})
	selected := ""
	runtime.SetLLMProviderConfigFactory(func(cfg llm.ProviderConfig) (LLMProvider, error) {
		selected = cfg.Model
		return &capturingLLMProvider{reply: `{"should_reply":false,"category":"none","reason":"test"}`}, nil
	})
	event := MessageEvent{Kind: EventKindGroup, ProfileID: "bot-b", GroupID: "group", UserID: "user", MessageID: "message", RawMessage: "测试"}
	stale := withLLMUsageContext(context.Background(), MessageEvent{ProfileID: "bot-a", MessageID: "stale"})
	if _, _, _, _ = runtime.routeProactiveReplyBatch(stale, []proactiveReplyCandidate{{Event: event, Text: event.RawMessage}}); selected != "model-b" {
		t.Fatalf("proactive router selected %q from stale context, want event profile model-b", selected)
	}
}

func TestWithLLMUsageContextReplacesPreviousEvent(t *testing.T) {
	first := MessageEvent{ProfileID: "first", MessageID: "first-message"}
	second := MessageEvent{ProfileID: "second", MessageID: "second-message"}
	ctx := withLLMUsageContext(context.Background(), first)
	ctx = withLLMUsageContext(ctx, second)
	state := llmUsageFromContext(ctx)
	if state == nil || state.event.MessageID != second.MessageID || state.event.ProfileID != second.ProfileID {
		t.Fatalf("usage context = %#v, want second event", state)
	}
}

func TestProactiveReplyBatchKeySeparatesProfilesWithoutNamespace(t *testing.T) {
	first := MessageEvent{Kind: EventKindGroup, ProfileID: "first", GroupID: "group", UserID: "user"}
	second := first
	second.ProfileID = "second"
	if firstKey, secondKey := proactiveReplyBatchKey(first), proactiveReplyBatchKey(second); firstKey == secondKey {
		t.Fatalf("different profiles shared proactive batch key %q", firstKey)
	}
}
