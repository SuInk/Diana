// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/SuInk/diana/model/applog"
	"github.com/SuInk/diana/model/llm"
)

func TestRelationshipEvaluationDecisionValidation(t *testing.T) {
	tests := []struct {
		name  string
		raw   string
		ok    bool
		delta int
	}{
		{name: "semantic update", raw: `{"should_update":true,"delta":-2,"confidence":0.91,"reason":"明确针对机器人的攻击"}`, ok: true, delta: -2},
		{name: "code fence", raw: "```json\n{\"should_update\":true,\"delta\":2,\"confidence\":0.8,\"reason\":\"真诚感谢\"}\n```", ok: true, delta: 2},
		{name: "no update normalizes delta", raw: `{"should_update":false,"delta":3,"confidence":0.99,"reason":"只是讨论规则"}`, ok: true, delta: 0},
		{name: "low confidence applies zero", raw: `{"should_update":true,"delta":3,"confidence":0.5,"reason":"不确定"}`, ok: true, delta: 0},
		{name: "out of range delta", raw: `{"should_update":true,"delta":4,"confidence":0.9,"reason":"bad"}`, ok: false},
		{name: "missing reason", raw: `{"should_update":true,"delta":1,"confidence":0.9}`, ok: false},
		{name: "invalid json", raw: `{"should_update":true`, ok: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			decision, ok := parseRelationshipEvaluationDecision(test.raw)
			if ok != test.ok {
				t.Fatalf("decision=%#v ok=%v, want %v", decision, ok, test.ok)
			}
			if ok && decision.effectiveDelta() != test.delta {
				t.Fatalf("effective delta=%d, want %d: %#v", decision.effectiveDelta(), test.delta, decision)
			}
		})
	}
}

func TestRelationshipEvaluationUsesRouterSemantics(t *testing.T) {
	provider := &capturingLLMProvider{reply: `{"should_update":false,"delta":0,"confidence":0.98,"reason":"消息在讨论计分规则，并非攻击机器人"}`}
	store := &stubLLMProfileStore{set: llm.ProfileSet{
		Profiles: []llm.Profile{
			{ID: "main", Name: "主聊天", Group: "chat", Config: llm.ProviderConfig{Provider: llm.ProviderOpenAICompatible, APIKey: "main-key", Model: "main-model"}},
			{ID: "routing", Name: "快速语义判定", Group: "routing", Config: llm.ProviderConfig{Provider: llm.ProviderOpenAICompatible, APIKey: "routing-key", Model: "routing-model"}},
		},
	}}
	memory := newMemoryUserMemoryStore()
	memory.profiles["user"] = UserMemoryProfile{UserID: "user", Favorability: 17, MessageCount: 8}
	runtime := NewRuntime(BotConfig{BotAccount: "bot", OwnerID: "owner"}, nilChannel{}, NewPluginManager(), store, nil, nil, nil)
	runtime.SetUserMemoryStore(memory)
	var usedModel string
	runtime.SetLLMProviderConfigFactory(func(cfg llm.ProviderConfig) (LLMProvider, error) {
		usedModel = cfg.Model
		return provider, nil
	})
	event := MessageEvent{
		Kind:       EventKindGroup,
		GroupID:    "group",
		UserID:     "user",
		MessageID:  "message",
		SenderName: "Alice",
		ToMe:       true,
		RawMessage: "还是说骂笨蛋，然后减几滴",
		Segments:   []MessageSegment{{Type: "text", Data: map[string]string{"text": "还是说骂笨蛋，然后减几滴"}}},
	}
	decision, before, evaluated := runtime.evaluateRelationshipUpdate(context.Background(), event, PlainText(event.Segments), true)
	if !evaluated || decision.effectiveDelta() != 0 || before.Favorability != 17 {
		t.Fatalf("decision=%#v before=%#v evaluated=%v", decision, before, evaluated)
	}
	if usedModel != "routing-model" {
		t.Fatalf("used model = %q", usedModel)
	}
	if !strings.Contains(provider.request.Messages[0].Content, "不得按关键词") || !strings.Contains(provider.request.Messages[1].Content, event.RawMessage) || !strings.Contains(provider.request.Messages[1].Content, `"natural_interaction_gain_enabled":true`) {
		t.Fatalf("evaluation request = %#v", provider.request.Messages)
	}
}

