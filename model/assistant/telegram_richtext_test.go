// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"reflect"
	"sort"
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
	for _, platform := range []string{PlatformTelegram, PlatformDingTalk, PlatformFeishu, PlatformWeCom} {
		if markdownToPlainForConfig(BotConfig{Platform: platform}) {
			t.Fatalf("%s 出站发富文本，默认应当保留 Markdown", platform)
		}
	}
	// OneBot 和 QQ 官方的出站只有纯文本消息类型，留着标记就会以字面量漏出去。
	for _, platform := range []string{PlatformOneBotV11, PlatformQQOfficial} {
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

// Telegram 没有表格 entity，转成等宽块是唯一还能让列对齐的表达。
func TestTelegramRichTextRendersTableAsMonospace(t *testing.T) {
	text, entities := telegramRichText("看这个：\n| 平台 | 状态 |\n|------|------|\n| 钉钉 | 待办 |", nil)
	if strings.Contains(text, "|") || strings.Contains(text, "---") {
		t.Fatalf("表格标记没清干净：%q", text)
	}
	pre, ok := findEntity(entities, "pre")
	if !ok {
		t.Fatalf("表格没渲染成等宽块：%#v", entities)
	}
	block := entityAt(t, text, pre)
	for _, cell := range []string{"平台", "状态", "钉钉", "待办"} {
		if !strings.Contains(block, cell) {
			t.Fatalf("单元格 %q 丢了：%q", cell, block)
		}
	}
}

// 中文占两格。按 rune 数补空格的话，中文列会整体偏左，表格就不成表格了。
func TestRenderTableBlockAlignsWideRunes(t *testing.T) {
	block := renderTableBlock([][]string{{"平台", "x"}, {"ab", "y"}})
	lines := strings.Split(block, "\n")
	if len(lines) < 3 {
		t.Fatalf("表格行数不对：%q", block)
	}
	// 「平台」占 4 格，「ab」占 2 格，后者要补两个空格才能和第二列对齐。
	if displayWidth(lines[0]) != displayWidth(lines[2]) {
		t.Fatalf("两行宽度不一致：%q(%d) vs %q(%d)", lines[0], displayWidth(lines[0]), lines[2], displayWidth(lines[2]))
	}
}

// 随手写的竖线不是表格：少了分隔行就不该被吞掉。
func TestTelegramRichTextIgnoresPipesWithoutSeparator(t *testing.T) {
	source := "今天 | 明天 | 后天"
	text, entities := telegramRichText(source, nil)
	if text != source || len(entities) != 0 {
		t.Fatalf("普通竖线被当成表格了：%q %#v", text, entities)
	}
}

// [ ] 和 [x] 在聊天窗口里就是一对方括号，看不出是勾选框。
func TestTelegramRichTextRendersTaskList(t *testing.T) {
	text, _ := telegramRichText("- [x] 写转换器\n- [ ] 改适配器", nil)
	if !strings.Contains(text, "☑ 写转换器") || !strings.Contains(text, "☐ 改适配器") {
		t.Fatalf("待办框没转换：%q", text)
	}
	if strings.Contains(text, "[") {
		t.Fatalf("方括号没清干净：%q", text)
	}
}

// Keep the legacy Markdown cases as a parity baseline for the production renderer.
func TestTelegramOutgoingLegacyMarkdownParity(t *testing.T) {
	cases := []struct{ name, source string }{
		{"bold and italic", "这是**重点**也有 *侧重* 的话"},
		{"UTF16 offsets", "🎉🎉 **重点** 收尾"},
		{"code block", "看这段：\n```go\nname := *ptr\nvalue := a_b_c\n```\n就这样"},
		{"inline code", "执行 `go test ./...` 就好"},
		{"link", "详见[发布说明](https://example.com/rel)最后一段"},
		{"mention alignment", "**重点**看 [diana-at:10002] 说的"},
		{"heading", "## 今日小结\n正文在这里"},
		{"bullets and quote", "- 第一条\n- 第二条\n> 引用一句"},
		{"non-markup symbols", "面积是 a*b*c，变量叫 user_name_id"},
		{"plain text", "今天天气不错，出去走走吧"},
		{"table", "看这个：\n| 平台 | 状态 |\n|------|------|\n| 钉钉 | 待办 |"},
		{"ordinary pipes", "今天 | 明天 | 后天"},
		{"task list", "- [x] 写转换器\n- [ ] 改适配器"},
		{"nested list", "- 第一层\n  - 第二层\n    - 第三层"},
	}
	canonical := func(entities []telegramEntitySpec) []telegramEntitySpec {
		result := append([]telegramEntitySpec(nil), entities...)
		sort.Slice(result, func(i, j int) bool {
			if result[i].Offset != result[j].Offset {
				return result[i].Offset < result[j].Offset
			}
			return result[i].Type < result[j].Type
		})
		return result
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			names := map[string]string{"10002": "轩诺"}
			rendered, mentions := renderDianaMentions(tc.source, names)
			wantText, wantEntities := telegramRichText(rendered, mentions)
			text, entities := renderTelegramOutgoingText(tc.source, names, nil)
			if text != wantText || !reflect.DeepEqual(canonical(entities), canonical(wantEntities)) {
				t.Fatalf("production = %q %#v; legacy = %q %#v", text, entities, wantText, wantEntities)
			}
		})
	}
	// The new entry point resolves Diana markers rather than exposing the legacy
	// Markdown-only stage, but a following non-link must still remain literal.
	text, entities := renderTelegramOutgoingText("[diana-at:10002](不是链接)", nil, nil)
	if text != "@10002(不是链接)" || len(entities) != 1 || entities[0].Type != "text_mention" {
		t.Fatalf("Diana marker followed by non-link = %q %#v", text, entities)
	}
}

