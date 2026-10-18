// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"errors"
	"github.com/SuInk/diana/model/llm"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestReplyAndRecordStopsAfterTerminalGroupSendFailure(t *testing.T) {
	channel := &failingOutboundChannel{
		err:      errors.New("opaque OneBot send failure"),
		groupIDs: []string{"123456", "20005"},
	}
	runtime := NewRuntime(BotConfig{OwnerID: "10001"}, channel, NewPluginManager(), nil, nil, nil, nil)
	event := MessageEvent{
		Kind:      EventKindGroup,
		GroupID:   "20006",
		UserID:    "10001",
		MessageID: "message-1",
		Time:      time.Now().Add(-time.Minute).Unix(),
	}

	outcome, err := runtime.replyAndRecord(context.Background(), event, "帮助", "replied")
	if err != nil {
		t.Fatalf("replyAndRecord() error = %v", err)
	}
	if outcome != "ignored_unavailable_group" {
		t.Fatalf("outcome = %q", outcome)
	}
	if got := channel.sendAttempts(); got != 1 {
		t.Fatalf("send attempts = %d, want 1", got)
	}

	if err := runtime.send(context.Background(), event, "不要再发错误提示"); !errors.Is(err, errGroupSendUnavailable) {
		t.Fatalf("blocked send error = %v", err)
	}
	if got := channel.sendAttempts(); got != 1 {
		t.Fatalf("blocked send reached OneBot; attempts = %d", got)
	}
	if err := runtime.blockedGroupSendError(MessageEvent{Kind: EventKindGroup, GroupID: "20005"}); err != nil {
		t.Fatalf("unrelated group was blocked: %v", err)
	}
	if got := channel.groupListAttempts(); got != 1 {
		t.Fatalf("group list checks = %d, want 1", got)
	}

	_, _, handled, ignoredOutcome := runtime.prepareMessageEvent(context.Background(), event)
	if handled || ignoredOutcome != "ignored_unavailable_group" {
		t.Fatalf("prepare handled=%v outcome=%q", handled, ignoredOutcome)
	}
}

func TestReplyAndRecordDoesNotSendFallbackAfterTransientSendFailure(t *testing.T) {
	channel := &failingOutboundChannel{
		err:      errors.New("发送失败，你已被移出该群，请重新加群。"),
		groupIDs: []string{"123456"},
	}
	runtime := NewRuntime(BotConfig{OwnerID: "10001"}, channel, NewPluginManager(), nil, nil, nil, nil)
	event := MessageEvent{Kind: EventKindGroup, GroupID: "123456", UserID: "10001", MessageID: "message-2"}

	outcome, err := runtime.replyAndRecord(context.Background(), event, "帮助", "replied")
	if outcome != "" || !errors.Is(err, errOutboundSend) {
		t.Fatalf("outcome=%q error=%v", outcome, err)
	}
	if errors.Is(err, errGroupSendUnavailable) {
		t.Fatalf("transient error marked group unavailable: %v", err)
	}
	if got := channel.sendAttempts(); got != 1 {
		t.Fatalf("send attempts = %d, want 1 (no immediate error fallback)", got)
	}
}

func TestUnavailableGroupClearsOnlyForNewerLiveEvent(t *testing.T) {
	channel := &failingOutboundChannel{err: errors.New("opaque send failure"), groupIDs: []string{"123456"}}
	runtime := NewRuntime(BotConfig{}, channel, NewPluginManager(), nil, nil, nil, nil)
	event := MessageEvent{Kind: EventKindGroup, GroupID: "20006", UserID: "10001", Time: time.Now().Add(-time.Minute).Unix()}
	if err := runtime.send(context.Background(), event, "first"); !errors.Is(err, errGroupSendUnavailable) {
		t.Fatalf("first send error = %v", err)
	}
	if !runtime.ignoreUnavailableGroupEvent(event) {
		t.Fatal("older queued event unexpectedly cleared unavailable group")
	}

	live := event
	live.Time = time.Now().Add(2 * time.Second).Unix()
	if runtime.ignoreUnavailableGroupEvent(live) {
		t.Fatal("newer live event did not clear unavailable group")
	}
	if err := runtime.blockedGroupSendError(live); err != nil {
		t.Fatalf("group remained blocked: %v", err)
	}
}

