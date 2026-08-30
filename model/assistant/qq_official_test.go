// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"encoding/json"
	"testing"
)

func TestQQOfficialEventFromDispatchGroupMessage(t *testing.T) {
	data := json.RawMessage(`{
	  "id":"msg-1","content":"帮我查下天气","group_openid":"grp-1",
	  "timestamp":"2023-11-14T22:13:20+00:00",
	  "author":{"id":"author-1","member_openid":"member-1"}
	}`)
	event, ok := qqOfficialEventFromDispatch("GROUP_AT_MESSAGE_CREATE", data, "bot-1")
	if !ok {
		t.Fatal("group message was not mapped")
	}
	if event.Kind != EventKindGroup || event.GroupID != "grp-1" {
		t.Fatalf("kind = %q group = %q, want group/grp-1", event.Kind, event.GroupID)
	}
	// 群里用 member_openid，它才是这个群内稳定的成员标识。
	if event.UserID != "member-1" {
		t.Fatalf("user = %q, want member-1", event.UserID)
	}
	// 开放平台只推送 @ 了机器人的群消息，收到即意味着被点名。
	if !event.ToMe {
		t.Fatal("group messages from the gateway are always addressed to the bot")
	}
	if event.Time != 1700000000 {
		t.Fatalf("time = %d, want 1700000000 parsed from RFC3339", event.Time)
	}
}

func TestQQOfficialEventFromDispatchPrivateMessage(t *testing.T) {
	data := json.RawMessage(`{"id":"msg-2","content":"在吗","author":{"id":"a","user_openid":"user-1"}}`)
	event, ok := qqOfficialEventFromDispatch("C2C_MESSAGE_CREATE", data, "bot-1")
	if !ok {
		t.Fatal("private message was not mapped")
	}
	if event.Kind != EventKindPrivate {
		t.Fatalf("kind = %q, want private", event.Kind)
	}
	if event.UserID != "user-1" {
		t.Fatalf("user = %q, want user-1", event.UserID)
	}
}

func TestQQOfficialEventFromDispatchGuildMessage(t *testing.T) {
	data := json.RawMessage(`{"id":"msg-3","content":"hi","channel_id":"ch-1","guild_id":"g-1",
	  "author":{"id":"author-1"},"member":{"nick":"阿猫"}}`)
	event, ok := qqOfficialEventFromDispatch("AT_MESSAGE_CREATE", data, "bot-1")
	if !ok {
		t.Fatal("guild message was not mapped")
	}
	if event.GroupID != "ch-1" {
		t.Fatalf("group = %q, want the channel id", event.GroupID)
	}
	if event.SenderName != "阿猫" {
		t.Fatalf("sender = %q, want the guild nickname", event.SenderName)
	}
}

func TestQQOfficialEventFromDispatchKeepsQuotedMessage(t *testing.T) {
	data := json.RawMessage(`{"id":"msg-4","content":"这个呢","group_openid":"grp-1",
	  "author":{"member_openid":"member-1"},"message_reference":{"message_id":"msg-1"}}`)
	event, ok := qqOfficialEventFromDispatch("GROUP_AT_MESSAGE_CREATE", data, "bot-1")
	if !ok {
		t.Fatal("message was not mapped")
	}
	if event.Quoted == nil || event.Quoted.MessageID != "msg-1" {
		t.Fatal("the quoted message reference was dropped")
	}
	if len(event.Segments) == 0 || event.Segments[0].Type != "reply" {
		t.Fatal("the reply segment should lead the segment list, matching OneBot")
	}
}

// 未订阅或不认识的事件类型不该被硬塞成一条对话。
func TestQQOfficialEventFromDispatchIgnoresUnknownTypes(t *testing.T) {
	data := json.RawMessage(`{"id":"x","content":"y","author":{"id":"a"}}`)
	if _, ok := qqOfficialEventFromDispatch("GUILD_CREATE", data, "bot-1"); ok {
		t.Fatal("an unrelated dispatch was mapped to a chat event")
	}
}

// 拿不到发送者就没法做权限和记忆归属，这条消息只能丢弃。
func TestQQOfficialEventFromDispatchRequiresSender(t *testing.T) {
	data := json.RawMessage(`{"id":"x","content":"y","group_openid":"grp-1","author":{}}`)
	if _, ok := qqOfficialEventFromDispatch("GROUP_AT_MESSAGE_CREATE", data, "bot-1"); ok {
		t.Fatal("a message without any sender identifier was accepted")
	}
}

func TestQQGatewayPayloadMarshalOmitsEmptyFields(t *testing.T) {
	encoded, err := json.Marshal(qqGatewayPayload{Op: qqOpHeartbeat})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	// 心跳没有 seq 时必须发 null 而不是 0——网关按 d 判断续传位置。
	if string(encoded) != `{"op":1}` {
		t.Fatalf("heartbeat payload = %s, want {\"op\":1}", encoded)
	}
}
