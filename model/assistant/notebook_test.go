// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/SuInk/diana/model/agent"
)

// memoryNotebookStore 是笔记本的内存实现，只服务测试。匹配逻辑照着 SQLite 那边写：
// 条目和别名都参与「这段话里出现了哪个词」的子串匹配。
type memoryNotebookStore struct {
	entries map[string]NotebookEntry
	touched map[string]int
	nextID  int
}

func newMemoryNotebookStore() *memoryNotebookStore {
	return &memoryNotebookStore{entries: map[string]NotebookEntry{}, touched: map[string]int{}}
}

func notebookStoreKey(scope, term string) string {
	return scope + "|" + NormalizeNotebookTitle(term)
}

func (s *memoryNotebookStore) UpsertNotebookEntry(_ context.Context, request NotebookUpsertRequest) (NotebookEntry, bool, error) {
	key := notebookStoreKey(request.ScopeKey, request.Term)
	entry, found := s.entries[key]
	created := !found
	if created {
		s.nextID++
		entry = NotebookEntry{
			ID:           "notebook-" + itoa(s.nextID),
			ScopeKey:     request.ScopeKey,
			AuthorUserID: request.EditorUserID,
			AuthorName:   request.EditorName,
			CreatedAt:    request.Now,
		}
	}
	entry.Term = request.Term
	entry.Aliases = NormalizeNotebookAliases(request.Term, request.Aliases)
	entry.Meaning = request.Meaning
	entry.Example = request.Example
	entry.Note = request.Note
	entry.EditorUserID = request.EditorUserID
	entry.EditorName = request.EditorName
	entry.Version++
	entry.Status = NotebookStatusActive
	entry.UpdatedAt = request.Now
	entry.Revisions = append([]NotebookRevision{{
		Version: entry.Version, Meaning: request.Meaning, Note: request.Note,
		EditorUserID: request.EditorUserID, RecordedAt: request.Now,
	}}, entry.Revisions...)
	s.entries[key] = entry
	return entry, created, nil
}

func (s *memoryNotebookStore) setStatus(scope, term string, status NotebookStatus, editorUserID, editorName, note string, now time.Time) (NotebookEntry, bool, error) {
	key := notebookStoreKey(scope, term)
	entry, found := s.entries[key]
	if !found {
		return NotebookEntry{}, false, nil
	}
	entry.Status = status
	entry.Note = note
	entry.EditorUserID = editorUserID
	entry.EditorName = editorName
	entry.Version++
	entry.UpdatedAt = now
	s.entries[key] = entry
	return entry, true, nil
}

func (s *memoryNotebookStore) DeleteNotebookEntry(_ context.Context, scopeKey, term, editorUserID, editorName, note string, now time.Time) (NotebookEntry, bool, error) {
	return s.setStatus(scopeKey, term, NotebookStatusDeleted, editorUserID, editorName, note, now)
}

func (s *memoryNotebookStore) RestoreNotebookEntry(_ context.Context, scopeKey, term, editorUserID, editorName string, now time.Time) (NotebookEntry, bool, error) {
	return s.setStatus(scopeKey, term, NotebookStatusActive, editorUserID, editorName, "", now)
}

func (s *memoryNotebookStore) LookupNotebookEntries(_ context.Context, query NotebookQuery) ([]NotebookEntry, error) {
	text := NormalizeNotebookTitle(query.Text)
	hits := make([]NotebookEntry, 0, len(s.entries))
	for _, scope := range query.ScopeKeys {
		for _, entry := range s.entries {
			if entry.ScopeKey != scope || entry.Status != NotebookStatusActive {
				continue
			}
			matched := false
			for _, candidate := range append([]string{entry.Term}, entry.Aliases...) {
				if normalized := NormalizeNotebookTitle(candidate); normalized != "" && strings.Contains(text, normalized) {
					matched = true
					break
				}
			}
			if matched {
				hits = append(hits, entry)
			}
		}
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].Term < hits[j].Term })
	SortNotebookEntriesByScope(hits, query.ScopeKeys)
	return hits, nil
}

