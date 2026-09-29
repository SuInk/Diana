// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"testing"
	"time"
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

// 开通「接收所有群消息」能力后，@ 机器人与普通群消息统一走 GROUP_MESSAGE_CREATE；
// 是否被点名认 mentions 里的 is_you 标记，content 里残留的 mention 标记要剥掉。
func TestQQOfficialEventFromDispatchFullGroupMessageMention(t *testing.T) {
	data := json.RawMessage(`{
	  "id":"msg-5","content":"<@330F50FCA0ABA93B781CA31A9F9389F6> 在干嘛",
	  "group_openid":"grp-1","group_id":"grp-1",
	  "timestamp":"2023-11-14T22:13:20+00:00",
	  "author":{"member_openid":"member-1"},
	  "mentions":[{"id":"330F50FCA0ABA93B781CA31A9F9389F6","is_you":true}]
	}`)
	event, ok := qqOfficialEventFromDispatch("GROUP_MESSAGE_CREATE", data, "bot-1")
	if !ok {
		t.Fatal("full-mode group message was not mapped")
	}
	if event.Kind != EventKindGroup || event.GroupID != "grp-1" {
		t.Fatalf("kind = %q group = %q, want group/grp-1", event.Kind, event.GroupID)
	}
	if event.UserID != "member-1" {
		t.Fatalf("user = %q, want member-1", event.UserID)
	}
	if !event.ToMe {
		t.Fatal("a message with an is_you mention must be addressed to the bot")
	}
	if event.RawMessage != "在干嘛" {
		t.Fatalf("text = %q, want the mention token stripped", event.RawMessage)
	}
}

// 全量模式下普通群消息不是被点名，ToMe 必须为 false，交给群触发词与接话策略。
func TestQQOfficialEventFromDispatchFullGroupMessageWithoutMention(t *testing.T) {
	data := json.RawMessage(`{
	  "id":"msg-6","content":"今天天气不错","group_openid":"grp-1",
	  "author":{"member_openid":"member-1"},
	  "mentions":[{"id":"someone-else"}]
	}`)
	event, ok := qqOfficialEventFromDispatch("GROUP_MESSAGE_CREATE", data, "bot-1")
	if !ok {
		t.Fatal("full-mode group message was not mapped")
	}
	if event.ToMe {
		t.Fatal("an ordinary group message is not addressed to the bot")
	}
}

// 机器人自己的发言也在全量推送里回推，必须归到 SelfID 让运行时按自发消息处理。
func TestQQOfficialEventFromDispatchFullGroupMessageSelfEcho(t *testing.T) {
	data := json.RawMessage(`{
	  "id":"msg-7","content":"刷视频呢","group_openid":"grp-1",
	  "author":{"id":"bot-1","member_openid":"bot-member-openid","bot":true}
	}`)
	event, ok := qqOfficialEventFromDispatch("GROUP_MESSAGE_CREATE", data, "bot-1")
	if !ok {
		t.Fatal("bot self echo was not mapped")
	}
	if event.UserID != "bot-1" {
		t.Fatalf("user = %q, want the bot id so the runtime can treat it as self", event.UserID)
	}
	if event.ToMe {
		t.Fatal("the bot does not mention itself")
	}
}

// 回推的 author.id 对不上 selfID 时，按发送记录认出自己的发言。
func TestQQOfficialDispatchRecognizesSelfEchoBySentLedger(t *testing.T) {
	cases := []struct {
		name string
		data string
	}{
		{"same message id", `{"id":"sent-1","content":"别的正文","group_openid":"grp-1","author":{"member_openid":"bot-member","bot":true}}`},
		{"same text", `{"id":"echo-9","content":"刷视频呢","group_openid":"grp-1","author":{"member_openid":"bot-member","bot":true}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := &QQOfficialChannel{}
			c.setStatus(true, "bot-1", "")
			c.sent.record("grp-1", "sent-1", "刷视频呢", time.Now())
			var got []MessageEvent
			c.handler = func(_ context.Context, event MessageEvent) error {
				got = append(got, event)
				return nil
			}
			c.handleDispatch(context.Background(), qqGatewayPayload{T: "GROUP_MESSAGE_CREATE", Data: json.RawMessage(tc.data)})
			if len(got) != 1 || got[0].UserID != "bot-1" {
				t.Fatalf("events = %+v, want one self event with user bot-1", got)
			}
		})
	}
}

// 别的机器人、或者真人复读同样的正文，都不能被当成自己。
func TestQQOfficialDispatchKeepsOtherSendersDespiteSentLedger(t *testing.T) {
	cases := []struct {
		name string
		data string
	}{
		{"human repeats text", `{"id":"m-1","content":"刷视频呢","group_openid":"grp-1","author":{"member_openid":"member-1"}}`},
		{"other group", `{"id":"m-2","content":"刷视频呢","group_openid":"grp-2","author":{"member_openid":"bot-member","bot":true}}`},
		{"other bot text", `{"id":"m-3","content":"别的话","group_openid":"grp-1","author":{"member_openid":"other-bot","bot":true}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := &QQOfficialChannel{}
			c.setStatus(true, "bot-1", "")
			c.sent.record("grp-1", "sent-1", "刷视频呢", time.Now())
			var got []MessageEvent
			c.handler = func(_ context.Context, event MessageEvent) error {
				got = append(got, event)
				return nil
			}
			c.handleDispatch(context.Background(), qqGatewayPayload{T: "GROUP_MESSAGE_CREATE", Data: json.RawMessage(tc.data)})
			if len(got) != 1 || got[0].UserID == "bot-1" {
				t.Fatalf("events = %+v, want the sender kept as-is", got)
			}
		})
	}
}

