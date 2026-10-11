// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type telegramRecallProbeStore struct {
	*recallPersistenceStore
}

func newTelegramRecallProbeStore() *telegramRecallProbeStore {
	return &telegramRecallProbeStore{recallPersistenceStore: newRecallPersistenceStore()}
}

func (s *telegramRecallProbeStore) ListMessageEventsBetween(_ context.Context, session string, fromTime, throughTime int64) ([]MessageEvent, error) {
	var events []MessageEvent
	for _, event := range s.events[session] {
		if event.Kind == EventKindNotice || event.Time < fromTime || event.Time > throughTime {
			continue
		}
		events = append(events, event)
	}
	sort.SliceStable(events, func(i, j int) bool { return events[i].Time < events[j].Time })
	return events, nil
}

func (s *telegramRecallProbeStore) ListGroupRecallEvents(_ context.Context, groupID string) ([]MessageEvent, error) {
	var recalls []MessageEvent
	for _, events := range s.events {
		for _, event := range events {
			if isRecallNotice(event) && event.GroupID == groupID {
				recalls = append(recalls, event)
			}
		}
	}
	return recalls, nil
}

func (s *telegramRecallProbeStore) RecordNoticeEvent(_ context.Context, _ string, _ MessageEvent) error {
	return nil
}

type flakyTelegramRecallProbeStore struct {
	*telegramRecallProbeStore
	failRecallAppend bool
	actions          []string
}

func (s *flakyTelegramRecallProbeStore) AppendMessageEvent(ctx context.Context, session string, event MessageEvent) error {
	if isRecallNotice(event) {
		s.actions = append(s.actions, "append")
		if s.failRecallAppend {
			return errors.New("test recall append failure")
		}
	}
	return s.telegramRecallProbeStore.AppendMessageEvent(ctx, session, event)
}

func (s *flakyTelegramRecallProbeStore) RecordNoticeEvent(_ context.Context, _ string, _ MessageEvent) error {
	s.actions = append(s.actions, "notice")
	return nil
}

type telegramRecallProbeCallRecord struct {
	action string
	params map[string]any
}

type telegramRecallProbeChannel struct {
	nilChannel
	selfID string
	errors map[int64]error
	calls  []telegramRecallProbeCallRecord
}

type blockingTelegramRecallProbeChannel struct {
	*telegramRecallProbeChannel
	attempts atomic.Int32
	started  chan struct{}
	release  chan struct{}
}

func (c *blockingTelegramRecallProbeChannel) CallAPI(ctx context.Context, action string, params map[string]any) (map[string]any, error) {
	if c.attempts.Add(1) == 1 {
		close(c.started)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-c.release:
		}
	}
	return c.telegramRecallProbeChannel.CallAPI(ctx, action, params)
}

func (c *telegramRecallProbeChannel) Status() ChannelStatus {
	return ChannelStatus{Connected: true, Platform: PlatformTelegram, SelfID: c.selfID}
}

func (c *telegramRecallProbeChannel) CallAPI(_ context.Context, action string, params map[string]any) (map[string]any, error) {
	copied := make(map[string]any, len(params))
	for key, value := range params {
		copied[key] = value
	}
	c.calls = append(c.calls, telegramRecallProbeCallRecord{action: action, params: copied})
	id := int64(intFromAny(params["message_id"]))
	return map[string]any{"result": true}, c.errors[id]
}

func telegramRecallProbeEvent(now int64, messageID, userID, text string) MessageEvent {
	return MessageEvent{
		Platform:         PlatformTelegram,
		ProfileID:        "telegram-profile",
		ContextNamespace: "telegram-profile",
		Kind:             EventKindGroup,
		Time:             now,
		SelfID:           "9000",
		GroupID:          "-100123",
		UserID:           userID,
		MessageID:        messageID,
		RawMessage:       text,
		Segments:         []MessageSegment{{Type: "text", Data: map[string]string{"text": text}}},
		SenderName:       "群友" + userID,
	}
}