func (s *memoryNotebookStore) ListNotebookEntries(_ context.Context, query NotebookQuery) ([]NotebookEntry, error) {
	items := make([]NotebookEntry, 0, len(s.entries))
	for _, scope := range query.ScopeKeys {
		for _, entry := range s.entries {
			if entry.ScopeKey != scope {
				continue
			}
			if entry.Status == NotebookStatusDeleted && !query.IncludeDeleted {
				continue
			}
			items = append(items, entry)
		}
	}
	SortNotebookEntriesByScope(items, query.ScopeKeys)
	return items, nil
}

func (s *memoryNotebookStore) NotebookEntryDetail(_ context.Context, scopeKey, term string) (NotebookEntry, bool, error) {
	entry, found := s.entries[notebookStoreKey(scopeKey, term)]
	return entry, found, nil
}

func (s *memoryNotebookStore) TouchNotebookEntries(_ context.Context, ids []string, _ time.Time) error {
	for _, id := range ids {
		s.touched[id]++
	}
	return nil
}

func newNotebookRuntime(t *testing.T, store NotebookStore) *Runtime {
	t.Helper()
	runtime := NewRuntime(BotConfig{OwnerID: "10001", BotAccount: "10000"}, &recordingChannel{}, NewPluginManager(), nil, nil, nil, nil)
	if store != nil {
		runtime.SetNotebookStore(store)
	}
	return runtime
}

// newIsolatedNotebookRuntime 关掉「笔记本跟随机器人」，给按会话隔离那一档的用例用。
func newIsolatedNotebookRuntime(t *testing.T, store NotebookStore) *Runtime {
	t.Helper()
	runtime := NewRuntime(BotConfig{OwnerID: "10001", BotAccount: "10000", NotebookSharedScopeEnabled: boolPointer(false)}, &recordingChannel{}, NewPluginManager(), nil, nil, nil, nil)
	if store != nil {
		runtime.SetNotebookStore(store)
	}
	return runtime
}

func notebookTestEvent(userID, text string) MessageEvent {
	return MessageEvent{
		Kind:      EventKindGroup,
		SelfID:    "10000",
		UserID:    userID,
		GroupID:   "20002",
		MessageID: "m1",
		Segments:  []MessageSegment{{Type: "text", Data: map[string]string{"text": text}}},
	}
}

func runNotebookTool(t *testing.T, runtime *Runtime, event MessageEvent, owner bool, input map[string]any) (dianaNotebookResult, error) {
	t.Helper()
	tool := newDianaNotebookTool(runtime, event, RelationshipPolicy{Owner: owner})
	raw, err := tool.Run(context.Background(), input)
	if err != nil {
		return dianaNotebookResult{}, err
	}
	var result dianaNotebookResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	return result, nil
}

