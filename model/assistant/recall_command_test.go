// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

const recallTestBot = "10000"

func recallCommandRuntime(t *testing.T, roles map[string]string) (*Runtime, *roleTestChannel, *captureAppLogs) {
	t.Helper()
	channel := newRoleTestChannel(roles)
	runtime := NewRuntime(BotConfig{OwnerID: "20001", BotAccount: recallTestBot, Platform: PlatformOneBotV11}, channel, NewDefaultPluginManager(), nil, nil, nil, nil)
	logs := &captureAppLogs{}
	runtime.SetAppLogWriter(logs)
	return runtime, channel, logs
}

// recallCommandEvent 造一条「引用 quoted + @机器人 + 正文」的群消息。
func recallCommandEvent(sender, body string, quoted *QuotedMessage, mention bool) MessageEvent {
	segments := []MessageSegment{}
	if quoted != nil {
		segments = append(segments, MessageSegment{Type: "reply", Data: map[string]string{"id": quoted.MessageID}})
	}
	if mention {
		segments = append(segments, MessageSegment{Type: "at", Data: map[string]string{"qq": recallTestBot}})
	}
	segments = append(segments, MessageSegment{Type: "text", Data: map[string]string{"text": " " + body}})
	return MessageEvent{
		Kind:      EventKindGroup,
		GroupID:   "30001",
		UserID:    sender,
		SelfID:    recallTestBot,
		MessageID: "cmd-1",
		Platform:  PlatformOneBotV11,
		Segments:  segments,
		Quoted:    quoted,
	}
}

func recallDeletedIDs(channel *roleTestChannel) []string {
	var ids []string
	for _, call := range recordedCallsByAction(channel.callsSnapshot(), "delete_msg") {
		ids = append(ids, fmt.Sprint(call.params["message_id"]))
	}
	return ids
}

func rememberGroupMessage(runtime *Runtime, userID, messageID, text string, outbound bool) {
	runtime.remember(MessageEvent{Kind: EventKindGroup, GroupID: "30001", SelfID: recallTestBot, Platform: PlatformOneBotV11, UserID: userID, MessageID: messageID, RawMessage: text, Outbound: outbound})
}

func TestRecallCommandMatchesOnlyMentionPlusCommand(t *testing.T) {
	runtime, _, _ := recallCommandRuntime(t, nil)
	quoted := &QuotedMessage{MessageID: "m-1", UserID: "20002"}
	cases := []struct {
		name  string
		event MessageEvent
		want  bool
	}{
		{"mention and quote", recallCommandEvent("20002", "/recall", quoted, true), true},
		{"case insensitive", recallCommandEvent("20002", "/RECALL", quoted, true), true},
		{"no mention", recallCommandEvent("20002", "/recall", quoted, false), false},
		{"extra words", recallCommandEvent("20002", "/recall 一下", quoted, true), false},
		{"natural language", recallCommandEvent("20002", "帮我撤回这条消息", quoted, true), false},
	}
	otherMention := recallCommandEvent("20002", "/recall", quoted, true)
	otherMention.Segments = append(otherMention.Segments, MessageSegment{Type: "at", Data: map[string]string{"qq": "20003"}})
	cases = append(cases, struct {
		name  string
		event MessageEvent
		want  bool
	}{"mentions someone else too", otherMention, false})
	for _, tc := range cases {
		if got := runtime.isRecallCommand(tc.event, ""); got != tc.want {
			t.Fatalf("%s: isRecallCommand = %v, want %v", tc.name, got, tc.want)
		}
	}
	private := recallCommandEvent("20002", "/recall", quoted, true)
	private.Kind, private.GroupID = EventKindPrivate, ""
	if runtime.isRecallCommand(private, "") {
		t.Fatal("private message must not match /recall")
	}
}

// 截图里的场景：普通成员引用自己的消息 @机器人，机器人是管理员，直接撤，不回话。
func TestRecallCommandMemberRecallsOwnQuotedMessage(t *testing.T) {
	runtime, channel, _ := recallCommandRuntime(t, map[string]string{recallTestBot: "admin", "20002": "member"})
	rememberGroupMessage(runtime, "20002", "own-1", "能插四张卡吗", false)
	event := recallCommandEvent("20002", "/recall", &QuotedMessage{MessageID: "own-1", UserID: "20002"}, true)

	reply, handled := runtime.handleRecallCommand(context.Background(), event, "")
	if !handled || reply != "" {
		t.Fatalf("reply=%q handled=%v", reply, handled)
	}
	if ids := recallDeletedIDs(channel); len(ids) != 1 || ids[0] != "own-1" {
		t.Fatalf("delete_msg ids = %v", ids)
	}
}

func TestRecallCommandMemberCannotRecallOthers(t *testing.T) {
	runtime, channel, _ := recallCommandRuntime(t, map[string]string{recallTestBot: "admin", "20002": "member", "20003": "member"})
	rememberGroupMessage(runtime, "20003", "other-1", "别人的话", false)
	event := recallCommandEvent("20002", "/recall", &QuotedMessage{MessageID: "other-1", UserID: "20003"}, true)

	reply, handled := runtime.handleRecallCommand(context.Background(), event, "")
	if !handled || !strings.HasPrefix(reply, "撤回失败：") || !strings.Contains(reply, "群管理员") {
		t.Fatalf("reply=%q handled=%v", reply, handled)
	}
	if ids := recallDeletedIDs(channel); len(ids) != 0 {
		t.Fatalf("member deleted someone else's message: %v", ids)
	}
}

