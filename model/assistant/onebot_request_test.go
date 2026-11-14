// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SuInk/diana/model/agent"
)

type memoryOneBotRequestStore struct {
	mu    sync.Mutex
	items []OneBotRequestRecord
}

func (s *memoryOneBotRequestStore) SaveOneBotRequest(_ context.Context, item OneBotRequestRecord) (OneBotRequestRecord, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existing := range s.items {
		if existing.ProfileID == item.ProfileID && existing.RequestType == item.RequestType && existing.SubType == item.SubType && existing.Flag == item.Flag {
			return existing, false, nil
		}
	}
	item.ID = fmt.Sprintf("request-%d", len(s.items)+1)
	s.items = append(s.items, item)
	return item, true, nil
}

// matching 照 SQLite 实现：按 (created_at, id) 新到旧，Before 之后的才算。
func (s *memoryOneBotRequestStore) matching(query OneBotRequestQuery) []OneBotRequestRecord {
	out := []OneBotRequestRecord{}
	for _, item := range s.items {
		if item.ProfileID != query.ProfileID || (query.Status != "" && item.Status != query.Status) {
			continue
		}
		if before := query.Before; before != nil && !(item.CreatedAt.Before(before.At) || (item.CreatedAt.Equal(before.At) && item.ID < before.ID)) {
			continue
		}
		out = append(out, item)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.After(out[j].CreatedAt)
		}
		return out[i].ID > out[j].ID
	})
	return out
}

func (s *memoryOneBotRequestStore) ListOneBotRequests(_ context.Context, query OneBotRequestQuery) ([]OneBotRequestRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.matching(query)
	if query.Limit > 0 && len(out) > query.Limit {
		out = out[:query.Limit]
	}
	return out, nil
}

func (s *memoryOneBotRequestStore) CountOneBotRequests(_ context.Context, query OneBotRequestQuery) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.matching(query)), nil
}

func (s *memoryOneBotRequestStore) GetOneBotRequest(_ context.Context, profileID, id string) (OneBotRequestRecord, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, item := range s.items {
		if item.ProfileID == profileID && item.ID == id {
			return item, true, nil
		}
	}
	return OneBotRequestRecord{}, false, nil
}

func (s *memoryOneBotRequestStore) ResolveOneBotRequest(_ context.Context, profileID, id string, status OneBotRequestStatus, reason string, decidedAt time.Time) (OneBotRequestRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for index := range s.items {
		item := &s.items[index]
		if item.ProfileID == profileID && item.ID == id {
			item.Status = status
			item.Reason = reason
			item.DecidedAt = decidedAt
			return *item, nil
		}
	}
	return OneBotRequestRecord{}, fmt.Errorf("not found")
}

func TestOneBotRequestEnvelopeKeepsFlagOutOfEventJSON(t *testing.T) {
	const secretFlag = "sensitive-request-flag"
	event := messageEventFromEnvelope(oneBotEnvelope{
		Time: 123, SelfID: "42", PostType: "request", RequestType: "group", SubType: "invite",
		UserID: "10001", GroupID: "20002", Comment: "请加入测试群", Flag: secretFlag,
	})
	if event.Kind != EventKindRequest || event.SubType != "invite" || event.oneBotRequest == nil {
		t.Fatalf("event = %#v", event)
	}
	if event.oneBotRequest.Flag != secretFlag || event.MessageID == "" {
		t.Fatalf("request metadata = %#v", event.oneBotRequest)
	}
	encoded, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), secretFlag) {
		t.Fatalf("request flag leaked into event JSON: %s", encoded)
	}
	if !strings.Contains(string(encoded), `"request_type":"group"`) {
		t.Fatalf("safe request metadata missing: %s", encoded)
	}
}