// 笔记本的价值在自动命中：用户不会为了一个梗特意说「查一下笔记本」，模型也不会主动去
// 查一个它以为自己认识的词。
func TestNotebookContextInjectsMatchedEntries(t *testing.T) {
	store := newMemoryNotebookStore()
	runtime := newNotebookRuntime(t, store)
	if _, _, err := store.UpsertNotebookEntry(context.Background(), NotebookUpsertRequest{
		ScopeKey: "group:20002", Term: "带薪拉屎", Aliases: []string{"DXLS"},
		Meaning: "上班时间摸鱼", Example: "今天带薪拉屎半小时", Now: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}

	event := notebookTestEvent("10005", "今天 dxls 了半小时")
	block := runtime.notebookContext(context.Background(), event, "今天 dxls 了半小时")
	if !strings.Contains(block, "带薪拉屎") || !strings.Contains(block, "上班时间摸鱼") {
		t.Fatalf("block = %q", block)
	}
	// 命中的笔记是拿来理解的，不是拿来复述的：少了这句模型会回一段词义解释。
	if !strings.Contains(block, "不要复述笔记") {
		t.Fatalf("block 缺少使用约束: %q", block)
	}
	if !strings.Contains(block, "又作 DXLS") || !strings.Contains(block, "例：今天带薪拉屎半小时") {
		t.Fatalf("别名和例句没有进上下文: %q", block)
	}

	// 命中要回写，否则冷热排序永远不动，笔记本也就无从维护。
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && len(store.touched) == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	if len(store.touched) != 1 {
		t.Fatalf("touched = %v", store.touched)
	}

	if block := runtime.notebookContext(context.Background(), notebookTestEvent("10005", "今天天气不错"), "今天天气不错"); block != "" {
		t.Fatalf("没命中时不该注入任何东西: %q", block)
	}
}

func TestProactiveRouterReceivesMatchedNotebookContext(t *testing.T) {
	store := newMemoryNotebookStore()
	provider := &capturingLLMProvider{reply: `{"should_reply":true,"confidence":0.98,"category":"needs_response","target_message_id":"m1","turn_message_ids":["m1"],"directed_at_bot":false,"answerable":true,"substantive":true,"requests_response":true,"blocker":"none","reason":"笔记本说明 zgm 是公开询问在干嘛"}`}
	runtime := NewRuntime(BotConfig{
		OwnerID: "10001", BotAccount: "10000", ProactiveReplyThreshold: 0.9, ProactiveReplyChance: 1,
	}, &recordingChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
		return provider, nil
	})
	runtime.SetNotebookStore(store)
	if _, _, err := store.UpsertNotebookEntry(context.Background(), NotebookUpsertRequest{
		ScopeKey: "group:20002", Term: "zgm", Meaning: "在干嘛，用于随口询问对方正在做什么", Now: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	event := notebookTestEvent("10005", "zgm")
	if !runtime.shouldHandleProactiveReply(context.Background(), event, "zgm") {
		t.Fatal("known notebook question was not allowed by proactive routing")
	}
	request := provider.requestSnapshot()
	if len(request.Messages) < 2 {
		t.Fatalf("router request = %#v", request.Messages)
	}
	payload := request.Messages[1].Content
	for _, want := range []string{`"notebook_context"`, "zgm", "在干嘛"} {
		if !strings.Contains(payload, want) {
			t.Fatalf("router payload missing %q: %s", want, payload)
		}
	}
	prompt := request.Messages[0].Content
	for _, want := range []string{"notebook_context", "不能再称它为未解释缩写", "zgm=在干嘛"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("router prompt missing %q: %s", want, prompt)
		}
	}
	if len(store.touched) != 0 {
		t.Fatalf("routing lookup changed notebook usage counts: %#v", store.touched)
	}
}

// 没有存储层时笔记本整体静默失效，回复照常。
func TestNotebookContextEmptyWithoutStore(t *testing.T) {
	runtime := newNotebookRuntime(t, nil)
	if block := runtime.notebookContext(context.Background(), notebookTestEvent("10005", "dxls"), "dxls"); block != "" {
		t.Fatalf("block = %q", block)
	}
	if _, err := runNotebookTool(t, runtime, notebookTestEvent("10005", "记一下"), true, map[string]any{
		"operation": "upsert", "term": "梗", "meaning": "意思",
	}); err == nil {
		t.Fatal("没有存储层时工具应当明确报错，而不是假装记住了")
	}
}

// 同一个词第二次写入是修订，不是新建：笔记本要能一直被改。
func TestDianaNotebookToolUpsertRevises(t *testing.T) {
	store := newMemoryNotebookStore()
	runtime := newIsolatedNotebookRuntime(t, store)
	event := notebookTestEvent("10005", "记一下")

	result, err := runNotebookTool(t, runtime, event, false, map[string]any{
		"operation": "upsert", "term": "typo姐", "meaning": "打错字最多的人", "aliases": []any{"typo"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Action != "created" || result.Entry == nil || result.Entry.ScopeKey != "group:20002" {
		t.Fatalf("result = %+v", result)
	}

	result, err = runNotebookTool(t, runtime, event, false, map[string]any{
		"operation": "upsert", "term": "Typo姐", "meaning": "现在是夸人", "note": "用法反转了",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Action != "updated" || result.Entry.Version != 2 {
		t.Fatalf("result = %+v", result)
	}

	got, err := runNotebookTool(t, runtime, event, false, map[string]any{"operation": "get", "term": "typo姐"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Entry == nil || got.Entry.Meaning != "现在是夸人" {
		t.Fatalf("get = %+v", got)
	}

	missing, err := runNotebookTool(t, runtime, event, false, map[string]any{"operation": "get", "term": "没收录过的词"})
	if err != nil {
		t.Fatal(err)
	}
	if missing.Action != "missing" || missing.Entry != nil {
		t.Fatalf("missing = %+v", missing)
	}
}

// 释义为空时必须报错：不知道意思就别记，别编一个。
func TestDianaNotebookToolRejectsEmptyMeaning(t *testing.T) {
	runtime := newNotebookRuntime(t, newMemoryNotebookStore())
	if _, err := runNotebookTool(t, runtime, notebookTestEvent("10005", "记一下"), true, map[string]any{
		"operation": "upsert", "term": "梗", "meaning": "   ",
	}); err == nil {
		t.Fatal("空释义应当被拒绝")
	}
}

// 全局笔记本是主人特权：一个群的内部梗不该由一个人替所有群定义。
// 默认笔记本跟随机器人：群聊和私聊里普通成员记的都进这台机器人的全局本，
// 不用传 global，也不分群。
func TestDianaNotebookToolFollowsBotByDefault(t *testing.T) {
	store := newMemoryNotebookStore()
	runtime := newNotebookRuntime(t, store)
	group := notebookTestEvent("10005", "记一下")
	group.ProfileID = "qq"
	private := MessageEvent{Kind: EventKindPrivate, SelfID: "10000", UserID: "10006", MessageID: "m2", ProfileID: "qq"}

	result, err := runNotebookTool(t, runtime, group, false, map[string]any{
		"operation": "upsert", "term": "鸽", "meaning": "放鸽子",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Entry == nil || result.Entry.ScopeKey != NotebookScopeBotPrefix+"qq" {
		t.Fatalf("group write scope = %+v", result)
	}
	result, err = runNotebookTool(t, runtime, private, false, map[string]any{
		"operation": "get", "term": "鸽",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Entry == nil || result.Entry.Meaning != "放鸽子" {
		t.Fatalf("private chat must read the same notebook: %+v", result)
	}
}

// 关掉「跟随机器人」、按会话隔离时，global 才是主人特权。
func TestDianaNotebookToolRestrictsGlobalScope(t *testing.T) {
	store := newMemoryNotebookStore()
	runtime := newIsolatedNotebookRuntime(t, store)
	event := notebookTestEvent("10005", "记一下")

	if _, err := runNotebookTool(t, runtime, event, false, map[string]any{
		"operation": "upsert", "term": "鸽", "meaning": "放鸽子", "global": true,
	}); err == nil {
		t.Fatal("普通成员不该写得进全局笔记本")
	}

	result, err := runNotebookTool(t, runtime, notebookTestEvent("10001", "记一下"), true, map[string]any{
		"operation": "upsert", "term": "鸽", "meaning": "放鸽子", "global": "true",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Entry == nil || result.Entry.ScopeKey != NotebookScopeGlobal {
		t.Fatalf("result = %+v", result)
	}
}

// 笔记本是共用的：谁都能抹掉别人立的规矩就没法用了。
func TestDianaNotebookToolProtectsOtherPeoplesEntries(t *testing.T) {
	store := newMemoryNotebookStore()
	runtime := newNotebookRuntime(t, store)
	author := notebookTestEvent("10005", "记一下")
	stranger := notebookTestEvent("10006", "删掉")

	if _, err := runNotebookTool(t, runtime, author, false, map[string]any{
		"operation": "upsert", "term": "老梗", "meaning": "旧释义",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := runNotebookTool(t, runtime, stranger, false, map[string]any{
		"operation": "delete", "term": "老梗",
	}); err == nil {
		t.Fatal("别人记下的条目不该被随手作废")
	}

	deleted, err := runNotebookTool(t, runtime, author, false, map[string]any{
		"operation": "delete", "term": "老梗", "note": "过时了",
	})
	if err != nil {
		t.Fatal(err)
	}
	if deleted.Action != "deleted" {
		t.Fatalf("deleted = %+v", deleted)
	}
	// 作废之后不再自动命中。
	if block := runtime.notebookContext(context.Background(), notebookTestEvent("10005", "还有人说老梗吗"), "还有人说老梗吗"); block != "" {
		t.Fatalf("作废的条目仍在命中: %q", block)
	}
	// 主人删得动别人的条目，删错了也恢复得回来。
	restored, err := runNotebookTool(t, runtime, notebookTestEvent("10001", "恢复"), true, map[string]any{
		"operation": "restore", "term": "老梗",
	})
	if err != nil {
		t.Fatal(err)
	}
	if restored.Action != "restored" || restored.Entry.Status != NotebookStatusActive {
		t.Fatalf("restored = %+v", restored)
	}
}

// 本群释义优先于全局：全局笔记本是兜底，不是覆盖。
func TestNotebookContextPrefersSessionScope(t *testing.T) {
	store := newMemoryNotebookStore()
	runtime := newNotebookRuntime(t, store)
	ctx := context.Background()
	for _, request := range []NotebookUpsertRequest{
		{ScopeKey: NotebookScopeGlobal, Term: "鸽", Meaning: "放人鸽子", Now: time.Now()},
		{ScopeKey: "group:20002", Term: "鸽", Meaning: "本群指某位群友的头像", Now: time.Now()},
	} {
		if _, _, err := store.UpsertNotebookEntry(ctx, request); err != nil {
			t.Fatal(err)
		}
	}
	block := runtime.notebookContext(ctx, notebookTestEvent("10005", "他又鸽了"), "他又鸽了")
	local := strings.Index(block, "本群指某位群友的头像")
	global := strings.Index(block, "放人鸽子")
	if local < 0 || global < 0 || local > global {
		t.Fatalf("本群释义应排在全局之前: %q", block)
	}
}

// 注册了笔记本工具才注入笔记本规则；没注册时提示词里一个字都不该出现（上面那条
// TestSystemPromptOmitsUnselectedToolRules 守的是反面）。
func TestSystemPromptInjectsNotebookRuleWithTool(t *testing.T) {
	runtime := newNotebookRuntime(t, newMemoryNotebookStore())
	registry := agent.NewToolRegistry(newDianaNotebookTool(runtime, notebookTestEvent("10001", ""), RelationshipPolicy{Owner: true}))
	prompt := runtime.systemPromptWithRelationshipAndAgentTools(
		MessageEvent{Kind: EventKindGroup, GroupID: "20002", UserID: "10001"},
		nil, false, RelationshipPolicy{Owner: true}, true, registry,
	)
	if !strings.Contains(prompt, promptToolNotebook) {
		t.Fatalf("prompt 缺少笔记本规则: %s", prompt)
	}
	// 规则要说清「一直维护」，只会查不会改的笔记本等于没有。
	for _, want := range []string{"upsert", "delete", "restore"} {
		if !strings.Contains(promptToolNotebook, want) {
			t.Fatalf("笔记本规则没有交代 %s", want)
		}
	}
}

// 关掉「跟随机器人」后按会话隔离：一个群记下的梗只写进这个群的作用域，别的群查不到。
func TestNotebookScopeKeyForWriteIsolatesBySessionWhenDisabled(t *testing.T) {
	cfg := DefaultBotConfig()
	cfg.NotebookSharedScopeEnabled = boolPointer(false)
	cfg = cfg.WithDefaults()
	event := MessageEvent{Kind: EventKindGroup, GroupID: "123", UserID: "10001", ContextNamespace: "profile-a"}

	if got := notebookScopeKeyForWrite(event, cfg, false, false); got != "profile-a:group:123" {
		t.Fatalf("default write scope = %q", got)
	}
	// global 是主人特权：普通成员即使传 global 也只能写本群。
	if got := notebookScopeKeyForWrite(event, cfg, true, false); got != "profile-a:group:123" {
		t.Fatalf("non-owner global write scope = %q", got)
	}
	if got := notebookScopeKeyForWrite(event, cfg, true, true); got != NotebookScopeGlobal {
		t.Fatalf("owner global write scope = %q", got)
	}
}

// 默认笔记本跟随机器人：群聊、私聊的条目都写进全局，不按会话分家。
func TestNotebookScopeKeyForWriteSharedAcrossGroups(t *testing.T) {
	cfg := DefaultBotConfig().WithDefaults()
	if !boolValue(cfg.NotebookSharedScopeEnabled, false) {
		t.Fatal("notebook must follow the bot by default")
	}

	for _, event := range []MessageEvent{
		{Kind: EventKindGroup, GroupID: "123", UserID: "10001", ContextNamespace: "profile-a"},
		{Kind: EventKindGroup, GroupID: "456", UserID: "20002"},
		{Kind: EventKindPrivate, UserID: "30003"},
	} {
		if got := notebookScopeKeyForWrite(event, cfg, false, false); got != NotebookScopeGlobal {
			t.Fatalf("shared write scope for %#v = %q", event, got)
		}
	}
}

// 读取顺序始终是「当前会话优先、global 兜底」：打开开关之前各群记下的条目
// 不该凭空消失，仍要在自己群里压过全局那条。
func TestNotebookScopeKeysPreferSessionThenGlobal(t *testing.T) {
	event := MessageEvent{Kind: EventKindGroup, GroupID: "123", ContextNamespace: "profile-a"}
	if got := notebookScopeKeys(event); len(got) != 2 || got[0] != "profile-a:group:123" || got[1] != NotebookScopeGlobal {
		t.Fatalf("scope keys = %#v", got)
	}
	if got := notebookScopeKeys(MessageEvent{}); len(got) != 1 || got[0] != NotebookScopeGlobal {
		t.Fatalf("empty session scope keys = %#v", got)
	}
}

// 每台机器人各有一本全局笔记本：同一个梗在两台那里可以有不同的记法，写入不该串台。
func TestNotebookGlobalScopeIsPerBot(t *testing.T) {
	cfg := DefaultBotConfig().WithDefaults()
	qq := MessageEvent{Kind: EventKindGroup, GroupID: "123", UserID: "10001", ProfileID: "qq"}
	tg := MessageEvent{Kind: EventKindGroup, GroupID: "123", UserID: "10001", ProfileID: "tg"}

	qqScope := notebookScopeKeyForWrite(qq, cfg, true, true)
	tgScope := notebookScopeKeyForWrite(tg, cfg, true, true)
	if qqScope != NotebookScopeBotPrefix+"qq" || tgScope != NotebookScopeBotPrefix+"tg" {
		t.Fatalf("owner global write scopes = %q / %q", qqScope, tgScope)
	}
	// 拿不到机器人身份时退回升级前那本共用笔记本，总比把条目写丢好。
	if got := notebookScopeKeyForWrite(MessageEvent{Kind: EventKindGroup, GroupID: "123"}, cfg, true, true); got != NotebookScopeGlobal {
		t.Fatalf("profileless owner global write scope = %q, want %q", got, NotebookScopeGlobal)
	}
}

// 查词顺序：本会话 → 这台机器人的全局本 → 升级前那本共用的（迁移后通常已空）。
func TestNotebookScopeKeysFallBackThroughBotThenLegacyGlobal(t *testing.T) {
	event := MessageEvent{Kind: EventKindGroup, GroupID: "123", ProfileID: "qq", ContextNamespace: "qq"}
	got := notebookScopeKeys(event)
	want := []string{"qq:group:123", NotebookScopeBotPrefix + "qq", NotebookScopeGlobal}
	if len(got) != len(want) {
		t.Fatalf("scope keys = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("scope keys = %#v, want %#v", got, want)
		}
	}
	// 没开平台隔离时会话键没有前缀，两台机器人共用群作用域，但全局本各是各的。
	shared := notebookScopeKeys(MessageEvent{Kind: EventKindGroup, GroupID: "123", ProfileID: "tg"})
	if len(shared) != 3 || shared[0] != "group:123" || shared[1] != NotebookScopeBotPrefix+"tg" {
		t.Fatalf("shared session scope keys = %#v", shared)
	}
}

// 既没有群号也没有账号的事件不能拼出 "private:" 这种只有前缀的作用域：那不是
// 一个会话，而是所有匿名事件共用的桶，当成作用域用会把互不相干的上下文串到一起。
func TestNotebookScopeRejectsIdentitylessSession(t *testing.T) {
	empty := MessageEvent{}
	if got := notebookSessionScope(empty); got != "" {
		t.Fatalf("identityless session scope = %q, want empty", got)
	}
	if got := notebookScopeKeys(empty); len(got) != 1 || got[0] != NotebookScopeGlobal {
		t.Fatalf("scope keys = %#v, want global only", got)
	}
	cfg := DefaultBotConfig().WithDefaults()
	if got := notebookScopeKeyForWrite(empty, cfg, false, false); got != NotebookScopeGlobal {
		t.Fatalf("identityless write scope = %q, want global", got)
	}
	// 带命名空间但仍然没有身份，同样不能算一个会话。
	if got := notebookSessionScope(MessageEvent{ContextNamespace: "profile-a"}); got != "" {
		t.Fatalf("namespaced identityless scope = %q, want empty", got)
	}
}

// 类型是后加的，老数据和写错的值都必须落到条目上——为此拒绝一条本来能用的笔记不值得。
func TestNormalizeNotebookKindFallsBackToTerm(t *testing.T) {
	for _, raw := range []string{"", "   ", "TERM", "term", "unknown", "梗"} {
		got := NormalizeNotebookKind(raw)
		if !got.Valid() {
			t.Fatalf("NormalizeNotebookKind(%q) = %q, want a valid kind", raw, got)
		}
	}
	if got := NormalizeNotebookKind("FACT"); got != NotebookKindFact {
		t.Fatalf("NormalizeNotebookKind(\"FACT\") = %q, want case-insensitive matching", got)
	}
	if got := NormalizeNotebookKind("unknown"); got != NotebookKindTerm {
		t.Fatalf("NormalizeNotebookKind(\"unknown\") = %q, want the term fallback", got)
	}
}

// 条目收的是词，其余类型的标题是一句概括，两者的长度上限不能是同一个。
func TestNotebookKindTitleLimits(t *testing.T) {
	if NotebookKindTerm.TitleLimit() != NotebookTermTitleMaxRunes {
		t.Fatalf("term title limit = %d", NotebookKindTerm.TitleLimit())
	}
	for _, kind := range []NotebookKind{NotebookKindFact, NotebookKindTodo, NotebookKindPerson} {
		if kind.TitleLimit() <= NotebookTermTitleMaxRunes {
			t.Fatalf("%s title limit = %d, want room for a whole sentence", kind, kind.TitleLimit())
		}
	}
	// 「群规：十点后不刷屏」这类标题必须放得下，不然一升级就被截断。
	title := "群规：晚上十点之后不要在群里连续刷屏，有事私聊管理员"
	if got := TruncateNotebookText(title, NotebookKindFact.TitleLimit()); got != title {
		t.Fatalf("a realistic fact title was truncated: %q", got)
	}
}

// 每种类型都要有中文名，界面和提示词共用这一份。
func TestNotebookKindLabelsCoverEveryKind(t *testing.T) {
	for _, kind := range NotebookKinds() {
		if label := kind.Label(); label == "" || label == string(kind) {
			t.Fatalf("kind %q has no Chinese label", kind)
		}
	}
	// 未知类型显示原值而不是空串：宁可显示一个陌生的类型名，也不该显示一个没有类型的条目。
	if got := NotebookKind("mystery").Label(); got != "mystery" {
		t.Fatalf("unknown kind label = %q", got)
	}
}

// 注进提示词的那一行要标出类型：同一句话作为事实、待办、条目，含义完全不同。
func TestFormatNotebookLineMarksNonTermKinds(t *testing.T) {
	fact := NotebookEntry{Kind: NotebookKindFact, Term: "群规：十点后不刷屏", Meaning: "管理员定的", Aliases: []string{"刷屏"}}
	line := formatNotebookLine(fact)
	if !strings.HasPrefix(line, "[事实] ") {
		t.Fatalf("line = %q, want the kind marked", line)
	}
	// 非条目的触发词是检索钩子，不是别名。写成「又作」会让模型以为它们是同义词。
	if strings.Contains(line, "又作") {
		t.Fatalf("line = %q, keywords must not be presented as aliases", line)
	}

	term := NotebookEntry{Kind: NotebookKindTerm, Term: "带薪拉屎", Meaning: "上班摸鱼", Aliases: []string{"DXLS"}}
	termLine := formatNotebookLine(term)
	if strings.HasPrefix(termLine, "[") {
		t.Fatalf("term line = %q, a term needs no kind marker", termLine)
	}
	if !strings.Contains(termLine, "又作 DXLS") {
		t.Fatalf("term line = %q, want the alias kept", termLine)
	}

	// 没有类型的老数据按条目渲染，不该多出一个空标记。
	legacy := NotebookEntry{Term: "旧条目", Meaning: "升级前记的"}
	if line := formatNotebookLine(legacy); strings.HasPrefix(line, "[") {
		t.Fatalf("legacy line = %q", line)
	}
}
