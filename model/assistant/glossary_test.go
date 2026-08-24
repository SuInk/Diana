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

// memoryGlossaryStore 是词典的内存实现，只服务测试。匹配逻辑照着 SQLite 那边写：
// 词条和别名都参与「这段话里出现了哪个词」的子串匹配。
type memoryGlossaryStore struct {
	entries map[string]GlossaryEntry
	touched map[string]int
	nextID  int
}

func newMemoryGlossaryStore() *memoryGlossaryStore {
	return &memoryGlossaryStore{entries: map[string]GlossaryEntry{}, touched: map[string]int{}}
}

func glossaryStoreKey(scope, term string) string {
	return scope + "|" + NormalizeGlossaryTerm(term)
}

func (s *memoryGlossaryStore) UpsertGlossaryEntry(_ context.Context, request GlossaryUpsertRequest) (GlossaryEntry, bool, error) {
	key := glossaryStoreKey(request.ScopeKey, request.Term)
	entry, found := s.entries[key]
	created := !found
	if created {
		s.nextID++
		entry = GlossaryEntry{
			ID:           "glossary-" + itoa(s.nextID),
			ScopeKey:     request.ScopeKey,
			AuthorUserID: request.EditorUserID,
			AuthorName:   request.EditorName,
			CreatedAt:    request.Now,
		}
	}
	entry.Term = request.Term
	entry.Aliases = NormalizeGlossaryAliases(request.Term, request.Aliases)
	entry.Meaning = request.Meaning
	entry.Example = request.Example
	entry.Note = request.Note
	entry.EditorUserID = request.EditorUserID
	entry.EditorName = request.EditorName
	entry.Version++
	entry.Status = GlossaryStatusActive
	entry.UpdatedAt = request.Now
	entry.Revisions = append([]GlossaryRevision{{
		Version: entry.Version, Meaning: request.Meaning, Note: request.Note,
		EditorUserID: request.EditorUserID, RecordedAt: request.Now,
	}}, entry.Revisions...)
	s.entries[key] = entry
	return entry, created, nil
}