func TestGroupSendFailureRequiresVerifiedGroupList(t *testing.T) {
	channel := &failingOutboundChannel{
		err:          errors.New("opaque send failure"),
		groupListErr: errors.New("group list temporarily unavailable"),
	}
	runtime := NewRuntime(BotConfig{}, channel, NewPluginManager(), nil, nil, nil, nil)
	event := MessageEvent{Kind: EventKindGroup, GroupID: "20006", UserID: "10001"}
	err := runtime.send(context.Background(), event, "test")
	if !errors.Is(err, errOutboundSend) || errors.Is(err, errGroupSendUnavailable) {
		t.Fatalf("unverified send error = %v", err)
	}
	if blockedErr := runtime.blockedGroupSendError(event); blockedErr != nil {
		t.Fatalf("failed membership check blocked group: %v", blockedErr)
	}
}

func TestGroupOutboundGateDefersWhileChannelOffline(t *testing.T) {
	scripted := newScriptedBackoffChannel("123456")
	channel := &statusOverrideChannel{Channel: scripted}
	channel.setStatus(ChannelStatus{Connected: false})
	runtime := NewRuntime(BotConfig{}, channel, NewPluginManager(), nil, nil, nil, nil)
	ctx := withOutboundDeliveryPolicy(context.Background(), fastOutboundDeliveryPolicy())
	event := MessageEvent{Kind: EventKindGroup, GroupID: "123456", UserID: "10001", MessageID: "offline-1"}

	err := runtime.send(ctx, event, "断连期间生成的回复")
	if !errors.Is(err, errOutboundChannelOffline) {
		t.Fatalf("offline send error = %v", err)
	}
	if errors.Is(err, errOutboundDeliveryDropped) || errors.Is(err, errGroupSendUnavailable) {
		t.Fatalf("offline send used a terminal classification: %v", err)
	}
	if got := len(scripted.attemptTexts("123456")); got != 0 {
		t.Fatalf("offline send reached OneBot %d times", got)
	}

	// 账号风控（WS 正常但账号异常）同样延后发送而不是消耗失败窗口。
	channel.setStatus(ChannelStatus{Connected: true, AccountStatusKnown: true, AccountOnline: false})
	if err := runtime.send(ctx, event, "封控期间生成的回复"); !errors.Is(err, errOutboundChannelOffline) {
		t.Fatalf("account-down send error = %v", err)
	}
	if got := len(scripted.attemptTexts("123456")); got != 0 {
		t.Fatalf("account-down send reached OneBot %d times", got)
	}

	channel.setStatus(ChannelStatus{Connected: true})
	if err := runtime.send(ctx, event, "恢复后的回复"); err != nil {
		t.Fatalf("recovered send error = %v", err)
	}
	if got := scripted.attemptTexts("123456"); len(got) != 1 || got[0] != "恢复后的回复" {
		t.Fatalf("recovered attempts = %#v", got)
	}
}

func TestGroupSendFailureNotMarkedUnavailableWhileOffline(t *testing.T) {
	failing := &failingOutboundChannel{
		err:      errors.New("opaque OneBot send failure"),
		groupIDs: []string{"123456"},
	}
	channel := &statusOverrideChannel{Channel: failing}
	channel.setStatus(ChannelStatus{Connected: true, AccountStatusKnown: true, AccountOnline: false})
	runtime := NewRuntime(BotConfig{}, channel, NewPluginManager(), nil, nil, nil, nil)
	event := MessageEvent{Kind: EventKindGroup, GroupID: "20006", UserID: "10001"}

	err := runtime.send(context.Background(), event, "test")
	if !errors.Is(err, errOutboundSend) || errors.Is(err, errGroupSendUnavailable) {
		t.Fatalf("offline send error = %v", err)
	}
	if blockedErr := runtime.blockedGroupSendError(event); blockedErr != nil {
		t.Fatalf("offline verification blocked group: %v", blockedErr)
	}
	if got := failing.groupListAttempts(); got != 0 {
		t.Fatalf("group list consulted while account was down: %d", got)
	}
}

func TestGroupSendFailureEmptyGroupListIsUnverified(t *testing.T) {
	channel := &failingOutboundChannel{err: errors.New("opaque send failure")}
	runtime := NewRuntime(BotConfig{}, channel, NewPluginManager(), nil, nil, nil, nil)
	event := MessageEvent{Kind: EventKindGroup, GroupID: "20006", UserID: "10001"}

	err := runtime.send(context.Background(), event, "test")
	if !errors.Is(err, errOutboundSend) || errors.Is(err, errGroupSendUnavailable) {
		t.Fatalf("empty-list send error = %v", err)
	}
	if blockedErr := runtime.blockedGroupSendError(event); blockedErr != nil {
		t.Fatalf("empty group list blocked group: %v", blockedErr)
	}
}

