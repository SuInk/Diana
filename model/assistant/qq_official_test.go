// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"strings"
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

// 群聊真实形态的引用事件：message_type=103，被引用正文在 msg_elements 里，索引键
// 在 message_scene.ext 的 ref_msg_idx，不出现 message_reference。
// 被引用的发送者只能由 handleDispatch 拿索引反查回填，这里单独验映射层不丢正文。
func TestQQOfficialEventFromDispatchQuotedMessageFromMsgElements(t *testing.T) {
	data := json.RawMessage(`{
	  "id":"msg-q1","content":"这条信息引用了上一条消息","group_openid":"grp-1",
	  "message_type":103,
	  "message_scene":{"ext":[
	    "ref_msg_idx=REFIDX_quoted",
	    "msg_idx=REFIDX_current",
	    "auth_token=<redacted>"
	  ],"source":"default"},
	  "msg_elements":[{
	    "content":"这是一条即将被引用的信息",
	    "message_type":103,
	    "msg_idx":"REFIDX_quoted"
	  }],
	  "parallel_message":{"msg_nodes":[{"content":"这是一条即将被引用的信息","message_type":0}]},
	  "author":{"member_openid":"member-1"}
	}`)
	event, ok := qqOfficialEventFromDispatch("GROUP_MESSAGE_CREATE", data, "bot-1")
	if !ok {
		t.Fatal("quoted group message was not mapped")
	}
	if event.Quoted == nil {
		t.Fatal("the quoted message was dropped")
	}
	if event.Quoted.MessageID != "REFIDX_quoted" {
		t.Fatalf("quoted id = %q, want the ref_msg_idx key", event.Quoted.MessageID)
	}
	if event.Quoted.RawMessage != "这是一条即将被引用的信息" {
		t.Fatalf("quoted text = %q, want the payload from msg_elements", event.Quoted.RawMessage)
	}
	// 本轮输入在 content 里，不带被引用文本。
	if event.RawMessage != "这条信息引用了上一条消息" {
		t.Fatalf("raw message = %q", event.RawMessage)
	}
	if len(event.Segments) == 0 || event.Segments[0].Type != "text" {
		t.Fatalf("segments = %+v, want the current text to lead", event.Segments)
	}
}

// 引用携带媒体：元素附件与顶层 attachments 完全同构，拆成对应消息段进入识图链路。
func TestQQOfficialEventFromDispatchQuotedMessageKeepsMedia(t *testing.T) {
	data := json.RawMessage(`{
	  "id":"msg-q2","content":"引用图片","group_openid":"grp-1",
	  "message_type":103,
	  "message_scene":{"ext":["msg_idx=REFIDX_current","ref_msg_idx=REFIDX_quoted"]},
	  "msg_elements":[{
	    "content":"",
	    "message_type":103,
	    "msg_idx":"REFIDX_quoted",
	    "attachments":[{
	      "content":"","content_type":"image/jpeg","filename":"a.jpg",
	      "height":1500,"width":1887,"size":352256,
	      "url":"https://multimedia.nt.qq.com.cn/download?fileid=a"
	    }]
	  }],
	  "author":{"member_openid":"member-1"}
	}`)
	event, ok := qqOfficialEventFromDispatch("GROUP_MESSAGE_CREATE", data, "bot-1")
	if !ok {
		t.Fatal("quoted media message was not mapped")
	}
	if event.Quoted == nil {
		t.Fatal("the quoted message was dropped")
	}
	found := false
	for _, segment := range event.Quoted.Segments {
		if segment.Type == "image" && segment.Data["url"] == "https://multimedia.nt.qq.com.cn/download?fileid=a" {
			found = true
		}
	}
	if !found {
		t.Fatalf("quoted segments = %+v, want the image attachment kept", event.Quoted.Segments)
	}
}