func TestTelegramOutgoingMentions(t *testing.T) {
	for _, tc := range []struct {
		name, source, username, display, kind string
	}{
		{"username", "[diana-at:10002]", "some_user_name", "@some_user_name", "mention"},
		{"short trusted username", "[diana-at:10002]", "a", "@a", "mention"},
		{"numeric trusted username", "[diana-at:10002]", "2_a", "@2_a", "mention"},
		{"all numeric trusted username", "[diana-at:10002]", "12345", "@12345", "mention"},
		{"case preserved", "[diana-at:10002]", "Some_User", "@Some_User", "mention"},
		{"max length", "[diana-at:10002]", strings.Repeat("a", 32), "@" + strings.Repeat("a", 32), "mention"},
		{"too long", "[diana-at:10002]", strings.Repeat("a", 33), "@小😀_*[名]", "text_mention"},
		{"whitespace", "[diana-at:10002]", " handle ", "@小😀_*[名]", "text_mention"},
		{"nickname", "[diana-at:10002]", "", "@小😀_*[名]", "text_mention"},
		{"invalid username", "[diana-at:10002]", "bad.name", "@小😀_*[名]", "text_mention"},
		{"unicode username", "[diana-at:10002]", "全角", "@小😀_*[名]", "text_mention"},
		{"at username", "[diana-at:10002]", "@handle", "@小😀_*[名]", "text_mention"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			text, entities := renderTelegramOutgoingText(tc.source, map[string]string{"10002": "小😀_*[名]"}, map[string]string{"10002": tc.username})
			entity, ok := findEntity(entities, tc.kind)
			if text != tc.display || len(entities) != 1 || !ok || entityAt(t, text, entity) != tc.display {
				t.Fatalf("text=%q entities=%#v", text, entities)
			}
			if tc.kind == "text_mention" && entity.URL != "diana-mention:10002" {
				t.Fatalf("mention target=%q", entity.URL)
			}
		})
	}
	text, entities := renderTelegramOutgoingText("[diana-at:10003]", nil, nil)
	if text != "@10003" || len(entities) != 1 || entityAt(t, text, entities[0]) != text {
		t.Fatalf("ID fallback text=%q entities=%#v", text, entities)
	}
}