func TestOneBotChannelsDispatchRequestEvents(t *testing.T) {
	payload := []byte(`{"time":123,"self_id":42,"post_type":"request","request_type":"friend","user_id":10001,"comment":"hello","flag":"flag-1"}`)
	forward := NewOneBotChannel(OneBotConfig{})
	forwardEvents := make(chan MessageEvent, 1)
	if err := forward.handleFrame(context.Background(), func(_ context.Context, event MessageEvent) error {
		forwardEvents <- event
		return nil
	}, payload); err != nil {
		t.Fatal(err)
	}
	select {
	case event := <-forwardEvents:
		if event.Kind != EventKindRequest || event.oneBotRequest.RequestType != "friend" {
			t.Fatalf("forward event = %#v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("forward request was not dispatched")
	}

	reverse := NewOneBotReverseServer(OneBotConfig{})
	reverseEvents := make(chan MessageEvent, 1)
	reverse.handler = func(_ context.Context, event MessageEvent) error {
		reverseEvents <- event
		return nil
	}
	if err := reverse.handleFrame(payload); err != nil {
		t.Fatal(err)
	}
	select {
	case event := <-reverseEvents:
		if event.Kind != EventKindRequest || event.oneBotRequest.RequestType != "friend" {
			t.Fatalf("reverse event = %#v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("reverse request was not dispatched")
	}
}

func TestRuntimePersistsGroupRequestsWithoutNotifyingOwner(t *testing.T) {
	for _, subType := range []string{"add", "invite"} {
		t.Run(subType, func(t *testing.T) {
			store := &memoryOneBotRequestStore{}
			channel := &recordingChannel{}
			runtime := NewRuntime(BotConfig{
				ID: "bot-1", Platform: PlatformOneBotV11, BotAccount: "42", OwnerID: "owner",
			}.WithDefaults(), channel, NewDefaultPluginManager(), nil, nil, nil, nil)
			runtime.SetOneBotRequestStore(store)
			event := messageEventFromEnvelope(oneBotEnvelope{
				Time: 123, SelfID: "42", PostType: "request", RequestType: "group", SubType: subType,
				UserID: "10001", GroupID: "20002", Flag: "flag-1",
			})
			for i := 0; i < 2; i++ {
				if err := runtime.HandleEvent(context.Background(), event); err != nil {
					t.Fatal(err)
				}
			}
			if len(store.items) != 1 || store.items[0].Status != OneBotRequestPending {
				t.Fatalf("stored requests = %#v", store.items)
			}
			if sent := channel.sentSnapshot(); len(sent) != 0 {
				t.Fatalf("unexpected owner notification = %#v", sent)
			}
		})
	}
}

func TestRuntimePersistsFriendRequestAndNotifiesOwnerOnce(t *testing.T) {
	store := &memoryOneBotRequestStore{}
	channel := &recordingChannel{}
	runtime := NewRuntime(BotConfig{
		ID: "bot-1", Platform: PlatformOneBotV11, BotAccount: "42", OwnerID: "owner",
	}.WithDefaults(), channel, NewDefaultPluginManager(), nil, nil, nil, nil)
	runtime.SetOneBotRequestStore(store)
	event := messageEventFromEnvelope(oneBotEnvelope{
		Time: 123, SelfID: "42", PostType: "request", RequestType: "friend",
		UserID: "10001", Flag: "flag-1",
	})
	if err := runtime.HandleEvent(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	if err := runtime.HandleEvent(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	if len(store.items) != 1 {
		t.Fatalf("stored requests = %#v", store.items)
	}
	sent := channel.sentSnapshot()
	if len(sent) != 1 || sent[0].UserID != "owner" || !strings.Contains(sent[0].Text, "好友请求") || !strings.Contains(sent[0].Text, "request-1") {
		t.Fatalf("owner notification = %#v", sent)
	}
	if strings.Contains(sent[0].Text, "flag-1") {
		t.Fatalf("request flag leaked in notification: %q", sent[0].Text)
	}
}

func TestOneBotRequestsToolApprovesGroupAndRejectsFriend(t *testing.T) {
	store := &memoryOneBotRequestStore{items: []OneBotRequestRecord{
		{ID: "group-request", ProfileID: "bot-1", Platform: PlatformOneBotV11, RequestType: "group", SubType: "invite", GroupID: "20002", UserID: "10001", Flag: "group-secret", Status: OneBotRequestPending, CreatedAt: time.Now()},
		{ID: "friend-request", ProfileID: "bot-1", Platform: PlatformOneBotV11, RequestType: "friend", UserID: "10002", Flag: "friend-secret", Status: OneBotRequestPending, CreatedAt: time.Now()},
	}}
	channel := &recordingChannel{}
	runtime := NewRuntime(BotConfig{ID: "bot-1", Platform: PlatformOneBotV11, OwnerID: "owner"}.WithDefaults(), channel, NewDefaultPluginManager(), nil, nil, nil, nil)
	runtime.SetOneBotRequestStore(store)
	tool := newDianaOneBotRequestsTool(runtime, MessageEvent{ProfileID: "bot-1", Platform: PlatformOneBotV11, Kind: EventKindPrivate, UserID: "owner"})

	output, err := tool.Run(context.Background(), map[string]any{"operation": "approve", "id": "group-request"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output, "group-secret") || !strings.Contains(output, `"status":"approved"`) {
		t.Fatalf("approve output = %s", output)
	}
	if _, err := tool.Run(context.Background(), map[string]any{"operation": "reject", "id": "friend-request", "reason": "not known"}); err != nil {
		t.Fatal(err)
	}
	calls := channel.callsSnapshot()
	if len(calls) != 2 || calls[0].action != "set_group_add_request" || calls[1].action != "set_friend_add_request" {
		t.Fatalf("calls = %#v", calls)
	}
	if calls[0].params["flag"] != "group-secret" || calls[0].params["sub_type"] != "invite" || calls[0].params["approve"] != true {
		t.Fatalf("group params = %#v", calls[0].params)
	}
	if calls[1].params["flag"] != "friend-secret" || calls[1].params["approve"] != false {
		t.Fatalf("friend params = %#v", calls[1].params)
	}

	member := newDianaOneBotRequestsTool(runtime, MessageEvent{ProfileID: "bot-1", Platform: PlatformOneBotV11, Kind: EventKindPrivate, UserID: "member"})
	if _, err := member.Run(context.Background(), map[string]any{"operation": "list"}); err == nil || !strings.Contains(err.Error(), "只有机器人主人") {
		t.Fatalf("member error = %v", err)
	}
}

func TestOneBotRequestsToolDoesNotRepeatResolvedMutation(t *testing.T) {
	store := &memoryOneBotRequestStore{items: []OneBotRequestRecord{{
		ID: "done", ProfileID: "bot-1", Platform: PlatformOneBotV11, RequestType: "group", SubType: "invite",
		Flag: "secret", Status: OneBotRequestApproved, CreatedAt: time.Now(),
	}}}
	channel := &recordingChannel{}
	runtime := NewRuntime(BotConfig{ID: "bot-1", Platform: PlatformOneBotV11, OwnerID: "owner"}.WithDefaults(), channel, NewDefaultPluginManager(), nil, nil, nil, nil)
	runtime.SetOneBotRequestStore(store)
	output, err := newDianaOneBotRequestsTool(runtime, MessageEvent{ProfileID: "bot-1", Platform: PlatformOneBotV11, UserID: "owner"}).Run(
		context.Background(), map[string]any{"operation": "approve", "id": "done"},
	)
	if err != nil || !strings.Contains(output, "已经处理过") || len(channel.callsSnapshot()) != 0 {
		t.Fatalf("output=%s err=%v calls=%#v", output, err, channel.callsSnapshot())
	}
}

func TestOneBotRequestActionsMapApprovalFields(t *testing.T) {
	groupAction, groupParams, err := oneBotRequestAction(OneBotRequestRecord{
		RequestType: "group", SubType: "add", Flag: "group-flag",
	}, false, map[string]any{"reason": "资料不完整"})
	if err != nil || groupAction != "set_group_add_request" || groupParams["sub_type"] != "add" || groupParams["reason"] != "资料不完整" {
		t.Fatalf("group action=%q params=%#v err=%v", groupAction, groupParams, err)
	}
	friendAction, friendParams, err := oneBotRequestAction(OneBotRequestRecord{
		RequestType: "friend", Flag: "friend-flag",
	}, true, map[string]any{"remark": "群友"})
	if err != nil || friendAction != "set_friend_add_request" || friendParams["remark"] != "群友" {
		t.Fatalf("friend action=%q params=%#v err=%v", friendAction, friendParams, err)
	}
}

func TestOneBotRequestPromptIsOwnerOnly(t *testing.T) {
	store := &memoryOneBotRequestStore{}
	runtime := NewRuntime(BotConfig{ID: "bot-1", Platform: PlatformOneBotV11, OwnerID: "owner"}.WithDefaults(), &recordingChannel{}, NewDefaultPluginManager(), nil, nil, nil, nil)
	runtime.SetOneBotRequestStore(store)
	event := MessageEvent{ProfileID: "bot-1", Platform: PlatformOneBotV11, Kind: EventKindPrivate, UserID: "owner"}
	registry := agent.NewToolRegistry(newDianaOneBotRequestsTool(runtime, event))
	prompt := runtime.systemPromptWithRelationshipAndAgentTools(event, nil, false, RelationshipPolicy{Owner: true}, true, registry)
	if !strings.Contains(prompt, "先 list") || !strings.Contains(prompt, dianaOneBotRequestsToolName) {
		t.Fatalf("owner request prompt missing: %q", prompt)
	}
	nonOwner := runtime.systemPromptWithRelationshipAndAgentTools(event, nil, false, RelationshipPolicy{}, true, registry)
	if strings.Contains(nonOwner, promptToolOneBotRequests) {
		t.Fatalf("request mutation prompt leaked to non-owner: %q", nonOwner)
	}
}

type oneBotRequestPageResult struct {
	Items []oneBotRequestView `json:"items"`
	Page  *toolCursorPage     `json:"page"`
}

func newPagedOneBotRequestStore(count int, comment string) *memoryOneBotRequestStore {
	store := &memoryOneBotRequestStore{}
	base := time.Unix(1_788_300_000, 0)
	for i := range count {
		// 每两条共用一个时间戳，游标要靠 id 区分同一时刻的请求。
		at := base.Add(time.Duration(i/2) * time.Second)
		store.items = append(store.items, OneBotRequestRecord{
			ID: fmt.Sprintf("req-%03d", i), ProfileID: "bot-1", RequestType: "friend", UserID: itoa(10000 + i),
			Comment: comment, Status: OneBotRequestPending, CreatedAt: at,
		})
	}
	return store
}

func listOneBotRequestPageForTest(t *testing.T, ctx context.Context, store *memoryOneBotRequestStore, input map[string]any) oneBotRequestPageResult {
	t.Helper()
	raw, err := listOneBotRequestPage(ctx, store, input, "bot-1", "list", "")
	if err != nil {
		t.Fatal(err)
	}
	if limit := agent.ToolOutputBudget(ctx); limit > 0 && len([]rune(raw)) > limit {
		t.Fatalf("结果 %d 字超过单次上限 %d", len([]rune(raw)), limit)
	}
	var got oneBotRequestPageResult
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatal(err)
	}
	if got.Page == nil {
		t.Fatalf("没有分页字段：%s", raw)
	}
	return got
}

// 待处理请求按键集游标整条分页，附言给全不截短；翻页之间处理掉已看过的请求，
// 后面的页不跳条、不重复（offset 翻页会因列表前移跳过一批）。
func TestOneBotRequestListCursorSurvivesResolve(t *testing.T) {
	longComment := strings.Repeat("想", 300)
	store := newPagedOneBotRequestStore(30, longComment)
	ctx := context.Background()
	first := listOneBotRequestPageForTest(t, ctx, store, map[string]any{"limit": 20})
	if len(first.Items) != 20 || !first.Page.HasMore || first.Page.RemainingCount != 10 || first.Page.NextCursor == "" {
		t.Fatalf("first page = %+v", first.Page)
	}
	if first.Items[0].ID != "req-029" || first.Items[0].Comment != longComment {
		t.Fatalf("排序或附言不对：%s %d 字", first.Items[0].ID, len([]rune(first.Items[0].Comment)))
	}
	seen := map[string]bool{}
	for _, item := range first.Items {
		seen[item.ID] = true
		if _, err := store.ResolveOneBotRequest(ctx, "bot-1", item.ID, OneBotRequestApproved, "", time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	rest := listOneBotRequestPageForTest(t, ctx, store, map[string]any{"cursor": first.Page.NextCursor})
	if len(rest.Items) != 10 || rest.Page.HasMore || rest.Page.NextCursor != "" {
		t.Fatalf("rest page = %+v items=%d", rest.Page, len(rest.Items))
	}
	for _, item := range rest.Items {
		if seen[item.ID] {
			t.Fatalf("重复给了 %s", item.ID)
		}
		seen[item.ID] = true
	}
	if len(seen) != 30 {
		t.Fatalf("处理之后再翻页跳条：只看到 %d 条", len(seen))
	}
	if _, err := listOneBotRequestPage(ctx, store, map[string]any{"cursor": "not-a-cursor"}, "bot-1", "list", ""); err == nil {
		t.Fatal("无效游标应该报错")
	}
}

// 单次上限装不下时按整条切页：每页都在上限内，续读把剩下的全给齐。
func TestOneBotRequestListSplitsByOutputBudget(t *testing.T) {
	store := newPagedOneBotRequestStore(12, strings.Repeat("想", 300))
	ctx := agent.WithToolOutputBudget(context.Background(), 2500)
	seen := map[string]bool{}
	input := map[string]any{}
	for pages := 0; ; pages++ {
		if pages > 12 {
			t.Fatal("翻页没有结束")
		}
		got := listOneBotRequestPageForTest(t, ctx, store, input)
		if len(got.Items) == 0 {
			t.Fatal("一页一条都没给")
		}
		for _, item := range got.Items {
			if seen[item.ID] {
				t.Fatalf("重复给了 %s", item.ID)
			}
			seen[item.ID] = true
		}
		if !got.Page.HasMore {
			break
		}
		if pages == 0 && (len(got.Items) >= 12 || !strings.Contains(got.Page.PageNote, "单次结果上限")) {
			t.Fatalf("第一页应按上限切开：%d 条 note=%q", len(got.Items), got.Page.PageNote)
		}
		input = map[string]any{"cursor": got.Page.NextCursor}
	}
	if len(seen) != 12 {
		t.Fatalf("只拿到 %d 条", len(seen))
	}
}
