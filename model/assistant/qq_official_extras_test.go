package assistant

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
)

func TestParseQQExtras(t *testing.T) {
	if got := parseMessageStyle("大字，居中 随便"); got != (QQMarkdownStyle{FontSize: "large", Centered: true}) {
		t.Fatalf("style = %#v", got)
	}
	if got := parseMessageStyle(""); !got.isZero() {
		t.Fatalf("空值应当是默认版式：%#v", got)
	}
	if got := parseSuggestions("展开讲讲 | 那 B 呢|\n再来一个|第四句"); !reflect.DeepEqual(got, []string{"展开讲讲", "那 B 呢", "再来一个"}) {
		t.Fatalf("suggestions = %#v", got)
	}
	card := parseCard("Diana | 一个 QQ 机器人 | https://example.test/diana | https://example.test/cover.png")
	if card == nil || card.Title != "Diana" || card.URL != "https://example.test/diana" || card.PicURL != "https://example.test/cover.png" {
		t.Fatalf("card = %#v", card)
	}
	for _, bad := range []string{"只有标题", "标题|描述|不是网址", "|描述|https://example.test"} {
		if card := parseCard(bad); card != nil {
			t.Fatalf("%q 不该生成卡片：%#v", bad, card)
		}
	}
}

// 单聊里版式、推荐回复、赞踩栏都拼进同一条 Markdown，卡片跟在后面单独一条。
func TestQQOfficialSendsC2CExtras(t *testing.T) {
	api := newQQFakeAPI(t)
	if err := api.channel().Send(context.Background(), OutgoingMessage{
		UserID: "U", Text: "看这里", PassiveReplyMessageID: "in1",
		MarkdownStyle: QQMarkdownStyle{FontSize: "small", Centered: true},
		Suggestions:   []string{"展开讲讲", "再来一个"},
		QQFeedback:    true,
		Card:          &QQCard{Title: "Diana", Description: "介绍", URL: "https://example.test"},
	}); err != nil {
		t.Fatal(err)
	}
	if len(api.messages) != 2 {
		t.Fatalf("messages = %#v", api.messages)
	}
	text, card := api.messages[0], api.messages[1]
	markdown := text["markdown"].(map[string]any)
	style := markdown["style"].(map[string]any)
	if text["msg_type"] != float64(2) || style["main_font_size"] != "small" || style["layout"] != qqMarkdownLayoutCentered {
		t.Fatalf("text = %#v", text)
	}
	prompt, _ := json.Marshal(text["prompt_keyboard"])
	if string(prompt) != `{"keyboard":{"content":{"rows":[{"buttons":[{"action":{"type":2},"render_data":{"label":"展开讲讲","style":2}},{"action":{"type":2},"render_data":{"label":"再来一个","style":2}}]}]}}}` {
		t.Fatalf("prompt_keyboard = %s", prompt)
	}
	if text["action_button"].(map[string]any)["feedback"] != true {
		t.Fatalf("action_button = %#v", text["action_button"])
	}
	content := card["card"].(map[string]any)
	if card["msg_type"] != float64(qqMsgTypeCard) || content["type"] != qqCardTypeTuwen || card["msg_id"] != "in1" {
		t.Fatalf("card = %#v", card)
	}
}

// 群里只认版式，推荐回复和赞踩栏不往群里发。
func TestQQOfficialGroupSkipsC2COnlyExtras(t *testing.T) {
	api := newQQFakeAPI(t)
	if err := api.channel().Send(context.Background(), OutgoingMessage{
		GroupID: "G", Text: "看这里", PassiveReplyMessageID: "in1",
		MarkdownStyle: QQMarkdownStyle{Centered: true},
		Suggestions:   []string{"展开讲讲"},
		QQFeedback:    true,
	}); err != nil {
		t.Fatal(err)
	}
	msg := api.messages[0]
	if _, has := msg["prompt_keyboard"]; has {
		t.Fatalf("群消息不该带推荐回复：%#v", msg)
	}
	if _, has := msg["action_button"]; has {
		t.Fatalf("群消息不该带赞踩栏：%#v", msg)
	}
	if msg["markdown"].(map[string]any)["style"] == nil {
		t.Fatalf("群消息应当带版式：%#v", msg)
	}
}

// 文档外的字段被拒时去掉重发，正文照样到。
func TestQQOfficialRetriesWithoutExtrasWhenRejected(t *testing.T) {
	api := newQQFakeAPI(t)
	api.rejectExtras = true
	if err := api.channel().Send(context.Background(), OutgoingMessage{
		UserID: "U", Text: "看这里", PassiveReplyMessageID: "in1",
		Suggestions: []string{"展开讲讲"}, QQFeedback: true,
	}); err != nil {
		t.Fatal(err)
	}
	if len(api.messagePosts) != 2 || len(api.messages) != 1 || qqFakeHasExtras(api.messages[0]) {
		t.Fatalf("posts = %#v", api.messagePosts)
	}
	if api.messages[0]["msg_type"] != float64(2) {
		t.Fatalf("重发仍应是 Markdown：%#v", api.messages[0])
	}
}

