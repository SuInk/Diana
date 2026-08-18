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
		ActiveID: "default",
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
	runtime := NewRuntime(BotConfig{BotQQ: "bot"}, nilChannel{}, NewPluginManager(), profiles, nil, nil, nil)
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
	if !strings.Contains(contextText, "依据") || ranked[0].RetrievalReason == "" {
		t.Fatalf("retrieval explanation missing: ranked=%#v context=%s", ranked, contextText)
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

func TestStructuredMemoryRankingUnderstandsTimeIntent(t *testing.T) {
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
	recent := rankStructuredMemories(items, event, "最近讨论的部署方案是什么", now)
	if len(recent) < 2 || recent[0].ID != "recent" || !strings.Contains(recent[0].RetrievalReason, "近期记忆") {
		t.Fatalf("recent ranking = %#v", recent)
	}
	historical := rankStructuredMemories(items, event, "以前讨论过的部署方案是什么", now)
	if len(historical) < 2 || historical[0].ID != "old" || !strings.Contains(historical[0].RetrievalReason, "历史回忆") {
		t.Fatalf("historical ranking = %#v", historical)
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
	runtime := NewRuntime(BotConfig{BotQQ: "bot"}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
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
		items = append(items, item)
	}
	return items, nil
}
