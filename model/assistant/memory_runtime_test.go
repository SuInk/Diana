// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SuInk/diana/model/llm"
)

func TestMemoryGateUsesMemoryProfileAndExistingKeys(t *testing.T) {
	profiles := &stubLLMProfileStore{set: llm.ProfileSet{
		Profiles: []llm.Profile{
			{ID: "default", Group: "default", Config: llm.ProviderConfig{Provider: llm.ProviderOpenAICompatible, APIKey: "default", Model: "default-model"}},
			{ID: "memory", Group: "memory", Config: llm.ProviderConfig{Provider: llm.ProviderOpenAICompatible, APIKey: "memory", Model: "memory-model"}},
		},
	}}
	memory := &testStructuredMemoryStore{items: []StructuredMemoryItem{{
		ID:            "old",
		ScopeKey:      "group:123",
		SubjectUserID: "user",
		Key:           "preference.food.spicy",
		Kind:          MemoryKindPreference,
		Topic:         "饮食偏好",
		Content:       "Alice喜欢辣味食物",
		Confidence:    0.98,
		Importance:    0.7,
		Visibility:    MemoryVisibilitySession,
		Version:       1,
	}}}
	usedModel := ""
	provider := &capturingLLMProvider{reply: `{"memories":[{"action":"upsert","key":"preference.food.spicy","kind":"preference","topic":"饮食偏好","entity":"辣味食物","content":"Alice现在不喜欢辣味食物","evidence":"我现在不吃辣了","source_type":"explicit","confidence":0.99,"importance":0.75,"visibility":"session","sensitive":false,"retention_days":0}]}`}
	runtime := NewRuntime(BotConfig{BotAccount: "bot"}, nilChannel{}, NewPluginManager(), profiles, nil, nil, nil)
	runtime.SetStructuredMemoryStore(memory)
	runtime.SetLLMProviderConfigFactory(func(cfg llm.ProviderConfig) (LLMProvider, error) {
		usedModel = cfg.Model
		return provider, nil
	})
	event := MessageEvent{
		Kind:       EventKindGroup,
		GroupID:    "123",
		UserID:     "user",
		SenderName: "Alice",
		MessageID:  "m2",
		Time:       200,
		Segments:   []MessageSegment{{Type: "text", Data: map[string]string{"text": "我现在不吃辣了"}}},
	}
	err := runtime.processEventMemoryJob(context.Background(), memory, MemoryJobPayload{
		Kind: MemoryJobEvent, Session: "group:123", Event: event,
	})
	if err != nil {
		t.Fatal(err)
	}
	if usedModel != "memory-model" {
		t.Fatalf("used model = %q, want memory-model", usedModel)
	}
	if len(memory.applied) != 1 || len(memory.applied[0].Candidates) != 1 || memory.applied[0].Candidates[0].Key != "preference.food.spicy" {
		t.Fatalf("applied = %#v", memory.applied)
	}
	prompt := provider.request.Messages[len(provider.request.Messages)-1].Content
	if !strings.Contains(prompt, "preference.food.spicy") || !strings.Contains(prompt, "我现在不吃辣了") || !strings.Contains(provider.request.Messages[0].Content, "当前任务里的格式要求") {
		t.Fatalf("memory gate prompt missing context: %s", prompt)
	}
}