func TestTelegramOutgoingMentionPositions(t *testing.T) {
	for _, tc := range []struct{ source, want, bold string }{
		{"🎉 **[diana-at:10002]**[diana-at:10002] [diana-at:10003]", "🎉 @a_b_c@a_b_c @小😀", "@a_b_c"},
		{"**前[diana-at:10002]后**", "前@a_b_c后", "前@a_b_c后"},
		{"**[diana-at:10002]尾**", "@a_b_c尾", "@a_b_c尾"},
		{"**头[diana-at:10002]**", "头@a_b_c", "头@a_b_c"},
		{"# [diana-at:10002]\n- [diana-at:10003]", "@a_b_c\n• @小😀", "@a_b_c"},
		{"# **[diana-at:10002]** 标题\n- [x] [diana-at:10003]", "@a_b_c 标题\n☑ @小😀", "@a_b_c 标题"},
		{"`x_*` **[diana-at:10002]**", "x_* @a_b_c", "@a_b_c"},
	} {
		t.Run(tc.source, func(t *testing.T) {
			text, entities := renderTelegramOutgoingText(tc.source, map[string]string{"10003": "小😀"}, map[string]string{"10002": "a_b_c"})
			if text != tc.want {
				t.Fatalf("text=%q want=%q", text, tc.want)
			}
			foundBold, mentions := false, 0
			for _, entity := range entities {
				body := entityAt(t, text, entity)
				switch entity.Type {
				case "bold":
					foundBold = foundBold || body == tc.bold
				case "mention", "text_mention":
					mentions++
					if body != "@a_b_c" && body != "@小😀" {
						t.Fatalf("misaligned entity=%#v body=%q", entity, body)
					}
				}
			}
			if !foundBold || mentions != strings.Count(tc.source, "[diana-at:") {
				t.Fatalf("entities=%#v", entities)
			}
		})
	}
}

func TestTelegramOutgoingCodeMarkersStayLiteral(t *testing.T) {
	literal := "[diana-at:10002] **literal**"
	for _, tc := range []struct{ source, kind string }{
		{"`" + literal + "`", "code"},
		{"``" + literal + "`x``", "code"},
		{"```text\n" + literal + "\n```", "pre"},
		{"~~~text\n" + literal + "\n~~~", "pre"},
		{"```text\n" + literal, "pre"},
		{"~~~text\n" + literal, "pre"},
		{"````text\n```\n" + literal + "\n````", "pre"},
	} {
		t.Run(tc.source, func(t *testing.T) {
			text, entities := renderTelegramOutgoingText("[diana-at:10003]\n"+tc.source, nil, map[string]string{"10002": "some_user", "10003": "other_user"})
			code, ok := findEntity(entities, tc.kind)
			if !ok || !strings.Contains(entityAt(t, text, code), literal) {
				t.Fatalf("text=%q entities=%#v", text, entities)
			}
			mentions := 0
			for _, entity := range entities {
				if entity.Type == "mention" || entity.Type == "text_mention" {
					mentions++
					if entityAt(t, text, entity) != "@other_user" {
						t.Fatalf("code marker became mention: %#v", entity)
					}
				}
			}
			if mentions != 1 {
				t.Fatalf("entities=%#v", entities)
			}
		})
	}
}

func TestTelegramOutgoingMentionLinkPriority(t *testing.T) {
	for _, source := range []string{
		"[[diana-at:10002]](https://example.com)",
		"[前 **[diana-at:10002]** 后](https://example.com)",
	} {
		text, entities := renderTelegramOutgoingText(source+" [网址](https://example.org)", nil, map[string]string{"10002": "some_user"})
		mention, ok := findEntity(entities, "mention")
		if !ok || entityAt(t, text, mention) != "@some_user" {
			t.Fatalf("text=%q entities=%#v", text, entities)
		}
		for _, entity := range entities {
			if entity.Type == "text_link" && entityAt(t, text, entity) != "网址" {
				t.Fatalf("conflicting link=%#v", entity)
			}
		}
	}
}

