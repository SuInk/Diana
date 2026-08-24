// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"reflect"
	"strings"
	"testing"
	"unicode/utf16"
)

func TestDianaMentionsToCQ(t *testing.T) {
	got := dianaMentionsToCQ("[diana-at:10002] 你看下这个，还有 [diana-at:10008]")
	want := "[CQ:at,qq=10002] 你看下这个，还有 [CQ:at,qq=10008]"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	// 没有标记的正文原样返回，不做多余分配。
	if plain := dianaMentionsToCQ("普通一句话"); plain != "普通一句话" {
		t.Fatalf("plain text was rewritten: %q", plain)
	}
}

// OneBot 侧走完整条出站链路：标记必须变成真正的 at 段。
func TestOneBotSegmentsRenderMentionMarker(t *testing.T) {
	segments := TextToOneBotSegments("[diana-at:10002] 看下")
	if len(segments) == 0 || segments[0].Type != "at" || segments[0].Data["qq"] != "10002" {
		t.Fatalf("segments = %#v, want a leading at segment", segments)
	}
	if PlainText(segments) == "" {
		t.Fatalf("正文被吃掉了：%#v", segments)
	}
}

func TestRenderDianaMentionsUsesDisplayNamesAndFallsBackToID(t *testing.T) {
	text, spans := renderDianaMentions("[diana-at:10002] 和 [diana-at:10008] 都看下",
		map[string]string{"10002": "Alice"})
	if text != "@Alice 和 @10008 都看下" {
		t.Fatalf("text = %q", text)
	}
	want := []dianaMentionSpan{
		{UserID: "10002", Display: "@Alice", Offset: 0, Length: 6},
		{UserID: "10008", Display: "@10008", Offset: 9, Length: 6},
	}
	if !reflect.DeepEqual(spans, want) {
		t.Fatalf("spans = %#v, want %#v", spans, want)
	}
	// 偏移量必须真的指向渲染后的那段文字，否则 Telegram 会把提及贴错位置。
	units := utf16Units(text)
	for _, span := range spans {
		if got := string(utf16Decode(units[span.Offset : span.Offset+span.Length])); got != span.Display {
			t.Fatalf("offset %d..%d = %q, want %q", span.Offset, span.Offset+span.Length, got, span.Display)
		}
	}
}

// Telegram 的偏移量按 UTF-16 码元算：一个 emoji 占两个，按 rune 数会整体错位。
func TestRenderDianaMentionsCountsUTF16Units(t *testing.T) {
	text, spans := renderDianaMentions("🎉🎉 [diana-at:10002]", map[string]string{"10002": "Alice"})
	if len(spans) != 1 {
		t.Fatalf("spans = %#v", spans)
	}
	if spans[0].Offset != 5 {
		t.Fatalf("offset = %d, want 5（两个 emoji 四个码元加一个空格）", spans[0].Offset)
	}
	units := utf16Units(text)
	if got := string(utf16Decode(units[spans[0].Offset : spans[0].Offset+spans[0].Length])); got != "@Alice" {
		t.Fatalf("offset points at %q", got)
	}
}

func TestTelegramMentionEntities(t *testing.T) {
	entities := telegramMentionEntities([]dianaMentionSpan{
		{UserID: "10002", Display: "@Alice", Offset: 3, Length: 6},
		// 非数字 id 进不了 Telegram 的 user.id，跳过这一条而不是让整条消息发不出去。
		{UserID: "im_user_unrestored", Display: "@Bob", Offset: 20, Length: 4},
	})
	if len(entities) != 1 {
		t.Fatalf("entities = %#v, want exactly the numeric one", entities)
	}
	entity := entities[0]
	if entity["type"] != "text_mention" || entity["offset"] != 3 || entity["length"] != 6 {
		t.Fatalf("entity = %#v", entity)
	}
	user, ok := entity["user"].(map[string]any)
	if !ok || user["id"] != int64(10002) {
		t.Fatalf("entity user = %#v, want numeric id", entity["user"])
	}
	if telegramMentionEntities(nil) != nil {
		t.Fatal("没有提及时不该传 entities 参数")
	}
}

func TestMentionedIDsInText(t *testing.T) {
	got := mentionedIDsInText("[diana-at:10002] 和 [diana-at:10008]，再叫一次 [diana-at:10002]")
	if !reflect.DeepEqual(got, []string{"10002", "10008"}) {
		t.Fatalf("got %#v", got)
	}
	if ids := mentionedIDsInText("没有提及"); ids != nil {
		t.Fatalf("got %#v, want nil", ids)
	}
}

