// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func botMuteNotice(userID, subType, duration string) MessageEvent {
	data := map[string]string{"notice_type": botMuteNoticeSubType, "sub_type": subType}
	if duration != "" {
		data["duration"] = duration
	}
	return MessageEvent{
		Kind: EventKindNotice, SubType: botMuteNoticeSubType, Platform: PlatformTelegram,
		ProfileID: "a", GroupID: "g1", SelfID: "42", UserID: userID, Time: time.Now().Unix(),
		Segments: []MessageSegment{{Type: "notice", Data: data}},
	}
}

// OneBot 的 group_ban 通知要把 duration 带下来，否则算不出什么时候解禁。
func TestOneBotGroupBanNoticeKeepsDuration(t *testing.T) {
	var envelope oneBotEnvelope
	raw := `{"post_type":"notice","notice_type":"group_ban","sub_type":"ban","self_id":42,"user_id":42,"operator_id":7,"group_id":123,"duration":600,"time":1700000000}`
	if err := json.Unmarshal([]byte(raw), &envelope); err != nil {
		t.Fatal(err)
	}
	event := messageEventFromEnvelope(envelope)
	if event.Kind != EventKindNotice || event.SubType != botMuteNoticeSubType {
		t.Fatalf("kind=%q subtype=%q", event.Kind, event.SubType)
	}
	data := noticeSegmentData(event)
	if data["duration"] != "600" || data["sub_type"] != "ban" {
		t.Fatalf("notice data = %v", data)
	}
}

// 被禁言期间：消息照常进历史和长期记忆，但一次模型调用都不花。解禁后恢复。
func TestMutedGroupRecordsContextButSkipsModelCalls(t *testing.T) {
	h := newDisabledGroupSkipHarness(t, BotConfig{}, true)
	if !h.runtime.observeBotMuteNotice(context.Background(), botMuteNotice("42", "ban", "600")) {
		t.Fatal("bot's own ban notice was not consumed")
	}
	event := disabledGroupSignalEvent()
	event.ToMe = true

	_, _, handled, outcome := h.runtime.prepareMessageEvent(context.Background(), event)
	if handled || outcome != "ignored_bot_muted" {
		t.Fatalf("handled=%v outcome=%q, want ignored_bot_muted", handled, outcome)
	}
	if got := h.provider.callCount(); got != 0 {
		t.Fatalf("muted group made %d model calls, want 0", got)
	}
	if h.history.searches != 0 {
		t.Fatalf("muted group ran %d cross-group searches, want 0", h.history.searches)
	}
	if got := len(h.runtime.history[sessionKey(event)]); got == 0 {
		t.Fatal("muted group message was not kept in history")
	}
	if got := len(h.memory.enqueued); got == 0 {
		t.Fatal("muted group message was not enqueued into long-term memory")
	}

	h.runtime.observeBotMuteNotice(context.Background(), botMuteNotice("42", "lift_ban", "0"))
	event.MessageID = "m2"
	event.ToMe = false
	h.runtime.prepareMessageEvent(context.Background(), event)
	if h.provider.callCount() == 0 {
		t.Fatal("after unmute the group never reached the reply router")
	}
}

func TestMutedReplyPauseCanBeTurnedOff(t *testing.T) {
	h := newDisabledGroupSkipHarness(t, BotConfig{MutedReplyPauseEnabled: boolPointer(false)}, true)
	h.runtime.observeBotMuteNotice(context.Background(), botMuteNotice("42", "ban", "600"))
	_, _, _, outcome := h.runtime.prepareMessageEvent(context.Background(), disabledGroupSignalEvent())
	if outcome == "ignored_bot_muted" {
		t.Fatal("pause switched off but the muted group was still skipped")
	}
	if h.provider.callCount() == 0 {
		t.Fatal("pause switched off but the reply router never ran")
	}
}

// 分群开关覆盖机器人级：机器人开着，这个群单独关掉。
func TestGroupOverridesMutedReplyPause(t *testing.T) {
	h := newDisabledGroupSkipHarness(t, BotConfig{}, true)
	store := &testWritableGroupConfigStore{}
	base := h.runtime.profileConfig("a")
	if _, err := store.SaveGroupConfig(GroupConfig{BotProfileID: "a", GroupID: "g1", Enabled: true, EnabledSet: true, MutedReplyPauseEnabled: boolPointer(false)}, base); err != nil {
		t.Fatal(err)
	}
	h.runtime.SetGroupConfigStore(store)
	h.runtime.observeBotMuteNotice(context.Background(), botMuteNotice("42", "ban", "600"))
	if _, muted := h.runtime.botMutedForReply(disabledGroupSignalEvent()); muted {
		t.Fatal("group turned the pause off but it still applied")
	}
}

