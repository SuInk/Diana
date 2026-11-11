// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

const qqFeedbackTestEventID = qqInteractionEventPrefix + "feedback-1"

func qqFeedbackTestNotice(opt string, checked int) MessageEvent {
	var interaction qqOfficialInteraction
	interaction.ID = "feedback-1"
	interaction.ChatType = qqInteractionChatC2C
	interaction.UserOpenID = "U1"
	interaction.Data.Type = qqInteractionTypeFeedback
	interaction.Data.Resolved.MessageID = "m1"
	interaction.Data.Resolved.FeedbackOpt = opt
	interaction.Data.Resolved.Checked = checked
	event, _ := qqOfficialFeedbackEvent(qqFeedbackTestEventID, interaction, "self")
	return event
}

// newQQFeedbackTestRuntime 给出一个私聊里已经回过一条「m1」的运行时，没指定准入时放开私聊。
func newQQFeedbackTestRuntime(cfg BotConfig, reply string) (*Runtime, *recordingChannel, *capturingLLMProvider, *noticeRecordingHistory) {
	if cfg.PrivateAdmission.Mode == "" {
		cfg.PrivateAdmission.Mode = PrivateAdmissionAll
	}
	channel := &recordingChannel{}
	provider := &capturingLLMProvider{reply: reply}
	runtime := NewRuntime(cfg, channel, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
		return provider, nil
	})
	store := &noticeRecordingHistory{MessageHistoryStore: newMemoryMessageHistoryStore()}
	runtime.SetMessageHistoryStore(store)
	runtime.remember(MessageEvent{
		Platform: PlatformQQOfficial, Kind: EventKindPrivate, MessageType: "private",
		UserID: "U1", SelfID: "self", MessageID: "m1", Outbound: true, Time: 1700000000,
		RawMessage: "明天北京有小雨，记得带伞",
		Segments:   []MessageSegment{{Type: "text", Data: map[string]string{"text": "明天北京有小雨，记得带伞"}}},
	})
	return runtime, channel, provider, store
}

func qqFeedbackHistoryLines(runtime *Runtime) []string {
	var lines []string
	for _, item := range runtime.sessionHistory(MessageEvent{Platform: PlatformQQOfficial, Kind: EventKindPrivate, UserID: "U1"}) {
		if isQQFeedbackHistoryEvent(item) {
			lines = append(lines, PlainText(item.Segments))
		}
	}
	return lines
}

func TestQQFeedbackAction(t *testing.T) {
	cases := []struct {
		opt     string
		checked int
		want    string
	}{
		{"LIKE", 1, qqFeedbackLike},
		{"like", 1, qqFeedbackLike},
		{"DISLIKE", 1, qqFeedbackDislike},
		{"thumb_down", 1, qqFeedbackDislike},
		{"LIKE", 0, qqFeedbackCancel},
		{"DISLIKE", 0, qqFeedbackCancel},
		{"", 1, qqFeedbackUnknown},
		{"3", 1, qqFeedbackUnknown},
	}
	for _, tc := range cases {
		if got := qqFeedbackAction(tc.opt, tc.checked); got != tc.want {
			t.Fatalf("qqFeedbackAction(%q, %d) = %q, want %q", tc.opt, tc.checked, got, tc.want)
		}
	}
}

