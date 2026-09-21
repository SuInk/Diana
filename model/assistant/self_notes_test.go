// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
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
}

func (s *stubSelfNoteStore) WriteSelfNote(_ context.Context, request SelfNoteWriteRequest) (SelfNote, error) {
	if s.writeErr != nil {
		return SelfNote{}, s.writeErr
	}
	note := SelfNote{
		ID: "note-" + itoa(len(s.notes)+1), ProfileID: request.ProfileID,
		Topic: request.Topic, Content: request.Content, SupersedesID: request.SupersedesID,
		SourceUserID: request.SourceUserID, Status: SelfNoteStatusActive, Version: 1,
	}
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

func (s *stubSelfNoteStore) ListSelfNotes(_ context.Context, _ string, _ bool, _ int) ([]SelfNote, error) {
	return append([]SelfNote(nil), s.notes...), nil
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
	if block, usage := selfNoteTestRuntime(t, true, nil).selfNoteContext(context.Background(), event); block != "" || usage.Layer != "" {
		t.Fatalf("context without store = %q usage=%#v", block, usage)
	}
	// 开关默认关着：装了存储也不注入，升级不会让机器人突然开始自我改写。
	if block, _ := selfNoteTestRuntime(t, false, store).selfNoteContext(context.Background(), event); block != "" {
		t.Fatalf("disabled context = %q", block)
	}

	block, usage := selfNoteTestRuntime(t, true, store).selfNoteContext(context.Background(), event)
	if !strings.Contains(block, "一句说完就不铺三句") || !strings.Contains(block, "说话方式") {
		t.Fatalf("enabled context = %q", block)
	}
	// 标注必须在：少了「不是用户消息」和「不覆盖人设」，这段就成了越权入口。
	if !strings.HasPrefix(block, selfNoteContextPrefix) {
		t.Fatalf("missing marker: %q", block)
	}
	if !strings.Contains(block, "以人设为准") {
		t.Fatalf("missing persona precedence: %q", block)
	}
	if usage.Layer != "self_notes" || usage.SelectedItems != 1 || usage.Reason != contextLayerReasonFits {
		t.Fatalf("usage = %#v", usage)
	}
}

// 自述是每台机器人一本：拿不到机器人身份时整层关闭，不写也不读那个共享的空桶，
// 否则两台机器人会互相改写对方的自我认知。
func TestSelfNoteRequiresBotIdentity(t *testing.T) {
	store := &stubSelfNoteStore{notes: []SelfNote{{ID: "n1", Topic: "说话方式", Content: "一句说完就不铺三句"}}}
	runtime := selfNoteTestRuntime(t, true, store)
	anonymous := MessageEvent{Kind: EventKindPrivate, UserID: "10001"}

	if block, usage := runtime.selfNoteContext(context.Background(), anonymous); block != "" || usage.Layer != "" {
		t.Fatalf("anonymous context = %q usage=%#v", block, usage)
	}
	tool := newDianaSelfNoteTool(runtime, anonymous, RelationshipPolicy{Owner: true})
	if _, err := tool.Run(context.Background(), map[string]any{"operation": "add", "content": "记一条"}); err == nil {
		t.Fatal("writing without a bot identity should fail")
	}
	if len(store.notes) != 1 {
		t.Fatalf("store mutated: %#v", store.notes)
	}
}

func TestSelfNoteContextStopsAtLayerBudget(t *testing.T) {
	notes := make([]SelfNote, 0, 8)
	for index := 0; index < 8; index++ {
		notes = append(notes, SelfNote{ID: itoa(index), Topic: "说话方式", Content: strings.Repeat("很长的一条自我观察", 6)})
	}
	// 预算刚够开头（约 140 token）加两三行：尾部几条必须被挡在外面，而且要记账。
	block, usage := formatSelfNoteContext(notes, 200)
	if block == "" {
		t.Fatal("expected a truncated block, got none")
	}
	if usage.SelectedItems == 0 || usage.SelectedItems >= len(notes) {
		t.Fatalf("selected = %d of %d", usage.SelectedItems, len(notes))
	}
	if usage.Reason != contextLayerReasonBudget {
		t.Fatalf("reason = %q", usage.Reason)
	}
	// 预算小到一条都装不下时不注入光杆开头：只有标题的块什么信息都不带。
	if block, usage := formatSelfNoteContext(notes, 100); block != "" || usage.SelectedItems != 0 {
		t.Fatalf("tiny budget block = %q usage=%#v", block, usage)
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

// 自述的规则必须进稳定头部、而且只在工具真挂上时才出现：写进尾部会让它每条消息
// 重发一遍，挂不上工具还注入就是白付 token 教一件做不到的事。
func TestSelfNotePromptRuleFollowsToolRegistration(t *testing.T) {
	runtime := selfNoteTestRuntime(t, true, &stubSelfNoteStore{})
	event := MessageEvent{Kind: EventKindGroup, GroupID: "g1", UserID: "1", ProfileID: "bot-1"}

	head, tail := runtime.systemPromptPartsWithRelationshipAndAgentTools(event, nil, false, RelationshipPolicy{}, true,
		agentToolRegistryWithSelfNote(runtime, event))
	if !strings.Contains(head, promptToolSelfNote) {
		t.Fatalf("head missing the self note rule: %s", head)
	}
	if strings.Contains(tail, promptToolSelfNote) {
		t.Fatalf("self note rule leaked into the per-speaker tail: %s", tail)
	}

	bare := runtime.systemPromptWithRelationshipAndAgentTools(event, nil, false, RelationshipPolicy{}, true, agentToolRegistryWithSelfNote(nil, event))
	if strings.Contains(bare, dianaSelfNoteToolName) {
		t.Fatalf("prompt mentions an unregistered tool: %s", bare)
	}
}