func TestTelegramOutgoingLinkTargetsRestoreOpaqueText(t *testing.T) {
	for _, tc := range []struct {
		name     string
		target   string
		username string
		wantURL  string
		wantText string
		kind     string
	}{
		{"username", "[diana-at:10002]", "alice_name", "https://example.com/@alice_name", "个人页 @alice_name", "mention"},
		{"fallback", "[diana-at:10002]", "", "https://example.com/@Alice", "个人页 @Alice", "text_mention"},
		{"repeated", "[diana-at:10002]/[diana-at:10002]", "alice_name", "https://example.com/@alice_name/@alice_name", "个人页 @alice_name", "mention"},
		{"code", "`[diana-at:10002]`", "alice_name", "https://example.com/[diana-at:10002]", "个人页 @alice_name", "mention"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := "[个人页](https://example.com/" + tc.target + ") [diana-at:10002]"
			text, entities := renderTelegramOutgoingText(source, map[string]string{"10002": "Alice"}, map[string]string{"10002": tc.username})
			link, ok := findEntity(entities, "text_link")
			if text != tc.wantText || !ok || link.URL != tc.wantURL || entityAt(t, text, link) != "个人页" {
				t.Fatalf("text=%q entities=%#v wantURL=%q", text, entities, tc.wantURL)
			}
			mention, ok := findEntity(entities, tc.kind)
			if len(entities) != 2 || !ok || entityAt(t, text, mention) != strings.TrimPrefix(tc.wantText, "个人页 ") {
				t.Fatalf("unexpected visible entities: %#v", entities)
			}
			for _, param := range telegramEntityParams(entities) {
				if param["type"] == "text_link" && param["url"] != tc.wantURL {
					t.Fatalf("serialized link=%#v", param)
				}
			}
		})
	}
}

func TestTelegramOutgoingLinkTargetsValidateRestoredURL(t *testing.T) {
	for _, tc := range []struct {
		name   string
		target string
	}{
		{"nickname", "[diana-at:10002]"},
		{"code", "`Alice%ZZ`"},
		{"multiple", "[diana-at:10002]/`suffix`"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := "😀 [主页](https://example.com/" + tc.target + ") [正常](https://example.com/ok) **[diana-at:10002]**"
			text, entities := renderTelegramOutgoingText(source, map[string]string{"10002": "Alice%ZZ"}, nil)
			if text != "😀 主页 正常 @Alice%ZZ" {
				t.Fatalf("text=%q", text)
			}
			links := 0
			for _, entity := range entities {
				if entity.Type == "text_link" {
					links++
					if normalizedHTTPURL(entity.URL) == "" || entity.URL != "https://example.com/ok" || entityAt(t, text, entity) != "正常" {
						t.Fatalf("invalid restored link: %#v", entity)
					}
				}
			}
			if links != 1 {
				t.Fatalf("links=%d entities=%#v", links, entities)
			}
			for _, kind := range []string{"bold", "text_mention"} {
				entity, ok := findEntity(entities, kind)
				if !ok || entityAt(t, text, entity) != "@Alice%ZZ" {
					t.Fatalf("lost %s: %#v", kind, entities)
				}
			}
		})
	}
}

func TestTelegramOutgoingMentionTables(t *testing.T) {
	source := "| 用户 | x |\n|---|---|\n| **[diana-at:10002]** | y |\n| [diana-at:10003] | z |\n\n| 常规 | 表 |\n|---|---|\n| 内容 | 好 |"
	text, entities := renderTelegramOutgoingText(source, map[string]string{"10003": "小😀"}, map[string]string{"10002": "some_long_user"})
	lines := strings.Split(text, "\n")
	for _, index := range []int{2, 3} {
		if displayWidth(lines[index]) != displayWidth(lines[0]) {
			t.Fatalf("unaligned visible table: %q", text)
		}
	}
	for _, kind := range []string{"mention", "text_mention", "bold", "pre"} {
		entity, ok := findEntity(entities, kind)
		if !ok {
			t.Fatalf("missing %s: %#v", kind, entities)
		}
		body := entityAt(t, text, entity)
		if kind == "pre" && (!strings.Contains(body, "常规") || strings.Contains(body, "@")) {
			t.Fatalf("table pre=%q", body)
		}
		if kind == "bold" && body != "@some_long_user" {
			t.Fatalf("bold=%q", body)
		}
	}
}

func TestTelegramOutgoingOrdinaryCodeTableWidths(t *testing.T) {
	for _, value := range []string{"x", "汉字😀", "[diana-at:10002]", "a|b", strings.Repeat("long", 10)} {
		source := "| 用户 | x |\n|---|---|\n| `" + value + "` | y |"
		text, entities := renderTelegramOutgoingText(source, nil, map[string]string{"10002": "some_user"})
		want := renderTableBlock([][]string{{"用户", "x"}, {value, "y"}})
		if text != want || len(entities) != 1 || entities[0].Type != "pre" || entityAt(t, text, entities[0]) != want {
			t.Fatalf("text=%q want=%q entities=%#v", text, want, entities)
		}
	}
}

