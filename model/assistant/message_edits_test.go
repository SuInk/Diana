// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"testing"
	"time"
)

type persistedEditRevisionStore struct {
	persisted      MessageEvent
	appendSessions []string
	appended       []MessageEvent
	deletedIn      []string
	deletedIDs     []string
	savedVectors   int
}

func (s *persistedEditRevisionStore) AppendMessageEvent(_ context.Context, session string, event MessageEvent) error {
	s.appendSessions = append(s.appendSessions, session)
	s.appended = append(s.appended, event)
	return nil
}

func (s *persistedEditRevisionStore) AppendMessageEventRevision(_ context.Context, session string, event MessageEvent) (MessageEvent, bool, error) {
	s.appendSessions = append(s.appendSessions, session)
	s.appended = append(s.appended, event)
	return s.persisted, false, nil
}

func (s *persistedEditRevisionStore) ListRecentMessageEvents(context.Context, string, int) ([]MessageEvent, error) {
	return nil, nil
}

func (s *persistedEditRevisionStore) SaveMessageEventVector(context.Context, string, string, string, []float32) error {
	s.savedVectors++
	return nil
}

func (s *persistedEditRevisionStore) SearchMessageEventsByVector(context.Context, MessageHistoryVectorQuery) ([]MessageEvent, error) {
	return nil, nil
}

func (s *persistedEditRevisionStore) DeleteMessageEventVector(_ context.Context, session, messageID string) error {
	s.deletedIn = append(s.deletedIn, session)
	s.deletedIDs = append(s.deletedIDs, messageID)
	return nil
}

type editAwareInboundStore struct {
	*memoryInboundEventStore
	result       InboundMessageEditRefresh
	refreshCalls int
	lastEdit     MessageEvent
}

func newEditAwareInboundStore(result InboundMessageEditRefresh) *editAwareInboundStore {
	return &editAwareInboundStore{memoryInboundEventStore: newMemoryInboundEventStore(), result: result}
}

func (s *editAwareInboundStore) RefreshInboundMessageEdit(_ context.Context, _ string, event MessageEvent, _ ...int) (InboundMessageEditRefresh, error) {
	s.refreshCalls++
	s.lastEdit = event
	return s.result, nil
}

