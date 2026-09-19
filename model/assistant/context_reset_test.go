package assistant

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type resetHistoryTestStore struct {
	*memoryMessageHistoryStore
	offsets  map[string]int
	resetErr error
}

func (s *resetHistoryTestStore) ResetContextHistory(_ context.Context, session string, _ time.Time) error {
	if s.resetErr != nil {
		return s.resetErr
	}
	s.offsets[session] = len(s.events[session])
	return nil
}
func (s *resetHistoryTestStore) ListContextMessageEvents(_ context.Context, session string, limit int) ([]MessageEvent, error) {
	events := s.events[session][s.offsets[session]:]
	if limit > 0 && len(events) > limit {
		events = events[len(events)-limit:]
	}
	return append([]MessageEvent(nil), events...), nil
}

func TestContextResetExcludesPersistentHistoryFromBothPromptPaths(t *testing.T) {
	store := &resetHistoryTestStore{memoryMessageHistoryStore: newMemoryMessageHistoryStore(), offsets: map[string]int{}}
	runtime := NewRuntime(BotConfig{OwnerID: "owner"}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.SetMessageHistoryStore(store)
	event := MessageEvent{Kind: EventKindGroup, ProfileID: "bot", GroupID: "one", UserID: "owner", MessageID: "old", Time: time.Now().Unix(), RawMessage: "旧话题"}
	other := event
	other.GroupID = "two"
	runtime.remember(event)
	runtime.remember(other)
	session := sessionKey(event)
	runtime.contextSummaries[session] = "旧摘要"
	runtime.agentCarryovers = map[string]agentRunCarryover{agentCarryoverKey(event): {entries: []string{"旧任务"}, at: time.Now()}}
	runtime.beginReplyTurn(event, time.Now())
	command := event
	command.MessageID = "reset"
	reply, handled := runtime.handleOwnerCommand(command, "清空上下文")
	if !handled || !strings.Contains(reply, "已清空") {
		t.Fatalf("reply=%q handled=%v", reply, handled)
	}
	if runtime.contextSummary(event) != "" {
		t.Fatal("old summary retained")
	}
	if _, ok := runtime.agentCarryoverMessage(event); ok {
		t.Fatal("old agent task retained")
	}
	if _, ok := runtime.beginReplyTurn(event, time.Now()); ok {
		t.Fatal("old reply retained")
	}
	for _, restarted := range []bool{false, true} {
		if restarted {
			runtime = NewRuntime(BotConfig{OwnerID: "owner"}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
			runtime.SetMessageHistoryStore(store)
		}
		if got := runtime.contextHistory(command); len(got) != 0 {
			t.Fatalf("restarted=%v recent=%v", restarted, got)
		}
		if got := runtime.promptContextHistory(command, runtime.ProfileConfig("")); len(got) != 0 {
			t.Fatalf("restarted=%v prompt=%v", restarted, got)
		}
		if got := runtime.contextHistory(other); len(got) != 1 {
			t.Fatalf("other session lost: %v", got)
		}
	}
	if len(store.events[session]) != 1 {
		t.Fatal("archive deleted")
	}
	next := event
	next.MessageID = "new"
	next.RawMessage = "新话题"
	runtime.remember(next)
	if got := runtime.contextHistory(command); len(got) != 1 || got[0].MessageID != "new" {
		t.Fatalf("new recent=%v", got)
	}
	if got := runtime.promptContextHistory(command, runtime.ProfileConfig("")); len(got) != 1 || got[0].MessageID != "new" {
		t.Fatalf("new prompt=%v", got)
	}
}

func TestContextResetFailureDoesNotClaimSuccessOrClearMemory(t *testing.T) {
	runtime := NewRuntime(BotConfig{OwnerID: "owner"}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	store := &resetHistoryTestStore{memoryMessageHistoryStore: newMemoryMessageHistoryStore(), resetErr: errors.New("write failed")}
	runtime.SetMessageHistoryStore(store)
	event := MessageEvent{Kind: EventKindPrivate, UserID: "owner", MessageID: "old", RawMessage: "保留"}
	runtime.remember(event)
	reply, handled := runtime.handleOwnerCommand(event, "清空上下文")
	if !handled || !strings.Contains(reply, "失败") {
		t.Fatalf("reply=%q", reply)
	}
	if len(runtime.history[sessionKey(event)]) != 1 {
		t.Fatal("memory cleared after failed durable reset")
	}
	stranger := event
	stranger.UserID = "stranger"
	if _, handled := runtime.handleOwnerCommand(stranger, "清空上下文"); handled {
		t.Fatal("non-owner reset accepted")
	}
}

func TestContextResetOwnerGroupCommandDoesNotRequireMention(t *testing.T) {
	runtime := NewRuntime(BotConfig{OwnerID: "owner"}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	event := MessageEvent{Kind: EventKindGroup, GroupID: "one", UserID: "owner"}
	for _, command := range []string{"清空上下文", "清除上下文"} {
		if !runtime.shouldHandleChatTrigger(event, command) {
			t.Fatalf("owner command did not trigger: %s", command)
		}
		event.UserID = "stranger"
		if runtime.isOwnerContextResetCommand(event, command) {
			t.Fatal("non-owner command accepted")
		}
		event.UserID = "owner"
	}
}

func TestContextResetChecksCurrentProfileOwner(t *testing.T) {
	runtime := NewRuntime(BotConfig{OwnerID: "default-owner"}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.profileConfigs = map[string]BotConfig{"other": {OwnerID: "profile-owner"}}
	event := MessageEvent{Kind: EventKindPrivate, ProfileID: "other", UserID: "default-owner"}
	if _, handled := runtime.handleOwnerCommand(event, "清空上下文"); handled {
		t.Fatal("default profile owner must not reset another profile")
	}
	event.UserID = "profile-owner"
	if reply, handled := runtime.handleOwnerCommand(event, "清空上下文"); !handled || !strings.Contains(reply, "已清空") {
		t.Fatalf("profile owner reset: %q handled=%v", reply, handled)
	}
}