func TestRelationshipEvaluationAllowsNaturalInteractionBeforeThreshold(t *testing.T) {
	provider := &capturingLLMProvider{reply: `{"should_update":true,"delta":1,"confidence":0.96,"reason":"初识阶段的一次真实提问会带来轻微熟悉"}`}
	memory := newMemoryUserMemoryStore()
	memory.profiles["user"] = UserMemoryProfile{UserID: "user", Favorability: 19, MessageCount: 8}
	runtime := NewRuntime(BotConfig{BotAccount: "bot", OwnerID: "owner"}, nilChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
		return provider, nil
	})
	runtime.SetUserMemoryStore(memory)
	event := MessageEvent{
		Kind:       EventKindPrivate,
		UserID:     "user",
		MessageID:  "message",
		SenderName: "Alice",
		RawMessage: "今天有什么适合散步的地方？",
		Segments:   []MessageSegment{{Type: "text", Data: map[string]string{"text": "今天有什么适合散步的地方？"}}},
	}

	decision, before, evaluated := runtime.evaluateRelationshipUpdate(context.Background(), event, PlainText(event.Segments), true)
	if !evaluated || decision.effectiveDelta() != 1 || before.Favorability != 19 {
		t.Fatalf("decision=%#v before=%#v evaluated=%v", decision, before, evaluated)
	}
	if !requestMessagesContain(provider.request.Messages, `"natural_interaction_gain_enabled":true`) || !requestMessagesContain(provider.request.Messages, `"natural_interaction_threshold":20`) {
		t.Fatalf("natural interaction phase missing: %#v", provider.request.Messages)
	}
	if !requestMessagesContain(provider.request.Messages, "默认应 should_update=true、delta=1") || !requestMessagesContain(provider.request.Messages, "不能仅以“普通提问”“功能请求”或“任务指令”为理由判为 0") {
		t.Fatalf("natural interaction rule is not explicit enough: %#v", provider.request.Messages)
	}
}

func TestRuntimeAppliesNaturalInteractionFavorability(t *testing.T) {
	provider := &sequenceLLMProvider{replies: []string{
		"我来帮你整理。",
		`{"should_update":true,"delta":1,"confidence":0.96,"reason":"初识阶段的真实任务互动"}`,
	}}
	memory := newMemoryUserMemoryStore()
	channel := &recordingChannel{}
	runtime := NewRuntime(BotConfig{BotAccount: "bot", OwnerID: "owner", AgentEnabled: true}, channel, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
		return provider, nil
	})
	runtime.SetUserMemoryStore(memory)
	logs := &captureAppLogs{}
	runtime.SetAppLogWriter(logs)
	event := MessageEvent{
		Kind:       EventKindPrivate,
		UserID:     "user",
		MessageID:  "message",
		SenderName: "Alice",
		RawMessage: "帮我整理一下今天的学习计划",
		Segments:   []MessageSegment{{Type: "text", Data: map[string]string{"text": "帮我整理一下今天的学习计划"}}},
	}

	prepared, text, handled, outcome := runtime.prepareMessageEvent(context.Background(), event)
	profile := memory.profiles[event.UserID]
	if !handled || outcome != "replied" || profile.Favorability != 0 || profile.MessageCount != 1 {
		t.Fatalf("handled=%v outcome=%q profile=%#v", handled, outcome, profile)
	}
	if len(provider.requests) != 0 {
		t.Fatalf("relationship evaluation blocked message preparation: %d calls", len(provider.requests))
	}
	if _, err := runtime.replyAndRecord(context.Background(), prepared, text, outcome); err != nil {
		t.Fatal(err)
	}
	waitCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if !runtime.waitForRelationshipEvaluations(waitCtx) {
		t.Fatal("relationship evaluation did not finish")
	}
	profile = memory.profiles[event.UserID]
	if profile.Favorability != 1 || profile.MessageCount != 1 {
		t.Fatalf("profile after async evaluation = %#v", profile)
	}
	changes := memory.favorabilityChanges[event.UserID]
	if len(changes) != 1 || changes[0].Delta != 1 || changes[0].Source != "interaction" || changes[0].Reason != "初识阶段的真实任务互动" || changes[0].MessageID != "message" {
		t.Fatalf("favorability changes = %#v", changes)
	}
	if len(provider.requests) != 2 {
		t.Fatalf("provider calls = %d, want one reply and one relationship evaluation", len(provider.requests))
	}
	var relationshipLog *applog.Entry
	for index := range logs.entries {
		if logs.entries[index].Action == "diana.relationship_evaluation" {
			relationshipLog = &logs.entries[index]
			break
		}
	}
	if relationshipLog == nil || relationshipLog.Metadata["delta"] != 1 {
		t.Fatalf("logs = %#v", logs.entries)
	}
}

