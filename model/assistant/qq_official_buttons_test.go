package assistant

import (
	"context"
	"reflect"
	"testing"
)

func TestParseFinalizeButtons(t *testing.T) {
	got := parseFinalizeButtons("继续讲 | 换个话题\n官方文档=https://bot.q.qq.com/wiki/|来一局=开始游戏\n\n|  |")
	want := [][]OutgoingButton{
		{{Label: "继续讲", Data: "继续讲"}, {Label: "换个话题", Data: "换个话题"}},
		{{Label: "官方文档", Data: "https://bot.q.qq.com/wiki/", Link: true}, {Label: "来一局", Data: "开始游戏"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseFinalizeButtons = %#v", got)
	}
	if rows := parseFinalizeButtons("1|2|3|4|5|6\n2\n3\n4\n5\n6"); len(rows) != 5 || len(rows[0]) != 5 {
		t.Fatalf("超过 5 排、每排 5 个应当截断：%#v", rows)
	}
	if rows := parseFinalizeButtons("  "); rows != nil {
		t.Fatalf("空值不该产生按钮：%#v", rows)
	}
}

// 一排里两个以上指令按钮共用 group_id（同组互斥），链接按钮和单个指令按钮不带。
func TestQQOfficialKeyboardGroupsCommandRows(t *testing.T) {
	keyboard := qqOfficialKeyboard([][]OutgoingButton{
		{{Label: "A", Data: "A"}, {Label: "B", Data: "B"}, {Label: "文档", Data: "https://x.test", Link: true}},
		{{Label: "继续", Data: "继续"}},
	})
	rows := keyboard["content"].(map[string]any)["rows"].([]any)
	first := rows[0].(map[string]any)["buttons"].([]any)
	a, link := first[0].(map[string]any), first[2].(map[string]any)
	if a["group_id"] != "row1" || first[1].(map[string]any)["group_id"] != "row1" {
		t.Fatalf("同排指令按钮应当同组：%#v", first)
	}
	if _, has := link["group_id"]; has || link["action"].(map[string]any)["type"] != qqButtonActionLink {
		t.Fatalf("链接按钮 = %#v", link)
	}
	action := a["action"].(map[string]any)
	if action["type"] != qqButtonActionCommand || action["enter"] != true || action["data"] != "A" {
		t.Fatalf("指令按钮 = %#v", a)
	}
	single := rows[1].(map[string]any)["buttons"].([]any)[0].(map[string]any)
	if _, has := single["group_id"]; has {
		t.Fatalf("单个按钮不该分组：%#v", single)
	}
}

// 大白话带按钮也要换成 Markdown 消息，按钮才挂得上。
func TestQQOfficialSendsButtonsOnMarkdownMessage(t *testing.T) {
	api := newQQFakeAPI(t)
	if err := api.channel().Send(context.Background(), OutgoingMessage{
		GroupID: "G", Text: "想听哪个", ReplyMessageID: "in1",
		Buttons: [][]OutgoingButton{{{Label: "A", Data: "A"}, {Label: "B", Data: "B"}}},
	}); err != nil {
		t.Fatal(err)
	}
	msg := api.messages[0]
	markdown, _ := msg["markdown"].(map[string]any)
	if msg["msg_type"] != float64(2) || markdown["content"] != "想听哪个" || msg["keyboard"] == nil {
		t.Fatalf("message = %#v", msg)
	}
}

// 没开通自定义按钮被拒时去掉按钮重发，Markdown 照样保住。
func TestQQOfficialRetriesWithoutButtonsWhenKeyboardRejected(t *testing.T) {
	api := newQQFakeAPI(t)
	api.rejectKeyboard = true
	if err := api.channel().Send(context.Background(), OutgoingMessage{
		GroupID: "G", Text: "**选一个**", ReplyMessageID: "in1",
		Buttons: [][]OutgoingButton{{{Label: "A", Data: "A"}}},
	}); err != nil {
		t.Fatal(err)
	}
	if len(api.messagePosts) != 2 || len(api.messages) != 1 {
		t.Fatalf("posts = %#v", api.messagePosts)
	}
	msg := api.messages[0]
	if _, has := msg["keyboard"]; has || msg["msg_type"] != float64(2) || msg["msg_seq"] != float64(2) {
		t.Fatalf("retried message = %#v", msg)
	}
}

func TestOffersButtonsFinalizeFieldOnlyOnQQOfficialChats(t *testing.T) {
	cfg := BotConfig{Platform: PlatformQQOfficial}
	if !offersButtonsFinalizeField(cfg, MessageEvent{Platform: PlatformQQOfficial}) {
		t.Fatal("QQ 官方群聊/单聊应当提供按钮字段")
	}
	if offersButtonsFinalizeField(cfg, MessageEvent{Platform: PlatformQQOfficial, PlatformScope: "qq_guild"}) {
		t.Fatal("频道没开放原生 Markdown，不该提供按钮字段")
	}
	if offersButtonsFinalizeField(BotConfig{Platform: PlatformTelegram}, MessageEvent{Platform: PlatformTelegram}) {
		t.Fatal("其他平台不该提供按钮字段")
	}
	plain := true
	if offersButtonsFinalizeField(BotConfig{Platform: PlatformQQOfficial, MarkdownToPlain: &plain}, MessageEvent{Platform: PlatformQQOfficial}) {
		t.Fatal("配置成发纯文本时按钮挂不上，不该提供字段")
	}
}

// 回复拆成几条时按钮挂在最后一条文字上，只挂一次。
func TestDeliverChunksAttachesButtonsToLastText(t *testing.T) {
	channel := &recordingChannel{}
	cfg := BotConfig{Platform: PlatformQQOfficial, SendChunkIntervalMS: 1}
	runtime := NewRuntime(cfg, channel, NewPluginManager(), nil, nil, nil, nil)
	event := MessageEvent{Kind: EventKindPrivate, Platform: PlatformQQOfficial, UserID: "u1"}
	ctx, holder := withFinalizeButtons(context.Background())
	holder.set("继续|换一个")
	if _, err := runtime.deliverChunks(ctx, event, []string{"第一段", "第二段", ""}, cfg.WithDefaults(), outboundDecoration{}); err != nil {
		t.Fatal(err)
	}
	channel.mu.Lock()
	sent := append([]OutgoingMessage(nil), channel.sent...)
	channel.mu.Unlock()
	if len(sent) != 2 || len(sent[0].Buttons) != 0 || len(sent[1].Buttons) != 1 || len(sent[1].Buttons[0]) != 2 {
		t.Fatalf("sent = %#v", sent)
	}
	if rows := holder.take(); rows != nil {
		t.Fatalf("按钮应当已被取走：%#v", rows)
	}
}