// 别人被禁言不关机器人的事；到期的禁言自动失效。
func TestBotMuteIgnoresOtherMembersAndExpires(t *testing.T) {
	h := newDisabledGroupSkipHarness(t, BotConfig{}, true)
	if h.runtime.observeBotMuteNotice(context.Background(), botMuteNotice("u1", "ban", "600")) {
		t.Fatal("another member's ban was treated as the bot's")
	}
	if _, muted := h.runtime.botMutedForReply(disabledGroupSignalEvent()); muted {
		t.Fatal("another member's ban paused the bot")
	}
	event := disabledGroupSignalEvent()
	target := event
	h.runtime.setBotMute(target, botMuteState{PersonalUntil: time.Now().Add(20 * time.Millisecond)})
	if _, muted := h.runtime.botMutedForReply(event); !muted {
		t.Fatal("fresh mute not applied")
	}
	time.Sleep(30 * time.Millisecond)
	if _, muted := h.runtime.botMutedForReply(event); muted {
		t.Fatal("expired mute still applied")
	}
}

// 全员禁言查不到机器人身份时不暂停：宁可白生成一次，也不能让管理员机器人莫名沉默。
func TestWholeGroupMuteWithoutVerifiedRoleDoesNotPause(t *testing.T) {
	h := newDisabledGroupSkipHarness(t, BotConfig{}, true)
	h.runtime.observeBotMuteNotice(context.Background(), botMuteNotice("0", "ban", "-1"))
	if _, muted := h.runtime.botMutedForReply(disabledGroupSignalEvent()); muted {
		t.Fatal("whole-group mute paused a bot whose role was never verified")
	}
}

func TestBotMuteUntil(t *testing.T) {
	now := time.Unix(1_700_000_100, 0)
	if until, indefinite := botMuteUntil(map[string]string{"duration": "60"}, 1_700_000_000, now); indefinite || !until.Equal(time.Unix(1_700_000_060, 0)) {
		t.Fatalf("duration from event time: until=%v indefinite=%v", until, indefinite)
	}
	if _, indefinite := botMuteUntil(map[string]string{"until": "0"}, 0, now); !indefinite {
		t.Fatal("telegram until=0 should be indefinite")
	}
	if until, _ := botMuteUntil(map[string]string{"until": "1700001000"}, 0, now); !until.Equal(time.Unix(1_700_001_000, 0)) {
		t.Fatalf("telegram until = %v", until)
	}
}

func TestTelegramBotMuteEvent(t *testing.T) {
	no := false
	yes := true
	update := &telegramMemberUpdate{
		Chat: telegramChat{ID: -100, Type: "supergroup"},
		Old:  telegramChatMember{User: telegramUser{ID: 42}, Status: "member"},
		New:  telegramChatMember{User: telegramUser{ID: 42}, Status: "restricted", IsMember: true, CanSendMessages: &no, UntilDate: 1_700_001_000},
	}
	event := telegramBotMuteEvent(update, "42")
	data := noticeSegmentData(event)
	if event.SubType != botMuteNoticeSubType || data["sub_type"] != "ban" || data["until"] != "1700001000" || event.UserID != "42" {
		t.Fatalf("ban event = %+v data=%v", event, data)
	}
	update.Old, update.New = update.New, telegramChatMember{User: telegramUser{ID: 42}, Status: "restricted", IsMember: true, CanSendMessages: &yes}
	if data := noticeSegmentData(telegramBotMuteEvent(update, "42")); data["sub_type"] != "lift_ban" {
		t.Fatalf("lift data = %v", data)
	}
	update.Old = update.New
	if event := telegramBotMuteEvent(update, "42"); event.Kind != "" {
		t.Fatalf("no permission change should produce no event: %+v", event)
	}
}

// mutedOneBotChannel 模拟错过禁言通知的情况：群还在，发送失败，查机器人自己的
// 成员信息能看到 shut_up_timestamp。
type mutedOneBotChannel struct {
	mu       sync.Mutex
	attempts int
	until    int64
	role     string
	allShut  int
}

