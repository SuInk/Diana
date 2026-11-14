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

// agentToolRegistryWithSelfNote 在 runtime 非 nil 时挂上自述工具，否则给一个空注册表。
func agentToolRegistryWithSelfNote(runtime *Runtime, event MessageEvent) *agent.ToolRegistry {
	if runtime == nil {
		return agent.NewToolRegistry()
	}
	return agent.NewToolRegistry(newDianaSelfNoteTool(runtime, event, RelationshipPolicy{}))
}

type stubSelfNoteStore struct {
	notes    []SelfNote
	writeErr error
	purged   int
	writes   int
}

func (s *stubSelfNoteStore) WriteSelfNote(_ context.Context, request SelfNoteWriteRequest) (SelfNote, error) {
	if s.writeErr != nil {
		return SelfNote{}, s.writeErr
	}
	note := SelfNote{
		ID: "note-" + itoa(len(s.notes)+1), ProfileID: request.ProfileID,
		Topic: request.Topic, Content: request.Content, SupersedesID: request.SupersedesID,
		SourceUserID: request.SourceUserID, Status: SelfNoteStatusActive, Version: 1,
		CreatedAt: time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC).Add(time.Duration(len(s.notes)+s.writes) * time.Minute),
	}
	s.writes++
	if request.SupersedesID != "" {
		filtered := make([]SelfNote, 0, len(s.notes))
		for _, existing := range s.notes {
			if existing.ID != request.SupersedesID {
				filtered = append(filtered, existing)
			}
		}
		s.notes = filtered
		note.Version = 2
	}
	s.notes = append(s.notes, note)
	return note, nil
}

func (s *stubSelfNoteStore) ListSelfNotes(_ context.Context, _ string, _ bool, limit int) ([]SelfNote, error) {
	notes := append([]SelfNote(nil), s.notes...)
	if limit > 0 && len(notes) > limit {
		notes = notes[:limit]
	}
	return notes, nil
}

// ListSelfNotesAfter 照 SQLite 的语义：(created_at, id) 旧到新，游标只要严格排在它后面的。
func (s *stubSelfNoteStore) notesAfter(query SelfNoteListQuery) []SelfNote {
	notes := make([]SelfNote, 0, len(s.notes))
	for _, note := range s.notes {
		if !query.IncludeInactive && note.Status != SelfNoteStatusActive {
			continue
		}
		if !query.After.IsZero() && !note.CreatedAt.After(query.After) && !(note.CreatedAt.Equal(query.After) && note.ID > query.AfterID) {
			continue
		}
		notes = append(notes, note)
	}
	sort.SliceStable(notes, func(i, j int) bool {
		if !notes[i].CreatedAt.Equal(notes[j].CreatedAt) {
			return notes[i].CreatedAt.Before(notes[j].CreatedAt)
		}
		return notes[i].ID < notes[j].ID
	})
	return notes
}

func (s *stubSelfNoteStore) ListSelfNotesAfter(_ context.Context, query SelfNoteListQuery) ([]SelfNote, error) {
	notes := s.notesAfter(query)
	if query.Limit > 0 && len(notes) > query.Limit {
		notes = notes[:query.Limit]
	}
	return notes, nil
}

func (s *stubSelfNoteStore) CountSelfNotes(_ context.Context, query SelfNoteListQuery) (int, error) {
	return len(s.notesAfter(query)), nil
}

func (s *stubSelfNoteStore) DeleteSelfNote(_ context.Context, _, id, _, _ string, _ time.Time) (SelfNote, bool, error) {
	for index, note := range s.notes {
		if note.ID != id {
			continue
		}
		note.Status = SelfNoteStatusDeleted
		s.notes = append(s.notes[:index], s.notes[index+1:]...)
		return note, true, nil
	}
	return SelfNote{}, false, nil
}

func (s *stubSelfNoteStore) PurgeSelfNotes(_ context.Context, _, _, _ string, _ time.Time) (int, error) {
	s.purged = len(s.notes)
	s.notes = nil
	return s.purged, nil
}