// 单聊的赞踩转成通知交给运行时，不应答；群里的不处理。
func TestQQOfficialFeedbackInteractionBecomesNotice(t *testing.T) {
	api := newQQFakeAPI(t)
	channel := api.channel()
	var got []MessageEvent
	channel.handler = func(_ context.Context, event MessageEvent) error {
		got = append(got, event)
		return nil
	}
	send := func(chatType int) {
		data, _ := json.Marshal(map[string]any{
			"id": "feedback-1", "chat_type": chatType, "user_openid": "U1", "group_openid": "G1", "group_member_openid": "U1",
			"data": map[string]any{"type": qqInteractionTypeFeedback, "resolved": map[string]any{"feedback_opt": "DISLIKE", "checked": 1, "message_id": "m1"}},
		})
		channel.handleInteraction(context.Background(), qqGatewayPayload{ID: qqFeedbackTestEventID, Data: data})
	}
	send(qqInteractionChatGroup)
	if len(got) != 0 {
		t.Fatalf("group feedback forwarded: %#v", got)
	}
	send(qqInteractionChatC2C)
	if len(got) != 1 || len(api.paths) != 0 {
		t.Fatalf("events = %#v, paths = %v", got, api.paths)
	}
	event := got[0]
	if event.Kind != EventKindNotice || event.SubType != qqFeedbackSubType || event.UserID != "U1" ||
		event.TargetID != "m1" || event.MessageID != qqFeedbackTestEventID || event.GroupID != "" {
		t.Fatalf("event = %#v", event)
	}
	if action, opt, ok := qqFeedbackFromEvent(event); !ok || action != qqFeedbackDislike || opt != "DISLIKE" {
		t.Fatalf("action = %q opt = %q ok = %v", action, opt, ok)
	}
}

// 点赞：记进事件页和私聊历史，模型愿意回就按互动事件 ID 回一句。
func TestQQFeedbackLikeIsRecordedAndReplied(t *testing.T) {
	runtime, channel, provider, store := newQQFeedbackTestRuntime(BotConfig{SystemPrompt: "你是猫娘"}, `{"reply":"嘿嘿，有用就好"}`)
	if err := runtime.handleNotice(context.Background(), qqFeedbackTestNotice("LIKE", 1)); err != nil {
		t.Fatal(err)
	}
	notices := store.texts()
	if len(notices) != 1 || notices[0] != "[通知] 对方给回复「明天北京有小雨，记得带伞」点了赞" {
		t.Fatalf("notices = %#v", notices)
	}
	if lines := qqFeedbackHistoryLines(runtime); len(lines) != 1 || lines[0] != "[回复反馈] 给回复「明天北京有小雨，记得带伞」点了赞" {
		t.Fatalf("history = %#v", lines)
	}
	sent := channel.sentSnapshot()
	if len(sent) != 1 || sent[0].Text != "嘿嘿，有用就好" || sent[0].UserID != "U1" || sent[0].GroupID != "" {
		t.Fatalf("sent = %#v", sent)
	}
	if firstNonEmpty(sent[0].PassiveReplyMessageID, sent[0].ReplyMessageID) != qqFeedbackTestEventID {
		t.Fatalf("reply id = %q / %q", sent[0].PassiveReplyMessageID, sent[0].ReplyMessageID)
	}
	prompt := provider.requestSnapshot().Messages
	last := prompt[len(prompt)-1].Content
	if !strings.Contains(last, "点了赞") || !strings.Contains(last, "明天北京有小雨") {
		t.Fatalf("prompt = %s", last)
	}
	if !strings.Contains(prompt[0].Content, "你是猫娘") {
		t.Fatalf("persona missing: %s", prompt[0].Content)
	}
}

// 点踩：同样记录；模型留空就不发消息，旁白仍然留在历史里给下一轮看。
func TestQQFeedbackDislikeIsRecordedAndMayStaySilent(t *testing.T) {
	runtime, channel, provider, store := newQQFeedbackTestRuntime(BotConfig{}, `{"reply":""}`)
	if err := runtime.handleNotice(context.Background(), qqFeedbackTestNotice("DISLIKE", 1)); err != nil {
		t.Fatal(err)
	}
	if notices := store.texts(); len(notices) != 1 || !strings.HasSuffix(notices[0], "点了踩") {
		t.Fatalf("notices = %#v", notices)
	}
	if sent := channel.sentSnapshot(); len(sent) != 0 {
		t.Fatalf("sent = %#v", sent)
	}
	prompt := provider.requestSnapshot().Messages
	if last := prompt[len(prompt)-1].Content; !strings.Contains(last, "点了踩") {
		t.Fatalf("prompt = %s", last)
	}
	history := runtime.sessionHistory(MessageEvent{Platform: PlatformQQOfficial, Kind: EventKindPrivate, UserID: "U1"})
	narration := history[len(history)-1]
	if !isQQFeedbackHistoryEvent(narration) || !isNarrationHistoryEvent(narration) {
		t.Fatalf("last history = %#v", narration)
	}
	line := qqFeedbackHistoryPromptText(narration)
	if !strings.Contains(line, "[回复反馈，不是对方说的话] 对方给你的回复「明天北京有小雨，记得带伞」点了踩") {
		t.Fatalf("prompt line = %q", line)
	}
}