func TestRecallCommandGroupAdminRecallsMemberButNotOtherAdmin(t *testing.T) {
	runtime, channel, _ := recallCommandRuntime(t, map[string]string{recallTestBot: "admin", "20004": "admin", "20003": "member", "20005": "admin"})
	rememberGroupMessage(runtime, "20003", "member-1", "刷屏", false)
	rememberGroupMessage(runtime, "20005", "admin-1", "另一个管理员", false)

	reply, _ := runtime.handleRecallCommand(context.Background(), recallCommandEvent("20004", "/recall", &QuotedMessage{MessageID: "member-1", UserID: "20003"}, true), "")
	if reply != "" {
		t.Fatalf("admin recall member reply=%q", reply)
	}
	reply, _ = runtime.handleRecallCommand(context.Background(), recallCommandEvent("20004", "/recall", &QuotedMessage{MessageID: "admin-1", UserID: "20005"}, true), "")
	if !strings.Contains(reply, "不能对群主或其他管理员") {
		t.Fatalf("admin recall admin reply=%q", reply)
	}
	if ids := recallDeletedIDs(channel); len(ids) != 1 || ids[0] != "member-1" {
		t.Fatalf("delete_msg ids = %v", ids)
	}
}

// 机器人不是管理员时，撤别人的消息要明确说出来，不能悄悄失败。
func TestRecallCommandReportsBotNotAdmin(t *testing.T) {
	runtime, channel, _ := recallCommandRuntime(t, map[string]string{recallTestBot: "member", "20003": "member"})
	rememberGroupMessage(runtime, "20003", "member-1", "刷屏", false)
	event := recallCommandEvent("20001", "/recall", &QuotedMessage{MessageID: "member-1", UserID: "20003"}, true)

	reply, handled := runtime.handleRecallCommand(context.Background(), event, "")
	if !handled || !strings.Contains(reply, "不是这个群的管理员") {
		t.Fatalf("reply=%q handled=%v", reply, handled)
	}
	if ids := recallDeletedIDs(channel); len(ids) != 0 {
		t.Fatalf("delete_msg ids = %v", ids)
	}
}

// 机器人自己的消息谁都能让它撤，不需要它是管理员。
func TestRecallCommandRecallsBotOwnMessageWithoutAdmin(t *testing.T) {
	runtime, channel, logs := recallCommandRuntime(t, map[string]string{recallTestBot: "member", "20002": "member"})
	rememberGroupMessage(runtime, recallTestBot, "bot-1", "说错的话", true)
	event := recallCommandEvent("20002", "/recall", &QuotedMessage{MessageID: "bot-1", UserID: recallTestBot}, true)

	reply, handled := runtime.handleRecallCommand(context.Background(), event, "")
	if !handled || reply != "" {
		t.Fatalf("reply=%q handled=%v", reply, handled)
	}
	if ids := recallDeletedIDs(channel); len(ids) != 1 || ids[0] != "bot-1" {
		t.Fatalf("delete_msg ids = %v", ids)
	}
	entries := logs.entriesSnapshot()
	if len(entries) == 0 || entries[len(entries)-1].Metadata["operation"] != platformOpRecall {
		t.Fatalf("recall not audited: %#v", entries)
	}
}

func TestRecallCommandWithoutQuoteExplainsUsage(t *testing.T) {
	runtime, channel, _ := recallCommandRuntime(t, nil)
	reply, handled := runtime.handleRecallCommand(context.Background(), recallCommandEvent("20002", "/recall", nil, true), "")
	if !handled || !strings.Contains(reply, "回复要撤回的那条消息") {
		t.Fatalf("reply=%q handled=%v", reply, handled)
	}
	if len(channel.callsSnapshot()) != 0 {
		t.Fatalf("unexpected api calls: %#v", channel.callsSnapshot())
	}
}

// 指令走 replyTo 时在加载上下文、调模型之前结束；成功不发任何消息，也不并进连发。
func TestRecallCommandRunsBeforeModelInReplyTo(t *testing.T) {
	runtime, channel, _ := recallCommandRuntime(t, map[string]string{recallTestBot: "admin", "20002": "member"})
	rememberGroupMessage(runtime, "20002", "own-1", "发错了", false)
	event := recallCommandEvent("20002", "/recall", &QuotedMessage{MessageID: "own-1", UserID: "20002"}, true)

	if runtime.burstChatMessage(event, "") {
		t.Fatal("/recall must not be merged into a sender burst")
	}
	reply, err := runtime.replyTo(context.Background(), event, "")
	if err != nil || reply != "" {
		t.Fatalf("replyTo reply=%q err=%v", reply, err)
	}
	if ids := recallDeletedIDs(channel); len(ids) != 1 || ids[0] != "own-1" {
		t.Fatalf("delete_msg ids = %v", ids)
	}
	if sent := channel.sentSnapshot(); len(sent) != 0 {
		t.Fatalf("success should stay silent, sent %#v", sent)
	}
}
