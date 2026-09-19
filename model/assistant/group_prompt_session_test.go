package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/SuInk/diana/model/llm"
)

func TestGroupHistoryDoesNotShrinkAtLegacyMessageThreshold(t *testing.T) {
	r := NewRuntime(BotConfig{RecentContextLimit: 3, ContextSummaryThreshold: 5}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	e := MessageEvent{Kind: EventKindGroup, GroupID: "one", UserID: "member", Time: 1000}
	for i := 0; i < 12; i++ {
		item := e
		item.Time = int64(i + 1)
		item.MessageID = fmt.Sprint(i)
		item.RawMessage = "short public message"
		r.remember(item)
	}
	e.MessageID = "current"
	if history := r.promptContextHistory(e, r.effectiveConfigForEvent(e)); len(history) != 12 {
		t.Fatalf("warm history prematurely shrank to %d", len(history))
	}
	if err := r.clearSessionHistory(e); err != nil {
		t.Fatal(err)
	}
	if history := r.promptContextHistory(e, r.effectiveConfigForEvent(e)); len(history) != 0 {
		t.Fatal("cleared group restored history")
	}
}

func TestPromptCacheRoutingScope(t *testing.T) {
	e := MessageEvent{Kind: EventKindGroup, Platform: PlatformOneBotV11, ProfileID: "bot", SelfID: "self", GroupID: "sensitive-group", UserID: "alice"}
	key := promptCacheRoutingKey(e, "reply")
	e.UserID = "bob"
	if key != promptCacheRoutingKey(e, "reply") {
		t.Fatal("sender switch changed group route")
	}
	if key == promptCacheRoutingKey(e, "memory") {
		t.Fatal("different purposes share route")
	}
	if strings.Contains(key, e.GroupID) || len(key) > 64 {
		t.Fatal("routing key is not opaque/bounded")
	}
	e.GroupID = "other"
	if key == promptCacheRoutingKey(e, "reply") {
		t.Fatal("group route leaked")
	}
}

func TestGroupPromptRejectsForeignBotWithoutContextNamespace(t *testing.T) {
	r := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	first := MessageEvent{Kind: EventKindGroup, ProfileID: "bot-one", GroupID: "same-number", MessageID: "source", UserID: "member", RawMessage: "not for bot two", Time: 1}
	r.remember(first)
	second := first
	second.ProfileID = "bot-two"
	second.MessageID = "current"
	second.Time = 2
	if history := r.promptContextHistory(second, r.effectiveConfigForEvent(second)); len(history) != 0 {
		t.Fatal("foreign profile leaked through short history fallback")
	}
	stable, _ := r.stableGroupHistory(context.Background(), second, r.effectiveConfigForEvent(second), []MessageEvent{first}, true, nil)
	if len(stable) != 0 {
		t.Fatal("foreign profile persisted")
	}
}

type promptSessionTestStore struct {
	*memoryMessageHistoryStore
	mu     sync.Mutex
	states map[string]GroupPromptSession
	loads  int
}

func (s *promptSessionTestStore) LoadGroupPromptSession(_ context.Context, key, _ string) (GroupPromptSession, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loads++
	var out GroupPromptSession
	data, _ := json.Marshal(s.states[key])
	_ = json.Unmarshal(data, &out)
	return out, nil
}
func (s *promptSessionTestStore) SaveGroupPromptSession(_ context.Context, key, _ string, state GroupPromptSession) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out GroupPromptSession
	data, _ := json.Marshal(state)
	_ = json.Unmarshal(data, &out)
	s.states[key] = out
	return true, nil
}

func TestGroupPromptCheckpointPersistsAndRestores(t *testing.T) {
	store := &promptSessionTestStore{memoryMessageHistoryStore: newMemoryMessageHistoryStore(), states: map[string]GroupPromptSession{}}
	event := MessageEvent{Kind: EventKindGroup, Platform: PlatformOneBotV11, ProfileID: "profile", SelfID: "bot", GroupID: "group"}
	first := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	first.SetMessageHistoryStore(store)
	if got := first.groupPromptSession(event).rememberCheckpoint("Alice 负责上线，Bob 负责回归。"); got == "" {
		t.Fatal("checkpoint was not stored")
	}

	second := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	second.SetMessageHistoryStore(store)
	if got := second.groupPromptSession(event).rememberCheckpoint(""); got != "Alice 负责上线，Bob 负责回归。" {
		t.Fatalf("restored checkpoint = %q", got)
	}
}

