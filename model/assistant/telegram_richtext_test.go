// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"strings"
	"testing"
	"unicode/utf16"
)

// entityAt 按 UTF-16 偏移把 entity 覆盖的那段文字取出来，用来验证格式盖对了位置。
func entityAt(t *testing.T, text string, entity telegramEntitySpec) string {
	t.Helper()
	units := utf16.Encode([]rune(text))
	if entity.Offset < 0 || entity.Offset+entity.Length > len(units) {
		t.Fatalf("entity 越界：offset=%d length=%d 文本 UTF-16 长度=%d", entity.Offset, entity.Length, len(units))
	}
	return string(utf16.Decode(units[entity.Offset : entity.Offset+entity.Length]))
}

func findEntity(entities []telegramEntitySpec, kind string) (telegramEntitySpec, bool) {
	for _, entity := range entities {
		if entity.Type == kind {
			return entity, true
		}
	}
	return telegramEntitySpec{}, false
}

func TestTelegramRichTextBoldAndItalic(t *testing.T) {
	text, entities := telegramRichText("这是**重点**也有 *侧重* 的话", nil)
	if strings.Contains(text, "*") {
		t.Fatalf("标记没删干净：%q", text)
	}
	bold, ok := findEntity(entities, "bold")
	if !ok {
		t.Fatalf("没有 bold：%#v", entities)
	}
	if got := entityAt(t, text, bold); got != "重点" {
		t.Fatalf("bold 覆盖 = %q，期望「重点」", got)
	}
	italic, ok := findEntity(entities, "italic")
	if !ok {
		t.Fatalf("没有 italic：%#v", entities)
	}
	if got := entityAt(t, text, italic); got != "侧重" {
		t.Fatalf("italic 覆盖 = %q，期望「侧重」", got)
	}
}

// emoji 在 UTF-16 里占两个 code unit；按 rune 或 byte 算偏移都会让格式盖偏。
func TestTelegramRichTextOffsetsUseUTF16(t *testing.T) {
	text, entities := telegramRichText("🎉🎉 **重点** 收尾", nil)
	bold, ok := findEntity(entities, "bold")
	if !ok {
		t.Fatalf("没有 bold：%#v", entities)
	}
	if got := entityAt(t, text, bold); got != "重点" {
		t.Fatalf("bold 覆盖 = %q，期望「重点」——emoji 后偏移错位了", got)
	}
}

// 代码块里的 * _ ` 是内容不是标记，不能被内联规则啃掉。
func TestTelegramRichTextCodeBlockKeepsContentVerbatim(t *testing.T) {
	source := "看这段：\n```go\nname := *ptr\nvalue := a_b_c\n```\n就这样"
	text, entities := telegramRichText(source, nil)
	if !strings.Contains(text, "name := *ptr") || !strings.Contains(text, "value := a_b_c") {
		t.Fatalf("代码内容被改写了：%q", text)
	}
	pre, ok := findEntity(entities, "pre")
	if !ok {
		t.Fatalf("没有 pre：%#v", entities)
	}
	if got := entityAt(t, text, pre); !strings.Contains(got, "name := *ptr") {
		t.Fatalf("pre 覆盖 = %q", got)
	}
	if pre.Language != "go" {
		t.Fatalf("语言 = %q，期望 go", pre.Language)
	}
}

func TestTelegramRichTextInlineCode(t *testing.T) {
	text, entities := telegramRichText("执行 `go test ./...` 就好", nil)
	if strings.Contains(text, "`") {
		t.Fatalf("反引号没删干净：%q", text)
	}
	code, ok := findEntity(entities, "code")
	if !ok {
		t.Fatalf("没有 code：%#v", entities)
	}
	if got := entityAt(t, text, code); got != "go test ./..." {
		t.Fatalf("code 覆盖 = %q", got)
	}
}

func TestTelegramRichTextLink(t *testing.T) {
	text, entities := telegramRichText("详见[发布说明](https://example.com/rel)最后一段", nil)
	if strings.Contains(text, "https://example.com") {
		t.Fatalf("链接地址不该留在正文里：%q", text)
	}
	link, ok := findEntity(entities, "text_link")
	if !ok {
		t.Fatalf("没有 text_link：%#v", entities)
	}
	if got := entityAt(t, text, link); got != "发布说明" {
		t.Fatalf("link 覆盖 = %q", got)
	}
	if link.URL != "https://example.com/rel" {
		t.Fatalf("link URL = %q", link.URL)
	}
}

// Diana 自己的方括号标记不是链接，被当成链接拆掉的话出站就认不出提及了。
func TestTelegramRichTextKeepsDianaMarkers(t *testing.T) {
	text, _ := telegramRichText("[diana-at:10002](不是链接)", nil)
	if !strings.Contains(text, "[diana-at:10002]") {
		t.Fatalf("Diana 标记被拆掉了：%q", text)
	}
}