// 聊天记录引用：ref_msg_idx 是 TMP_ 前缀（另一套键空间），元素不带 msg_idx；
// 正文仍在元素里，引用关系照常交付，只是查不到发送者。
func TestQQOfficialEventFromDispatchQuotedChatRecord(t *testing.T) {
	data := json.RawMessage(`{
	  "id":"msg-q3","content":"引用聊天记录","group_openid":"grp-1",
	  "message_type":103,
	  "message_scene":{"ext":["msg_idx=REFIDX_current","ref_msg_idx=TMP_f01d7008-df53-42f0-840e-7d9a6ffc60b4"]},
	  "msg_elements":[{
	    "content":"=== 消息 1 ===\n[消息内容] 合并信息测试\n[发送者] 张三"
	  }],
	  "author":{"member_openid":"member-1"}
	}`)
	event, ok := qqOfficialEventFromDispatch("GROUP_MESSAGE_CREATE", data, "bot-1")
	if !ok {
		t.Fatal("chat-record quote was not mapped")
	}
	if event.Quoted == nil {
		t.Fatal("the quoted chat record was dropped")
	}
	if event.Quoted.MessageID != "TMP_f01d7008-df53-42f0-840e-7d9a6ffc60b4" {
		t.Fatalf("quoted id = %q, want the TMP key as-is", event.Quoted.MessageID)
	}
	if !strings.Contains(event.Quoted.RawMessage, "合并信息测试") {
		t.Fatalf("quoted text = %q, want the nested payload kept", event.Quoted.RawMessage)
	}
}

// 入站登记与引用反查：handleDispatch 把入站 msg_idx、出站 ref_idx 都登记进索引，
// 收到引用时用 ref_msg_idx 反查发送者；引用机器人的自发消息时回填 ToMe。
func TestQQOfficialDispatchEnrichesQuoteSenderFromRefIndex(t *testing.T) {
	c := &QQOfficialChannel{}
	c.setStatus(true, "bot-1", "")
	var got []MessageEvent
	c.handler = func(_ context.Context, event MessageEvent) error {
		got = append(got, event)
		return nil
	}
	// 先收一条普通消息登记它的 msg_idx。
	c.handleDispatch(context.Background(), qqGatewayPayload{T: "GROUP_MESSAGE_CREATE", Data: json.RawMessage(`{
	  "id":"m-1","content":"这是一条即将被引用的信息","group_openid":"grp-1",
	  "message_type":0,
	  "message_scene":{"ext":["msg_idx=REFIDX_member-msg"]},
	  "author":{"member_openid":"member-1"}
	}`)})
	// 出站发送返回的 ref_idx 指向自己。
	c.refs.recordOutbound("REFIDX_bot-msg", "m-bot", "bot-1", time.Now())
	// 再收一条引用 member-1 那条消息的引用事件。
	c.handleDispatch(context.Background(), qqGatewayPayload{T: "GROUP_MESSAGE_CREATE", Data: json.RawMessage(`{
	  "id":"m-2","content":"引用了上一条","group_openid":"grp-1",
	  "message_type":103,
	  "message_scene":{"ext":["msg_idx=REFIDX_m-2","ref_msg_idx=REFIDX_member-msg"]},
	  "msg_elements":[{"content":"这是一条即将被引用的信息","message_type":103,"msg_idx":"REFIDX_member-msg"}],
	  "author":{"member_openid":"member-1"}
	}`)})
	if len(got) != 2 {
		t.Fatalf("events = %d, want two dispatched events", len(got))
	}
	quoted := got[1].Quoted
	if quoted == nil || quoted.UserID != "member-1" {
		t.Fatalf("quoted = %+v, want the sender resolved from the ref index", quoted)
	}
	if got[1].ToMe {
		t.Fatal("quoting another member's message is not addressed to the bot")
	}
	// 引用机器人自己的发言：反查出自己并置 ToMe。
	c.handleDispatch(context.Background(), qqGatewayPayload{T: "GROUP_MESSAGE_CREATE", Data: json.RawMessage(`{
	  "id":"m-3","content":"引用了机器人的信息","group_openid":"grp-1",
	  "message_type":103,
	  "message_scene":{"ext":["msg_idx=REFIDX_m-3","ref_msg_idx=REFIDX_bot-msg"]},
	  "msg_elements":[{"content":"[probe] 收到","message_type":103,"msg_idx":"REFIDX_bot-msg"}],
	  "author":{"member_openid":"member-1"}
	}`)})
	if len(got) != 3 {
		t.Fatalf("events = %d, want three dispatched events", len(got))
	}
	if quoted := got[2].Quoted; quoted == nil || quoted.UserID != "bot-1" {
		t.Fatalf("quoted = %+v, want the bot itself resolved", quoted)
	}
	if !got[2].ToMe {
		t.Fatal("quoting the bot's own message must count as addressed to the bot")
	}
}