// 单聊没有被动回复可用时按互动召回发，不支持召回就退回普通主动消息。
func TestQQOfficialProactiveC2CUsesWakeup(t *testing.T) {
	api := newQQFakeAPI(t)
	if err := api.channel().Send(context.Background(), OutgoingMessage{UserID: "U", Text: "提醒你喝水"}); err != nil {
		t.Fatal(err)
	}
	if api.messages[0]["is_wakeup"] != true {
		t.Fatalf("message = %#v", api.messages[0])
	}
	api = newQQFakeAPI(t)
	api.rejectWakeup = true
	if err := api.channel().Send(context.Background(), OutgoingMessage{UserID: "U", Text: "提醒你喝水"}); err != nil {
		t.Fatal(err)
	}
	if len(api.messagePosts) != 2 || len(api.messages) != 1 {
		t.Fatalf("posts = %#v", api.messagePosts)
	}
	if _, has := api.messages[0]["is_wakeup"]; has {
		t.Fatalf("退回的主动消息不该再带 is_wakeup：%#v", api.messages[0])
	}
	api = newQQFakeAPI(t)
	if err := api.channel().Send(context.Background(), OutgoingMessage{UserID: "U", Text: "好", PassiveReplyMessageID: "in1"}); err != nil {
		t.Fatal(err)
	}
	if _, has := api.messages[0]["is_wakeup"]; has {
		t.Fatalf("被动回复不该带 is_wakeup：%#v", api.messages[0])
	}
}

// 「正在输入」每条入站消息只发一次，群聊不发。
func TestQQOfficialInputNotifyOncePerMessage(t *testing.T) {
	api := newQQFakeAPI(t)
	channel := api.channel()
	msg := OutgoingMessage{UserID: "U", PassiveReplyMessageID: "in1"}
	for range 3 {
		if err := channel.SendChatAction(context.Background(), msg, "typing"); err != nil {
			t.Fatal(err)
		}
	}
	if err := channel.SendChatAction(context.Background(), OutgoingMessage{GroupID: "G", PassiveReplyMessageID: "in2"}, "typing"); err != nil {
		t.Fatal(err)
	}
	if len(api.messages) != 1 {
		t.Fatalf("messages = %#v", api.messages)
	}
	notify := api.messages[0]
	if notify["msg_type"] != float64(qqMsgTypeInputNotify) || notify["msg_id"] != "in1" || notify["input_notify"].(map[string]any)["input_second"] != float64(qqInputNotifySeconds) {
		t.Fatalf("notify = %#v", notify)
	}
}

// 「仅他:」只给这轮发起人点，和用途名可以叠着写。
func TestFinalizeButtonsOnlyAsker(t *testing.T) {
	_, holder := withFinalizeButtons(context.Background())
	holder.set("仅他:主要:确认 | 取消", "U1")
	rows, _ := holder.take()
	if len(rows) != 1 || !reflect.DeepEqual(rows[0][0].OnlyUserIDs, []string{"U1"}) || rows[0][0].Style == nil || *rows[0][0].Style != qqButtonStyleBlueFill || rows[0][1].OnlyUserIDs != nil {
		t.Fatalf("rows = %#v", rows)
	}
	keyboard := qqOfficialKeyboard(rows, "")
	button := keyboard["content"].(map[string]any)["rows"].([]any)[0].(map[string]any)["buttons"].([]any)[0].(map[string]any)
	permission := button["action"].(map[string]any)["permission"].(map[string]any)
	if permission["type"] != qqButtonPermissionUser || !reflect.DeepEqual(permission["specify_user_ids"], []string{"U1"}) || button["render_data"].(map[string]any)["label"] != "确认" {
		t.Fatalf("button = %#v", button)
	}
}

// 赞踩反馈只记日志：不应答、也不当成对方说的话交给模型。
func TestQQOfficialFeedbackInteractionIsNotAMessage(t *testing.T) {
	api := newQQFakeAPI(t)
	channel := api.channel()
	called := false
	channel.handler = func(context.Context, MessageEvent) error {
		called = true
		return nil
	}
	data, _ := json.Marshal(map[string]any{
		"id": "interaction-1", "chat_type": qqInteractionChatC2C, "user_openid": "U",
		"data": map[string]any{"type": qqInteractionTypeFeedback, "resolved": map[string]any{"button_data": "reply", "feedback_opt": "LIKE", "checked": 1, "message_id": "m1"}},
	})
	channel.handleInteraction(context.Background(), qqGatewayPayload{ID: qqInteractionEventPrefix + "interaction-1", Data: data})
	if called || len(api.paths) != 0 {
		t.Fatalf("called = %v, paths = %v", called, api.paths)
	}
}
