// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"
)

func TestCrossGroupContextMergesRelatedMessageWhenAuthorIsInTargetGroup(t *testing.T) {
	channel := &crossGroupMembershipChannel{allowed: map[string]bool{"current|speaker": true}}
	store := &crossGroupHistoryStore{candidates: []MessageEvent{
		crossGroupTestEvent(190, "shared", "speaker", "related", "图片缓存修复已经部署到测试机器"),
		crossGroupTestEvent(191, "shared", "other", "other-user", "图片缓存修复也需要重启"),
		crossGroupTestEvent(192, "unrelated", "speaker", "unrelated", "今天晚饭吃什么"),
		crossGroupTestEvent(193, "current", "speaker", "same-group", "图片缓存修复当前群消息"),
	}}
	runtime := NewRuntime(BotConfig{CrossGroupMemoryEnabled: boolPointer(true)}, channel, NewPluginManager(), nil, nil, nil, nil)
	runtime.SetMessageHistoryStore(store)
	current := crossGroupTestEvent(200, "current", "requester", "current", "图片缓存修复现在怎么样")
	current.ContextNamespace = "bot-a"
	current.Platform = PlatformOneBotV11
	runtime.remember(current)

	history := runtime.contextHistory(current)
	if len(history) != 2 {
		t.Fatalf("history=%#v", history)
	}
	cross := history[0]
	if !cross.crossGroupContext || cross.GroupID != "shared" || cross.UserID != "speaker" {
		t.Fatalf("cross-group event=%#v", cross)
	}
	if cross.MessageID != "" || cross.Quoted != nil || len(cross.SemanticSourceMessageIDs) != 0 || len(cross.Segments) != 1 || cross.Segments[0].Type != "text" {
		t.Fatalf("cross-group event retained private linkage or media: %#v", cross)
	}
	prompt := historyPromptTextAt(cross, current.Time)
	if !strings.Contains(prompt, "跨群参考") || strings.Contains(prompt, "shared") {
		t.Fatalf("cross-group prompt=%q", prompt)
	}
	if !store.query.CrossSession || store.query.SessionPrefix != "bot-a:group:" || store.query.Session != "bot-a:group:current" {
		t.Fatalf("search query=%#v", store.query)
	}
	if calls := channel.callsSnapshot(); strings.Join(calls, ",") != "current|other,current|speaker" {
		t.Fatalf("membership calls=%#v", calls)
	}
	_ = runtime.contextHistory(current)
	if calls := channel.callsSnapshot(); countString(calls, "current|speaker") != 1 {
		t.Fatalf("successful membership cache missed: calls=%#v", calls)
	}
}

func TestCrossGroupContextDefaultsToIsolatedAndFailsClosed(t *testing.T) {
	candidate := crossGroupTestEvent(190, "shared", "speaker", "related", "图片缓存修复已经部署")
	current := crossGroupTestEvent(200, "current", "speaker", "current", "图片缓存修复现在怎么样")

	disabledStore := &crossGroupHistoryStore{candidates: []MessageEvent{candidate}}
	disabled := NewRuntime(BotConfig{}, &crossGroupMembershipChannel{allowed: map[string]bool{"current|speaker": true}}, NewPluginManager(), nil, nil, nil, nil)
	disabled.SetMessageHistoryStore(disabledStore)
	disabled.remember(current)
	if history := disabled.contextHistory(current); len(history) != 1 || disabledStore.searchCalls != 0 {
		t.Fatalf("disabled history=%#v search_calls=%d", history, disabledStore.searchCalls)
	}

	deniedStore := &crossGroupHistoryStore{candidates: []MessageEvent{candidate}}
	denied := NewRuntime(BotConfig{CrossGroupMemoryEnabled: boolPointer(true)}, &crossGroupMembershipChannel{}, NewPluginManager(), nil, nil, nil, nil)
	denied.SetMessageHistoryStore(deniedStore)
	denied.remember(current)
	if history := denied.contextHistory(current); len(history) != 1 {
		t.Fatalf("membership failure leaked cross-group history: %#v", history)
	}
}