func newTelegramRecallProbeRuntime(channel Channel, store MessageHistoryStore) *Runtime {
	runtime := NewRuntime(BotConfig{
		ID: "telegram-profile", Platform: PlatformTelegram, BotAccount: "9000",
	}, channel, NewPluginManager(NewMessageHistoryPlugin()), nil, nil, nil, nil)
	runtime.SetMessageHistoryStore(store)
	return runtime
}

func TestTelegramRecallProbeClassifiesOnlyExactNotFoundAndPersistsContent(t *testing.T) {
	now := time.Date(2026, 10, 11, 9, 0, 0, 0, time.UTC).Unix()
	discoveredAt := now + 120
	store := newTelegramRecallProbeStore()
	for index := int64(1); index <= 4; index++ {
		event := telegramRecallProbeEvent(now-60+index, strconv.FormatInt(index, 10), "user-"+strconv.FormatInt(index, 10), "缓存原文 "+strconv.FormatInt(index, 10))
		if err := store.AppendMessageEvent(context.Background(), sessionKey(event), event); err != nil {
			t.Fatal(err)
		}
	}
	channel := &telegramRecallProbeChannel{selfID: "9000", errors: map[int64]error{
		4: &telegramAPIError{Code: 400, Description: "Bad Request: message to react not found"},
		3: &telegramAPIError{Code: 400, Description: "Bad Request: message can't be reacted"},
		2: &telegramAPIError{Code: 400, Description: "message to react not found"},
	}}
	runtime := newTelegramRecallProbeRuntime(channel, store)
	runtime.now = func() time.Time { return time.Unix(discoveredAt, 0) }
	current := telegramRecallProbeEvent(now, "99", "requester", "他撤回了什么")
	result, err := newDianaChatHistoryTool(runtime, current).recalls(context.Background(), map[string]any{"limit": 20})
	if err != nil {
		t.Fatal(err)
	}
	probe := result.TelegramRecallProbe
	if probe == nil || !probe.GuardVerified || probe.Complete || probe.AttemptedCount != 4 ||
		probe.ReachableCount != 1 || probe.ConfirmedUnavailableCount != 2 || probe.RecalledCount != 2 ||
		probe.UnknownCount != 1 || probe.UncheckedCount != 0 {
		t.Fatalf("probe = %#v", probe)
	}
	if strings.Contains(result.Message, "没有记录到群消息撤回") || !strings.Contains(result.Message, "确认 2 条") ||
		!strings.Contains(result.Message, "完整缓存原文") || strings.Contains(result.Message, "合并转发卡片发出") {
		t.Fatalf("message = %s", result.Message)
	}
	if len(result.Items) != 2 {
		t.Fatalf("items = %#v", result.Items)
	}
	for _, item := range result.Items {
		if item.Status != "unavailable_at_probe" || item.TimeSemantics != "event_time_is_probe_discovery_time" ||
			item.OperatorRole != telegramRecallProbeOperatorRole || item.OriginalTime == 0 || item.DiscoveredAt != discoveredAt {
			t.Fatalf("item lost Telegram time semantics: %#v", item)
		}
	}

	wantIDs := []int64{99, 4, 3, 2, 1}
	if len(channel.calls) != len(wantIDs) {
		t.Fatalf("calls = %#v", channel.calls)
	}
	for index, call := range channel.calls {
		if call.action != "setMessageReaction" || call.params["chat_id"] != "-100123" || int64(intFromAny(call.params["message_id"])) != wantIDs[index] {
			t.Fatalf("call[%d] = %#v", index, call)
		}
		reaction, ok := call.params["reaction"].([]any)
		if !ok || len(reaction) != 0 {
			t.Fatalf("reaction must be an empty JSON array: %#v", call.params["reaction"])
		}
	}

	recalls, err := store.ListGroupRecallEvents(context.Background(), current.GroupID)
	if err != nil {
		t.Fatal(err)
	}
	if len(recalls) != 2 {
		t.Fatalf("recalls = %#v", recalls)
	}
	byID := map[string]MessageEvent{}
	for _, recall := range recalls {
		byID[recall.MessageID] = recall
	}
	for _, id := range []string{"2", "4"} {
		recall := byID[id]
		if recall.RawMessage != "缓存原文 "+id || recall.OperatorRole != telegramRecallProbeOperatorRole ||
			recall.OperatorID != "" || recall.Time != discoveredAt || recall.OriginalTime == 0 {
			t.Fatalf("recall %s = %#v", id, recall)
		}
		if !strings.Contains(recallConclusionText(recall), "实际删除时间") {
			t.Fatalf("probe conclusion lost its boundary: %s", recallConclusionText(recall))
		}
	}

	before := len(store.events[sessionKey(current)])
	second, err := newDianaChatHistoryTool(runtime, current).recalls(context.Background(), map[string]any{"limit": 20})
	if err != nil {
		t.Fatal(err)
	}
	if second.TelegramRecallProbe == nil || second.TelegramRecallProbe.RecalledCount != 0 {
		t.Fatalf("second probe = %#v", second.TelegramRecallProbe)
	}
	if after := len(store.events[sessionKey(current)]); after != before {
		t.Fatalf("duplicate probe persisted another notice: before=%d after=%d", before, after)
	}
}