func selfNoteTestRuntime(t *testing.T, enabled bool, store SelfNoteStore) *Runtime {
	t.Helper()
	runtime := NewRuntime(BotConfig{SelfNoteEnabled: boolPointer(enabled)}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	if store != nil {
		runtime.SetSelfNoteStore(store)
	}
	return runtime
}

func TestSelfNoteContextHonorsStoreAndConfigGate(t *testing.T) {
	event := MessageEvent{Kind: EventKindPrivate, UserID: "10001", ProfileID: "bot-1"}
	store := &stubSelfNoteStore{notes: []SelfNote{{ID: "n1", Topic: "说话方式", Content: "一句说完就不铺三句"}}}

	// 没配存储时整层静默失效：注入为空，聊天链路不感知这个功能。
	if block := selfNoteTestRuntime(t, true, nil).selfNoteContext(context.Background(), event); block != "" {
		t.Fatalf("context without store = %q", block)
	}
	// 开关默认关着：装了存储也不注入，升级不会让机器人突然开始自我改写。
	if block := selfNoteTestRuntime(t, false, store).selfNoteContext(context.Background(), event); block != "" {
		t.Fatalf("disabled context = %q", block)
	}

	block := selfNoteTestRuntime(t, true, store).selfNoteContext(context.Background(), event)
	if !strings.Contains(block, "一句说完就不铺三句") || !strings.Contains(block, "说话方式") {
		t.Fatalf("enabled context = %q", block)
	}
	// 标注必须在：少了「不是用户消息」和「不覆盖人设」，这段就成了越权入口。
	if !strings.HasPrefix(block, selfNoteContextPrefix) {
		t.Fatalf("missing marker: %q", block)
	}
	// 自述不能凌驾于 SOUL.md 之上：人设只有人能改，这句话要出现在注入的标注里。
	if !strings.Contains(block, "以人设为准") || !strings.Contains(block, "SOUL.md") {
		t.Fatalf("missing soul precedence: %q", block)
	}
}

// 自述是每台机器人一本：拿不到机器人身份时整层关闭，不写也不读那个共享的空桶，
// 否则两台机器人会互相改写对方的自我认知。
func TestSelfNoteRequiresBotIdentity(t *testing.T) {
	store := &stubSelfNoteStore{notes: []SelfNote{{ID: "n1", Topic: "说话方式", Content: "一句说完就不铺三句"}}}
	runtime := selfNoteTestRuntime(t, true, store)
	anonymous := MessageEvent{Kind: EventKindPrivate, UserID: "10001"}

	if block := runtime.selfNoteContext(context.Background(), anonymous); block != "" {
		t.Fatalf("anonymous context = %q", block)
	}
	tool := newDianaSelfNoteTool(runtime, anonymous, RelationshipPolicy{Owner: true})
	if _, err := tool.Run(context.Background(), map[string]any{"operation": "add", "content": "记一条"}); err == nil {
		t.Fatal("writing without a bot identity should fail")
	}
	if len(store.notes) != 1 {
		t.Fatalf("store mutated: %#v", store.notes)
	}
}

// 自述全部写出，不按配额截掉尾部几条。
func TestSelfNoteContextWritesEveryNote(t *testing.T) {
	notes := make([]SelfNote, 0, MaximumActiveSelfNotes)
	for index := 0; index < MaximumActiveSelfNotes; index++ {
		notes = append(notes, SelfNote{ID: itoa(index), Topic: "说话方式", Content: strings.Repeat("很长的一条自我观察", 6) + itoa(index)})
	}
	block := formatSelfNoteContext(notes)
	for _, note := range notes {
		if !strings.Contains(block, note.Content) {
			t.Fatalf("note %s missing", note.ID)
		}
	}
}

func TestSelfNoteContentNormalizationAndLimits(t *testing.T) {
	if got := NormalizeSelfNoteContent("第一行\n第二行"); got != "第一行 第二行" {
		t.Fatalf("newlines survived: %q", got)
	}
	long := strings.Repeat("字", SelfNoteContentMaxRunes+50)
	if got := NormalizeSelfNoteContent(long); len([]rune(got)) > SelfNoteContentMaxRunes {
		t.Fatalf("content not truncated: %d runes", len([]rune(got)))
	}
	if got := NormalizeSelfNoteTopic("   "); got != "自我" {
		t.Fatalf("empty topic = %q", got)
	}
}

func TestSelfNoteToolWritesRevisesAndGuardsPurge(t *testing.T) {
	event := MessageEvent{Kind: EventKindGroup, UserID: "10001", GroupID: "1", ProfileID: "bot-1", SenderName: "阿狸", MessageID: "m1"}
	store := &stubSelfNoteStore{}
	runtime := selfNoteTestRuntime(t, true, store)
	member := newDianaSelfNoteTool(runtime, event, RelationshipPolicy{})
	ctx := context.Background()

	raw, err := member.Run(ctx, map[string]any{"operation": "add", "topic": "说话方式", "content": "我老把话说太长"})
	if err != nil {
		t.Fatal(err)
	}
	var added dianaSelfNoteResult
	if err := json.Unmarshal([]byte(raw), &added); err != nil {
		t.Fatal(err)
	}
	if !added.OK || added.Note == nil || added.Note.Content != "我老把话说太长" {
		t.Fatalf("added = %#v", added)
	}
	// 来源必须留痕：自述跨群生效，主人要能回溯是谁在场时写下的。
	if added.Note.SourceUserID != "10001" {
		t.Fatalf("source lost: %#v", added.Note)
	}

	if _, err := member.Run(ctx, map[string]any{"operation": "revise", "content": "换个说法"}); err == nil {
		t.Fatal("revise without id should fail")
	}
	raw, err = member.Run(ctx, map[string]any{"operation": "revise", "id": added.Note.ID, "content": "一句说完就不铺三句"})
	if err != nil {
		t.Fatal(err)
	}
	var revised dianaSelfNoteResult
	if err := json.Unmarshal([]byte(raw), &revised); err != nil {
		t.Fatal(err)
	}
	if revised.Action != "revise" || len(store.notes) != 1 || store.notes[0].Content != "一句说完就不铺三句" {
		t.Fatalf("revised = %#v store=%#v", revised, store.notes)
	}

	// 清空是一次性抹掉全部修订史，只有主人能做。
	if _, err := member.Run(ctx, map[string]any{"operation": "purge"}); err == nil {
		t.Fatal("member purge should be rejected")
	}
	owner := newDianaSelfNoteTool(runtime, event, RelationshipPolicy{Owner: true})
	if _, err := owner.Run(ctx, map[string]any{"operation": "purge"}); err != nil {
		t.Fatal(err)
	}
	if store.purged != 1 || len(store.notes) != 0 {
		t.Fatalf("purged = %d store=%#v", store.purged, store.notes)
	}

	// 关掉开关之后工具明确报错，不假装写成功：模型收到 ok 就会告诉用户记住了。
	disabled := newDianaSelfNoteTool(selfNoteTestRuntime(t, false, store), event, RelationshipPolicy{Owner: true})
	if _, err := disabled.Run(ctx, map[string]any{"operation": "add", "content": "偷偷记一条"}); err == nil {
		t.Fatal("disabled tool should fail")
	}
}

type selfNoteListTestResult struct {
	Message    string     `json:"message"`
	Items      []SelfNote `json:"items"`
	Total      int        `json:"total"`
	HasMore    bool       `json:"has_more"`
	Remaining  int        `json:"remaining_count"`
	NextCursor string     `json:"next_cursor"`
	PageNote   string     `json:"page_note"`
}

func runSelfNoteList(t *testing.T, ctx context.Context, tool *dianaSelfNoteTool, input map[string]any) selfNoteListTestResult {
	t.Helper()
	out, err := tool.Run(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	var result selfNoteListTestResult
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("decode %s: %v", out, err)
	}
	return result
}

func selfNoteListTestTool(t *testing.T, total int, content string) (*dianaSelfNoteTool, *stubSelfNoteStore) {
	t.Helper()
	store := &stubSelfNoteStore{}
	base := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	for index := 0; index < total; index++ {
		store.notes = append(store.notes, SelfNote{
			ID: "note-" + itoa(index+1), Content: content + itoa(index+1), Status: SelfNoteStatusActive,
			CreatedAt: base.Add(time.Duration(index) * time.Second),
		})
	}
	tool := newDianaSelfNoteTool(selfNoteTestRuntime(t, true, store), MessageEvent{Kind: EventKindPrivate, UserID: "10001", ProfileID: "bot-1"}, RelationshipPolicy{Owner: true})
	return tool, store
}

// list 报真实总数（不是这一页的条数），多出来的按整条分页，按写入顺序的键集游标续读；
// 翻页之间删掉、改写已读的条目，后面的页不跳条也不重复（offset 会跳过一条）。
func TestSelfNoteToolListReportsRealTotalAndPages(t *testing.T) {
	total := MaximumActiveSelfNotes*2 + 7
	tool, _ := selfNoteListTestTool(t, total, "第 ")
	ctx := context.Background()
	first := runSelfNoteList(t, ctx, tool, map[string]any{"operation": "list", "limit": 10})
	if first.Total != total || len(first.Items) != 10 || !first.HasMore || first.NextCursor == "" || first.Remaining != total-10 {
		t.Fatalf("first page = %+v", first)
	}
	if !strings.Contains(first.Message, itoa(total)) {
		t.Fatalf("message should report real total %d: %q", total, first.Message)
	}
	// 读完第一页后删掉其中一条、改写另一条：改写出的新版本排到末尾。
	if _, err := tool.Run(ctx, map[string]any{"operation": "delete", "id": "note-2"}); err != nil {
		t.Fatal(err)
	}
	if _, err := tool.Run(ctx, map[string]any{"operation": "revise", "id": "note-3", "content": "改写后的第 3 条"}); err != nil {
		t.Fatal(err)
	}
	rest := runSelfNoteList(t, ctx, tool, map[string]any{"operation": "list", "limit": 100, "cursor": first.NextCursor})
	if rest.HasMore || len(rest.Items) != total-10+1 || rest.Items[0].ID != "note-11" ||
		rest.Items[len(rest.Items)-2].ID != "note-"+itoa(total) || rest.Items[len(rest.Items)-1].Content != "改写后的第 3 条" {
		t.Fatalf("rest page = %+v", rest)
	}
	if _, err := tool.Run(ctx, map[string]any{"operation": "list", "cursor": "不是游标"}); err == nil {
		t.Fatal("invalid cursor must fail")
	}
}

// 按单次上限切页：上限很小时每页只装下几条，page_note 写明原因，游标续读按顺序拿全。
func TestSelfNoteToolListSplitsAtOutputBudget(t *testing.T) {
	const total = 30
	tool, _ := selfNoteListTestTool(t, total, strings.Repeat("长", 100))
	ctx := agent.WithToolOutputBudget(context.Background(), 2000)
	next := 1
	cursor := ""
	for page := 0; ; page++ {
		input := map[string]any{"operation": "list", "limit": 1000}
		if cursor != "" {
			input["cursor"] = cursor
		}
		result := runSelfNoteList(t, ctx, tool, input)
		if len(result.Items) == 0 || len(result.Items) >= total || result.Total != total {
			t.Fatalf("page %d should be cut by budget: %+v", page, result)
		}
		for _, note := range result.Items {
			if note.ID != "note-"+itoa(next) {
				t.Fatalf("page %d got %s, want note-%d", page, note.ID, next)
			}
			next++
		}
		if !result.HasMore {
			break
		}
		if !strings.Contains(result.PageNote, "单次结果上限") {
			t.Fatalf("page note = %q", result.PageNote)
		}
		cursor = result.NextCursor
	}
	if next != total+1 {
		t.Fatalf("read %d notes, want %d", next-1, total)
	}
}

func TestSelfNoteToolCapacityErrorTellsModelWhatToDo(t *testing.T) {
	store := &stubSelfNoteStore{writeErr: ErrSelfNoteCapacity}
	tool := newDianaSelfNoteTool(selfNoteTestRuntime(t, true, store), MessageEvent{Kind: EventKindPrivate, UserID: "1", ProfileID: "bot-1"}, RelationshipPolicy{})
	_, err := tool.Run(context.Background(), map[string]any{"operation": "add", "content": "又一条观察"})
	if err == nil {
		t.Fatal("expected capacity error")
	}
	// 满了不是「记不住」，错误本身要指出下一步该干什么。
	if !strings.Contains(err.Error(), "revise") || !strings.Contains(err.Error(), itoa(MaximumActiveSelfNotes)) {
		t.Fatalf("capacity error = %v", err)
	}
}

// 自述的用法写在工具描述里，目录写全文（replyFeaturedTools）；系统提示词不再另写一段，
// 挂不上工具时也不提它。
func TestSelfNoteUsageLivesInToolDefinition(t *testing.T) {
	runtime := selfNoteTestRuntime(t, true, &stubSelfNoteStore{})
	event := MessageEvent{Kind: EventKindGroup, GroupID: "g1", UserID: "1", ProfileID: "bot-1"}

	if !slicesContains(replyFeaturedTools, dianaSelfNoteToolName) {
		t.Fatal("self_note 不在 replyFeaturedTools 里，目录只截一行")
	}
	desc := (*dianaSelfNoteTool)(nil).Description()
	for _, want := range []string{"别人的评价", "人设", "不会生效"} {
		if !strings.Contains(desc, want) {
			t.Fatalf("self_note 描述缺 %q：%s", want, desc)
		}
	}
	head, _ := runtime.systemPromptPartsWithRelationshipAndAgentTools(event, nil, false, RelationshipPolicy{}, true,
		agentToolRegistryWithSelfNote(runtime, event))
	if strings.Contains(head, "自述是你自己写的自我认知") {
		t.Fatalf("系统提示词里不该再另写一段自述用法: %s", head)
	}

	bare := runtime.systemPromptWithRelationshipAndAgentTools(event, nil, false, RelationshipPolicy{}, true, agentToolRegistryWithSelfNote(nil, event))
	if strings.Contains(bare, dianaSelfNoteToolName) {
		t.Fatalf("prompt mentions an unregistered tool: %s", bare)
	}
}