func TestDefaultOutboundBackoffUsesLongIntervals(t *testing.T) {
	policy := defaultOutboundDeliveryPolicy()
	if policy.InitialDelay != time.Minute || policy.MaximumDelay != 15*time.Minute {
		t.Fatalf("retry delays = %s..%s", policy.InitialDelay, policy.MaximumDelay)
	}
	if policy.FailureWindow != 30*time.Minute || policy.DropCooldown != 30*time.Minute {
		t.Fatalf("failure window = %s, cooldown = %s", policy.FailureWindow, policy.DropCooldown)
	}
	event := MessageEvent{Kind: EventKindGroup, GroupID: "123456"}
	for failures, minimum := range map[int]time.Duration{
		1: time.Minute,
		2: 2 * time.Minute,
		3: 4 * time.Minute,
		4: 8 * time.Minute,
		5: 15 * time.Minute,
	} {
		delay := outboundBackoffDelay(event, "send_group_msg", failures, policy)
		if delay < minimum || delay > policy.MaximumDelay {
			t.Fatalf("failure %d delay = %s, want %s..%s", failures, delay, minimum, policy.MaximumDelay)
		}
	}
}

func TestGroupOutboundBackoffRetriesExactSendAndRecovers(t *testing.T) {
	channel := newScriptedBackoffChannel("123456", "20005")
	channel.failuresRemaining["123456"] = 2
	runtime := NewRuntime(BotConfig{}, channel, NewPluginManager(), nil, nil, nil, nil)
	ctx := withOutboundDeliveryPolicy(context.Background(), recoveringOutboundDeliveryPolicy())
	event := MessageEvent{Kind: EventKindGroup, GroupID: "123456", UserID: "10001", MessageID: "retry-1"}

	if err := runtime.send(ctx, event, "same payload"); err != nil {
		t.Fatalf("send() error = %v", err)
	}
	if got := channel.attemptTexts("123456"); len(got) != 3 || got[0] != "same payload" || got[1] != "same payload" || got[2] != "same payload" {
		t.Fatalf("attempted payloads = %#v", got)
	}
	if got := channel.groupListAttempts(); got != 2 {
		t.Fatalf("group list checks = %d, want 2", got)
	}

	if err := runtime.send(ctx, event, "next payload"); err != nil {
		t.Fatalf("next send() error = %v", err)
	}
	if got := channel.attemptTexts("123456"); len(got) != 4 || got[3] != "next payload" {
		t.Fatalf("recovered attempts = %#v", got)
	}
}

func TestGroupOutboundBackoffContinuesRemainingChunksAfterFirstSuccess(t *testing.T) {
	channel := newScriptedBackoffChannel("123456")
	channel.attemptErrors = []error{nil, errors.New("second chunk failed once"), nil}
	runtime := NewRuntime(BotConfig{ForwardReplyThreshold: 5000}, channel, NewPluginManager(), nil, nil, nil, nil)
	ctx := withOutboundDeliveryPolicy(context.Background(), recoveringOutboundDeliveryPolicy())
	event := MessageEvent{Kind: EventKindGroup, GroupID: "123456", UserID: "10001", MessageID: "chunks-1"}

	if _, err := runtime.sendWithMessageIDs(ctx, event, "first"+notificationSplitMarker+"second"); err != nil {
		t.Fatalf("sendWithMessageIDs() error = %v", err)
	}
	if got := channel.attemptTexts("123456"); len(got) != 3 || got[0] != "first" || got[1] != "second" || got[2] != "second" {
		t.Fatalf("chunk attempts = %#v", got)
	}
}