func TestQQFeedbackDislikeReplies(t *testing.T) {
	runtime, channel, _, _ := newQQFeedbackTestRuntime(BotConfig{}, "```json\n{\"reply\":\"哪里说得不对呀\"}\n```")
	if err := runtime.handleNotice(context.Background(), qqFeedbackTestNotice("DISLIKE", 1)); err != nil {
		t.Fatal(err)
	}
	if sent := channel.sentSnapshot(); len(sent) != 1 || sent[0].Text != "哪里说得不对呀" {
		t.Fatalf("sent = %#v", sent)
	}
}

// 取消：只记录，不问模型、不发消息。
func TestQQFeedbackCancelIsRecordedWithoutReply(t *testing.T) {
	runtime, channel, provider, store := newQQFeedbackTestRuntime(BotConfig{}, `{"reply":"不该出现"}`)
	if err := runtime.handleNotice(context.Background(), qqFeedbackTestNotice("LIKE", 0)); err != nil {
		t.Fatal(err)
	}
	if notices := store.texts(); len(notices) != 1 || notices[0] != "[通知] 对方取消了对回复「明天北京有小雨，记得带伞」的赞踩" {
		t.Fatalf("notices = %#v", notices)
	}
	if lines := qqFeedbackHistoryLines(runtime); len(lines) != 1 {
		t.Fatalf("history = %#v", lines)
	}
	if sent := channel.sentSnapshot(); len(sent) != 0 || provider.calls != 0 {
		t.Fatalf("sent = %#v, llm calls = %d", sent, provider.calls)
	}
}

// 来回点：一分钟内只回一次，但每次都记。
func TestQQFeedbackReplyCooldown(t *testing.T) {
	runtime, channel, _, store := newQQFeedbackTestRuntime(BotConfig{}, `{"reply":"收到"}`)
	for _, opt := range []string{"LIKE", "DISLIKE"} {
		if err := runtime.handleNotice(context.Background(), qqFeedbackTestNotice(opt, 1)); err != nil {
			t.Fatal(err)
		}
	}
	if sent := channel.sentSnapshot(); len(sent) != 1 {
		t.Fatalf("sent = %#v", sent)
	}
	if notices := store.texts(); len(notices) != 2 {
		t.Fatalf("notices = %#v", notices)
	}
}

// 查不到被评价的那条回复时照样记录，只是不带原文。
func TestQQFeedbackUnknownMessageStillRecorded(t *testing.T) {
	runtime, _, _, store := newQQFeedbackTestRuntime(BotConfig{}, `{"reply":""}`)
	notice := qqFeedbackTestNotice("LIKE", 1)
	notice.TargetID = "missing"
	if err := runtime.handleNotice(context.Background(), notice); err != nil {
		t.Fatal(err)
	}
	if notices := store.texts(); len(notices) != 1 || notices[0] != "[通知] 对方给回复点了赞" {
		t.Fatalf("notices = %#v", notices)
	}
}

// 私聊准入不放行的人：反馈照记，但不问模型、不回。
func TestQQFeedbackNotAdmittedIsRecordedWithoutReply(t *testing.T) {
	cfg := BotConfig{OwnerID: "OWNER", PrivateAdmission: PrivateAdmission{Mode: PrivateAdmissionOwnerOnly}}
	runtime, channel, provider, store := newQQFeedbackTestRuntime(cfg, `{"reply":"不该出现"}`)
	if err := runtime.handleNotice(context.Background(), qqFeedbackTestNotice("DISLIKE", 1)); err != nil {
		t.Fatal(err)
	}
	if notices := store.texts(); len(notices) != 1 {
		t.Fatalf("notices = %#v", notices)
	}
	if sent := channel.sentSnapshot(); len(sent) != 0 || provider.calls != 0 {
		t.Fatalf("sent = %#v, llm calls = %d", sent, provider.calls)
	}
}