func (s *memoryGlossaryStore) setStatus(scope, term string, status GlossaryStatus, editorUserID, editorName, note string, now time.Time) (GlossaryEntry, bool, error) {
	key := glossaryStoreKey(scope, term)
	entry, found := s.entries[key]
	if !found {
		return GlossaryEntry{}, false, nil
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

func (s *memoryGlossaryStore) DeleteGlossaryEntry(_ context.Context, scopeKey, term, editorUserID, editorName, note string, now time.Time) (GlossaryEntry, bool, error) {
	return s.setStatus(scopeKey, term, GlossaryStatusDeleted, editorUserID, editorName, note, now)
}

func (s *memoryGlossaryStore) RestoreGlossaryEntry(_ context.Context, scopeKey, term, editorUserID, editorName string, now time.Time) (GlossaryEntry, bool, error) {
	return s.setStatus(scopeKey, term, GlossaryStatusActive, editorUserID, editorName, "", now)
}

func (s *memoryGlossaryStore) LookupGlossaryEntries(_ context.Context, query GlossaryQuery) ([]GlossaryEntry, error) {
	text := NormalizeGlossaryTerm(query.Text)
	hits := make([]GlossaryEntry, 0, len(s.entries))
	for _, scope := range query.ScopeKeys {
		for _, entry := range s.entries {
			if entry.ScopeKey != scope || entry.Status != GlossaryStatusActive {
				continue
			}
			matched := false
			for _, candidate := range append([]string{entry.Term}, entry.Aliases...) {
				if normalized := NormalizeGlossaryTerm(candidate); normalized != "" && strings.Contains(text, normalized) {
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
	SortGlossaryEntriesByScope(hits, query.ScopeKeys)
	return hits, nil
}

func (s *memoryGlossaryStore) ListGlossaryEntries(_ context.Context, query GlossaryQuery) ([]GlossaryEntry, error) {
	items := make([]GlossaryEntry, 0, len(s.entries))
	for _, scope := range query.ScopeKeys {
		for _, entry := range s.entries {
			if entry.ScopeKey != scope {
				continue
			}
			if entry.Status == GlossaryStatusDeleted && !query.IncludeDeleted {
				continue
			}
			items = append(items, entry)
		}
	}
	SortGlossaryEntriesByScope(items, query.ScopeKeys)
	return items, nil
}

func (s *memoryGlossaryStore) GlossaryEntryDetail(_ context.Context, scopeKey, term string) (GlossaryEntry, bool, error) {
	entry, found := s.entries[glossaryStoreKey(scopeKey, term)]
	return entry, found, nil
}

func (s *memoryGlossaryStore) TouchGlossaryEntries(_ context.Context, ids []string, _ time.Time) error {
	for _, id := range ids {
		s.touched[id]++
	}
	return nil
}

func newGlossaryRuntime(t *testing.T, store GlossaryStore) *Runtime {
	t.Helper()
	runtime := NewRuntime(BotConfig{OwnerID: "10001", BotAccount: "10000"}, &recordingChannel{}, NewPluginManager(), nil, nil, nil, nil)
	if store != nil {
		runtime.SetGlossaryStore(store)
	}
	return runtime
}

func glossaryTestEvent(userID, text string) MessageEvent {
	return MessageEvent{
		Kind:      EventKindGroup,
		SelfID:    "10000",
		UserID:    userID,
		GroupID:   "20002",
		MessageID: "m1",
		Segments:  []MessageSegment{{Type: "text", Data: map[string]string{"text": text}}},
	}
}

func runGlossaryTool(t *testing.T, runtime *Runtime, event MessageEvent, owner bool, input map[string]any) (dianaGlossaryResult, error) {
	t.Helper()
	tool := newDianaGlossaryTool(runtime, event, RelationshipPolicy{Owner: owner})
	raw, err := tool.Run(context.Background(), input)
	if err != nil {
		return dianaGlossaryResult{}, err
	}
	var result dianaGlossaryResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	return result, nil
}

// 词典的价值在自动命中：用户不会为了一个梗特意说「查一下词典」，模型也不会主动去
// 查一个它以为自己认识的词。
func TestGlossaryContextInjectsMatchedEntries(t *testing.T) {
	store := newMemoryGlossaryStore()
	runtime := newGlossaryRuntime(t, store)
	if _, _, err := store.UpsertGlossaryEntry(context.Background(), GlossaryUpsertRequest{
		ScopeKey: "group:20002", Term: "带薪拉屎", Aliases: []string{"DXLS"},
		Meaning: "上班时间摸鱼", Example: "今天带薪拉屎半小时", Now: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}

	event := glossaryTestEvent("10005", "今天 dxls 了半小时")
	block := runtime.glossaryContext(context.Background(), event, "今天 dxls 了半小时")
	if !strings.Contains(block, "带薪拉屎") || !strings.Contains(block, "上班时间摸鱼") {
		t.Fatalf("block = %q", block)
	}
	// 命中的词条是拿来理解的，不是拿来复述的：少了这句模型会回一段词义解释。
	if !strings.Contains(block, "不要复述词条") {
		t.Fatalf("block 缺少使用约束: %q", block)
	}
	if !strings.Contains(block, "又作 DXLS") || !strings.Contains(block, "例：今天带薪拉屎半小时") {
		t.Fatalf("别名和例句没有进上下文: %q", block)
	}

	// 命中要回写，否则冷热排序永远不动，词典也就无从维护。
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && len(store.touched) == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	if len(store.touched) != 1 {
		t.Fatalf("touched = %v", store.touched)
	}

	if block := runtime.glossaryContext(context.Background(), glossaryTestEvent("10005", "今天天气不错"), "今天天气不错"); block != "" {
		t.Fatalf("没命中时不该注入任何东西: %q", block)
	}
}

// 没有存储层时词典整体静默失效，回复照常。
func TestGlossaryContextEmptyWithoutStore(t *testing.T) {
	runtime := newGlossaryRuntime(t, nil)
	if block := runtime.glossaryContext(context.Background(), glossaryTestEvent("10005", "dxls"), "dxls"); block != "" {
		t.Fatalf("block = %q", block)
	}
	if _, err := runGlossaryTool(t, runtime, glossaryTestEvent("10005", "记一下"), true, map[string]any{
		"operation": "upsert", "term": "梗", "meaning": "意思",
	}); err == nil {
		t.Fatal("没有存储层时工具应当明确报错，而不是假装记住了")
	}
}

// 同一个词第二次写入是修订，不是新建：词典要能一直被改。
func TestDianaGlossaryToolUpsertRevises(t *testing.T) {
	store := newMemoryGlossaryStore()
	runtime := newGlossaryRuntime(t, store)
	event := glossaryTestEvent("10005", "记一下")

	result, err := runGlossaryTool(t, runtime, event, false, map[string]any{
		"operation": "upsert", "term": "typo姐", "meaning": "打错字最多的人", "aliases": []any{"typo"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Action != "created" || result.Entry == nil || result.Entry.ScopeKey != "group:20002" {
		t.Fatalf("result = %+v", result)
	}

	result, err = runGlossaryTool(t, runtime, event, false, map[string]any{
		"operation": "upsert", "term": "Typo姐", "meaning": "现在是夸人", "note": "用法反转了",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Action != "updated" || result.Entry.Version != 2 {
		t.Fatalf("result = %+v", result)
	}

	got, err := runGlossaryTool(t, runtime, event, false, map[string]any{"operation": "get", "term": "typo姐"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Entry == nil || got.Entry.Meaning != "现在是夸人" {
		t.Fatalf("get = %+v", got)
	}

	missing, err := runGlossaryTool(t, runtime, event, false, map[string]any{"operation": "get", "term": "没收录过的词"})
	if err != nil {
		t.Fatal(err)
	}
	if missing.Action != "missing" || missing.Entry != nil {
		t.Fatalf("missing = %+v", missing)
	}
}

// 释义为空时必须报错：不知道意思就别记，别编一个。
func TestDianaGlossaryToolRejectsEmptyMeaning(t *testing.T) {
	runtime := newGlossaryRuntime(t, newMemoryGlossaryStore())
	if _, err := runGlossaryTool(t, runtime, glossaryTestEvent("10005", "记一下"), true, map[string]any{
		"operation": "upsert", "term": "梗", "meaning": "   ",
	}); err == nil {
		t.Fatal("空释义应当被拒绝")
	}
}

// 全局词典是主人特权：一个群的内部梗不该由一个人替所有群定义。
func TestDianaGlossaryToolRestrictsGlobalScope(t *testing.T) {
	store := newMemoryGlossaryStore()
	runtime := newGlossaryRuntime(t, store)
	event := glossaryTestEvent("10005", "记一下")

	if _, err := runGlossaryTool(t, runtime, event, false, map[string]any{
		"operation": "upsert", "term": "鸽", "meaning": "放鸽子", "global": true,
	}); err == nil {
		t.Fatal("普通成员不该写得进全局词典")
	}

	result, err := runGlossaryTool(t, runtime, glossaryTestEvent("10001", "记一下"), true, map[string]any{
		"operation": "upsert", "term": "鸽", "meaning": "放鸽子", "global": "true",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Entry == nil || result.Entry.ScopeKey != GlossaryScopeGlobal {
		t.Fatalf("result = %+v", result)
	}
}

// 词典是共用的：谁都能抹掉别人立的规矩就没法用了。
func TestDianaGlossaryToolProtectsOtherPeoplesEntries(t *testing.T) {
	store := newMemoryGlossaryStore()
	runtime := newGlossaryRuntime(t, store)
	author := glossaryTestEvent("10005", "记一下")
	stranger := glossaryTestEvent("10006", "删掉")

	if _, err := runGlossaryTool(t, runtime, author, false, map[string]any{
		"operation": "upsert", "term": "老梗", "meaning": "旧释义",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := runGlossaryTool(t, runtime, stranger, false, map[string]any{
		"operation": "delete", "term": "老梗",
	}); err == nil {
		t.Fatal("别人记下的词条不该被随手作废")
	}

	deleted, err := runGlossaryTool(t, runtime, author, false, map[string]any{
		"operation": "delete", "term": "老梗", "note": "过时了",
	})
	if err != nil {
		t.Fatal(err)
	}
	if deleted.Action != "deleted" {
		t.Fatalf("deleted = %+v", deleted)
	}
	// 作废之后不再自动命中。
	if block := runtime.glossaryContext(context.Background(), glossaryTestEvent("10005", "还有人说老梗吗"), "还有人说老梗吗"); block != "" {
		t.Fatalf("作废的词条仍在命中: %q", block)
	}
	// 主人删得动别人的词条，删错了也恢复得回来。
	restored, err := runGlossaryTool(t, runtime, glossaryTestEvent("10001", "恢复"), true, map[string]any{
		"operation": "restore", "term": "老梗",
	})
	if err != nil {
		t.Fatal(err)
	}
	if restored.Action != "restored" || restored.Entry.Status != GlossaryStatusActive {
		t.Fatalf("restored = %+v", restored)
	}
}

// 本群释义优先于全局：全局词典是兜底，不是覆盖。
func TestGlossaryContextPrefersSessionScope(t *testing.T) {
	store := newMemoryGlossaryStore()
	runtime := newGlossaryRuntime(t, store)
	ctx := context.Background()
	for _, request := range []GlossaryUpsertRequest{
		{ScopeKey: GlossaryScopeGlobal, Term: "鸽", Meaning: "放人鸽子", Now: time.Now()},
		{ScopeKey: "group:20002", Term: "鸽", Meaning: "本群指某位群友的头像", Now: time.Now()},
	} {
		if _, _, err := store.UpsertGlossaryEntry(ctx, request); err != nil {
			t.Fatal(err)
		}
	}
	block := runtime.glossaryContext(ctx, glossaryTestEvent("10005", "他又鸽了"), "他又鸽了")
	local := strings.Index(block, "本群指某位群友的头像")
	global := strings.Index(block, "放人鸽子")
	if local < 0 || global < 0 || local > global {
		t.Fatalf("本群释义应排在全局之前: %q", block)
	}
}

// 注册了词典工具才注入词典规则；没注册时提示词里一个字都不该出现（上面那条
// TestSystemPromptOmitsUnselectedToolRules 守的是反面）。
func TestSystemPromptInjectsGlossaryRuleWithTool(t *testing.T) {
	runtime := newGlossaryRuntime(t, newMemoryGlossaryStore())
	registry := agent.NewToolRegistry(newDianaGlossaryTool(runtime, glossaryTestEvent("10001", ""), RelationshipPolicy{Owner: true}))
	prompt := runtime.systemPromptWithRelationshipAndAgentTools(
		MessageEvent{Kind: EventKindGroup, GroupID: "20002", UserID: "10001"},
		nil, false, RelationshipPolicy{Owner: true}, true, registry,
	)
	if !strings.Contains(prompt, promptToolGlossary) {
		t.Fatalf("prompt 缺少词典规则: %s", prompt)
	}
	// 规则要说清「一直维护」，只会查不会改的词典等于没有。
	for _, want := range []string{"upsert", "delete", "restore"} {
		if !strings.Contains(promptToolGlossary, want) {
			t.Fatalf("词典规则没有交代 %s", want)
		}
	}
}

// 默认按会话隔离：一个群记下的梗只写进这个群的作用域，别的群查不到。
func TestGlossaryScopeKeyForWriteIsolatesBySessionByDefault(t *testing.T) {
	cfg := DefaultBotConfig().WithDefaults()
	event := MessageEvent{Kind: EventKindGroup, GroupID: "123", UserID: "10001", ContextNamespace: "profile-a"}

	if got := glossaryScopeKeyForWrite(event, cfg, false, false); got != "profile-a:group:123" {
		t.Fatalf("default write scope = %q", got)
	}
	// global 是主人特权：普通成员即使传 global 也只能写本群。
	if got := glossaryScopeKeyForWrite(event, cfg, true, false); got != "profile-a:group:123" {
		t.Fatalf("non-owner global write scope = %q", got)
	}
	if got := glossaryScopeKeyForWrite(event, cfg, true, true); got != GlossaryScopeGlobal {
		t.Fatalf("owner global write scope = %q", got)
	}
}

// 打开跨群共用之后所有词条都写进全局，不再按群分家。
func TestGlossaryScopeKeyForWriteSharedAcrossGroups(t *testing.T) {
	cfg := DefaultBotConfig()
	cfg.GlossarySharedScopeEnabled = boolPointer(true)
	cfg = cfg.WithDefaults()

	for _, event := range []MessageEvent{
		{Kind: EventKindGroup, GroupID: "123", UserID: "10001", ContextNamespace: "profile-a"},
		{Kind: EventKindGroup, GroupID: "456", UserID: "20002"},
		{Kind: EventKindPrivate, UserID: "30003"},
	} {
		if got := glossaryScopeKeyForWrite(event, cfg, false, false); got != GlossaryScopeGlobal {
			t.Fatalf("shared write scope for %#v = %q", event, got)
		}
	}
}

// 读取顺序始终是「当前会话优先、global 兜底」：打开开关之前各群记下的词条
// 不该凭空消失，仍要在自己群里压过全局那条。
func TestGlossaryScopeKeysPreferSessionThenGlobal(t *testing.T) {
	event := MessageEvent{Kind: EventKindGroup, GroupID: "123", ContextNamespace: "profile-a"}
	if got := glossaryScopeKeys(event); len(got) != 2 || got[0] != "profile-a:group:123" || got[1] != GlossaryScopeGlobal {
		t.Fatalf("scope keys = %#v", got)
	}
	if got := glossaryScopeKeys(MessageEvent{}); len(got) != 1 || got[0] != GlossaryScopeGlobal {
		t.Fatalf("empty session scope keys = %#v", got)
	}
}

// 既没有群号也没有账号的事件不能拼出 "private:" 这种只有前缀的作用域：那不是
// 一个会话，而是所有匿名事件共用的桶，当成作用域用会把互不相干的上下文串到一起。
func TestGlossaryScopeRejectsIdentitylessSession(t *testing.T) {
	empty := MessageEvent{}
	if got := glossarySessionScope(empty); got != "" {
		t.Fatalf("identityless session scope = %q, want empty", got)
	}
	if got := glossaryScopeKeys(empty); len(got) != 1 || got[0] != GlossaryScopeGlobal {
		t.Fatalf("scope keys = %#v, want global only", got)
	}
	cfg := DefaultBotConfig().WithDefaults()
	if got := glossaryScopeKeyForWrite(empty, cfg, false, false); got != GlossaryScopeGlobal {
		t.Fatalf("identityless write scope = %q, want global", got)
	}
	// 带命名空间但仍然没有身份，同样不能算一个会话。
	if got := glossarySessionScope(MessageEvent{ContextNamespace: "profile-a"}); got != "" {
		t.Fatalf("namespaced identityless scope = %q, want empty", got)
	}
}