func TestGroupOutboundBackoffDropsAfterFailureWindow(t *testing.T) {
	channel := newScriptedBackoffChannel("123456", "20005")
	channel.alwaysFail["123456"] = true
	runtime := NewRuntime(BotConfig{}, channel, NewPluginManager(), nil, nil, nil, nil)
	ctx := withOutboundDeliveryPolicy(context.Background(), fastOutboundDeliveryPolicy())
	failedEvent := MessageEvent{Kind: EventKindGroup, GroupID: "123456", UserID: "10001", MessageID: "drop-1"}

	err := runtime.send(ctx, failedEvent, "eventually dropped")
	if !errors.Is(err, errOutboundDeliveryDropped) {
		t.Fatalf("send error = %v", err)
	}
	attemptsBeforeCooldown := len(channel.attemptTexts("123456"))
	if attemptsBeforeCooldown < 2 {
		t.Fatalf("attempts before drop = %d", attemptsBeforeCooldown)
	}
	channel.mu.Lock()
	channel.alwaysFail["123456"] = false
	channel.mu.Unlock()
	if err := runtime.send(ctx, failedEvent, "next message succeeds"); err != nil {
		t.Fatalf("one failed payload cooled down the group: %v", err)
	}
	if got := len(channel.attemptTexts("123456")); got != attemptsBeforeCooldown+1 {
		t.Fatalf("next message never reached transport: attempts=%d", got)
	}

	otherEvent := MessageEvent{Kind: EventKindGroup, GroupID: "20005", UserID: "10002", MessageID: "other-1"}
	if err := runtime.send(ctx, otherEvent, "other group still works"); err != nil {
		t.Fatalf("other group send error = %v", err)
	}
}

func TestGroupOutboundBackoffDropsWhenReplyDeadlineInterruptsRetry(t *testing.T) {
	channel := newScriptedBackoffChannel("123456")
	channel.alwaysFail["123456"] = true
	runtime := NewRuntime(BotConfig{}, channel, NewPluginManager(), nil, nil, nil, nil)
	policy := fastOutboundDeliveryPolicy()
	policy.InitialDelay = 100 * time.Millisecond
	policy.MaximumDelay = 100 * time.Millisecond
	policy.FailureWindow = time.Second
	ctx, cancel := context.WithTimeout(withOutboundDeliveryPolicy(context.Background(), policy), 5*time.Millisecond)
	defer cancel()

	err := runtime.send(ctx, MessageEvent{Kind: EventKindGroup, GroupID: "123456"}, "drop without regenerating")
	if !errors.Is(err, errOutboundDeliveryDropped) {
		t.Fatalf("send error = %v", err)
	}
}

func TestResolverAlternativeUsesSameBackoffGate(t *testing.T) {
	tempDir := t.TempDir()
	videoPath := filepath.Join(tempDir, "video.mp4")
	if err := os.WriteFile(videoPath, []byte("fake video"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	channel := newScriptedBackoffChannel("123456")
	channel.attemptErrors = []error{errors.New("OneBot rejected direct video")}
	runtime := NewRuntime(BotConfig{}, channel, NewPluginManager(), nil, nil, nil, nil)
	runtime.SetLocalMediaSharer(&recordingLocalMediaSharer{url: "http://127.0.0.1:18080/media/token"})
	ctx := withOutboundDeliveryPolicy(context.Background(), recoveringOutboundDeliveryPolicy())
	event := MessageEvent{Kind: EventKindGroup, GroupID: "123456", UserID: "10001", MessageID: "video-1"}

	if err := runtime.sendDirectPluginResponse(ctx, event, "链接解析结果", nil, []string{videoPath}); err != nil {
		t.Fatalf("sendDirectPluginResponse() error = %v", err)
	}
	messages := channel.attemptMessages("123456")
	if len(messages) != 3 {
		t.Fatalf("attempted messages = %#v", messages)
	}
	if len(messages[0].VideoURLs) != 1 || len(messages[1].VideoURLs) != 0 || messages[1].Text != "链接解析结果" {
		t.Fatalf("media fallback attempts = %#v", messages)
	}
	if got := channel.apiActionCount("upload_group_file"); got != 1 {
		t.Fatalf("upload_group_file calls = %d, want 1", got)
	}
}

// fastOutboundDeliveryPolicy 把退避和失败窗口都压到毫秒级，给「窗口耗尽就丢弃」
// 这类用例用：它们要的正是窗口很快过去。
func fastOutboundDeliveryPolicy() outboundDeliveryPolicy {
	return outboundDeliveryPolicy{
		InitialDelay:  time.Millisecond,
		MaximumDelay:  2 * time.Millisecond,
		FailureWindow: 8 * time.Millisecond,
		DropCooldown:  50 * time.Millisecond,
	}
}

// recoveringOutboundDeliveryPolicy 给「失败几次后恢复」的用例用：退避照样是毫秒级，
// 失败窗口却放宽到一分钟。8ms 的窗口要求几次重试加上中间的计时器唤醒全部挤在
// 8ms 内，CI 机器一卡就超出窗口，第一次失败后直接被丢弃，报成「send failed after
// 1 attempts」——测的是恢复，却因为调度抖动走进了丢弃分支。
func recoveringOutboundDeliveryPolicy() outboundDeliveryPolicy {
	policy := fastOutboundDeliveryPolicy()
	policy.FailureWindow = time.Minute
	return policy
}

// statusOverrideChannel 包装任意测试通道并允许用例中途切换连接/账号状态。
type statusOverrideChannel struct {
	Channel
	mu     sync.Mutex
	status ChannelStatus
}

func (c *statusOverrideChannel) Status() ChannelStatus {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.status
}

func (c *statusOverrideChannel) setStatus(status ChannelStatus) {
	c.mu.Lock()
	c.status = status
	c.mu.Unlock()
}

func (c *statusOverrideChannel) OutboundBackoffEnabled() bool {
	capable, ok := c.Channel.(outboundBackoffChannel)
	return ok && capable.OutboundBackoffEnabled()
}

type failingOutboundChannel struct {
	mu           sync.Mutex
	attempts     int
	listAttempts int
	err          error
	groupListErr error
	groupIDs     []string
}

func (c *failingOutboundChannel) Connect(context.Context, EventHandler) error { return nil }

func (c *failingOutboundChannel) Send(context.Context, OutgoingMessage) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.attempts++
	return c.err
}

func (c *failingOutboundChannel) CallAPI(_ context.Context, action string, _ map[string]any) (map[string]any, error) {
	if action != "get_group_list" {
		return nil, c.err
	}
	c.mu.Lock()
	c.listAttempts++
	listErr := c.groupListErr
	groupIDs := append([]string(nil), c.groupIDs...)
	c.mu.Unlock()
	if listErr != nil {
		return nil, listErr
	}
	items := make([]any, 0, len(groupIDs))
	for _, groupID := range groupIDs {
		items = append(items, map[string]any{"group_id": groupID})
	}
	return map[string]any{"items": items}, nil
}

func (c *failingOutboundChannel) Status() ChannelStatus {
	return ChannelStatus{Connected: true, SelfID: "10000"}
}

func (c *failingOutboundChannel) Close() error { return nil }

func (c *failingOutboundChannel) sendAttempts() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.attempts
}