func TestQQSentLedgerExpiresAndConsumes(t *testing.T) {
	var l qqSentLedger
	now := time.Now()
	l.record("grp-1", "sent-1", "你好", now)
	if !l.matches("grp-1", "", "你好", now.Add(time.Second)) {
		t.Fatal("fresh entry should match by text")
	}
	if l.matches("grp-1", "", "你好", now.Add(2*time.Second)) {
		t.Fatal("a matched entry must not be claimed twice")
	}
	l.record("grp-1", "sent-2", "再见", now)
	if l.matches("grp-1", "sent-2", "", now.Add(qqSentLedgerTTL+time.Second)) {
		t.Fatal("expired entry must not match")
	}
}

// 没带 is_you、只能靠 id 比对认出的 @，标记也要剥掉。
func TestQQOfficialFullGroupMessageStripsMentionMatchedByOpenID(t *testing.T) {
	data := json.RawMessage(`{
	  "id":"msg-8","content":"<@bot-1> 在吗","group_openid":"grp-1",
	  "author":{"member_openid":"member-1"},
	  "mentions":[{"openid":"bot-1"}]
	}`)
	event, ok := qqOfficialEventFromDispatch("GROUP_MESSAGE_CREATE", data, "bot-1")
	if !ok || !event.ToMe {
		t.Fatalf("event = %+v, want a mention addressed to the bot", event)
	}
	if event.RawMessage != "在吗" {
		t.Fatalf("text = %q, want the mention token stripped", event.RawMessage)
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

// 名字在 ext 的 base64 JSON 里，翻成 [表情:[吃芒果]]；取不到名字时退回 [表情]。
func TestQQOfficialFaceTextDecodesNames(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    string
	}{
		{
			name:    "named face",
			content: `别这样<faceType=4,faceId="",ext="eyJ0ZXh0IjoiW+WQg+iKkuaenF0ifQ==">`,
			want:    "别这样[表情:[吃芒果]]",
		},
		{
			name:    "face without a name",
			content: `<faceType=6,faceId="0",ext="eyJ0ZXh0IjoiIn0=">`,
			want:    "[表情]",
		},
		{
			name:    "face without ext",
			content: `<faceType=1,faceId="76">`,
			want:    "[表情]",
		},
		{
			name:    "plain text untouched",
			content: "今天天气不错",
			want:    "今天天气不错",
		},
	}
	for _, testCase := range cases {
		if got := qqOfficialFaceText(testCase.content); got != testCase.want {
			t.Fatalf("%s: face text = %q, want %q", testCase.name, got, testCase.want)
		}
	}
}

// 自家表情是空名标签配一张附件图：正文只剩 [表情] 占位，图必须单独拆出来，
// 否则这条入站就等于纯文字。
func TestQQOfficialSelfMadeStickerKeepsImageBesidePlaceholder(t *testing.T) {
	data := json.RawMessage(`{
	  "id":"msg-11","content":"<faceType=6,faceId=\"0\",ext=\"eyJ0ZXh0IjoiIn0=\">",
	  "group_openid":"grp-1","author":{"member_openid":"member-1"},
	  "attachments":[{"content_type":"image/jpeg","filename":"a.jpg","url":"https://multimedia.nt.qq.com.cn/download?fileid=x"}]
	}`)
	event, ok := qqOfficialEventFromDispatch("GROUP_MESSAGE_CREATE", data, "bot-1")
	if !ok {
		t.Fatal("sticker message was not mapped")
	}
	if event.RawMessage != "[表情]" {
		t.Fatalf("text = %q, want the placeholder", event.RawMessage)
	}
	images := 0
	for _, segment := range event.Segments {
		if segment.Type == "image" {
			images++
		}
	}
	if images != 1 {
		t.Fatalf("segments = %+v, want the sticker image kept", event.Segments)
	}
}

// 图片全在 attachments 里、content 只剩文字；不拆成图片段，群友发的图就整个丢了。
func TestQQOfficialEventFromDispatchAppendsImageAttachments(t *testing.T) {
	data := json.RawMessage(`{
	  "id":"msg-9","content":"看这张","group_openid":"grp-1",
	  "author":{"member_openid":"member-1"},
	  "attachments":[
	    {"content_type":"image/jpeg","filename":"a.jpg","url":"https://grouppro.grouppro.qq.com/a.jpg"},
	    {"content_type":"video/mp4","filename":"b.mp4","url":"https://grouppro.grouppro.qq.com/b.mp4"},
	    {"content_type":"image/png","filename":"c.png","url":"//grouppro.grouppro.qq.com/c.png"}
	  ]
	}`)
	event, ok := qqOfficialEventFromDispatch("GROUP_AT_MESSAGE_CREATE", data, "bot-1")
	if !ok {
		t.Fatal("group message was not mapped")
	}
	if event.RawMessage != "看这张" {
		t.Fatalf("text = %q, want the plain content", event.RawMessage)
	}
	var images []MessageSegment
	for _, segment := range event.Segments {
		if segment.Type == "image" {
			images = append(images, segment)
		}
	}
	if len(images) != 2 {
		t.Fatalf("image segments = %+v, want the two image attachments", event.Segments)
	}
	if images[0].Data["url"] != "https://grouppro.grouppro.qq.com/a.jpg" {
		t.Fatalf("first image url = %q", images[0].Data["url"])
	}
	// 官方 CDN 偶尔不给协议头，得补上，否则下载器不认。
	if images[1].Data["url"] != "https://grouppro.grouppro.qq.com/c.png" {
		t.Fatalf("second image url = %q, want the https prefix restored", images[1].Data["url"])
	}
	// 正文段在前、图片段紧随其后，和 markdown 拆图同序。
	if event.Segments[len(event.Segments)-1].Type != "image" {
		t.Fatalf("segments = %+v, want images after the text segment", event.Segments)
	}
}

// 只有图片没有文字的消息也要进得来，否则图发出去就石沉大海。
func TestQQOfficialEventFromDispatchImageOnlyMessage(t *testing.T) {
	data := json.RawMessage(`{
	  "id":"msg-10","content":"","group_openid":"grp-1",
	  "author":{"member_openid":"member-1"},
	  "attachments":[{"content_type":"image/png","filename":"a.png","url":"https://grouppro.grouppro.qq.com/a.png"}]
	}`)
	event, ok := qqOfficialEventFromDispatch("GROUP_AT_MESSAGE_CREATE", data, "bot-1")
	if !ok {
		t.Fatal("image-only message was not mapped")
	}
	found := false
	for _, segment := range event.Segments {
		if segment.Type == "image" && segment.Data["url"] == "https://grouppro.grouppro.qq.com/a.png" {
			found = true
		}
	}
	if !found {
		t.Fatalf("segments = %+v, want the image segment kept", event.Segments)
	}
}

// 同群其他机器人的发言（作者是 bot 但不是自己）只当上下文，不能触发回复。
func TestQQOfficialEventFromDispatchOtherBotIsNotAddressedToMe(t *testing.T) {
	for _, eventType := range []string{"GROUP_AT_MESSAGE_CREATE", "GROUP_MESSAGE_CREATE"} {
		data := json.RawMessage(`{
		  "id":"msg-8","content":"欢迎你进群","group_openid":"grp-1",
		  "author":{"id":"other-bot","member_openid":"other-openid","bot":true},
		  "mentions":[{"id":"bot-1","is_you":true}]
		}`)
		event, ok := qqOfficialEventFromDispatch(eventType, data, "bot-1")
		if !ok {
			t.Fatalf("%s: other bot message was not mapped", eventType)
		}
		if event.ToMe {
			t.Fatalf("%s: a message from another bot must not be addressed to this bot", eventType)
		}
	}
}

func TestQQOfficialNextPassiveSeqIncrementsPerMessage(t *testing.T) {
	c := NewQQOfficialChannel(QQOfficialConfig{})
	if a, b, other := c.nextPassiveSeq("m1"), c.nextPassiveSeq("m1"), c.nextPassiveSeq("m2"); a != 1 || b != 2 || other != 1 {
		t.Fatalf("seq = %d,%d,%d, want 1,2,1", a, b, other)
	}
}

func TestRouteOutgoingToEventQQOfficialCarriesPassiveMessageID(t *testing.T) {
	msg := routeOutgoingToEvent(MessageEvent{Platform: PlatformQQOfficial, Kind: EventKindGroup, GroupID: "g", MessageID: "ROBOT1.0_x"}, OutgoingMessage{Text: "hi"})
	if msg.PassiveReplyMessageID != "ROBOT1.0_x" || msg.ReplyMessageID != "" {
		t.Fatalf("passive=%q reply=%q, want passive id only", msg.PassiveReplyMessageID, msg.ReplyMessageID)
	}
	other := routeOutgoingToEvent(MessageEvent{Platform: "onebot", Kind: EventKindGroup, GroupID: "g", MessageID: "1"}, OutgoingMessage{Text: "hi"})
	if other.PassiveReplyMessageID != "" {
		t.Fatalf("passive id leaked to another platform: %q", other.PassiveReplyMessageID)
	}
}