// 索引过期后反查不到发送者：引用关系仍交付，只是 UserID 为空，交给上层继续按
// 历史回查（enrichReplyReference 会用 MessageID 查本地历史）。
func TestQQRefLedgerExpiresAndPrunes(t *testing.T) {
	var l qqRefLedger
	now := time.Now()
	l.recordInbound("REFIDX_a", "m-a", "member-1", false, now)
	if sender, _, ok := l.lookup("REFIDX_a", now.Add(time.Second)); !ok || sender != "member-1" {
		t.Fatalf("lookup = %q,%v, want member-1", sender, ok)
	}
	l.recordInbound("REFIDX_a", "m-a", "member-2", false, now.Add(qqRefLedgerTTL+time.Second))
	if sender, _, ok := l.lookup("REFIDX_a", now.Add(qqRefLedgerTTL+2*time.Second)); !ok || sender != "member-2" {
		t.Fatalf("lookup = %q,%v, want the refreshed sender", sender, ok)
	}
	if _, _, ok := l.lookup("REFIDX_missing", now); ok {
		t.Fatal("an unknown key must not resolve")
	}
	// 平台 id → REFIDX_ 反查：入站与出站两侧都登记，同一张表双向可查。
	l.recordInbound("REFIDX_in", "m-in", "member-1", false, now)
	l.recordOutbound("REFIDX_out", "m-out", "bot-1", now)
	if key := l.refIdxFor("m-in", now.Add(time.Second)); key != "REFIDX_in" {
		t.Fatalf("refIdxFor(inbound) = %q, want REFIDX_in", key)
	}
	if key := l.refIdxFor("m-out", now.Add(time.Second)); key != "REFIDX_out" {
		t.Fatalf("refIdxFor(outbound) = %q, want REFIDX_out", key)
	}
	if key := l.refIdxFor("m-unknown", now.Add(time.Second)); key != "" {
		t.Fatalf("refIdxFor(unknown) = %q, want empty", key)
	}
	if key := l.refIdxFor("m-in", now.Add(qqRefLedgerTTL+time.Second)); key != "" {
		t.Fatalf("refIdxFor(expired) = %q, want empty", key)
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

// 图片全在 attachments 里、content 只剩文字；不拆成对应消息段，这些媒体不会进入
// 后续链路。视频、语音、文件附件同样走这条路（各自的真实事件体见后面几个用例）。
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
	videos := 0
	for _, segment := range event.Segments {
		switch segment.Type {
		case "image":
			images = append(images, segment)
		case "video":
			videos++
		}
	}
	if len(images) != 2 {
		t.Fatalf("image segments = %+v, want the two image attachments", event.Segments)
	}
	if videos != 1 {
		t.Fatalf("video segments = %d, want the video attachment kept", videos)
	}
	if images[0].Data["url"] != "https://grouppro.grouppro.qq.com/a.jpg" {
		t.Fatalf("first image url = %q", images[0].Data["url"])
	}
	// 官方 CDN 偶尔不给协议头，得补上，否则下载器不认。
	if images[1].Data["url"] != "https://grouppro.grouppro.qq.com/c.png" {
		t.Fatalf("second image url = %q, want the https prefix restored", images[1].Data["url"])
	}
	// 正文段在前、媒体段紧随其后，和 markdown 拆图同序。
	if event.Segments[len(event.Segments)-1].Type != "image" {
		t.Fatalf("segments = %+v, want media after the text segment", event.Segments)
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

// 视频事件体（私聊）：content 为空，视频信息全在 attachments 里，content_type 是
// 标准 video/mp4。不拆成视频段时，这条消息在运行时就是空消息。
func TestQQOfficialEventFromDispatchKeepsVideoAttachment(t *testing.T) {
	data := json.RawMessage(`{
	  "id": "msg-12",
	  "author": {"id": "user-1", "user_openid": "user-1"},
	  "content": "",
	  "timestamp": "2023-11-14T22:13:20+00:00",
	  "message_type": 0,
	  "attachments": [{
	    "content_type": "video/mp4",
	    "filename": "5b324291cc7c9436701d639fc317e6f3.mp4",
	    "size": 1149350,
	    "url": "https://multimedia.nt.qq.com.cn/download?appid=1413&format=origin"
	  }]
	}`)
	event, ok := qqOfficialEventFromDispatch("C2C_MESSAGE_CREATE", data, "bot-1")
	if !ok {
		t.Fatal("video message was not mapped")
	}
	found := false
	for _, segment := range event.Segments {
		if segment.Type == "video" {
			found = true
			if segment.Data["url"] != "https://multimedia.nt.qq.com.cn/download?appid=1413&format=origin" {
				t.Fatalf("video url = %q", segment.Data["url"])
			}
			if segment.Data["file"] != "5b324291cc7c9436701d639fc317e6f3.mp4" {
				t.Fatalf("video file = %q, want the attachment filename", segment.Data["file"])
			}
		}
	}
	if !found {
		t.Fatalf("segments = %+v, want the video attachment kept", event.Segments)
	}
}

// 语音附件的 content_type 是裸值 voice（不是 MIME），且平台已带 asr_refer_text
// 转写；拆成 record 段并直接采用平台转写，不再跑本地语音识别。
func TestQQOfficialEventFromDispatchKeepsVoiceAttachmentWithTranscript(t *testing.T) {
	data := json.RawMessage(`{
	  "id": "msg-13",
	  "author": {"id": "member-1", "member_openid": "member-1"},
	  "content": "",
	  "group_openid": "grp-1",
	  "attachments": [{
	    "content_type": "voice",
	    "filename": "9a4c0b298d23a98cdff9cf54cb0bde2c.amr",
	    "size": 4912,
	    "asr_refer_text": "群聊测试语音。",
	    "url": "https://multimedia.nt.qq.com.cn/download?appid=1402&fileid=x"
	  }]
	}`)
	event, ok := qqOfficialEventFromDispatch("GROUP_MESSAGE_CREATE", data, "bot-1")
	if !ok {
		t.Fatal("voice message was not mapped")
	}
	found := false
	for _, segment := range event.Segments {
		if segment.Type == "record" {
			found = true
			if segment.Data[voiceSTTTranscriptKey] != "群聊测试语音。" {
				t.Fatalf("transcript = %q, want the platform ASR text", segment.Data[voiceSTTTranscriptKey])
			}
			if segment.Data["url"] == "" {
				t.Fatal("voice url was dropped")
			}
		}
	}
	if !found {
		t.Fatalf("segments = %+v, want the voice attachment kept", event.Segments)
	}
}

// 文件附件的 content_type 是裸值 file，md / zip / mp3 的取值相同，区分只能靠
// 文件名扩展名；拆成 file 段交给文件解析插件。
func TestQQOfficialEventFromDispatchKeepsFileAttachment(t *testing.T) {
	data := json.RawMessage(`{
	  "id": "msg-14",
	  "author": {"id": "user-1", "user_openid": "user-1"},
	  "content": "",
	  "attachments": [{
	    "content_type": "file",
	    "filename": "笔记.md",
	    "size": 17,
	    "url": "https://multimedia.nt.qq.com.cn/download?appid=1408&fileid=y"
	  }]
	}`)
	event, ok := qqOfficialEventFromDispatch("C2C_MESSAGE_CREATE", data, "bot-1")
	if !ok {
		t.Fatal("file message was not mapped")
	}
	found := false
	for _, segment := range event.Segments {
		if segment.Type == "file" {
			found = true
			if segment.Data["name"] != "笔记.md" {
				t.Fatalf("file name = %q", segment.Data["name"])
			}
			if segment.Data["size"] != "17" {
				t.Fatalf("file size = %q, want the attachment size", segment.Data["size"])
			}
			if segment.Data["url"] == "" {
				t.Fatal("file url was dropped")
			}
		}
	}
	if !found {
		t.Fatalf("segments = %+v, want the file attachment kept", event.Segments)
	}
}

// 合并聊天记录没有 attachments，媒体只以「[附件1] 类型:… URL:…」的文本行压在
// content 里；不拆出来这些媒体不会进入识图/抽帧链路。图片/动图拆成 image 段、
// 视频拆成 video 段，正文里的 URL 抹掉、留下可读字段；文件类附件没有 URL，
// 拆不出段就整行保留。
func TestQQOfficialEventFromDispatchMergedForwardMediaSegments(t *testing.T) {
	imageURL := "https://multimedia.nt.qq.com.cn/download?appid=1407&fileid=E0EADC9617A004546F5531A7A80AE50A&rkey=CAEStest1&spec=0"
	gifURL := "https://multimedia.nt.qq.com.cn/download?appid=1407&fileid=9B96163CD7A3D14F282544DB1C663769&rkey=CAEStest2&spec=0"
	secondImageURL := "https://multimedia.nt.qq.com.cn/download?appid=1407&fileid=3724E7E487F6B0748FCEE7526678987C&rkey=CAEStest3&spec=0"
	videoURL := "https://multimedia.nt.qq.com.cn/download?appid=1415&format=origin&orgfmt=t265&spec=0&rkey=CAEStest4"
	content := strings.Join([]string{
		"[张三的聊天记录]",
		"=== 消息 1 ===",
		"[消息内容] 合并信息测试",
		"[发送者] 张三",
		"",
		"=== 消息 2 ===",
		"[消息内容] [吃芒果]",
		"[发送者] 张三",
		"",
		"=== 消息 3 ===",
		"[消息内容] [表情]",
		"[发送者] 张三",
		"[附件1] 类型:图片 文件名:E0EADC9617A004546F5531A7A80AE50A.jpg 尺寸:720x720 大小:77.4KB URL:" + imageURL,
		"",
		"=== 消息 4 ===",
		"[消息内容] [表情]",
		"[发送者] 张三",
		"[附件1] 类型:动图 文件名:9B96163CD7A3D14F282544DB1C663769.gif 尺寸:300x327 大小:4.6MB URL:" + gifURL,
		"",
		"=== 消息 5 ===",
		"[消息内容] 以上分别为普通表情，自制静态表情，自制动态git表情",
		"[发送者] 张三",
		"",
		"=== 消息 6 ===",
		"[发送者] 张三",
		"[附件1] 类型:图片 文件名:3724E7E487F6B0748FCEE7526678987C.jpg 尺寸:1079x1079 大小:105.2KB URL:" + secondImageURL,
		"",
		"=== 消息 7 ===",
		"[发送者] 张三",
		"[附件1] 类型:视频 文件名:6ecb7f75c70942ab7e11dcecfa656939.mp4 尺寸:640x1138 大小:753.0KB URL:" + videoURL,
		"",
		"=== 消息 8 ===",
		"[消息内容] 以上为图片，视频",
		"[发送者] 张三",
		"",
		"=== 消息 9 ===",
		"[发送者] 张三",
		"[附件1] 类型:文件 文件名:测试.md 大小:17B",
		"",
		"=== 消息 10 ===",
		"[发送者] 张三",
		"[附件1] 类型:文件 文件名:测试.zip 大小:321B",
		"",
		"=== 消息 11 ===",
		"[发送者] 张三",
		"[附件1] 类型:文件 文件名:12.31.mp3 大小:4.4MB",
		"",
		"=== 消息 12 ===",
		"[消息内容] 以上为文件测试",
		"[发送者] 张三",
	}, "\n")
	payload, err := json.Marshal(map[string]any{
		"id":           "msg-15",
		"content":      content,
		"group_openid": "grp-1",
		"author":       map[string]any{"member_openid": "member-1"},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	event, ok := qqOfficialEventFromDispatch("GROUP_MESSAGE_CREATE", payload, "bot-1")
	if !ok {
		t.Fatal("merged forward message was not mapped")
	}
	var imageURLs []string
	var videoSegments []MessageSegment
	for _, segment := range event.Segments {
		switch segment.Type {
		case "image":
			imageURLs = append(imageURLs, segment.Data["url"])
		case "video":
			videoSegments = append(videoSegments, segment)
		}
	}
	if len(imageURLs) != 3 || imageURLs[0] != imageURL || imageURLs[1] != gifURL || imageURLs[2] != secondImageURL {
		t.Fatalf("image urls = %v, want the jpg/gif/jpg attachment urls in order", imageURLs)
	}
	if len(videoSegments) != 1 {
		t.Fatalf("video segments = %d, want the video attachment kept", len(videoSegments))
	}
	if videoSegments[0].Data["url"] != videoURL {
		t.Fatalf("video url = %q", videoSegments[0].Data["url"])
	}
	if videoSegments[0].Data["file"] != "6ecb7f75c70942ab7e11dcecfa656939.mp4" {
		t.Fatalf("video file = %q, want the attachment filename", videoSegments[0].Data["file"])
	}
	// 一次性下载地址不能留在正文里：接话评分按尾部 180 字截断，截到 URL 时整段
	// 都是参数串；识图链路已在消息段里得到地址。
	if strings.Contains(event.RawMessage, "rkey=") || strings.Contains(event.RawMessage, "multimedia.nt.qq.com.cn") {
		t.Fatalf("raw message still carries download urls: %q", event.RawMessage)
	}
	// 抹掉 URL 的附件行保留类型/文件名/尺寸/大小这些可读字段；文件行没有 URL，
	// 原样保留。
	for _, want := range []string{
		"[附件1] 类型:图片 文件名:E0EADC9617A004546F5531A7A80AE50A.jpg 尺寸:720x720 大小:77.4KB",
		"[附件1] 类型:视频 文件名:6ecb7f75c70942ab7e11dcecfa656939.mp4 尺寸:640x1138 大小:753.0KB",
		"[附件1] 类型:文件 文件名:测试.md 大小:17B",
		"[附件1] 类型:文件 文件名:12.31.mp3 大小:4.4MB",
	} {
		if !strings.Contains(event.RawMessage, want) {
			t.Fatalf("raw message lost the readable attachment line %q: %q", want, event.RawMessage)
		}
	}
	// 正文段即抹掉 URL 后的正文，结构行（标题、分隔、发送者）原样保留。
	var textSegment string
	for _, segment := range event.Segments {
		if segment.Type == "text" {
			textSegment = segment.Data["text"]
		}
	}
	if textSegment != event.RawMessage {
		t.Fatalf("text segment = %q, want the cleaned raw message", textSegment)
	}
	if !strings.Contains(event.RawMessage, "[张三的聊天记录]") || !strings.Contains(event.RawMessage, "=== 消息 12 ===") {
		t.Fatalf("raw message lost the merged forward structure: %q", event.RawMessage)
	}
}

// 首行 [某某的聊天记录] 与 === 消息 N === 分隔行同时出现才认定是合并转发：只带
// 分隔行的普通长文、只有标题没有条目的文本都不能被误拆。另一种真实形态的标题是
// [群聊的聊天记录]，[消息内容] 行可以整行缺失，且可能还是未解码的表情标签。
func TestQQOfficialMergedForwardMediaRequiresHeaderAndSeparator(t *testing.T) {
	cases := []struct {
		name string
		text string
	}{
		{
			name: "separator without header",
			text: "记录一下\n=== 消息 1 ===\n[消息内容] 普通文本\n[发送者] 某人\n[附件1] 类型:图片 文件名:a.jpg 尺寸:1x1 大小:1KB URL:https://multimedia.nt.qq.com.cn/download?fileid=a&rkey=x",
		},
		{
			name: "header without separator",
			text: "[张三的聊天记录]\n有人直接打字引用了这个标题，内容不是转发",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			text, segments := qqOfficialMergedForwardMedia(testCase.text)
			if text != testCase.text || len(segments) != 0 {
				t.Fatalf("text = %q segments = %v, want the plain text untouched", text, segments)
			}
		})
	}
}

// 另一形态：标题是 [群聊的聊天记录]，部分条目没有 [消息内容] 行、
// 有的是未解码的表情标签；解析发生在表情转换之后，媒体仍能拆出。
func TestQQOfficialMergedForwardMediaHandlesGroupChatRecord(t *testing.T) {
	content := strings.Join([]string{
		"[群聊的聊天记录]",
		"=== 消息 1 ===",
		"[发送者] 张三",
		"[附件1] 类型:图片 文件名:4E7D7DC8D07F61AFF8BF284D73EC204C.jpg 尺寸:1254x1254 大小:173.8KB URL:https://multimedia.nt.qq.com.cn/download?appid=1407&fileid=4E7D7DC8&rkey=CAEStest&spec=0",
		"",
		"=== 消息 2 ===",
		"[发送者] 张三",
		"[附件1] 类型:视频 文件名:1d1d2927a7135c4c40953164db86a34d.mp4 尺寸:640x1138 大小:1.1MB URL:https://multimedia.nt.qq.com.cn/download?appid=1415&format=origin&orgfmt=t265&spec=0&rkey=CAEStest",
		"",
		"=== 消息 3 ===",
		`[消息内容] <faceType=6,faceId="0",ext="eyJ0ZXh0IjoiIn0=">`,
		"[发送者] 张三",
		"[附件1] 类型:图片 文件名:E0EADC9617A004546F5531A7A80AE50A.jpg 尺寸:720x720 大小:77.4KB URL:https://multimedia.nt.qq.com.cn/download?appid=1407&fileid=E0EADC96&rkey=CAEStest&spec=0",
		"",
		"=== 消息 4 ===",
		"[消息内容] [有点饿了]",
		"[发送者] 张三",
		"",
		"=== 消息 5 ===",
		`[消息内容] <faceType=6,faceId="0",ext="eyJ0ZXh0IjoiIn0=">`,
		"[发送者] 张三",
		"[附件1] 类型:动图 文件名:9B96163CD7A3D14F282544DB1C663769.gif 尺寸:300x327 大小:4.6MB URL:https://multimedia.nt.qq.com.cn/download?appid=1407&fileid=9B96163C&rkey=CAEStest&spec=0",
	}, "\n")
	payload, err := json.Marshal(map[string]any{
		"id":           "msg-16",
		"content":      content,
		"group_openid": "grp-1",
		"author":       map[string]any{"member_openid": "member-1"},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	event, ok := qqOfficialEventFromDispatch("GROUP_MESSAGE_CREATE", payload, "bot-1")
	if !ok {
		t.Fatal("merged forward message was not mapped")
	}
	images, videos := 0, 0
	for _, segment := range event.Segments {
		switch segment.Type {
		case "image":
			images++
		case "video":
			videos++
		}
	}
	if images != 3 || videos != 1 {
		t.Fatalf("images = %d videos = %d, want 3 images and 1 video from the record", images, videos)
	}
	// 表情标签已翻成 [表情] 占位，一次性地址也已抹掉。
	if strings.Contains(event.RawMessage, "<faceType") {
		t.Fatalf("raw message kept the face tag: %q", event.RawMessage)
	}
	if strings.Contains(event.RawMessage, "multimedia.nt.qq.com.cn") {
		t.Fatalf("raw message kept download urls: %q", event.RawMessage)
	}
	if !strings.Contains(event.RawMessage, "[消息内容] [表情]") {
		t.Fatalf("raw message lost the face placeholder: %q", event.RawMessage)
	}
}

// 未观测到的附件类型（如链接）不拆段，整行连同 URL 原样留在正文里，
// 保留信息。
func TestQQOfficialMergedForwardMediaKeepsUnknownAttachmentKind(t *testing.T) {
	text := strings.Join([]string{
		"[群聊的聊天记录]",
		"=== 消息 1 ===",
		"[发送者] 张三",
		"[附件1] 类型:链接 文件名:分享.html 大小:1KB URL:https://example.com/share",
	}, "\n")
	cleaned, segments := qqOfficialMergedForwardMedia(text)
	if len(segments) != 0 {
		t.Fatalf("segments = %+v, want no segments for an unknown kind", segments)
	}
	if cleaned != text {
		t.Fatalf("cleaned = %q, want the line kept verbatim", cleaned)
	}
}