func TestRelationshipEvaluationDisablesNaturalInteractionAtThreshold(t *testing.T) {
	provider := &capturingLLMProvider{reply: `{"should_update":false,"delta":0,"confidence":0.98,"reason":"已达到自然熟悉阈值，普通提问不再加分"}`}
	memory := newMemoryUserMemoryStore()
	memory.profiles["user"] = UserMemoryProfile{UserID: "user", Favorability: naturalInteractionFavorabilityThreshold, MessageCount: 10}
	runtime := NewRuntime(BotConfig{BotAccount: "bot", OwnerID: "owner"}, nilChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
		return provider, nil
	})
	runtime.SetUserMemoryStore(memory)
	event := MessageEvent{
		Kind:       EventKindPrivate,
		UserID:     "user",
		MessageID:  "message",
		SenderName: "Alice",
		RawMessage: "今天有什么适合散步的地方？",
		Segments:   []MessageSegment{{Type: "text", Data: map[string]string{"text": "今天有什么适合散步的地方？"}}},
	}

	decision, _, evaluated := runtime.evaluateRelationshipUpdate(context.Background(), event, PlainText(event.Segments), true)
	if !evaluated || decision.effectiveDelta() != 0 {
		t.Fatalf("decision=%#v evaluated=%v", decision, evaluated)
	}
	if !requestMessagesContain(provider.request.Messages, `"natural_interaction_gain_enabled":false`) {
		t.Fatalf("natural interaction phase should be disabled: %#v", provider.request.Messages)
	}
}

func TestRelationshipQuestionUsesNormalLLMReply(t *testing.T) {
	provider := &sequenceLLMProvider{replies: []string{
		`{"action":"none","prompt":""}`,
		"按我们最近的相处来看，现在是朋友；你离下一阶段还差一点稳定互动。",
	}}
	channel := &recordingChannel{}
	runtime := NewRuntime(BotConfig{AgentEnabled: false, OwnerID: "owner"}, channel, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
		return provider, nil
	})
	memory := newMemoryUserMemoryStore()
	memory.profiles["user"] = UserMemoryProfile{UserID: "user", DisplayName: "Alice", Favorability: 60, MessageCount: 30}
	runtime.SetUserMemoryStore(memory)
	event := MessageEvent{
		Kind:       EventKindPrivate,
		UserID:     "user",
		MessageID:  "question",
		SenderName: "Alice",
		RawMessage: "我和最高关系还有哪些差距？",
		Segments:   []MessageSegment{{Type: "text", Data: map[string]string{"text": "我和最高关系还有哪些差距？"}}},
	}
	reply, err := runtime.replyTo(context.Background(), event, PlainText(event.Segments))
	if err != nil {
		t.Fatal(err)
	}
	if len(provider.requests) != 2 || reply != "按我们最近的相处来看，现在是朋友；你离下一阶段还差一点稳定互动" {
		t.Fatalf("reply=%q requests=%#v", reply, provider.requests)
	}
	if !requestMessagesContain(provider.requests[1].Messages, "好感度：60") {
		t.Fatalf("relationship context missing: %#v", provider.requests[1].Messages)
	}
}

func requestMessagesContain(messages []llm.Message, want string) bool {
	for _, message := range messages {
		if strings.Contains(message.Content, want) {
			return true
		}
	}
	return false
}

func TestRelationshipEvaluationParsesPortraitObservations(t *testing.T) {
	raw := `{"should_update":false,"delta":0,"confidence":0.95,"reason":"闲聊","portrait":[
	  {"field":"residence","value":"住在杭州","evidence":"我在杭州","source":"stated","confidence":0.98},
	  {"field":"occupation","value":"做后端开发","source":"inferred","confidence":0.6},
	  {"field":"habit","value":"习惯早睡","source":"inferred","confidence":0.92}
	]}`
	decision, ok := parseRelationshipEvaluationDecision(raw)
	if !ok {
		t.Fatal("decision with portrait should parse")
	}
	traits := decision.portraitTraits(time.Now())
	if len(traits) != 2 {
		t.Fatalf("traits = %#v, want the low-confidence inference dropped", traits)
	}
	if traits[0].Field != PortraitFieldResidence || traits[0].Label != "居住地点" {
		t.Fatalf("first trait = %#v", traits[0])
	}
	if traits[1].Field != PortraitFieldHabit {
		t.Fatalf("second trait = %#v", traits[1])
	}
}

// 老提示词和不听话的小模型不给 portrait 字段照样算有效：漏一条画像，不该连好感度
// 一起判为无效。
func TestRelationshipEvaluationWithoutPortraitStaysValid(t *testing.T) {
	decision, ok := parseRelationshipEvaluationDecision(`{"should_update":true,"delta":1,"confidence":0.96,"reason":"真实互动"}`)
	if !ok || decision.effectiveDelta() != 1 || len(decision.portraitTraits(time.Now())) != 0 {
		t.Fatalf("decision=%#v ok=%v", decision, ok)
	}
}