func TestTelegramSpanReplacementBoundaries(t *testing.T) {
	buffer := &telegramSpanBuffer{
		text: "😀LtokenR!",
		entities: []telegramEntitySpec{
			{Type: "bold", Offset: 3, Length: 5},
			{Type: "italic", Offset: 2, Length: 6},
			{Type: "strikethrough", Offset: 3, Length: 6},
			{Type: "underline", Offset: 2, Length: 7},
			{Type: "before", Offset: 0, Length: 3},
			{Type: "after", Offset: 8, Length: 2},
			{Type: "left_partial", Offset: 2, Length: 3},
			{Type: "right_partial", Offset: 5, Length: 4},
			{Type: "inside", Offset: 4, Length: 2},
		},
	}
	buffer.replace(len("😀L"), len("😀Ltoken"), "@小😀")
	want := map[string]string{"bold": "@小😀", "italic": "L@小😀", "strikethrough": "@小😀R", "underline": "L@小😀R", "before": "😀L", "after": "R!"}
	if len(buffer.entities) != len(want) || buffer.text != "😀L@小😀R!" {
		t.Fatalf("text=%q entities=%#v", buffer.text, buffer.entities)
	}
	for _, entity := range buffer.entities {
		if got := entityAt(t, buffer.text, entity); got != want[entity.Type] {
			t.Fatalf("entity=%#v got=%q want=%q", entity, got, want[entity.Type])
		}
	}
}

func TestTelegramOutgoingDoesNotSupportTGUserLinks(t *testing.T) {
	source := "[Alice](tg://user?id=10002)"
	text, entities := renderTelegramOutgoingText(source, nil, nil)
	if text != source || len(entities) != 0 {
		t.Fatalf("text=%q entities=%#v", text, entities)
	}
}

func TestTelegramOutgoingOpaqueCollisions(t *testing.T) {
	name := "**x** _y_ [z](https://example.com) [diana-at:10003] \x000\x00 \x01 T0X0"
	source := "\x000\x00 \x01 T0X0 `code` [diana-at:10002] [diana-at:10002]"
	text, entities := renderTelegramOutgoingText(source, map[string]string{"10002": name}, nil)
	want := "\x000\x00 \x01 T0X0 code @" + name + " @" + name
	if text != want {
		t.Fatalf("text=%q want=%q", text, want)
	}
	for _, entity := range entities {
		if entity.Type == "text_mention" && entityAt(t, text, entity) != "@"+name {
			t.Fatalf("entity=%#v", entity)
		}
	}
}

// 嵌套层级靠符号区分：只留缩进的话在手机上几乎看不出来。
func TestTelegramRichTextMarksNestedListDepth(t *testing.T) {
	text, _ := telegramRichText("- 第一层\n  - 第二层\n    - 第三层", nil)
	for _, want := range []string{"• 第一层", "◦ 第二层", "▪ 第三层"} {
		if !strings.Contains(text, want) {
			t.Fatalf("缺少 %q：%q", want, text)
		}
	}
}

func TestTelegramTableInlineFormattingKeepsValidEntities(t *testing.T) {
	source := "前言\n| **命令** | 说明 |\n| --- | --- |\n| `very_long_command` | [文档](https://example.com) |\n| `中😀` | ~~旧版~~ |\n\n**结束** `tail`"
	text, entities := telegramRichText(source, nil)
	want := renderTableBlock([][]string{{"命令", "说明"}, {"very_long_command", "文档"}, {"中😀", "旧版"}})
	pre, ok := findEntity(entities, "pre")
	if !ok || entityAt(t, text, pre) != want {
		t.Fatalf("table text/entity mismatch: %q %#v; want %q", text, entities, want)
	}
	if strings.ContainsAny(text, "\x00`*~") {
		t.Fatalf("internal markers leaked: %q", text)
	}
	for _, entity := range entities {
		body := entityAt(t, text, entity)
		switch entity.Type {
		case "pre":
		case "bold":
			if body != "结束" {
				t.Fatalf("wrong bold span: %q", body)
			}
		case "code":
			if body != "tail" {
				t.Fatalf("code entity nested in table: %q", body)
			}
		default:
			t.Fatalf("unexpected entity: %#v", entity)
		}
	}
}
