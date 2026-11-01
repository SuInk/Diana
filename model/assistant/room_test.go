// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/SuInk/diana/model/agent"
	"github.com/SuInk/diana/model/llm"
)

type testRoomStore struct {
	values map[string]RoomIntent
	fail   bool
}

func (s *testRoomStore) LoadRoomIntent(_ context.Context, id string) (RoomIntent, error) {
	return s.values[id], nil
}
func (s *testRoomStore) SaveRoomIntent(_ context.Context, id string, v RoomIntent) error {
	if s.fail {
		return errors.New("disk unavailable")
	}
	s.values[id] = v
	return nil
}
func roomTestRuntime() (*Runtime, BotConfig, *testRoomStore) {
	cfg := BotConfig{ID: "one", OwnSpaceEnabled: boolPointer(true)}
	r := NewRuntime(cfg, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	r.SetProfiles(ProfileSet{Profiles: []BotConfig{cfg, {ID: "two", OwnSpaceEnabled: boolPointer(true)}}})
	store := &testRoomStore{values: map[string]RoomIntent{}}
	r.SetRoomStore(store)
	return r, cfg, store
}
func getRoom(t *testing.T, r *Runtime, id string) RoomState {
	t.Helper()
	s, err := r.RoomSnapshot(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestRoomReplyLifecycleConcurrencyAndIsolation(t *testing.T) {
	r, cfg, _ := roomTestRuntime()
	e := MessageEvent{ProfileID: "one"}
	ctx1, finish1 := r.beginRoomReply(context.Background(), cfg, e)
	ctx2, finish2 := r.beginRoomReply(context.Background(), cfg, e)
	s := getRoom(t, r, "one")
	if s.ActiveTasks != 2 || s.Pose != "desk" || s.Source != "runtime" {
		t.Fatalf("started: %+v", s)
	}
	r.observeRoomRun(ctx2, agent.RunEvent{Phase: agent.RunPhaseToolStarted})
	r.observeRoomRun(ctx2, agent.RunEvent{Phase: agent.RunPhaseToolStarted})
	r.observeRoomRun(ctx2, agent.RunEvent{Phase: agent.RunPhaseToolCompleted})
	if s := getRoom(t, r, "one"); s.Phase != "tool" {
		t.Fatalf("parallel tools: %+v", s)
	}
	finish1(nil)
	if s := getRoom(t, r, "one"); s.ActiveTasks != 1 || s.Phase != "tool" {
		t.Fatalf("early finish: %+v", s)
	}
	finish2(nil)
	completed := getRoom(t, r, "one")
	if completed.ActiveTasks != 0 || completed.Phase != "completed" || completed.Pose != "rest" {
		t.Fatalf("completed: %+v", completed)
	}
	finish2(errors.New("late"))
	r.observeRoomRun(ctx1, agent.RunEvent{Phase: agent.RunPhaseToolStarted})
	if getRoom(t, r, "one").Revision != completed.Revision {
		t.Fatal("late events resurrected task")
	}
	if s := getRoom(t, r, "two"); s.ActiveTasks != 0 || s.Pose != "rest" {
		t.Fatalf("other profile: %+v", s)
	}
}

func TestRoomToolPersistentIntentAndFailureAtomicity(t *testing.T) {
	r, cfg, store := roomTestRuntime()
	e := MessageEvent{ProfileID: "one"}
	ctx, finish := r.beginRoomReply(context.Background(), cfg, e)
	tool := &dianaRoomTool{runtime: r, event: e}
	if _, err := tool.Run(ctx, map[string]any{"operation": "set", "pose": "phone"}); err != nil {
		t.Fatal(err)
	}
	if s := getRoom(t, r, "one"); s.Pose != "phone" || s.Source != "agent" || s.ActiveTasks != 1 {
		t.Fatalf("intent: %+v", s)
	}
	finish(nil)
	if getRoom(t, r, "one").Pose != "phone" {
		t.Fatal("finish discarded intent")
	}
	if _, err := tool.Run(ctx, map[string]any{"operation": "set", "pose": "desk"}); err == nil {
		t.Fatal("finished run wrote room")
	}
	store.fail = true
	if _, err := tool.Run(context.Background(), map[string]any{"operation": "set", "pose": "desk"}); err == nil {
		t.Fatal("ignored storage error")
	}
	if getRoom(t, r, "one").Pose != "phone" {
		t.Fatal("failed write changed live state")
	}
	other, _, _ := roomTestRuntime()
	other.SetRoomStore(store)
	restored := getRoom(t, other, "one")
	if restored.Pose != "phone" || restored.ActiveTasks != 0 || restored.Epoch == getRoom(t, r, "one").Epoch {
		t.Fatalf("restart: %+v", restored)
	}
}

func TestRoomToolRejectsInvalidInputAndDisabledSpace(t *testing.T) {
	r, cfg, _ := roomTestRuntime()
	tool := &dianaRoomTool{runtime: r, event: MessageEvent{ProfileID: cfg.ID}}
	for _, input := range []map[string]any{
		{"operation": "set", "pose": "run_command"}, {"operation": "set"}, {"operation": "pay"},
		{"operation": "set", "pose": "desk", "profile": "two"}, {"operation": "set", "pose": "desk", "url": "https://example.com"},
	} {
		if _, err := tool.Run(context.Background(), input); err == nil {
			t.Fatalf("accepted %#v", input)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := tool.Run(ctx, map[string]any{"operation": "set", "pose": "desk"}); err == nil {
		t.Fatal("cancelled write")
	}
	_, finish := r.beginRoomReply(context.Background(), cfg, tool.event)
	cfg.OwnSpaceEnabled = boolPointer(false)
	r.SetProfiles(ProfileSet{Profiles: []BotConfig{cfg}})
	if _, err := tool.Run(context.Background(), map[string]any{"operation": "set", "pose": "desk"}); err == nil {
		t.Fatal("disabled write")
	}
	s := getRoom(t, r, "one")
	if s.ActiveTasks != 0 || s.Enabled {
		t.Fatalf("disabled task: %+v", s)
	}
	finish(nil)
	if _, err := r.RoomSnapshot(context.Background(), "unknown"); err == nil {
		t.Fatal("unknown profile")
	}
}

func TestRoomExpiryCancellationAndConcurrentSnapshots(t *testing.T) {
	r, cfg, _ := roomTestRuntime()
	now := time.Now()
	r.now = func() time.Time { return now }
	ctx, finish := r.beginRoomReply(context.Background(), cfg, MessageEvent{ProfileID: cfg.ID})
	now = now.Add(31 * time.Minute)
	s := getRoom(t, r, cfg.ID)
	if s.ActiveTasks != 0 || s.Phase != "expired" {
		t.Fatalf("expiry: %+v", s)
	}
	r.observeRoomRun(ctx, agent.RunEvent{Phase: agent.RunPhaseToolStarted})
	finish(nil)
	if getRoom(t, r, cfg.ID).Revision != s.Revision {
		t.Fatal("late work revived expired task")
	}
	r.now = time.Now
	cancelCtx, cancel := context.WithCancel(context.Background())
	_, finish = r.beginRoomReply(cancelCtx, cfg, MessageEvent{ProfileID: cfg.ID})
	cancel()
	finish(context.Canceled)
	if getRoom(t, r, cfg.ID).Phase != "cancelled" {
		t.Fatal("cancel not recorded")
	}
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, done := r.beginRoomReply(context.Background(), cfg, MessageEvent{ProfileID: cfg.ID})
			r.observeRoomRun(ctx, agent.RunEvent{Phase: agent.RunPhaseToolStarted})
			_ = r.Status()
			done(nil)
		}()
	}
	wg.Wait()
	if getRoom(t, r, cfg.ID).ActiveTasks != 0 {
		t.Fatal("leaked tasks")
	}
}

func TestRoomGenerateReplyIntegration(t *testing.T) {
	cfg := BotConfig{ID: "one", OwnSpaceEnabled: boolPointer(true), AgentEnabled: true, ReplySafetyMasterEnabled: boolPointer(false)}
	var r *Runtime
	provider := &privacyAwareTestProvider{}
	provider.generate = func(call int, _ llm.GenerateRequest) (string, error) {
		if state := getRoom(t, r, "one"); state.ActiveTasks != 1 || state.Phase != "thinking" {
			t.Fatalf("model call lacks runtime state: %+v", state)
		}
		switch call {
		case 1:
			return `{"action":"tool","tool":"tools_load","input":{"names":["room"]}}`, nil
		case 2:
			return `{"action":"tool","tool":"tools_execute","input":{"name":"room","input":{"operation":"set","pose":"phone"}}}`, nil
		default:
			return `{"action":"final","content":"在小窝看看手机。"}`, nil
		}
	}
	r = NewRuntime(cfg, nilChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) { return provider, nil })
	r.SetRoomStore(&testRoomStore{values: map[string]RoomIntent{}})
	event := MessageEvent{ProfileID: "one", Kind: EventKindGroup, GroupID: "20001", UserID: "10001", MessageID: "30001", ToMe: true}
	registry, err := r.newReplyAgentRegistry(context.Background(), cfg.WithDefaults(), event, RelationshipPolicy{Owner: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer registry.Close()
	_, err = r.generateReply(context.Background(), cfg, event, RelationshipPolicy{Owner: true}, []llm.Message{{Role: llm.RoleUser, Content: "去小窝看看手机"}}, registry)
	if err != nil {
		t.Fatal(err)
	}
	if state := getRoom(t, r, "one"); state.ActiveTasks != 0 || state.Phase != "completed" || state.Pose != "phone" {
		t.Fatalf("reply completion: %+v", state)
	}
}