// 主人以前根本不进评估，理由是他的分反正固定。现在好感度和画像都照常记录：
// 等级仍由身份决定，分数只是如实反映最近处得怎么样。
func TestRelationshipEvaluationScoresOwnerLikeAnyoneElse(t *testing.T) {
	provider := &capturingLLMProvider{reply: `{"should_update":true,"delta":3,"confidence":0.99,"reason":"主人夸了我","portrait":[{"field":"occupation","value":"做后端开发","evidence":"我平时写 Go","source":"stated","confidence":0.97}]}`}
	memory := newMemoryUserMemoryStore()
	memory.profiles["owner"] = UserMemoryProfile{UserID: "owner", Favorability: 100, MessageCount: 40}
	runtime := NewRuntime(BotConfig{BotAccount: "bot", OwnerID: "owner"}, nilChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
		return provider, nil
	})
	runtime.SetUserMemoryStore(memory)
	event := MessageEvent{
		Kind:       EventKindPrivate,
		UserID:     "owner",
		MessageID:  "message",
		SenderName: "主人",
		RawMessage: "我平时写 Go",
		Segments:   []MessageSegment{{Type: "text", Data: map[string]string{"text": "我平时写 Go"}}},
	}

	decision, _, evaluated := runtime.evaluateRelationshipUpdate(context.Background(), event, PlainText(event.Segments), true)
	if !evaluated {
		t.Fatal("owner messages must be evaluated too")
	}
	if decision.effectiveDelta() != 3 {
		t.Fatalf("owner favorability should move like anyone else: %#v", decision)
	}
	if traits := decision.portraitTraits(time.Now()); len(traits) != 1 || traits[0].Field != PortraitFieldOccupation {
		t.Fatalf("owner portrait = %#v", decision.portraitTraits(time.Now()))
	}
	if requestMessagesContain(provider.request.Messages, "favorability_locked") {
		t.Fatalf("the locked-score payload field should be gone: %#v", provider.request.Messages)
	}
	if !requestMessagesContain(provider.request.Messages, `"field":"residence"`) {
		t.Fatalf("payload did not carry the portrait field table: %#v", provider.request.Messages)
	}
}

// 主人的分能降下来，等级不跟着掉——等级看身份，分数看相处。
func TestRuntimeAppliesOwnerFavorabilityDrop(t *testing.T) {
	memory := newMemoryUserMemoryStore()
	memory.profiles["owner"] = UserMemoryProfile{UserID: "owner", Favorability: 100, MessageCount: 40}
	runtime := NewRuntime(BotConfig{BotAccount: "bot", OwnerID: "owner"}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.SetUserMemoryStore(memory)
	event := MessageEvent{Kind: EventKindPrivate, UserID: "owner", SenderName: "主人"}

	profile, ok := runtime.applyEvaluatedRelationshipUpdate(event, -3, "主人在骂我", nil)
	if !ok || profile.Favorability != 97 {
		t.Fatalf("owner favorability = %#v ok=%v", profile, ok)
	}
	if policy := RelationshipPolicyFor(profile, "owner", "owner"); policy.Tier != RelationshipOwner || policy.Score != 97 {
		t.Fatalf("owner tier must not follow the score: %#v", policy)
	}
}

// 已经记下的画像要喂回给模型，否则同一件事每轮都会被重新上报一遍。
func TestRelationshipEvaluationSendsKnownPortrait(t *testing.T) {
	provider := &capturingLLMProvider{reply: `{"should_update":false,"delta":0,"confidence":0.95,"reason":"闲聊","portrait":[]}`}
	memory := newMemoryUserMemoryStore()
	memory.profiles["user"] = UserMemoryProfile{
		UserID:       "user",
		Favorability: 30,
		MessageCount: 20,
		Portrait: []UserPortraitTrait{
			{Field: PortraitFieldResidence, Label: "居住地点", Value: "住在杭州", Source: PortraitSourceStated},
		},
	}
	runtime := NewRuntime(BotConfig{BotAccount: "bot", OwnerID: "owner"}, nilChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
		return provider, nil
	})
	runtime.SetUserMemoryStore(memory)
	event := MessageEvent{
		Kind:       EventKindPrivate,
		UserID:     "user",
		MessageID:  "message",
		SenderName: "Alice",
		RawMessage: "今天天气不错",
		Segments:   []MessageSegment{{Type: "text", Data: map[string]string{"text": "今天天气不错"}}},
	}
	if _, _, evaluated := runtime.evaluateRelationshipUpdate(context.Background(), event, PlainText(event.Segments), true); !evaluated {
		t.Fatal("evaluation did not run")
	}
	if !requestMessagesContain(provider.request.Messages, `"known_portrait"`) || !requestMessagesContain(provider.request.Messages, "住在杭州") {
		t.Fatalf("known portrait missing from payload: %#v", provider.request.Messages)
	}
}