func TestTelegramRecallProbePersistenceFailureIsCountedAndRetried(t *testing.T) {
	now := time.Now().Unix()
	store := &flakyTelegramRecallProbeStore{telegramRecallProbeStore: newTelegramRecallProbeStore()}
	cached := telegramRecallProbeEvent(now-10, "1", "user", "保存失败后仍应重试的原文")
	if err := store.AppendMessageEvent(context.Background(), sessionKey(cached), cached); err != nil {
		t.Fatal(err)
	}
	store.failRecallAppend = true
	channel := &telegramRecallProbeChannel{selfID: "9000", errors: map[int64]error{
		1: &telegramAPIError{Code: 400, Description: "Bad Request: message to react not found"},
	}}
	runtime := newTelegramRecallProbeRuntime(channel, store)
	runtime.now = func() time.Time { return time.Unix(now, 0) }
	current := telegramRecallProbeEvent(now, "99", "requester", "查撤回")

	first, err := newDianaChatHistoryTool(runtime, current).recalls(context.Background(), map[string]any{"limit": 20})
	if err != nil {
		t.Fatal(err)
	}
	if probe := first.TelegramRecallProbe; probe == nil || probe.ConfirmedUnavailableCount != 1 ||
		probe.RecalledCount != 0 || probe.PersistenceFailureCount != 1 {
		t.Fatalf("first probe = %#v", probe)
	}
	if !strings.Contains(first.Message, "本地记录保存失败") || !reflect.DeepEqual(store.actions, []string{"notice", "append"}) {
		t.Fatalf("first result message=%q actions=%v", first.Message, store.actions)
	}
	if recalls, err := store.ListGroupRecallEvents(context.Background(), current.GroupID); err != nil || len(recalls) != 0 {
		t.Fatalf("failed write persisted recalls=%#v err=%v", recalls, err)
	}

	store.failRecallAppend = false
	second, err := newDianaChatHistoryTool(runtime, current).recalls(context.Background(), map[string]any{"limit": 20})
	if err != nil {
		t.Fatal(err)
	}
	if probe := second.TelegramRecallProbe; probe == nil || probe.ConfirmedUnavailableCount != 1 ||
		probe.RecalledCount != 1 || probe.PersistenceFailureCount != 0 || probe.RecentlyCheckedCount != 0 {
		t.Fatalf("second probe = %#v", probe)
	}
	if !reflect.DeepEqual(store.actions, []string{"notice", "append", "notice", "append"}) {
		t.Fatalf("retry actions = %v", store.actions)
	}
	if recalls, err := store.ListGroupRecallEvents(context.Background(), current.GroupID); err != nil ||
		len(recalls) != 1 || recalls[0].RawMessage != cached.RawMessage {
		t.Fatalf("retried recalls=%#v err=%v", recalls, err)
	}
	if len(channel.calls) != 4 {
		t.Fatalf("retry should repeat guard and candidate probes: calls=%#v", channel.calls)
	}
}