func (c *failingOutboundChannel) groupListAttempts() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.listAttempts
}

type scriptedBackoffChannel struct {
	mu                sync.Mutex
	groups            []string
	failuresRemaining map[string]int
	alwaysFail        map[string]bool
	attemptErrors     []error
	attempts          map[string][]string
	messages          map[string][]OutgoingMessage
	apiActions        []string
	listAttempts      int
}

func newScriptedBackoffChannel(groupIDs ...string) *scriptedBackoffChannel {
	return &scriptedBackoffChannel{
		groups:            append([]string(nil), groupIDs...),
		failuresRemaining: make(map[string]int),
		alwaysFail:        make(map[string]bool),
		attempts:          make(map[string][]string),
		messages:          make(map[string][]OutgoingMessage),
	}
}

func (c *scriptedBackoffChannel) OutboundBackoffEnabled() bool { return true }
func (c *scriptedBackoffChannel) Connect(context.Context, EventHandler) error {
	return nil
}

func (c *scriptedBackoffChannel) Send(_ context.Context, msg OutgoingMessage) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	groupID := msg.GroupID
	c.attempts[groupID] = append(c.attempts[groupID], msg.Text)
	c.messages[groupID] = append(c.messages[groupID], msg)
	if len(c.attemptErrors) > 0 {
		err := c.attemptErrors[0]
		c.attemptErrors = c.attemptErrors[1:]
		return err
	}
	if c.alwaysFail[groupID] {
		return errors.New("opaque send failure")
	}
	if c.failuresRemaining[groupID] > 0 {
		c.failuresRemaining[groupID]--
		return errors.New("opaque send failure")
	}
	return nil
}