// 提及和 Markdown 标记混在一起时，删标记会让提及整体前移，两者必须一起算。
func TestTelegramRichTextKeepsMentionAligned(t *testing.T) {
	rendered, mentions := renderDianaMentions("**重点**看 [diana-at:10002] 说的", map[string]string{"10002": "轩诺"})
	if len(mentions) != 1 {
		t.Fatalf("提及解析失败：%#v", mentions)
	}
	text, entities := telegramRichText(rendered, mentions)
	mention, ok := findEntity(entities, "text_mention")
	if !ok {
		t.Fatalf("提及 entity 丢了：%#v", entities)
	}
	if got := entityAt(t, text, mention); got != "@轩诺" {
		t.Fatalf("提及覆盖 = %q，期望「@轩诺」——删粗体标记后没有跟着前移", got)
	}
	bold, ok := findEntity(entities, "bold")
	if !ok {
		t.Fatalf("没有 bold：%#v", entities)
	}
	if got := entityAt(t, text, bold); got != "重点" {
		t.Fatalf("bold 覆盖 = %q", got)
	}
}

func TestTelegramRichTextHeadingBecomesBold(t *testing.T) {
	text, entities := telegramRichText("## 今日小结\n正文在这里", nil)
	if strings.Contains(text, "#") {
		t.Fatalf("井号没删干净：%q", text)
	}
	bold, ok := findEntity(entities, "bold")
	if !ok {
		t.Fatalf("标题没有加粗：%#v", entities)
	}
	if got := entityAt(t, text, bold); got != "今日小结" {
		t.Fatalf("bold 覆盖 = %q", got)
	}
}

func TestTelegramRichTextBulletsAndQuote(t *testing.T) {
	text, _ := telegramRichText("- 第一条\n- 第二条\n> 引用一句", nil)
	if !strings.Contains(text, "• 第一条") || !strings.Contains(text, "• 第二条") {
		t.Fatalf("列表符号没换成圆点：%q", text)
	}
	if strings.Contains(text, ">") {
		t.Fatalf("引用符号没删掉：%q", text)
	}
}

// 普通乘号和 snake_case 不该被当成斜体。
func TestTelegramRichTextIgnoresNonMarkupSymbols(t *testing.T) {
	text, entities := telegramRichText("面积是 a*b*c，变量叫 user_name_id", nil)
	if _, ok := findEntity(entities, "italic"); ok {
		t.Fatalf("乘号或下划线被当成斜体了：%q %#v", text, entities)
	}
	if text != "面积是 a*b*c，变量叫 user_name_id" {
		t.Fatalf("正文被改写了：%q", text)
	}
}

// 非数字 user id 的提及要整条丢掉，否则 Telegram 会以参数非法拒收整条消息。
func TestTelegramEntityParamsDropsNonNumericMention(t *testing.T) {
	params := telegramEntityParams([]telegramEntitySpec{
		{Type: "text_mention", Offset: 0, Length: 3, URL: "diana-mention:not-a-number"},
		{Type: "bold", Offset: 4, Length: 2},
	})
	if len(params) != 1 || params[0]["type"] != "bold" {
		t.Fatalf("params = %#v", params)
	}
}

func TestTelegramEntityParamsRendersMentionUser(t *testing.T) {
	params := telegramEntityParams([]telegramEntitySpec{
		{Type: "text_mention", Offset: 0, Length: 3, URL: "diana-mention:10002"},
	})
	if len(params) != 1 {
		t.Fatalf("params = %#v", params)
	}
	user, _ := params[0]["user"].(map[string]any)
	if user == nil || user["id"] != int64(10002) {
		t.Fatalf("user = %#v", params[0])
	}
}

// 没有任何标记时正文必须一字不改，避免给所有普通消息引入无谓的差异。
func TestTelegramRichTextLeavesPlainTextAlone(t *testing.T) {
	source := "今天天气不错，出去走走吧"
	text, entities := telegramRichText(source, nil)
	if text != source || len(entities) != 0 {
		t.Fatalf("纯文本被改动了：%q %#v", text, entities)
	}
}

// 默认值随平台走：能渲染的保留标记，不能渲染的降级。
func TestMarkdownToPlainDefaultsByPlatform(t *testing.T) {
	if markdownToPlainForConfig(BotConfig{Platform: PlatformTelegram}) {
		t.Fatal("Telegram 默认应当保留 Markdown")
	}
	for _, platform := range []string{PlatformOneBotV11, PlatformQQOfficial, PlatformDingTalk, PlatformFeishu, PlatformWeCom} {
		if !markdownToPlainForConfig(BotConfig{Platform: platform}) {
			t.Fatalf("%s 只发纯文本，默认应当降级", platform)
		}
	}
}

// 显式设置永远优先于平台默认。
func TestMarkdownToPlainRespectsExplicitOverride(t *testing.T) {
	on, off := true, false
	if !markdownToPlainForConfig(BotConfig{Platform: PlatformTelegram, MarkdownToPlain: &on}) {
		t.Fatal("Telegram 上显式开启降级应当生效")
	}
	if markdownToPlainForConfig(BotConfig{Platform: PlatformOneBotV11, MarkdownToPlain: &off}) {
		t.Fatal("OneBot 上显式关闭降级应当生效")
	}
}

func TestPlatformSupportsRichText(t *testing.T) {
	if !PlatformSupportsRichText(PlatformTelegram) {
		t.Fatal("Telegram 应当支持富文本")
	}
	if PlatformSupportsRichText("unknown-platform") {
		t.Fatal("未知平台应当按不支持处理")
	}
}