// 提示词和工具返回值都不许再教平台方言：Telegram 群里那只会把字面量发出去。
func TestPromptsAndToolsUseNeutralMentionMarker(t *testing.T) {
	runtime := NewRuntime(BotConfig{BotAccount: "42"}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	event := MessageEvent{
		Kind: EventKindGroup, SelfID: "42", GroupID: "123456", UserID: "10001",
		MessageID: "m1", ToMe: true,
		Segments: []MessageSegment{{Type: "text", Data: map[string]string{"text": "在吗"}}},
	}
	for _, mode := range []ReplyDecorationMode{ReplyDecorationOn, ReplyDecorationAuto, ReplyDecorationOff} {
		cfg := BotConfig{BotAccount: "42", MentionUserMode: mode, ReplyReferenceMode: mode}.WithDefaults()
		prompt := runtime.replyMentionPrompt(cfg, event, nil)
		if strings.Contains(prompt, "[CQ:at") {
			t.Fatalf("mode %s 的提及规则里还有 CQ 方言：%s", mode, prompt)
		}
		if !strings.Contains(prompt, "diana-at") {
			t.Fatalf("mode %s 的提及规则没给出标记写法：%s", mode, prompt)
		}
	}
	if got := mentionMarkerFor("10002"); got != "[diana-at:10002]" {
		t.Fatalf("mentionMarkerFor = %q", got)
	}
}

// utf16Units / utf16Decode 只给测试用：断言偏移量真的落在那段文字上，
// 而不是把 renderDianaMentions 的算法在测试里抄一遍。
func utf16Units(text string) []uint16 {
	return utf16.Encode([]rune(text))
}

func utf16Decode(units []uint16) []rune {
	return utf16.Decode(units)
}

// 昵称由运行时在出站前按 id 查好，Telegram 侧才有可显示的文字。
func TestRuntimeResolvesMentionDisplayNames(t *testing.T) {
	runtime := NewRuntime(BotConfig{BotAccount: "42"}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	event := MessageEvent{
		Kind: EventKindGroup, SelfID: "42", GroupID: "123456", UserID: "10001",
		SenderName: "Carol", MessageID: "m2",
	}
	runtime.remember(MessageEvent{
		Kind: EventKindGroup, GroupID: "123456", UserID: "10002",
		SenderName: "Alice", MessageID: "m1",
	})

	msg := runtime.resolveOutgoingMentionNames(event, OutgoingMessage{
		GroupID: "123456",
		Text:    "[diana-at:10002] 和 [diana-at:10001] 都看下，还有 [diana-at:19999]",
	})
	if msg.MentionNames["10002"] != "Alice" {
		t.Fatalf("历史里的昵称没查到：%#v", msg.MentionNames)
	}
	if msg.MentionNames["10001"] != "Carol" {
		t.Fatalf("当前发言者的昵称没查到：%#v", msg.MentionNames)
	}
	// 查不到的 id 不留空条目，渲染时自然退回 @<id>。
	if _, has := msg.MentionNames["19999"]; has {
		t.Fatalf("查不到的 id 不该占位：%#v", msg.MentionNames)
	}

	// 正文里没有标记时不做任何事，省掉一次历史快照。
	plain := runtime.resolveOutgoingMentionNames(event, OutgoingMessage{GroupID: "123456", Text: "普通一句话"})
	if plain.MentionNames != nil {
		t.Fatalf("没有提及却查了昵称：%#v", plain.MentionNames)
	}
}

// Markdown 降级不能把 Diana 自己的标记吃掉。[diana-at:10002] 后面正好跟一个
// 半角括号时，链接规则会把它当成 [文字](目标) 拆掉方括号，标记就废了——出站
// 认不出来，字面量直接发进群。
func TestMarkdownDowngradeKeepsDianaMarkers(t *testing.T) {
	cases := []string{
		"[diana-at:10002] 看下 **这个**",
		"- [diana-at:10002] 项目",
		"[diana-at:10002](顺便说一句)",
		"[diana-reply:12345](顺便说一句)",
	}
	for _, input := range cases {
		got := normalizeReply(input, 3500, true)
		marker := "[diana-at:10002]"
		if strings.HasPrefix(input, "[diana-reply") {
			marker = "[diana-reply:12345]"
		}
		if !strings.Contains(got, marker) {
			t.Fatalf("normalizeReply(%q) 吃掉了标记：%q", input, got)
		}
	}
	// 真正的链接照常降级，不能因为这条豁免把链接处理也一起关掉。
	if got := normalizeReply("看这里 [文档](https://example.com/a)", 3500, true); !strings.Contains(got, "https://example.com/a") || strings.Contains(got, "](") {
		t.Fatalf("普通链接没有正常降级：%q", got)
	}
}