func (c *scriptedBackoffChannel) CallAPI(_ context.Context, action string, _ map[string]any) (map[string]any, error) {
	c.mu.Lock()
	c.apiActions = append(c.apiActions, action)
	c.mu.Unlock()
	if action != "get_group_list" {
		return map[string]any{"message_id": int64(1)}, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.listAttempts++
	items := make([]any, 0, len(c.groups))
	for _, groupID := range c.groups {
		items = append(items, map[string]any{"group_id": groupID})
	}
	return map[string]any{"items": items}, nil
}

func (c *scriptedBackoffChannel) Status() ChannelStatus {
	return ChannelStatus{Connected: true, SelfID: "10000"}
}
func (c *scriptedBackoffChannel) Close() error { return nil }

func (c *scriptedBackoffChannel) attemptTexts(groupID string) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.attempts[groupID]...)
}

func (c *scriptedBackoffChannel) groupListAttempts() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.listAttempts
}

func (c *scriptedBackoffChannel) attemptMessages(groupID string) []OutgoingMessage {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]OutgoingMessage(nil), c.messages[groupID]...)
}

func (c *scriptedBackoffChannel) apiActionCount(action string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	count := 0
	for _, attempted := range c.apiActions {
		if attempted == action {
			count++
		}
	}
	return count
}

func TestGroupPermanentUIDFailureDoesNotRetryOrBlockNextMessage(t *testing.T) {
	channel := newScriptedBackoffChannel("123456")
	channel.attemptErrors = []error{errors.New("failed to resolve UID for UIN 41001")}
	runtime := NewRuntime(BotConfig{}, channel, NewPluginManager(), nil, nil, nil, nil)
	event := MessageEvent{Kind: EventKindGroup, GroupID: "123456", MessageID: "bad-mention"}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := runtime.send(ctx, event, "bad mention"); !isPermanentSendRejection(err) {
		t.Fatalf("error = %v", err)
	}
	if err := runtime.send(ctx, event, "next message"); err != nil {
		t.Fatal(err)
	}
	if got := channel.attemptTexts("123456"); len(got) != 2 {
		t.Fatalf("attempts = %v", got)
	}
}

func TestGroupRetryBudgetExactlyThreeAndDoesNotCoolDownGroup(t *testing.T) {
	channel := newScriptedBackoffChannel("123456")
	channel.alwaysFail["123456"] = true
	runtime := NewRuntime(BotConfig{}, channel, NewPluginManager(), nil, nil, nil, nil)
	logs := &captureAppLogs{}
	runtime.SetAppLogWriter(logs)
	ctx := withOutboundDeliveryPolicy(context.Background(), recoveringOutboundDeliveryPolicy())
	event := MessageEvent{Kind: EventKindGroup, GroupID: "123456", MessageID: "bad"}
	if err := runtime.send(ctx, event, "fail three times"); !errors.Is(err, errOutboundDeliveryDropped) {
		t.Fatalf("error = %v", err)
	}
	if got := len(channel.attemptTexts("123456")); got != 3 {
		t.Fatalf("attempts=%d, want 3", got)
	}
	entries := logs.entriesSnapshot()
	if countAppLogAction(entries, "outbound_delivery_backoff") != 2 || countAppLogAction(entries, "outbound_delivery_dropped") != 1 {
		t.Fatalf("missing retry/failure audit: %+v", entries)
	}
	for _, entry := range entries {
		if entry.Action == "outbound_delivery_dropped" && (string(entry.Level) != "error" || entry.Detail == "" || entry.Metadata["failure_count"] != 3) {
			t.Fatalf("incomplete final failure log: %+v", entry)
		}
	}
	channel.mu.Lock()
	channel.alwaysFail["123456"] = false
	channel.mu.Unlock()
	event.MessageID = "good"
	if err := runtime.send(ctx, event, "later message"); err != nil {
		t.Fatal(err)
	}
}