func TestCrossGroupContextRequiresMeaningfulTopicOverlap(t *testing.T) {
	channel := &crossGroupMembershipChannel{allowed: map[string]bool{"current|speaker": true}}
	store := &crossGroupHistoryStore{candidates: []MessageEvent{
		crossGroupTestEvent(190, "shared", "speaker", "weak", "这个事情可以处理"),
	}}
	runtime := NewRuntime(BotConfig{CrossGroupMemoryEnabled: boolPointer(true)}, channel, NewPluginManager(), nil, nil, nil, nil)
	runtime.SetMessageHistoryStore(store)
	current := crossGroupTestEvent(200, "current", "speaker", "current", "这个怎么样")
	runtime.remember(current)
	if history := runtime.contextHistory(current); len(history) != 1 {
		t.Fatalf("weak topic overlap merged cross-group history: %#v", history)
	}
}

func crossGroupTestEvent(at int64, groupID, userID, messageID, text string) MessageEvent {
	return MessageEvent{
		Platform: PlatformOneBotV11, Kind: EventKindGroup, Time: at, GroupID: groupID,
		UserID: userID, MessageID: messageID, SenderName: userID,
		Segments: []MessageSegment{{Type: "text", Data: map[string]string{"text": text}}},
	}
}

type crossGroupHistoryStore struct {
	mu          sync.Mutex
	recent      map[string][]MessageEvent
	candidates  []MessageEvent
	query       MessageHistorySearchQuery
	searchCalls int
}

func (s *crossGroupHistoryStore) AppendMessageEvent(_ context.Context, session string, event MessageEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.recent == nil {
		s.recent = make(map[string][]MessageEvent)
	}
	s.recent[session] = append(s.recent[session], event)
	return nil
}

func (s *crossGroupHistoryStore) ListRecentMessageEvents(_ context.Context, session string, limit int) ([]MessageEvent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	events := append([]MessageEvent(nil), s.recent[session]...)
	if limit > 0 && len(events) > limit {
		events = events[len(events)-limit:]
	}
	return events, nil
}

func (s *crossGroupHistoryStore) SearchMessageEvents(_ context.Context, query MessageHistorySearchQuery) ([]MessageEvent, int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.query = query
	s.searchCalls++
	return append([]MessageEvent(nil), s.candidates...), len(s.candidates), nil
}

type crossGroupMembershipChannel struct {
	mu      sync.Mutex
	allowed map[string]bool
	calls   []string
}

func (*crossGroupMembershipChannel) Connect(context.Context, EventHandler) error { return nil }
func (*crossGroupMembershipChannel) Send(context.Context, OutgoingMessage) error { return nil }
func (*crossGroupMembershipChannel) Status() ChannelStatus                       { return ChannelStatus{} }
func (*crossGroupMembershipChannel) Close() error                                { return nil }

func (c *crossGroupMembershipChannel) CallAPI(_ context.Context, action string, params map[string]any) (map[string]any, error) {
	if action != "get_group_member_info" {
		return nil, fmt.Errorf("unexpected action %s", action)
	}
	groupID := stringFromAny(params["group_id"])
	userID := stringFromAny(params["user_id"])
	key := groupID + "|" + userID
	c.mu.Lock()
	c.calls = append(c.calls, key)
	allowed := c.allowed[key]
	c.mu.Unlock()
	if !allowed {
		return nil, fmt.Errorf("member not found")
	}
	return map[string]any{"group_id": groupID, "user_id": params["user_id"]}, nil
}

func (c *crossGroupMembershipChannel) callsSnapshot() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	calls := append([]string(nil), c.calls...)
	sort.Strings(calls)
	return calls
}

func countString(items []string, target string) int {
	count := 0
	for _, item := range items {
		if item == target {
			count++
		}
	}
	return count
}