func TestMemoryGateRecentEventsDoesNotRunCrossGroupSearch(t *testing.T) {
	store := &memoryGateHistoryStore{memoryMessageHistoryStore: newMemoryMessageHistoryStore()}
	store.events["group:123"] = []MessageEvent{
		{Kind: EventKindGroup, GroupID: "123", UserID: "other", MessageID: "m1", Time: 100, RawMessage: "当前群最近消息"},
	}
	runtime := NewRuntime(BotConfig{CrossGroupMemoryEnabled: boolPointer(true)}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.SetMessageHistoryStore(store)

	items := runtime.memoryGateRecentEvents(MessageEvent{
		Kind: EventKindGroup, GroupID: "123", UserID: "user", MessageID: "m2", Time: 200, RawMessage: "需要记忆的消息",
	})

	if store.searches != 0 {
		t.Fatalf("cross-group searches = %d, want 0", store.searches)
	}
	if len(items) != 1 || items[0].Text != "当前群最近消息" {
		t.Fatalf("recent events = %#v", items)
	}
}

func TestMemoryJobAttemptsExhausted(t *testing.T) {
	if memoryJobAttemptsExhausted(memoryMaxAttempts) {
		t.Fatalf("attempt %d should still run", memoryMaxAttempts)
	}
	if !memoryJobAttemptsExhausted(memoryMaxAttempts + 1) {
		t.Fatalf("attempt %d should be abandoned", memoryMaxAttempts+1)
	}
}

func TestStructuredMemoryRankingExcludesUnrelatedFacts(t *testing.T) {
	now := time.Now()
	items := []StructuredMemoryItem{
		{
			ID: "cat", SubjectUserID: "user", SubjectName: "Alice", Key: "profile.pet.cat.name",
			Kind: MemoryKindFact, Topic: "宠物猫", Entity: "小白", Content: "Alice的猫叫小白",
			SourceType: MemorySourceExplicit, SourceSession: "group:123", Confidence: 0.98, Importance: 0.75, LastVerifiedAt: now,
		},
		{
			ID: "game", SubjectUserID: "user", SubjectName: "Alice", Key: "preference.game.maimai",
			Kind: MemoryKindPreference, Topic: "街机游戏", Entity: "舞萌DX", Content: "Alice喜欢玩舞萌DX",
			SourceType: MemorySourceExplicit, SourceSession: "group:123", Confidence: 0.98, Importance: 0.75, LastVerifiedAt: now,
		},
		{
			ID: "style", SubjectUserID: "user", SubjectName: "Alice", Key: "instruction.reply.concise",
			Kind: MemoryKindInstruction, Topic: "回复风格", Content: "回复Alice时保持简洁",
			SourceType: MemorySourceExplicit, SourceSession: "group:123", Confidence: 0.97, Importance: 0.65, LastVerifiedAt: now,
		},
	}
	ranked := rankStructuredMemories(items, MessageEvent{Kind: EventKindGroup, GroupID: "123", UserID: "user"}, "我家那只猫叫什么来着", now)
	ids := map[string]bool{}
	for _, item := range ranked {
		ids[item.ID] = true
	}
	if !ids["cat"] || !ids["style"] {
		t.Fatalf("expected relevant fact and standing instruction, got %#v", ranked)
	}
	if ids["game"] {
		t.Fatalf("unrelated game preference leaked into cat query: %#v", ranked)
	}
	contextText := formatStructuredMemoryContext(UserMemoryProfile{
		UserID: "user", DisplayName: "Alice", Favorability: 20, MessageCount: 12,
	}, RelationshipPolicyFor(UserMemoryProfile{Favorability: 20, MessageCount: 12}, "owner", "user"), ranked)
	if !strings.Contains(contextText, "稳定事实") || !strings.Contains(contextText, "Alice的猫叫小白") || strings.Contains(contextText, "舞萌DX") {
		t.Fatalf("compiled memory context = %s", contextText)
	}
	// 检索依据只留在排序结果里给排障看，不再写进提示词：模型用不上，每条白付十几个 token。
	if ranked[0].RetrievalReason == "" || strings.Contains(contextText, "依据 ") || strings.Contains(contextText, "置信 ") {
		t.Fatalf("retrieval explanation misplaced: ranked=%#v context=%s", ranked, contextText)
	}
}

func TestStructuredMemoryRankingDoesNotInjectUnrelatedSafetyEpisode(t *testing.T) {
	now := time.Now()
	items := []StructuredMemoryItem{
		{
			ID: "risk", SubjectUserID: "user", Kind: MemoryKindEpisode,
			Topic: "安全风险", Content: "用户过去表达过自伤想法", Confidence: 1, Importance: 1,
			SourceSession: "group:123", LastVerifiedAt: now.Add(-24 * time.Hour),
		},
		{
			ID: "game", SubjectUserID: "user", Kind: MemoryKindFact,
			Topic: "游戏进度", Content: "用户正在玩Melvor Idle", Confidence: .98, Importance: .7,
			SourceSession: "group:123", LastVerifiedAt: now,
		},
	}
	event := MessageEvent{Kind: EventKindGroup, GroupID: "123", UserID: "user"}
	ranked := rankStructuredMemories(items, event, "Melvor Idle 的深渊入口在哪", now)
	if len(ranked) != 1 || ranked[0].ID != "game" {
		t.Fatalf("game query received unrelated safety context: %#v", ranked)
	}
	ranked = rankStructuredMemories(items, event, "我又想自杀了", now)
	foundRisk := false
	for _, item := range ranked {
		foundRisk = foundRisk || item.ID == "risk"
	}
	if !foundRisk {
		t.Fatalf("safety query lost relevant safety episode: %#v", ranked)
	}
}

func TestStructuredMemoryRankingDoesNotSwitchOnTimeWording(t *testing.T) {
	now := time.Now()
	items := []StructuredMemoryItem{
		{
			ID: "old", SubjectUserID: "user", Key: "episode.deploy.old", Kind: MemoryKindEpisode,
			Topic: "部署方案", Content: "讨论过使用容器部署", Confidence: 0.96, Importance: 0.8,
			SourceSession: "group:123", LastVerifiedAt: now.AddDate(-1, 0, 0),
		},
		{
			ID: "recent", SubjectUserID: "user", Key: "episode.deploy.recent", Kind: MemoryKindEpisode,
			Topic: "部署方案", Content: "讨论过使用二进制部署", Confidence: 0.96, Importance: 0.7,
			SourceSession: "group:123", LastVerifiedAt: now.Add(-24 * time.Hour),
		},
	}
	event := MessageEvent{Kind: EventKindGroup, GroupID: "123", UserID: "user"}

	// 排序过去会先用词表猜「用户问的是最近还是很久以前」，再切换两套打分。那是拿
	// 关键词判断语义意图，本项目不允许；现在两种问法必须得到同一个结果，排序只依据
	// 算得出来的信号：词面相关度、重要度、置信度和龄期。
	recentWording := rankStructuredMemories(items, event, "最近讨论的部署方案是什么", now)
	historicalWording := rankStructuredMemories(items, event, "以前讨论过的部署方案是什么", now)
	if len(recentWording) < 2 || len(historicalWording) < 2 {
		t.Fatalf("both phrasings should surface both episodes: recent=%#v historical=%#v", recentWording, historicalWording)
	}
	for index := range recentWording {
		if recentWording[index].ID != historicalWording[index].ID {
			t.Fatalf("ranking diverged on time wording: recent=%v historical=%v",
				recentWording[index].ID, historicalWording[index].ID)
		}
	}
	// 龄期仍然参与打分，只是不再由措辞切换曲线。
	if recentWording[0].ID != "recent" {
		t.Fatalf("recently verified episode did not rank first: %#v", recentWording)
	}
}

func TestStructuredMemoryRankingDiversifiesNearDuplicates(t *testing.T) {
	now := time.Now()
	items := []StructuredMemoryItem{
		{ID: "cat-1", Key: "pet.cat.food.1", Kind: MemoryKindFact, Topic: "猫粮", Content: "小白喜欢鸡肉猫粮", Confidence: 0.98, Importance: 0.9, LastVerifiedAt: now},
		{ID: "cat-2", Key: "pet.cat.food.2", Kind: MemoryKindFact, Topic: "猫粮", Content: "小白偏爱鸡肉口味猫粮", Confidence: 0.98, Importance: 0.89, LastVerifiedAt: now},
		{ID: "dog", Key: "pet.dog.food", Kind: MemoryKindFact, Topic: "狗粮", Content: "旺财喜欢牛肉狗粮", Confidence: 0.98, Importance: 0.86, LastVerifiedAt: now},
	}
	ranked := rankStructuredMemories(items, MessageEvent{}, "猫粮狗粮这些宠物食物偏好", now)
	if len(ranked) < 3 || (ranked[0].ID != "dog" && ranked[1].ID != "dog") ||
		(strings.HasPrefix(ranked[0].ID, "cat-") && strings.HasPrefix(ranked[1].ID, "cat-")) {
		t.Fatalf("diversified ranking = %#v", ranked)
	}
}

func TestMemoryEnqueueSkipsResolverAndMediaOnlyMessages(t *testing.T) {
	memory := &testStructuredMemoryStore{}
	runtime := NewRuntime(BotConfig{BotAccount: "bot"}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.SetStructuredMemoryStore(memory)
	resolver := MessageEvent{
		Kind: EventKindGroup, GroupID: "123", UserID: "user", MessageID: "link",
		Segments: []MessageSegment{{Type: "text", Data: map[string]string{"text": "https://www.bilibili.com/video/BV1abc"}}},
	}
	runtime.enqueueEventMemory(resolver, memoryEventText(resolver))
	image := MessageEvent{
		Kind: EventKindGroup, GroupID: "123", UserID: "user", MessageID: "image",
		Segments: []MessageSegment{{Type: "image", Data: map[string]string{"url": "https://example.test/image.png"}}},
	}
	runtime.enqueueEventMemory(image, memoryEventText(image))
	normal := MessageEvent{
		Kind: EventKindGroup, GroupID: "123", UserID: "user", MessageID: "normal",
		Segments: []MessageSegment{{Type: "text", Data: map[string]string{"text": "我养的猫叫小白"}}},
	}
	runtime.enqueueEventMemory(normal, memoryEventText(normal))
	if len(memory.enqueued) != 1 || memory.enqueued[0].Event.MessageID != "normal" {
		t.Fatalf("enqueued = %#v", memory.enqueued)
	}
}

func TestParseMemoryCandidatesRejectsNonJSON(t *testing.T) {
	if _, err := parseMemoryCandidates("不是 JSON"); err == nil {
		t.Fatal("expected invalid response error")
	}
	items, err := parseMemoryCandidates("```json\n{\"memories\":[]}\n```")
	if err != nil || len(items) != 0 {
		t.Fatalf("empty candidate response items=%#v err=%v", items, err)
	}
}

func TestContextCompressionEnqueuesStructuredSummary(t *testing.T) {
	memory := &testStructuredMemoryStore{}
	runtime := NewRuntime(BotConfig{RecentContextLimit: 2, ContextSummaryThreshold: 3}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.SetStructuredMemoryStore(memory)
	for index, text := range []string{"第一条", "第二条", "第三条", "第四条"} {
		runtime.remember(MessageEvent{
			Kind: EventKindGroup, GroupID: "123", UserID: "user", MessageID: text, Time: int64(100 + index),
			Segments: []MessageSegment{{Type: "text", Data: map[string]string{"text": text}}},
		})
	}
	if len(memory.enqueued) != 1 {
		t.Fatalf("summary jobs = %#v", memory.enqueued)
	}
	job := memory.enqueued[0]
	if job.Kind != MemoryJobSummary || job.Session != "group:123" || len(job.Events) != 2 || job.Events[0].MessageID != "第一条" || job.Events[1].MessageID != "第二条" {
		t.Fatalf("summary job = %#v", job)
	}
	if runtime.contextSummary(MessageEvent{Kind: EventKindGroup, GroupID: "123"}) != "" {
		t.Fatal("raw concatenated summary should be hidden when structured memory is enabled")
	}
}

func TestLongTermMemoryCanBeDisabled(t *testing.T) {
	disabled := false
	memory := &testStructuredMemoryStore{}
	runtime := NewRuntime(BotConfig{
		RecentContextLimit: 2, ContextSummaryThreshold: 3, LongTermMemoryEnabled: &disabled,
	}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.SetStructuredMemoryStore(memory)
	for index := 0; index < 4; index++ {
		event := MessageEvent{
			Kind: EventKindGroup, GroupID: "123", UserID: "user", MessageID: fmt.Sprintf("m%d", index),
			Segments: []MessageSegment{{Type: "text", Data: map[string]string{"text": "需要记住的稳定偏好"}}},
		}
		runtime.remember(event)
		runtime.enqueueEventMemory(event, memoryEventText(event))
	}
	if len(memory.enqueued) != 0 {
		t.Fatalf("disabled memory enqueued jobs = %#v", memory.enqueued)
	}
}

func TestSummaryMemoryJobStoresTopicSummary(t *testing.T) {
	memory := &testStructuredMemoryStore{}
	provider := &capturingLLMProvider{reply: `{"memories":[{"action":"upsert","key":"summary.2026-07-15.memory-design","kind":"summary","topic":"记忆系统设计","entity":"Diana","content":"群友讨论将记忆拆分为事实、情景、任务与摘要，并按相关性召回。","evidence":"较早会话整合","source_type":"summary","confidence":0.97,"importance":0.82,"visibility":"session","sensitive":false,"retention_days":365}]}`}
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
		return provider, nil
	})
	events := []MessageEvent{
		{Kind: EventKindGroup, GroupID: "123", UserID: "a", SenderName: "Alice", MessageID: "m1", Time: 100, Segments: []MessageSegment{{Type: "text", Data: map[string]string{"text": "记忆要分层"}}}},
		{Kind: EventKindGroup, GroupID: "123", UserID: "b", SenderName: "Bob", MessageID: "m2", Time: 200, Segments: []MessageSegment{{Type: "text", Data: map[string]string{"text": "还要按相关性召回"}}}},
	}
	err := runtime.processSummaryMemoryJob(context.Background(), memory, MemoryJob{
		ID: "summary-job", Payload: MemoryJobPayload{Kind: MemoryJobSummary, Session: "group:123", Events: events},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(memory.applied) != 1 || len(memory.applied[0].Candidates) != 1 {
		t.Fatalf("applied summaries = %#v", memory.applied)
	}
	request := memory.applied[0]
	candidate := request.Candidates[0]
	if request.SubjectUserID != "" || request.SourceMessageID != "summary:summary-job" || candidate.Kind != MemoryKindSummary || candidate.SourceType != MemorySourceSummary || candidate.Visibility != MemoryVisibilitySession {
		t.Fatalf("summary request = %#v", request)
	}
}

func TestSelectMemorySummaryRollupBuildsMonthThenYearLevels(t *testing.T) {
	base := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	daily := make([]StructuredMemoryItem, 0, memorySummaryRollupSize)
	for index := 0; index < memorySummaryRollupSize; index++ {
		daily = append(daily, StructuredMemoryItem{
			Key: fmt.Sprintf("summary.2025-01-%02d.topic", index+1), Kind: MemoryKindSummary,
			Content: "主题摘要", SourceEventTime: base.AddDate(0, 0, index),
		})
	}
	month := selectMemorySummaryRollup(daily)
	if month == nil || month.Level != "month" || len(month.Items) != memorySummaryRollupSize || !strings.HasPrefix(month.TargetKey, "summary.rollup.month.") {
		t.Fatalf("month rollup = %#v", month)
	}

	monthly := make([]StructuredMemoryItem, 0, memorySummaryRollupSize)
	for index := 0; index < memorySummaryRollupSize; index++ {
		monthly = append(monthly, StructuredMemoryItem{
			Key:  fmt.Sprintf("summary.rollup.month.2025-%02d-01.2025-%02d-28", index+1, index+1),
			Kind: MemoryKindSummary, Content: "月摘要", SourceEventTime: base.AddDate(0, index, 0),
		})
	}
	year := selectMemorySummaryRollup(monthly)
	if year == nil || year.Level != "year" || !strings.HasPrefix(year.TargetKey, "summary.rollup.year.") {
		t.Fatalf("year rollup = %#v", year)
	}
}

func TestSummaryMemoryJobRetiresSourcesOnlyAfterRollupIsWritten(t *testing.T) {
	base := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	memory := &testStructuredMemoryStore{}
	for index := 0; index < memorySummaryRollupSize; index++ {
		memory.items = append(memory.items, StructuredMemoryItem{
			Key: fmt.Sprintf("summary.2025.01.%02d.topic", index+1), Kind: MemoryKindSummary,
			Content: "主题摘要", Importance: 0.8, Status: MemoryStatusActive,
			SourceEventTime: base.AddDate(0, 0, index),
		})
	}
	rollup := selectMemorySummaryRollup(memory.items)
	provider := &capturingLLMProvider{reply: fmt.Sprintf(
		`{"memories":[{"action":"upsert","key":%q,"kind":"summary","topic":"历史滚动摘要","content":"十二条主题摘要已合并。","source_type":"summary","confidence":0.98,"importance":0.9,"visibility":"session","sensitive":false,"retention_days":730}]}`,
		rollup.TargetKey,
	)}
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
		return provider, nil
	})
	event := MessageEvent{
		Kind: EventKindGroup, GroupID: "123", UserID: "a", MessageID: "new", Time: base.AddDate(0, 1, 0).Unix(),
		Segments: []MessageSegment{{Type: "text", Data: map[string]string{"text": "新的长期主题"}}},
	}
	err := runtime.processSummaryMemoryJob(context.Background(), memory, MemoryJob{
		ID: "rollup-job", Payload: MemoryJobPayload{Kind: MemoryJobSummary, Session: "group:123", Events: []MessageEvent{event}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(memory.applied) != 3 {
		t.Fatalf("apply requests = %#v", memory.applied)
	}
	forgotten := 0
	for _, request := range memory.applied[1:] {
		for _, candidate := range request.Candidates {
			if candidate.Action != MemoryActionForget {
				t.Fatalf("retirement candidate = %#v", candidate)
			}
			forgotten++
		}
	}
	if forgotten != memorySummaryRollupSize {
		t.Fatalf("forgotten summaries = %d", forgotten)
	}
}

type testStructuredMemoryStore struct {
	mu       sync.Mutex
	items    []StructuredMemoryItem
	applied  []MemoryWriteRequest
	enqueued []MemoryJobPayload
	queries  []StructuredMemoryQuery
}

type memoryGateHistoryStore struct {
	*memoryMessageHistoryStore
	searches int
}

func (s *memoryGateHistoryStore) SearchMessageEvents(context.Context, MessageHistorySearchQuery) ([]MessageEvent, int, error) {
	s.searches++
	return nil, 0, nil
}

func (s *testStructuredMemoryStore) EnqueueMemoryJob(_ context.Context, payload MemoryJobPayload) (string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.enqueued = append(s.enqueued, payload)
	return "job", true, nil
}

func (s *testStructuredMemoryStore) ClaimNextMemoryJob(context.Context, string, time.Time) (MemoryJob, bool, error) {
	return MemoryJob{}, false, nil
}

func (s *testStructuredMemoryStore) CompleteMemoryJob(context.Context, string, string) error {
	return nil
}

func (s *testStructuredMemoryStore) RetryMemoryJob(context.Context, string, string, time.Time, string) error {
	return nil
}

func (s *testStructuredMemoryStore) ReleaseMemoryJobLeases(context.Context, string) error {
	return nil
}

func (s *testStructuredMemoryStore) ApplyMemoryCandidates(_ context.Context, request MemoryWriteRequest) ([]StructuredMemoryItem, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	request.Candidates = append([]MemoryCandidate(nil), request.Candidates...)
	s.applied = append(s.applied, request)
	written := make([]StructuredMemoryItem, 0, len(request.Candidates))
	for _, candidate := range request.Candidates {
		if candidate.Action == MemoryActionUpsert {
			written = append(written, StructuredMemoryItem{Key: candidate.Key, Kind: candidate.Kind, Status: MemoryStatusActive})
		}
	}
	return written, nil
}

func (s *testStructuredMemoryStore) ListStructuredMemories(_ context.Context, query StructuredMemoryQuery) ([]StructuredMemoryItem, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.queries = append(s.queries, query)
	items := make([]StructuredMemoryItem, 0, len(s.items))
	for _, item := range s.items {
		if len(query.Kinds) > 0 {
			matched := false
			for _, kind := range query.Kinds {
				if item.Kind == kind {
					matched = true
					break
				}
			}
			if !matched {
				continue
			}
		}
		excluded := false
		for _, kind := range query.ExcludeKinds {
			if item.Kind == kind {
				excluded = true
				break
			}
		}
		if excluded {
			continue
		}
		if !structuredMemoryFakeInScope(item, query) {
			continue
		}
		items = append(items, item)
	}
	return items, nil
}

func TestMemoryContextKeepsUserScopedMemoriesWhenCrossGroupIsOff(t *testing.T) {
	memory := &testStructuredMemoryStore{items: []StructuredMemoryItem{{
		Key: "instruction.persona.catgirl", Kind: MemoryKindInstruction,
		Topic: "猫娘设定", Entity: "Diana", Content: "猫娘模式下句尾说老吴，炸毛时说哈",
		Visibility: MemoryVisibilityUser, Confidence: 0.98, Importance: 0.8,
	}}}
	runtime := NewRuntime(BotConfig{BotAccount: "42", CrossGroupMemoryEnabled: boolPointer(false)}.WithDefaults(),
		nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.SetStructuredMemoryStore(memory)
	event := MessageEvent{Kind: EventKindGroup, GroupID: "123", UserID: "9", MessageID: "m-1", RawMessage: "继续扮猫娘"}

	context := runtime.memoryContext(context.Background(), event, event.RawMessage)
	if !strings.Contains(context, "老吴") {
		t.Fatalf("user-scoped memory was dropped from the reply prompt: %q", context)
	}
	if len(memory.queries) == 0 {
		t.Fatal("no memory query was issued")
	}
	// 跨群开关只该管别的群的会话记忆，不该把当前发言者自己的跨会话记忆一起关掉。
	query := memory.queries[len(memory.queries)-1]
	if query.CurrentSessionOnly {
		t.Fatalf("reply prompt query still restricts to the current session: %#v", query)
	}
	if query.CrossGroup {
		t.Fatalf("cross-group retrieval should stay off: %#v", query)
	}
}

func TestMemoryGateFetchesRelevantMemoriesBeforeImportantOnes(t *testing.T) {
	profiles := &stubLLMProfileStore{set: llm.ProfileSet{
		Profiles: []llm.Profile{{ID: "default", Group: "default", Config: llm.ProviderConfig{Provider: llm.ProviderOpenAICompatible, APIKey: "k", Model: "m"}}},
	}}
	memory := &testStructuredMemoryStore{}
	provider := &capturingLLMProvider{reply: `{"memories":[]}`}
	runtime := NewRuntime(BotConfig{BotAccount: "bot"}, nilChannel{}, NewPluginManager(), profiles, nil, nil, nil)
	runtime.SetStructuredMemoryStore(memory)
	runtime.SetLLMProviderConfigFactory(func(llm.ProviderConfig) (LLMProvider, error) { return provider, nil })

	event := MessageEvent{
		Kind: EventKindGroup, GroupID: "123", UserID: "user", SenderName: "Alice", MessageID: "m9", Time: 300,
		Segments: []MessageSegment{{Type: "text", Data: map[string]string{"text": "我现在改吃甜的了，不爱麻辣烫了"}}},
	}
	if err := runtime.processEventMemoryJob(context.Background(), memory, MemoryJobPayload{Kind: MemoryJobEvent, Session: "group:123", Event: event}); err != nil {
		t.Fatal(err)
	}

	// 门控必须先按当前消息的相关性取一批（带检索词），再用重要度批兜底：只按
	// 重要度取前 40 条时，与本次改口相关的旧 key 会掉出窗口，模型无从复用。
	memory.mu.Lock()
	queries := append([]StructuredMemoryQuery(nil), memory.queries...)
	memory.mu.Unlock()
	if len(queries) < 2 {
		t.Fatalf("expected a relevance query plus an importance fallback, got %d", len(queries))
	}
	if len(queries[0].SearchTerms) == 0 {
		t.Fatalf("first query must carry search terms from the message: %#v", queries[0])
	}
	found := false
	for _, term := range queries[0].SearchTerms {
		if strings.Contains(term, "麻辣烫") || strings.Contains(term, "麻辣") {
			found = true
		}
	}
	if !found {
		t.Fatalf("search terms do not reflect the message: %#v", queries[0].SearchTerms)
	}
	if len(queries[1].SearchTerms) != 0 {
		t.Fatalf("fallback query must stay importance-ordered: %#v", queries[1])
	}
}

func TestMergeStructuredMemoriesDedupesAndKeepsRelevantFirst(t *testing.T) {
	item := func(id string) StructuredMemoryItem { return StructuredMemoryItem{ID: id} }
	merged := mergeStructuredMemories(
		[]StructuredMemoryItem{item("a"), item("b")},
		[]StructuredMemoryItem{item("b"), item("c"), item("d")},
		3,
	)
	if len(merged) != 3 || merged[0].ID != "a" || merged[1].ID != "b" || merged[2].ID != "c" {
		t.Fatalf("merged = %#v", merged)
	}
}

// structuredMemoryFakeInScope 复刻真实存储层的 scope_key 取值范围。假实现以前只
// 按 kind 过滤，会话隔离完全靠调用方自己比较 key 兜着——这正是「便签 key 落库后
// 被归一化、读取端再也对不上」能一路活到线上的原因：单测两侧用的都是没归一化的
// 同一个字符串，永远相等。
//
// 没有设 ScopeKey 的条目视为不限会话，保持既有用例不用逐个补字段。
func structuredMemoryFakeInScope(item StructuredMemoryItem, query StructuredMemoryQuery) bool {
	scope := strings.TrimSpace(item.ScopeKey)
	if scope == "" || scope == strings.TrimSpace(query.Session) {
		return true
	}
	if query.CurrentSessionOnly {
		return false
	}
	// 默认取值范围额外捎上「本人的 visibility=user 记忆」。
	return item.Visibility == MemoryVisibilityUser &&
		strings.TrimSpace(item.SubjectUserID) == strings.TrimSpace(query.SubjectUserID)
}

// 摘要任务把「当前便签」一起喂给模型，让它在原有状态上续写而不是重开一条。
// 读取端以前按未归一化的 key 精确比较，currentThread 恒为空，于是每轮都从零
// 重写——库里便签一直存在，却永远只覆盖最近这一批事件。
func TestSummaryMemoryJobFeedsExistingThreadToModel(t *testing.T) {
	memory := &testStructuredMemoryStore{items: []StructuredMemoryItem{{
		ID:       "thread-1",
		ScopeKey: "group:123",
		// 落库形态：normalizeMemoryKey 已经把冒号吃掉了。
		Key:  "thread.group123",
		Kind: MemoryKindThread, Topic: "会话状态",
		Content: "上一轮的进行状态：正在挑选镜像线路。",
	}}}
	provider := &capturingLLMProvider{reply: `{"memories":[{"action":"upsert","key":"thread.group:123","kind":"thread","topic":"会话状态","entity":"Diana","content":"接着聊镜像线路，现在转到记忆分层。","evidence":"较早会话整合","source_type":"summary","confidence":0.97,"importance":0.82,"visibility":"session","sensitive":false,"retention_days":30}]}`}
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
		return provider, nil
	})
	events := []MessageEvent{
		{Kind: EventKindGroup, GroupID: "123", UserID: "a", SenderName: "Alice", MessageID: "m1", Time: 100, Segments: []MessageSegment{{Type: "text", Data: map[string]string{"text": "记忆要分层"}}}},
	}
	if err := runtime.processSummaryMemoryJob(context.Background(), memory, MemoryJob{
		ID: "summary-job", Payload: MemoryJobPayload{Kind: MemoryJobSummary, Session: "group:123", Events: events},
	}); err != nil {
		t.Fatal(err)
	}
	prompt := provider.request.Messages[len(provider.request.Messages)-1].Content
	if !strings.Contains(prompt, "正在挑选镜像线路") {
		t.Fatalf("已有便签没有进入摘要提示词: %s", prompt)
	}
}