func TestGroupPromptSurvivesRestartAndKeepsCrossGroupInTail(t *testing.T) {
	ctx := context.Background()
	store := &promptSessionTestStore{memoryMessageHistoryStore: newMemoryMessageHistoryStore(), states: map[string]GroupPromptSession{}}
	cfg := BotConfig{BotAccount: "bot", CrossGroupMemoryEnabled: boolPointer(true)}.WithDefaults()
	newRuntime := func() *Runtime {
		r := NewRuntime(cfg, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
		r.SetMessageHistoryStore(store)
		return r
	}
	r := newRuntime()
	current := MessageEvent{Kind: EventKindGroup, Platform: PlatformOneBotV11, ProfileID: "profile", SelfID: "bot", GroupID: "one", UserID: "alice", MessageID: "current", Time: 100}
	first := current
	first.MessageID = "old"
	first.Time = 50
	first.RawMessage = "共享旧消息"
	cross := first
	cross.GroupID = "two"
	cross.MessageID = "cross"
	cross.crossGroupContext = true
	cross.RawMessage = "外群资料"
	a, tail := r.stableGroupHistory(ctx, current, cfg, []MessageEvent{cross, first}, true, nil)
	if len(a) != 1 || len(tail) != 1 || strings.Contains(a[0].Content, "外群") {
		t.Fatalf("stable=%v tail=%v", a, tail)
	}
	s := r.groupPromptSession(current)
	s.rememberTools([]string{"read_file", "search"})
	s.rememberTools([]string{"search", "render"})
	s.rememberAnchor(messageHistoryDedupeKey(first))
	if store.loads != 1 {
		t.Fatalf("loaded %d times", store.loads)
	}
	for _, entry := range s.data.History {
		if strings.Contains(entry.Messages[0].Content, "外群") {
			t.Fatal("cross-group memory persisted")
		}
	}
	r = newRuntime()
	current.UserID = "bob" // public group history/tool discovery belongs to the group
	second := first
	second.MessageID = "new"
	second.Time = 75
	second.RawMessage = "新增群消息"
	cross.RawMessage = "本轮其他检索结果"
	b, tail := r.stableGroupHistory(ctx, current, cfg, []MessageEvent{first, cross, second}, true, nil)
	if len(b) != 2 || len(tail) != 1 || promptCacheCanonicalMessage(a[0]) != promptCacheCanonicalMessage(b[0]) {
		t.Fatalf("prefix changed: %v %v", a, b)
	}
	s = r.groupPromptSession(current)
	if !reflect.DeepEqual(s.loadedTools(), []string{"read_file", "search", "render"}) || s.anchor() != messageHistoryDedupeKey(first) {
		t.Fatalf("restart lost state: %+v", s.data)
	}
	for _, change := range []func(*MessageEvent){func(e *MessageEvent) { e.GroupID = "other" }, func(e *MessageEvent) { e.ProfileID = "other" }, func(e *MessageEvent) { e.Platform = PlatformTelegram }, func(e *MessageEvent) { e.SelfID = "other" }, func(e *MessageEvent) { e.ContextNamespace = "other" }} {
		other := current
		change(&other)
		if len(r.groupPromptSession(other).loadedTools()) != 0 {
			t.Fatal("session leaked tools")
		}
	}
	cfg.CrossGroupMemoryEnabled = boolPointer(false)
	_, tail = r.stableGroupHistory(ctx, current, cfg, []MessageEvent{first, cross, second}, true, nil)
	if len(tail) != 0 {
		t.Fatal("disabled cross-group data retained")
	}
}

func TestGroupPromptLateHistoryAppendsAndDeletedOrEditedHistoryInvalidates(t *testing.T) {
	r := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	e := MessageEvent{Kind: EventKindGroup, GroupID: "one", MessageID: "current", Time: 100}
	makeEvent := func(id, text string, at int64) MessageEvent {
		v := e
		v.MessageID = id
		v.RawMessage = text
		v.Time = at
		return v
	}
	a := makeEvent("a", "first", 50)
	b := makeEvent("b", "second", 60)
	late := makeEvent("late", "backfill", 40)
	project := func(items ...MessageEvent) []llm.Message {
		out, _ := r.stableGroupHistory(context.Background(), e, r.effectiveConfigForEvent(e), items, true, nil)
		return out
	}
	first := project(a, b)
	next := project(late, a, b)
	if len(next) != 3 || first[0].Content != next[0].Content || first[1].Content != next[1].Content || !strings.Contains(next[2].Content, "backfill") {
		t.Fatalf("late insertion broke prefix: %v", next)
	}
	a.RawMessage = "edited"
	next = project(a, b)
	if len(next) != 2 || !strings.Contains(next[0].Content, "edited") {
		t.Fatalf("edit/removal stale: %v", next)
	}
	if next = project(b); len(next) != 1 || strings.Contains(next[0].Content, "edited") {
		t.Fatal("deleted text retained")
	}
}

func TestGroupPromptConcurrentToolsAndReset(t *testing.T) {
	r := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	e := MessageEvent{Kind: EventKindGroup, GroupID: "one"}
	s := r.groupPromptSession(e)
	var wg sync.WaitGroup
	for _, name := range []string{"a", "b", "c", "a"} {
		wg.Add(1)
		go func(name string) { defer wg.Done(); s.rememberTools([]string{name}) }(name)
	}
	wg.Wait()
	if len(s.loadedTools()) != 3 {
		t.Fatal("concurrent load lost")
	}
	if err := r.clearSessionHistory(e); err != nil {
		t.Fatal(err)
	}
	s.rememberTools([]string{"late_callback"})
	if len(r.groupPromptSession(e).loadedTools()) != 0 {
		t.Fatal("in-flight run resurrected reset state")
	}
}