func (c *mutedOneBotChannel) Connect(context.Context, EventHandler) error { return nil }
func (c *mutedOneBotChannel) Send(context.Context, OutgoingMessage) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.attempts++
	return errors.New("opaque NapCat send failure")
}
func (c *mutedOneBotChannel) CallAPI(_ context.Context, action string, params map[string]any) (map[string]any, error) {
	switch action {
	case "get_group_list":
		return map[string]any{"items": []any{map[string]any{"group_id": "20006"}}}, nil
	case "get_group_member_info":
		role := c.role
		if role == "" {
			role = "member"
		}
		return map[string]any{"user_id": "10000", "role": role, "shut_up_timestamp": strconv.FormatInt(c.until, 10)}, nil
	case "get_group_info":
		// SnowLuma / NapCat：全员禁言开启时为 -1。
		return map[string]any{"group_id": "20006", "group_all_shut": c.allShut}, nil
	}
	return nil, errors.New("unsupported")
}
func (c *mutedOneBotChannel) Status() ChannelStatus {
	return ChannelStatus{Connected: true, SelfID: "10000"}
}
func (c *mutedOneBotChannel) Close() error                 { return nil }
func (c *mutedOneBotChannel) OutboundBackoffEnabled() bool { return true }
func (c *mutedOneBotChannel) sendAttempts() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.attempts
}

// 错过通知时：第一次发送失败后查出禁言，不进 30 分钟退避、不让队列重跑，后面的
// 发送在到达平台之前就拦下。
func TestSendFailureDetectsMissedMute(t *testing.T) {
	channel := &mutedOneBotChannel{until: time.Now().Add(time.Hour).Unix()}
	runtime := NewRuntime(BotConfig{OwnerID: "10001"}, channel, NewPluginManager(), nil, nil, nil, nil)
	event := MessageEvent{Kind: EventKindGroup, GroupID: "20006", UserID: "10001", MessageID: "message-1", Time: time.Now().Unix()}

	outcome, err := runtime.replyAndRecord(context.Background(), event, "帮助", "replied")
	if err != nil || outcome != "ignored_bot_muted" {
		t.Fatalf("outcome=%q err=%v, want ignored_bot_muted", outcome, err)
	}
	if got := channel.sendAttempts(); got != 1 {
		t.Fatalf("send attempts = %d, want 1 (no backoff retries)", got)
	}
	if err := runtime.send(context.Background(), event, "再发一条"); !errors.Is(err, errBotMuted) {
		t.Fatalf("follow-up send error = %v, want errBotMuted", err)
	}
	if got := channel.sendAttempts(); got != 1 {
		t.Fatalf("muted send reached the platform; attempts = %d", got)
	}
	if _, _, handled, outcome := runtime.prepareMessageEvent(context.Background(), event); handled || outcome != "ignored_bot_muted" {
		t.Fatalf("prepare handled=%v outcome=%q", handled, outcome)
	}
}

// user_id 为 0 的禁言通知不能直接当全员禁言：SnowLuma 解析不出被禁言者的 QQ 号时
// 也会填 0。以查到的 group_all_shut 和机器人身份为准。
func TestZeroUserBanNoticeTrustsProbe(t *testing.T) {
	cases := []struct {
		name    string
		role    string
		allShut int
		want    bool
	}{
		{"unresolved member ban, group not all-shut", "member", 0, false},
		{"whole group mute, bot is member", "member", -1, true},
		{"whole group mute, bot is admin", "admin", -1, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			channel := &mutedOneBotChannel{role: tc.role, allShut: tc.allShut}
			runtime := NewRuntime(BotConfig{OwnerID: "10001"}, channel, NewPluginManager(), nil, nil, nil, nil)
			notice := MessageEvent{
				Kind: EventKindNotice, SubType: botMuteNoticeSubType, GroupID: "20006", SelfID: "10000", UserID: "0",
				Segments: []MessageSegment{{Type: "notice", Data: map[string]string{"sub_type": "ban", "duration": "2147483647"}}},
			}
			runtime.observeBotMuteNotice(context.Background(), notice)
			_, muted := runtime.botMutedForReply(MessageEvent{Kind: EventKindGroup, GroupID: "20006"})
			if muted != tc.want {
				t.Fatalf("muted=%v, want %v", muted, tc.want)
			}
		})
	}
}

// noticeRecordingHistory 包一层历史存储，把写进事件页「通知」栏的记录攒下来。
type noticeRecordingHistory struct {
	MessageHistoryStore
	mu      sync.Mutex
	notices []MessageEvent
}

func (s *noticeRecordingHistory) RecordNoticeEvent(_ context.Context, _ string, event MessageEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.notices = append(s.notices, event)
	return nil
}

func (s *noticeRecordingHistory) texts() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.notices))
	for _, notice := range s.notices {
		out = append(out, notice.RawMessage)
	}
	return out
}