func TestRetrySleepDoesNotHoldGroupSendLock(t *testing.T) {
	channel := newScriptedBackoffChannel("123456")
	runtime := NewRuntime(BotConfig{}, channel, NewPluginManager(), nil, nil, nil, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	failed := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		attempt := 0
		_, err := runtime.executeOutboundCall(ctx, MessageEvent{Kind: EventKindGroup, GroupID: "123456"}, "send_group_msg", func(context.Context) (map[string]any, error) {
			attempt++
			if attempt == 1 {
				close(failed)
				return nil, errors.New("temporary failure")
			}
			return map[string]any{"message_id": "recovered"}, nil
		})
		done <- err
	}()
	<-failed
	other, stop := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer stop()
	if err := runtime.send(other, MessageEvent{Kind: EventKindGroup, GroupID: "123456"}, "while retry waits"); err != nil {
		t.Fatalf("retry blocks same group: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

// 只向当前模型请求生成；平台发送由本地故障通道接管，不向真实群发测试消息。
func TestLiveProductionGroupDeliveryFailureReplay(t *testing.T) {
	client := liveLLMClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	resp, err := client.Generate(ctx, llm.GenerateRequest{Messages: []llm.Message{
		{Role: "system", Content: "你是群聊机器人嘉然，用一句简短中文回复，不使用工具。"},
		{Role: "user", Content: "嘉然活了"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if resp == nil || strings.TrimSpace(resp.Text) == "" {
		t.Fatal("model returned empty reply")
	}
	channel := newScriptedBackoffChannel("51001")
	channel.attemptErrors = []error{errors.New("failed to resolve UID for UIN 41001")}
	runtime := NewRuntime(BotConfig{}, channel, NewPluginManager(), nil, nil, nil, nil)
	logs := &captureAppLogs{}
	runtime.SetAppLogWriter(logs)
	event := MessageEvent{Kind: EventKindGroup, GroupID: "51001", UserID: "replay", MessageID: "replay-uid"}
	if err := runtime.send(ctx, event, resp.Text); !isPermanentSendRejection(err) {
		t.Fatalf("UID failure not terminal: %v", err)
	}
	channel.mu.Lock()
	channel.alwaysFail[event.GroupID] = true
	channel.mu.Unlock()
	if err := runtime.send(ctx, event, resp.Text); !errors.Is(err, errOutboundDeliveryDropped) {
		t.Fatalf("retry failure not terminal: %v", err)
	}
	channel.mu.Lock()
	channel.alwaysFail[event.GroupID] = false
	channel.mu.Unlock()
	if err := runtime.send(ctx, event, resp.Text); err != nil {
		t.Fatal(err)
	}
	if got := len(channel.attemptTexts(event.GroupID)); got != 5 {
		t.Fatalf("attempts=%d, expected 1+3+1", got)
	}
	if !hasAppLogAction(logs.entriesSnapshot(), "outbound_delivery_dropped") {
		t.Fatal("final failure missing from app log")
	}
	t.Logf("model=%s: production UID failure stopped after 1 attempt; transient failure stopped after 3; next same-group message sent; app log persisted", resp.Model)
}

func TestMixedPayloadAndTransportFailuresShareThreeAttemptBudget(t *testing.T) {
	channel := newScriptedBackoffChannel("123456")
	channel.attemptErrors = []error{errors.New(`message element "image" requires a file/url source`), errors.New("temporary bridge failure"), errors.New("temporary bridge failure")}
	runtime := NewRuntime(BotConfig{}, channel, NewPluginManager(), nil, nil, nil, nil)
	ctx := withOutboundDeliveryPolicy(context.Background(), recoveringOutboundDeliveryPolicy())
	if err := runtime.send(ctx, MessageEvent{Kind: EventKindGroup, GroupID: "123456"}, "mixed failures"); !errors.Is(err, errOutboundDeliveryDropped) {
		t.Fatalf("error=%v", err)
	}
	if got := len(channel.attemptTexts("123456")); got != 3 {
		t.Fatalf("nested retries exceeded budget: %d", got)
	}
}

func TestGroupDeliveryExhaustionCompletesInboundWithoutRegeneration(t *testing.T) {
	store := newMemoryInboundEventStore()
	channel := newScriptedBackoffChannel("20005")
	channel.alwaysFail["20005"] = true
	provider := &sequenceLLMProvider{replies: []string{"这是一条回复"}}
	runtime := newQueuedTestRuntime(channel, store, provider)
	event := queuedDirectTestEvent("send-budget", time.Now().Unix())
	event.GroupID = "20005"
	id, inserted, err := store.EnqueueInboundEvent(context.Background(), sessionKey(event), event)
	if err != nil || !inserted {
		t.Fatalf("enqueue %v %v", inserted, err)
	}
	startTestRuntime(t, runtime)
	waitForCondition(t, 10*time.Second, func() bool { return store.isDone(id) })
	if err := runtime.Stop(); err != nil {
		t.Fatal(err)
	}
	outcome, attempts := store.outcomeAndAttempts(id)
	if outcome != "dropped_outbound_delivery" || attempts != 1 {
		t.Fatalf("queue regenerated failed delivery: outcome=%s attempts=%d", outcome, attempts)
	}
	if got := len(channel.attemptTexts("20005")); got != 3 {
		t.Fatalf("send attempts=%d", got)
	}
}
