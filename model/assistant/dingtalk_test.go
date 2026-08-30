// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import "testing"

func TestDingTalkEventFromCallbackGroupMessage(t *testing.T) {
	payload := []byte(`{
	  "msgId":"msg-1","msgtype":"text","createAt":1700000000000,
	  "conversationType":"2","conversationId":"cid-1","conversationTitle":"项目群",
	  "senderId":"sender-1","senderNick":"张三","senderStaffId":"staff-1","isAdmin":true,
	  "sessionWebhook":"https://oapi.dingtalk.com/robot/sendBySession?session=abc",
	  "text":{"content":" 帮我查下天气 "}
	}`)
	event, webhook, ok := dingTalkEventFromCallback(payload, "self")
	if !ok {
		t.Fatal("group message was not mapped")
	}
	if event.Kind != EventKindGroup || event.GroupID != "cid-1" {
		t.Fatalf("kind = %q group = %q, want group/cid-1", event.Kind, event.GroupID)
	}
	// staffId 是企业内唯一的员工号，优先于只在单次会话里有意义的 senderId。
	if event.UserID != "staff-1" {
		t.Fatalf("user = %q, want staff-1", event.UserID)
	}
	if event.RawMessage != "帮我查下天气" {
		t.Fatalf("text = %q, surrounding whitespace was not trimmed", event.RawMessage)
	}
	if event.SenderRole != "admin" {
		t.Fatalf("role = %q, want admin", event.SenderRole)
	}
	if event.Time != 1700000000 {
		t.Fatalf("time = %d, want 1700000000 (milliseconds should fold to seconds)", event.Time)
	}
	if webhook == "" {
		t.Fatal("sessionWebhook was dropped; passive replies would fall back to the quota-consuming API")
	}
}

func TestDingTalkEventFromCallbackPrivateMessage(t *testing.T) {
	payload := []byte(`{"msgId":"msg-2","msgtype":"text","conversationType":"1",
	  "senderId":"sender-2","senderNick":"李四","text":{"content":"在吗"}}`)
	event, _, ok := dingTalkEventFromCallback(payload, "self")
	if !ok {
		t.Fatal("private message was not mapped")
	}
	if event.Kind != EventKindPrivate {
		t.Fatalf("kind = %q, want private", event.Kind)
	}
	// 外部联系人没有 staffId，这时必须回落到 senderId，否则消息会被整条丢掉。
	if event.UserID != "sender-2" {
		t.Fatalf("user = %q, want sender-2 fallback", event.UserID)
	}
}

func TestDingTalkEventFromCallbackSkipsNonText(t *testing.T) {
	payload := []byte(`{"msgId":"msg-3","msgtype":"picture","conversationType":"1","senderId":"s"}`)
	if _, _, ok := dingTalkEventFromCallback(payload, "self"); ok {
		t.Fatal("picture message was unexpectedly mapped to a chat event")
	}
}

func TestDingTalkSessionWebhookExpires(t *testing.T) {
	channel := NewDingTalkChannel(DingTalkConfig{ClientID: "id", ClientSecret: "secret"})
	event := MessageEvent{GroupID: "cid-1"}
	channel.rememberSessionWebhook(event, "https://example.invalid/session")

	if got := channel.lookupSessionWebhook("cid-1", ""); got == "" {
		t.Fatal("a freshly stored session webhook was not found")
	}
	// 过期的地址必须当作不存在，否则发送会一直打到一个已失效的 URL 上。
	channel.webhookMu.Lock()
	entry := channel.sessionWebhooks["g:cid-1"]
	entry.ExpiresAt = entry.ExpiresAt.Add(-2 * dingTalkSessionWebhookTTL)
	channel.sessionWebhooks["g:cid-1"] = entry
	channel.webhookMu.Unlock()

	if got := channel.lookupSessionWebhook("cid-1", ""); got != "" {
		t.Fatalf("expired session webhook was returned: %q", got)
	}
}

func TestDingTalkSessionKeySeparatesGroupsFromUsers(t *testing.T) {
	if dingTalkSessionKey("cid-1", "user-1") != "g:cid-1" {
		t.Fatal("group conversations should be keyed by conversation id")
	}
	if dingTalkSessionKey("", "user-1") != "u:user-1" {
		t.Fatal("private conversations should be keyed by user id")
	}
	if dingTalkSessionKey("", "") != "" {
		t.Fatal("an empty conversation should not produce a key")
	}
}