func TestRuntimeKnownMessageEditStopsBeforeInboundSideEffects(t *testing.T) {
	newRuntime := func(store *editAwareInboundStore) *Runtime {
		runtime := NewRuntime(BotConfig{
			ID:               "telegram-bot",
			BotAccount:       "42",
			Platform:         PlatformTelegram,
			PrivateAdmission: PrivateAdmission{Mode: PrivateAdmissionAll},
		}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
		runtime.SetInboundEventStore(store)
		return runtime
	}

	t.Run("group membership governance and turn tracking", func(t *testing.T) {
		store := newEditAwareInboundStore(InboundMessageEditRefresh{EventID: "event-1", Session: "group:-100", Found: true, Applied: true})
		runtime := newRuntime(store)
		runtime.SetGroupConfigStore(&stubGroupConfigStore{configs: map[string]GroupConfig{
			"-100": {
				GroupID:    "-100",
				Enabled:    true,
				EnabledSet: true,
				Governance: &GroupGovernance{AntiSpamEnabled: true, SpamMaxMessages: 100},
			},
		}})
		event := MessageEvent{
			Platform:    PlatformTelegram,
			ProfileID:   "telegram-bot",
			SelfID:      "42",
			Kind:        EventKindGroup,
			GroupID:     "-100",
			UserID:      "100",
			MessageID:   "55",
			Time:        time.Now().Unix(),
			Edited:      true,
			EditTime:    time.Now().Unix() + 1,
			RawMessage:  "edited text",
			Segments:    []MessageSegment{{Type: "text", Data: map[string]string{"text": "edited text"}}},
			SenderLevel: 9,
			SenderRole:  "member",
			ToMe:        true,
		}

		if err := runtime.HandleEvent(context.Background(), event); err != nil {
			t.Fatalf("HandleEvent: %v", err)
		}
		if store.refreshCalls != 1 || store.lastEdit.MessageID != event.MessageID {
			t.Fatalf("edit refresh calls=%d last=%#v", store.refreshCalls, store.lastEdit)
		}
		store.mu.Lock()
		enqueued := len(store.order)
		store.mu.Unlock()
		if enqueued != 0 {
			t.Fatalf("known edit enqueued as a new turn: %d", enqueued)
		}
		if _, ok := runtime.members.lookup(event.GroupID, event.UserID); ok {
			t.Fatal("known edit refreshed the member cache")
		}
		runtime.governance.mu.Lock()
		governanceMembers := len(runtime.governance.members)
		governanceBlocked := len(runtime.governance.blocked)
		runtime.governance.mu.Unlock()
		if governanceMembers != 0 || governanceBlocked != 0 {
			t.Fatalf("known edit reached governance: members=%d blocked=%d", governanceMembers, governanceBlocked)
		}
		runtime.replyInterruptMu.Lock()
		directed := len(runtime.latestDirectedInbound)
		turns := len(runtime.senderTurns)
		runtime.replyInterruptMu.Unlock()
		if directed != 0 || turns != 0 {
			t.Fatalf("known edit registered as a new user turn: directed=%d sender_turns=%d", directed, turns)
		}
	})

	t.Run("private interceptor", func(t *testing.T) {
		// A stale duplicate edit is still a known transport message. Even when its
		// payload loses the revision check, it must not become a fresh user turn.
		store := newEditAwareInboundStore(InboundMessageEditRefresh{EventID: "event-2", Session: "private:100", Found: true, Applied: false})
		runtime := newRuntime(store)
		intercepts := 0
		runtime.SetPrivateMessageInterceptor(func(context.Context, MessageEvent, string) bool {
			intercepts++
			return false
		})
		event := MessageEvent{
			Platform:   PlatformTelegram,
			ProfileID:  "telegram-bot",
			SelfID:     "42",
			Kind:       EventKindPrivate,
			UserID:     "100",
			MessageID:  "56",
			Time:       time.Now().Unix(),
			Edited:     true,
			EditTime:   time.Now().Unix() + 1,
			RawMessage: "edited private text",
			Segments:   []MessageSegment{{Type: "text", Data: map[string]string{"text": "edited private text"}}},
		}

		if err := runtime.HandleEvent(context.Background(), event); err != nil {
			t.Fatalf("HandleEvent: %v", err)
		}
		if intercepts != 0 {
			t.Fatalf("known edit reached the private interceptor %d times", intercepts)
		}
		runtime.replyInterruptMu.Lock()
		directed := len(runtime.latestDirectedInbound)
		turns := len(runtime.senderTurns)
		runtime.replyInterruptMu.Unlock()
		if directed != 0 || turns != 0 {
			t.Fatalf("known private edit registered as a new user turn: directed=%d sender_turns=%d", directed, turns)
		}
	})

	t.Run("first seen edit follows ordinary enqueue", func(t *testing.T) {
		store := newEditAwareInboundStore(InboundMessageEditRefresh{})
		runtime := newRuntime(store)
		event := MessageEvent{
			Platform: PlatformTelegram, ProfileID: "telegram-bot", SelfID: "42",
			Kind: EventKindGroup, GroupID: "-100", UserID: "100", MessageID: "57",
			Time: time.Now().Unix(), Edited: true, EditTime: time.Now().Unix() + 1,
			RawMessage: "first visible revision",
			Segments:   []MessageSegment{{Type: "text", Data: map[string]string{"text": "first visible revision"}}},
		}

		if err := runtime.HandleEvent(context.Background(), event); err != nil {
			t.Fatalf("HandleEvent: %v", err)
		}
		store.mu.Lock()
		enqueued := len(store.order)
		store.mu.Unlock()
		if store.refreshCalls != 1 || enqueued != 1 {
			t.Fatalf("first-seen edit refresh_calls=%d enqueued=%d, want 1/1", store.refreshCalls, enqueued)
		}
	})
}

func TestRuntimeRefreshRememberedMessageEditPreservesBufferPosition(t *testing.T) {
	runtime := NewRuntime(BotConfig{ID: "telegram-bot", Platform: PlatformTelegram}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	first := messageEditTestEvent("1", "first", 100)
	original := messageEditTestEvent("2", "original", 200)
	last := messageEditTestEvent("3", "last", 300)
	edited := original
	edited.Edited = true
	edited.EditTime = 250
	edited.RawMessage = "edited"
	edited.Segments = []MessageSegment{{Type: "text", Data: map[string]string{"text": "edited"}}}
	session := sessionKey(original)
	promptKey := groupPromptSessionKey(original)
	runtime.mu.Lock()
	runtime.history[session] = []MessageEvent{first, original, last}
	runtime.groupPromptHistory = map[string]groupPromptHistoryBuffer{
		promptKey: {Session: session, Events: []MessageEvent{first, original, last}},
	}
	runtime.mu.Unlock()

	runtime.refreshRememberedMessageEdit(edited, session)

	runtime.mu.RLock()
	history := append([]MessageEvent(nil), runtime.history[session]...)
	promptHistory := append([]MessageEvent(nil), runtime.groupPromptHistory[promptKey].Events...)
	runtime.mu.RUnlock()
	assertEditedMessageOrder(t, history, "1", "2", "3")
	assertEditedMessageOrder(t, promptHistory, "1", "2", "3")
}

func TestRuntimeRefreshRememberedMessageEditDoesNotReinsertEvictedMessage(t *testing.T) {
	runtime := NewRuntime(BotConfig{ID: "telegram-bot", Platform: PlatformTelegram}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	first := messageEditTestEvent("1", "first", 100)
	last := messageEditTestEvent("3", "last", 300)
	edited := messageEditTestEvent("2", "edited", 200)
	edited.Edited = true
	edited.EditTime = 250
	session := sessionKey(edited)
	promptKey := groupPromptSessionKey(edited)
	runtime.mu.Lock()
	runtime.history[session] = []MessageEvent{first, last}
	runtime.groupPromptHistory = map[string]groupPromptHistoryBuffer{
		promptKey: {Session: session, Events: []MessageEvent{first, last}},
	}
	runtime.mu.Unlock()

	runtime.refreshRememberedMessageEdit(edited, session)

	runtime.mu.RLock()
	history := append([]MessageEvent(nil), runtime.history[session]...)
	promptHistory := append([]MessageEvent(nil), runtime.groupPromptHistory[promptKey].Events...)
	runtime.mu.RUnlock()
	assertMessageOrder(t, history, "1", "3")
	assertMessageOrder(t, promptHistory, "1", "3")
}

func TestRuntimeRememberUsesNewestEditAfterWorkerClaimedOldSnapshot(t *testing.T) {
	runtime := NewRuntime(BotConfig{ID: "telegram-bot", Platform: PlatformTelegram}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	original := messageEditTestEvent("2", "original", 200)
	edited := original
	edited.Edited = true
	edited.EditTime = 250
	edited.TelegramUpdateID = 101
	edited.RawMessage = "edited"
	edited.Segments = []MessageSegment{{Type: "text", Data: map[string]string{"text": "edited"}}}
	staleEdit := edited
	staleEdit.TelegramUpdateID = 100
	staleEdit.RawMessage = "stale same-second edit"
	staleEdit.Segments = []MessageSegment{{Type: "text", Data: map[string]string{"text": staleEdit.RawMessage}}}
	legacyStaleEdit := staleEdit
	legacyStaleEdit.TelegramUpdateID = 0
	legacyStaleEdit.RawMessage = "stale edit without update id"
	legacyStaleEdit.Segments = []MessageSegment{{Type: "text", Data: map[string]string{"text": legacyStaleEdit.RawMessage}}}

	// The durable edit lands after workers have claimed the original payload and
	// an older same-second edit. Neither stale snapshot may replace it in memory.
	runtime.refreshRememberedMessageEdit(edited, sessionKey(edited))
	runtime.remember(staleEdit)
	runtime.remember(legacyStaleEdit)
	runtime.remember(original)

	runtime.mu.RLock()
	history := append([]MessageEvent(nil), runtime.history[sessionKey(edited)]...)
	promptHistory := append([]MessageEvent(nil), runtime.groupPromptHistory[groupPromptSessionKey(edited)].Events...)
	runtime.mu.RUnlock()
	assertEditedMessageOrder(t, history, "2")
	assertEditedMessageOrder(t, promptHistory, "2")
	if history[0].TelegramUpdateID != edited.TelegramUpdateID || promptHistory[0].TelegramUpdateID != edited.TelegramUpdateID {
		t.Fatalf("stale same-second edit won: history=%+v prompt=%+v", history[0], promptHistory[0])
	}
}

func TestRuntimeEditTimeOutranksTelegramUpdateIDAcrossSeconds(t *testing.T) {
	runtime := NewRuntime(BotConfig{ID: "telegram-bot", Platform: PlatformTelegram}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	current := messageEditTestEvent("2", "current revision", 200)
	current.Edited = true
	current.EditTime = 250
	current.TelegramUpdateID = 500
	runtime.refreshRememberedMessageEdit(current, sessionKey(current))

	olderWithHigherID := current
	olderWithHigherID.EditTime = 249
	olderWithHigherID.TelegramUpdateID = 999
	olderWithHigherID.RawMessage = "older edit after update-id reset"
	olderWithHigherID.Segments = []MessageSegment{{Type: "text", Data: map[string]string{"text": olderWithHigherID.RawMessage}}}
	runtime.remember(olderWithHigherID)

	newerWithLowerID := current
	newerWithLowerID.EditTime = 251
	newerWithLowerID.TelegramUpdateID = 1
	newerWithLowerID.RawMessage = "newer edit after update-id reset"
	newerWithLowerID.Segments = []MessageSegment{{Type: "text", Data: map[string]string{"text": newerWithLowerID.RawMessage}}}
	runtime.remember(newerWithLowerID)

	runtime.mu.RLock()
	history := append([]MessageEvent(nil), runtime.history[sessionKey(current)]...)
	revision := runtime.latestMessageEdits[messageEditRevisionKey(current)].Event
	runtime.mu.RUnlock()
	if len(history) != 1 || history[0].RawMessage != newerWithLowerID.RawMessage || history[0].EditTime != newerWithLowerID.EditTime {
		t.Fatalf("runtime history chose wrong cross-second revision: %#v", history)
	}
	if revision.RawMessage != newerWithLowerID.RawMessage || revision.EditTime != newerWithLowerID.EditTime {
		t.Fatalf("runtime revision guard chose wrong cross-second revision: %#v", revision)
	}
}

func TestRuntimePersistOldWorkerSnapshotUsesDurableEditedRevision(t *testing.T) {
	original := messageEditTestEvent("2", "original", 200)
	original.ContextNamespace = "current-profile"
	original.durableSession = "group:-100"
	edited := original
	edited.Edited = true
	edited.EditTime = 250
	edited.EditRefreshOnly = true
	edited.RawMessage = "edited"
	edited.Segments = []MessageSegment{{Type: "text", Data: map[string]string{"text": "edited"}}}
	edited.durableSession = ""

	store := &persistedEditRevisionStore{persisted: edited}
	embedCalls := 0
	runtime := newSemanticRuntime(t, true, nil, &embedCalls)
	// Seed the live, namespaced history before attaching the durable store. This
	// models a worker that restored and remembered the old processing snapshot.
	runtime.remember(original)
	runtime.SetMessageHistoryStore(store)
	runtime.semanticIndexQueue = make(chan semanticIndexItem, semanticIndexQueueSize)
	runtime.semanticIndexOnce.Do(func() {})
	indexerDone := make(chan struct{})
	go func() {
		runtime.runSemanticIndexer()
		close(indexerDone)
	}()

	runtime.persistMessageEvent(original)
	close(runtime.semanticIndexQueue)
	select {
	case <-indexerDone:
	case <-time.After(2 * time.Second):
		t.Fatal("semantic indexer did not flush the edit tombstone")
	}

	if len(store.appendSessions) != 1 || store.appendSessions[0] != "group:-100" {
		t.Fatalf("persist sessions=%v, want legacy durable session", store.appendSessions)
	}
	if len(store.appended) != 1 || store.appended[0].Edited || store.appended[0].RawMessage != "original" {
		t.Fatalf("revision store did not receive the old worker snapshot: %#v", store.appended)
	}
	if embedCalls != 0 || store.savedVectors != 0 {
		t.Fatalf("persisted edit queued embedding work: embeds=%d saves=%d", embedCalls, store.savedVectors)
	}
	if len(store.deletedIn) != 1 || store.deletedIn[0] != "group:-100" || len(store.deletedIDs) != 1 || store.deletedIDs[0] != original.MessageID {
		t.Fatalf("edit tombstones sessions=%v ids=%v, want legacy session and message %q", store.deletedIn, store.deletedIDs, original.MessageID)
	}

	runtime.mu.RLock()
	history := append([]MessageEvent(nil), runtime.history[sessionKey(original)]...)
	promptHistory := append([]MessageEvent(nil), runtime.groupPromptHistory[groupPromptSessionKey(original)].Events...)
	revision := runtime.latestMessageEdits[messageEditRevisionKey(original)].Event
	runtime.mu.RUnlock()
	assertEditedMessageOrder(t, history, "2")
	assertEditedMessageOrder(t, promptHistory, "2")
	if revision.RawMessage != "edited" || revision.durableSession != "group:-100" {
		t.Fatalf("runtime revision=%#v, want persisted edit in legacy durable session", revision)
	}
}

func messageEditTestEvent(messageID, text string, eventTime int64) MessageEvent {
	return MessageEvent{
		Platform:   PlatformTelegram,
		ProfileID:  "telegram-bot",
		SelfID:     "42",
		Kind:       EventKindGroup,
		GroupID:    "-100",
		UserID:     "100",
		MessageID:  messageID,
		Time:       eventTime,
		RawMessage: text,
		Segments:   []MessageSegment{{Type: "text", Data: map[string]string{"text": text}}},
	}
}

func assertEditedMessageOrder(t *testing.T, events []MessageEvent, ids ...string) {
	t.Helper()
	assertMessageOrder(t, events, ids...)
	for _, event := range events {
		if event.MessageID == "2" {
			if !event.Edited || event.EditTime != 250 || event.RawMessage != "edited" {
				t.Fatalf("message 2 was not replaced with its edit: %#v", event)
			}
			return
		}
	}
	t.Fatal("edited message 2 is missing")
}

func assertMessageOrder(t *testing.T, events []MessageEvent, ids ...string) {
	t.Helper()
	if len(events) != len(ids) {
		t.Fatalf("message count=%d want=%d: %#v", len(events), len(ids), events)
	}
	for index, id := range ids {
		if events[index].MessageID != id {
			t.Fatalf("message order at %d=%q want=%q: %#v", index, events[index].MessageID, id, events)
		}
	}
}