func TestTelegramRecallProbeStopsOnRateLimit(t *testing.T) {
	now := time.Date(2026, 10, 11, 10, 0, 0, 0, time.UTC).Unix()
	store := newTelegramRecallProbeStore()
	for index := int64(1); index <= 3; index++ {
		event := telegramRecallProbeEvent(now-60+index, strconv.FormatInt(index, 10), "user", "消息")
		_ = store.AppendMessageEvent(context.Background(), sessionKey(event), event)
	}
	channel := &telegramRecallProbeChannel{selfID: "9000", errors: map[int64]error{
		2: &telegramAPIError{Code: 429, Description: "Too Many Requests: retry after 7", RetryAfterSeconds: 7},
	}}
	runtime := newTelegramRecallProbeRuntime(channel, store)
	runtime.now = func() time.Time { return time.Unix(now, 0) }
	current := telegramRecallProbeEvent(now, "99", "requester", "查撤回")
	result, err := newDianaChatHistoryTool(runtime, current).recalls(context.Background(), map[string]any{"limit": 20})
	if err != nil {
		t.Fatal(err)
	}
	probe := result.TelegramRecallProbe
	if probe == nil || !probe.GuardVerified || probe.Complete || probe.AttemptedCount != 2 ||
		probe.ReachableCount != 1 || probe.UnknownCount != 1 || probe.UncheckedCount != 1 || probe.RetryAfterSeconds != 7 {
		t.Fatalf("probe = %#v", probe)
	}
	if got := []int64{int64(intFromAny(channel.calls[0].params["message_id"])), int64(intFromAny(channel.calls[1].params["message_id"])), int64(intFromAny(channel.calls[2].params["message_id"]))}; !reflect.DeepEqual(got, []int64{99, 3, 2}) {
		t.Fatalf("calls = %v", got)
	}
	if !strings.Contains(result.Message, "提前停止") || strings.Contains(result.Message, "最近 24 小时没有记录到") {
		t.Fatalf("message = %s", result.Message)
	}
	second, err := newDianaChatHistoryTool(runtime, current).recalls(context.Background(), map[string]any{"limit": 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(channel.calls) != 3 {
		t.Fatalf("cooldown must not make more API calls: %#v", channel.calls)
	}
	if second.TelegramRecallProbe == nil || second.TelegramRecallProbe.GuardAttempted ||
		second.TelegramRecallProbe.RetryAfterSeconds != 7 || second.TelegramRecallProbe.UncheckedCount != 2 {
		t.Fatalf("cooldown probe = %#v", second.TelegramRecallProbe)
	}
	if !strings.Contains(second.Message, "冷却期") || strings.Contains(second.Message, "安全校验没有通过") {
		t.Fatalf("cooldown message = %s", second.Message)
	}
}

func TestTelegramRecallProbeGuardFailureDoesNotClassifyCachedMessages(t *testing.T) {
	now := time.Now().Unix()
	store := newTelegramRecallProbeStore()
	cached := telegramRecallProbeEvent(now-10, "1", "user", "仍在缓存里的原文")
	_ = store.AppendMessageEvent(context.Background(), sessionKey(cached), cached)
	channel := &telegramRecallProbeChannel{selfID: "9000", errors: map[int64]error{
		99: &telegramAPIError{Code: 403, Description: "Forbidden: bot was kicked from the supergroup chat"},
		1:  &telegramAPIError{Code: 400, Description: "Bad Request: message to react not found"},
	}}
	runtime := newTelegramRecallProbeRuntime(channel, store)
	current := telegramRecallProbeEvent(now, "99", "requester", "查撤回")
	result, err := newDianaChatHistoryTool(runtime, current).recalls(context.Background(), map[string]any{"limit": 20})
	if err != nil {
		t.Fatal(err)
	}
	probe := result.TelegramRecallProbe
	if probe == nil || probe.GuardVerified || probe.Complete || probe.UncheckedCount != 1 || probe.RecalledCount != 0 {
		t.Fatalf("probe = %#v", probe)
	}
	if len(channel.calls) != 1 || int64(intFromAny(channel.calls[0].params["message_id"])) != 99 {
		t.Fatalf("calls = %#v", channel.calls)
	}
	recalls, _ := store.ListGroupRecallEvents(context.Background(), current.GroupID)
	if len(recalls) != 0 {
		t.Fatalf("guard failure must not persist recalls: %#v", recalls)
	}
}

func TestTelegramRecallProbeResolvesSingleConfiguredProfileScope(t *testing.T) {
	now := time.Date(2026, 10, 11, 11, 0, 0, 0, time.UTC).Unix()
	store := newTelegramRecallProbeStore()
	cached := telegramRecallProbeEvent(now-10, "1", "user", "单档案回落后仍能读取的原文")
	_ = store.AppendMessageEvent(context.Background(), sessionKey(cached), cached)
	channel := &telegramRecallProbeChannel{selfID: "9000", errors: map[int64]error{
		1: &telegramAPIError{Code: 400, Description: "Bad Request: message to react not found"},
	}}
	runtime := newTelegramRecallProbeRuntime(channel, store)
	runtime.now = func() time.Time { return time.Unix(now, 0) }
	current := telegramRecallProbeEvent(now, "99", "requester", "查撤回")
	current.Platform = ""
	current.ProfileID = ""
	current.ContextNamespace = ""
	current.SelfID = ""

	result, err := newDianaChatHistoryTool(runtime, current).recalls(context.Background(), map[string]any{"limit": 20})
	if err != nil {
		t.Fatal(err)
	}
	if probe := result.TelegramRecallProbe; probe == nil || !probe.GuardVerified || probe.RecalledCount != 1 {
		t.Fatalf("probe = %#v", probe)
	}
	if len(result.Items) != 1 || result.Items[0].Text != cached.RawMessage {
		t.Fatalf("items = %#v", result.Items)
	}
}

func TestTelegramRecallProbeRecentChecksStayProfileScopedForSharedBot(t *testing.T) {
	now := time.Date(2026, 10, 11, 11, 30, 0, 0, time.UTC).Unix()
	store := newTelegramRecallProbeStore()
	for _, profileID := range []string{"telegram-a", "telegram-b"} {
		cached := telegramRecallProbeEvent(now-10, "1", "user", "属于 "+profileID+" 的缓存原文")
		cached.ProfileID = profileID
		cached.ContextNamespace = profileID
		if err := store.AppendMessageEvent(context.Background(), sessionKey(cached), cached); err != nil {
			t.Fatal(err)
		}
	}
	channelA := &telegramRecallProbeChannel{selfID: "9000", errors: map[int64]error{
		1: &telegramAPIError{Code: 400, Description: "Bad Request: message to react not found"},
	}}
	channelB := &telegramRecallProbeChannel{selfID: "9000", errors: map[int64]error{
		1: &telegramAPIError{Code: 400, Description: "Bad Request: message to react not found"},
	}}
	runtime := NewRuntime(BotConfig{ID: "telegram-a", Platform: PlatformTelegram, BotAccount: "9000"}, NewMultiChannel([]ChannelBinding{
		{ProfileID: "telegram-a", Platform: PlatformTelegram, Channel: channelA},
		{ProfileID: "telegram-b", Platform: PlatformTelegram, Channel: channelB},
	}), NewPluginManager(NewMessageHistoryPlugin()), nil, nil, nil, nil)
	runtime.SetProfiles(ProfileSet{Profiles: []BotConfig{
		{ID: "telegram-a", Platform: PlatformTelegram, BotAccount: "9000", Enabled: true},
		{ID: "telegram-b", Platform: PlatformTelegram, BotAccount: "9000", Enabled: true},
	}})
	runtime.SetMessageHistoryStore(store)
	runtime.now = func() time.Time { return time.Unix(now, 0) }

	for _, tc := range []struct {
		profileID string
		channel   *telegramRecallProbeChannel
	}{
		{profileID: "telegram-a", channel: channelA},
		{profileID: "telegram-b", channel: channelB},
	} {
		current := telegramRecallProbeEvent(now, "99", "requester", "查撤回")
		current.ProfileID = tc.profileID
		current.ContextNamespace = tc.profileID
		result, err := newDianaChatHistoryTool(runtime, current).recalls(context.Background(), map[string]any{"limit": 20})
		if err != nil {
			t.Fatal(err)
		}
		if probe := result.TelegramRecallProbe; probe == nil || probe.RecalledCount != 1 ||
			probe.RecentlyCheckedCount != 0 || probe.AttemptedCount != 1 {
			t.Fatalf("%s probe = %#v", tc.profileID, probe)
		}
		if len(result.Items) != 1 || result.Items[0].Text != "属于 "+tc.profileID+" 的缓存原文" {
			t.Fatalf("%s items = %#v", tc.profileID, result.Items)
		}
		if len(tc.channel.calls) != 2 {
			t.Fatalf("%s calls = %#v", tc.profileID, tc.channel.calls)
		}
	}
}

func TestTelegramRecallProbeSerializesConcurrentQueriesPerBot(t *testing.T) {
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC).Unix()
	store := newTelegramRecallProbeStore()
	cached := telegramRecallProbeEvent(now-10, "1", "user", "并发查询只探测一次")
	_ = store.AppendMessageEvent(context.Background(), sessionKey(cached), cached)
	base := &telegramRecallProbeChannel{selfID: "9000", errors: map[int64]error{}}
	channel := &blockingTelegramRecallProbeChannel{
		telegramRecallProbeChannel: base,
		started:                    make(chan struct{}),
		release:                    make(chan struct{}),
	}
	runtime := newTelegramRecallProbeRuntime(channel, store)
	runtime.now = func() time.Time { return time.Unix(now, 0) }
	current := telegramRecallProbeEvent(now, "99", "requester", "查撤回")
	results := make(chan error, 2)
	run := func() {
		_, err := newDianaChatHistoryTool(runtime, current).recalls(context.Background(), map[string]any{"limit": 20})
		results <- err
	}
	go run()
	<-channel.started
	go run()
	time.Sleep(25 * time.Millisecond)
	if got := channel.attempts.Load(); got != 1 {
		t.Fatalf("concurrent query escaped account lease: calls=%d", got)
	}
	close(channel.release)
	for range 2 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	if got := channel.attempts.Load(); got != 2 {
		t.Fatalf("guard plus one candidate should be probed once: calls=%d", got)
	}
}

func TestTelegramRecallProbeFiltersCandidatesAndCapsAtFifty(t *testing.T) {
	now := time.Now().Unix()
	store := newTelegramRecallProbeStore()
	for index := int64(1); index <= 55; index++ {
		event := telegramRecallProbeEvent(now-100+index, strconv.FormatInt(index, 10), "user", "有效消息")
		_ = store.AppendMessageEvent(context.Background(), sessionKey(event), event)
	}
	invalid := []MessageEvent{
		telegramRecallProbeEvent(now-10, "not-a-number", "user", "非法 ID"),
		telegramRecallProbeEvent(now-10, "70", "9000", "机器人自己的消息"),
		telegramRecallProbeEvent(now-10, "71", "user", "已标记出站"),
		telegramRecallProbeEvent(now-10, "72", "user", "其他档案"),
		telegramRecallProbeEvent(now-10, "73", "user", "其他机器人"),
		telegramRecallProbeEvent(now-10, "74", "user", "过期消息"),
		telegramRecallProbeEvent(0, "75", "user", "没有时间"),
		telegramRecallProbeEvent(now-10, "76", "user", ""),
		telegramRecallProbeEvent(now-10, "77", "user", "其他群"),
	}
	invalid[2].Outbound = true
	invalid[3].ProfileID = "other-profile"
	invalid[4].SelfID = "other-bot"
	invalid[5].Time = now - int64(25*time.Hour/time.Second)
	invalid[7].RawMessage = ""
	invalid[7].Segments = []MessageSegment{{Type: "text", Data: map[string]string{"text": ""}}}
	invalid[8].GroupID = "-100999"
	for _, event := range invalid {
		_ = store.AppendMessageEvent(context.Background(), sessionKey(telegramRecallProbeEvent(now, "99", "requester", "查撤回")), event)
	}

	channel := &telegramRecallProbeChannel{selfID: "9000", errors: map[int64]error{}}
	runtime := newTelegramRecallProbeRuntime(channel, store)
	current := telegramRecallProbeEvent(now, "99", "requester", "查撤回")
	result, err := newDianaChatHistoryTool(runtime, current).recalls(context.Background(), map[string]any{"limit": 50})
	if err != nil {
		t.Fatal(err)
	}
	probe := result.TelegramRecallProbe
	if probe == nil || probe.EligibleCount != 55 || probe.CandidateCount != 50 || probe.AttemptedCount != 50 ||
		probe.DeferredCount != 5 || !probe.Limited || probe.Complete {
		t.Fatalf("probe = %#v", probe)
	}
	if len(channel.calls) != 51 {
		t.Fatalf("calls = %d", len(channel.calls))
	}
	for index, want := range []int64{99, 55, 54, 53, 52, 51, 50, 49, 48, 47, 46, 45, 44, 43, 42, 41, 40, 39, 38, 37, 36, 35, 34, 33, 32, 31, 30, 29, 28, 27, 26, 25, 24, 23, 22, 21, 20, 19, 18, 17, 16, 15, 14, 13, 12, 11, 10, 9, 8, 7, 6} {
		if got := int64(intFromAny(channel.calls[index].params["message_id"])); got != want {
			t.Fatalf("call[%d] message_id=%d want=%d", index, got, want)
		}
	}
	second, err := newDianaChatHistoryTool(runtime, current).recalls(context.Background(), map[string]any{"limit": 50})
	if err != nil {
		t.Fatal(err)
	}
	if second.TelegramRecallProbe == nil || second.TelegramRecallProbe.CandidateCount != 5 ||
		second.TelegramRecallProbe.AttemptedCount != 5 || second.TelegramRecallProbe.RecentlyCheckedCount != 50 {
		t.Fatalf("second probe = %#v", second.TelegramRecallProbe)
	}
	if len(channel.calls) != 57 {
		t.Fatalf("calls after rotation = %d", len(channel.calls))
	}
	for index, want := range []int64{99, 5, 4, 3, 2, 1} {
		if got := int64(intFromAny(channel.calls[51+index].params["message_id"])); got != want {
			t.Fatalf("rotated call[%d] message_id=%d want=%d", index, got, want)
		}
	}
	third, err := newDianaChatHistoryTool(runtime, current).recalls(context.Background(), map[string]any{"limit": 50})
	if err != nil {
		t.Fatal(err)
	}
	if third.TelegramRecallProbe == nil || third.TelegramRecallProbe.CandidateCount != 0 || third.TelegramRecallProbe.RecentlyCheckedCount != 55 {
		t.Fatalf("third probe = %#v", third.TelegramRecallProbe)
	}
	if len(channel.calls) != 57 {
		t.Fatalf("recently checked messages must not be reprobed: calls=%d", len(channel.calls))
	}
	if !strings.Contains(third.Message, "刚刚检查过") || strings.Contains(third.Message, "安全校验没有通过") {
		t.Fatalf("recent-cache message = %s", third.Message)
	}
}

func TestTelegramAPIErrorPreservesRetryAfter(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok": false, "error_code": 429, "description": "Too Many Requests: retry after 11",
			"parameters": map[string]any{"retry_after": 11},
		})
	}))
	defer server.Close()
	channel := NewTelegramChannel(TelegramConfig{BotToken: "test-token", APIBaseURL: server.URL})
	_, err := channel.CallAPI(context.Background(), "setMessageReaction", map[string]any{"chat_id": "-100", "message_id": 1, "reaction": []any{}})
	var apiErr *telegramAPIError
	if !errors.As(err, &apiErr) || apiErr.Code != 429 || apiErr.RetryAfterSeconds != 11 {
		t.Fatalf("err = %#v", err)
	}
	missing, stop, retryAfter := telegramRecallProbeError(err)
	if missing || !stop || retryAfter != 11 {
		t.Fatalf("classification = missing=%v stop=%v retry=%d", missing, stop, retryAfter)
	}
}
