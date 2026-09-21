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

// 标记是给发送层看的中间形式，不能留在历史里：事件页要显示群里实际看到的样子，
// 模型下一轮读自己的发言也不该读到一个没渲染的标记。
func TestOutgoingHistoryRendersMentionMarker(t *testing.T) {
	runtime := NewRuntime(BotConfig{BotAccount: "42", Name: "Diana"}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	source := MessageEvent{
		Kind: EventKindGroup, SelfID: "42", GroupID: "123456", UserID: "10001", MessageID: "m1",
	}
	event := runtime.outgoingHistoryEvent(source, OutgoingMessage{
		GroupID: "123456",
		Text:    "[diana-at:10002] 看下这个",
	})
	if strings.Contains(event.RawMessage, dianaMentionMarkerPrefix) {
		t.Fatalf("历史里留下了未翻译的标记：%q", event.RawMessage)
	}
	if !strings.Contains(event.RawMessage, "10002") || !strings.Contains(event.RawMessage, "看下这个") {
		t.Fatalf("历史正文丢了内容：%q", event.RawMessage)
	}
	// 段落侧必须是真正的 at 段，历史检索和「谁被提到了」都依赖它。
	var hasAt bool
	for _, segment := range event.Segments {
		if segment.Type == "at" && segment.Data["qq"] == "10002" {
			hasAt = true
		}
	}
	if !hasAt {
		t.Fatalf("历史段落里没有 at 段：%#v", event.Segments)
	}

	// 没有标记的普通发言原样保留，不要绕道 PlainText 改写。
	plain := runtime.outgoingHistoryEvent(source, OutgoingMessage{GroupID: "123456", Text: "普通一句话"})
	if plain.RawMessage != "普通一句话" {
		t.Fatalf("普通发言被改写了：%q", plain.RawMessage)
	}
}

// 生产库里捞出来的真实样本：9/20 换上 mimo-x-flash-preview 之后，一天里写出六种
// 形态，dianaMentionMarkerPattern 一个都不认，全部当正文发进了群。id 还认得出来
// 的扶正成真提及，认不出来的丢掉。
func TestOutgoingNormalizesMentionVariantsFromProduction(t *testing.T) {
	runtime := NewRuntime(BotConfig{BotAccount: "42"}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	event := MessageEvent{Platform: PlatformOneBotV11, Kind: EventKindGroup, SelfID: "42", GroupID: "20005", UserID: "30007"}
	cases := []struct{ in, want string }{
		{"@diana-at-30007 撤啥呀，那两单早就撤过了", "[diana-at:30007]撤啥呀，那两单早就撤过了"},
		{"<diana-at:30003>半真半假。官方文档里", "[diana-at:30003]半真半假。官方文档里"},
		{"(diana-at:3135003586)没事没事，一整杯都糊脸上了", "[diana-at:3135003586]没事没事，一整杯都糊脸上了"},
		{"[ diana-at:1368248340]（揉了揉眼睛）三天就把 GPT 额度干完了", "[diana-at:1368248340]（揉了揉眼睛）三天就把 GPT 额度干完了"},
		{"[ diana-at:3135003586 ]嘿嘿，第三分收下", "[diana-at:3135003586]嘿嘿，第三分收下"},
		{"[diana-at:30007] 撤啥呀", "[diana-at:30007] 撤啥呀"},
		// id 认不出来的整个丢掉，不留半个标记，也不降级成纯文本。
		{"@diana-at 不难，这仓库还没 CONTRIBUTING.md", "不难，这仓库还没 CONTRIBUTING.md"},
		{"[diana-at:成员user_id] 撤啥呀", "撤啥呀"},
		{"[diana-at:im_user_abc] 撤啥呀", "撤啥呀"},
		{"[diana-at:diana-at-30007] 撤啥呀", "撤啥呀"},
	}
	for _, item := range cases {
		got := runtime.normalizeOutgoingMentions(event, OutgoingMessage{Text: item.in}).Text
		if got != item.want {
			t.Fatalf("输入 %q\n得到 %q\n期望 %q", item.in, got, item.want)
		}
		for _, segment := range TextToOneBotSegments(got) {
			if segment.Type == "at" && !numericChatID(segment.Data["qq"]) {
				t.Fatalf("输入 %q 仍然发出了 qq 非数字的 at 段：%#v", item.in, segment)
			}
			if segment.Type == "text" && strings.Contains(strings.ToLower(segment.Data["text"]), "diana-at") {
				t.Fatalf("输入 %q 的正文里还留着标记：%q", item.in, segment.Data["text"])
			}
		}
	}
}

// 讲实现时把标记写进反引号是在展示写法，不是要提及谁，不能动它。
func TestMentionNormalizationSkipsCodeSpans(t *testing.T) {
	runtime := NewRuntime(BotConfig{BotAccount: "42"}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	event := MessageEvent{Platform: PlatformOneBotV11, Kind: EventKindGroup, SelfID: "42", GroupID: "1", UserID: "10001"}
	for _, text := range []string{
		"提及写成 `[diana-at:<user_id>]`，发送前再翻译喵",
		"```\n[diana-at:10001]\n```",
	} {
		if got := runtime.normalizeOutgoingMentions(event, OutgoingMessage{Text: text}).Text; got != text {
			t.Fatalf("代码块被改写了：%q -> %q", text, got)
		}
	}
}

// 非数字 ID 的平台不能按 OneBot 的规矩卡：飞书的 open_id 本来就是 ou_ 开头。
func TestMentionIDAcceptableByPlatform(t *testing.T) {
	cases := []struct {
		platform string
		id       string
		want     bool
	}{
		{PlatformOneBotV11, "30007", true},
		{PlatformOneBotV11, "diana-at-30007", false},
		{PlatformTelegram, "10001", true},
		{PlatformTelegram, "user_id", false},
		{PlatformFeishu, "ou_9a8b7c", true},
		{PlatformFeishu, "im_user_9a8b", false},
	}
	for _, item := range cases {
		if got := mentionIDAcceptable(item.platform, item.id); got != item.want {
			t.Fatalf("mentionIDAcceptable(%q, %q) = %v", item.platform, item.id, got)
		}
	}
}

// 标记夹在句中时丢掉不能留下两个空格。
func TestDropUnusableMentionTidiesSpacing(t *testing.T) {
	got := dropUnusableDianaMentions("那就 [diana-at:成员user_id] 你来说 [diana-at:10002] 呢", numericChatID)
	if want := "那就 你来说 [diana-at:10002] 呢"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// 引用标记同一家族，写歪的方式也一样。生产库 9/20 那条：冒号两侧多了空格，
// consumeOutgoingReplyControl 要严格前缀，整条标记当正文发进了群。
func TestOutgoingNormalizesReplyMarkerVariants(t *testing.T) {
	cases := []struct{ in, want string }{
		{"[ diana-reply : 1447664451 ]喵？这张图这次没送进我眼里", "[diana-reply:1447664451]喵？这张图这次没送进我眼里"},
		{"<diana-reply:-1687981517>都没分？", "[diana-reply:-1687981517]都没分？"},
		{"(diana-reply:1231219659)碑先不刻", "[diana-reply:1231219659]碑先不刻"},
		{"[diana-reply:1231219659]碑先不刻", "[diana-reply:1231219659]碑先不刻"},
	}
	for _, item := range cases {
		got := normalizeDianaReplyVariants(item.in)
		if got != item.want {
			t.Fatalf("输入 %q\n得到 %q\n期望 %q", item.in, got, item.want)
		}
		if id, _, ok := consumeOutgoingReplyControl(got); !ok || !validOutgoingReplyMessageID(id) {
			t.Fatalf("扶正后仍然消费不了：%q", got)
		}
	}
}

// 发送层只认开头那一个引用标记；写在正文中间的没人消费，不能原样发出去。
func TestOutgoingDropsResidualReplyMarkers(t *testing.T) {
	runtime := NewRuntime(BotConfig{BotAccount: "42"}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	event := MessageEvent{Platform: PlatformOneBotV11, Kind: EventKindGroup, SelfID: "42", GroupID: "1", UserID: "10001"}
	got := runtime.normalizeOutgoingMentions(event, OutgoingMessage{Text: "这句话里 [diana-reply:123] 混了个引用标记"}).Text
	if want := "这句话里 混了个引用标记"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	code := "标记写成 `[diana-reply:123]` 这样喵"
	if got := runtime.normalizeOutgoingMentions(event, OutgoingMessage{Text: code}).Text; got != code {
		t.Fatalf("代码块被改写了：%q", got)
	}
}