// 禁言和解禁要留在事件页上：谁禁的、禁多久、到什么时候。
func TestBotMuteChangesAreRecordedAsNotices(t *testing.T) {
	h := newDisabledGroupSkipHarness(t, BotConfig{}, true)
	store := &noticeRecordingHistory{MessageHistoryStore: newMemoryMessageHistoryStore()}
	h.runtime.SetMessageHistoryStore(store)

	ban := botMuteNotice("42", "ban", "600")
	ban.OperatorID = "7"
	h.runtime.observeBotMuteNotice(context.Background(), ban)
	h.runtime.observeBotMuteNotice(context.Background(), botMuteNotice("42", "lift_ban", "0"))
	h.runtime.observeBotMuteNotice(context.Background(), botMuteNotice("u1", "ban", "600"))

	got := store.texts()
	if len(got) != 2 {
		t.Fatalf("recorded notices = %q, want ban + lift only", got)
	}
	if !strings.Contains(got[0], "机器人被禁言 10 分钟") || !strings.Contains(got[0], "操作人 7") {
		t.Fatalf("ban notice = %q", got[0])
	}
	if !strings.Contains(got[1], "机器人被解除禁言") {
		t.Fatalf("lift notice = %q", got[1])
	}
}

// 暂停期间转不转写语音可配置：默认照转，关掉才跳过；没被禁言时一律照转。
func TestMutedVoiceTranscriptionIsConfigurable(t *testing.T) {
	event := disabledGroupSignalEvent()

	h := newDisabledGroupSkipHarness(t, BotConfig{}, true)
	h.runtime.observeBotMuteNotice(context.Background(), botMuteNotice("42", "ban", "600"))
	if h.runtime.skipVoiceTranscriptionWhileMuted(event) {
		t.Fatal("default config skipped voice transcription while muted")
	}

	off := newDisabledGroupSkipHarness(t, BotConfig{MutedVoiceTranscriptionEnabled: boolPointer(false)}, true)
	if off.runtime.skipVoiceTranscriptionWhileMuted(event) {
		t.Fatal("transcription skipped although the bot is not muted")
	}
	off.runtime.observeBotMuteNotice(context.Background(), botMuteNotice("42", "ban", "600"))
	if !off.runtime.skipVoiceTranscriptionWhileMuted(event) {
		t.Fatal("transcription switched off but still ran while muted")
	}
}

func TestFormatMuteDuration(t *testing.T) {
	for d, want := range map[time.Duration]string{
		10 * time.Minute: "10 分钟",
		90 * time.Minute: "1 小时 30 分钟",
		30*24*time.Hour + 2*time.Hour + time.Minute: "30 天 2 小时",
		20 * time.Second: "不到 1 分钟",
	} {
		if got := formatMuteDuration(d); got != want {
			t.Fatalf("formatMuteDuration(%v) = %q, want %q", d, got, want)
		}
	}
}

// 暂停期间照常做回复判断：判断认为该回的，记成「判断该回但未发送」，不进生成。
func TestMutedReplyJudgmentRecordsWouldReply(t *testing.T) {
	h := newDisabledGroupSkipHarness(t, BotConfig{MutedReplyJudgmentEnabled: boolPointer(true)}, true)
	h.runtime.observeBotMuteNotice(context.Background(), botMuteNotice("42", "ban", "600"))

	direct := disabledGroupSignalEvent()
	direct.ToMe = true
	_, _, handled, outcome := h.runtime.prepareMessageEvent(context.Background(), direct)
	if handled || outcome != "ignored_bot_muted_judged" {
		t.Fatalf("direct mention while muted: handled=%v outcome=%q", handled, outcome)
	}

	// 不点名的消息要走接话路由：判断这一步真的调了模型。
	chatter := disabledGroupSignalEvent()
	chatter.MessageID = "m2"
	_, _, handled, _ = h.runtime.prepareMessageEvent(context.Background(), chatter)
	if handled {
		t.Fatal("muted group handed a message to reply generation")
	}
	if h.provider.callCount() == 0 {
		t.Fatal("reply judgment enabled but the router never ran")
	}
}

// 识图可以单独关：关掉后这条消息标记为不识图。
func TestMutedImageDescriptionIsConfigurable(t *testing.T) {
	for _, tc := range []struct {
		name     string
		cfg      BotConfig
		wantSkip bool
	}{
		{"default keeps describing images", BotConfig{}, false},
		{"switched off", BotConfig{MutedImageDescriptionEnabled: boolPointer(false)}, true},
		{"switched off with judgment on", BotConfig{MutedImageDescriptionEnabled: boolPointer(false), MutedReplyJudgmentEnabled: boolPointer(true)}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newDisabledGroupSkipHarness(t, tc.cfg, true)
			h.runtime.observeBotMuteNotice(context.Background(), botMuteNotice("42", "ban", "600"))
			event, _, _, _ := h.runtime.prepareMessageEvent(context.Background(), disabledGroupSignalEvent())
			if event.mutedSkipImages != tc.wantSkip {
				t.Fatalf("mutedSkipImages=%v, want %v", event.mutedSkipImages, tc.wantSkip)
			}
		})
	}
}
