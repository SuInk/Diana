// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"strings"
	"testing"
)

// TestMarkdownToPlainStripsCommonSyntax 验证对应功能场景。
func TestMarkdownToPlainStripsCommonSyntax(t *testing.T) {
	input := "# 标题\n\n**重点**内容和`代码`，还有[链接](https://example.com)。\n\n- 苹果\n- 香蕉\n\n> 引用一句\n\n---\n\n```go\nfmt.Println(\"hi\")\n```\n"
	got := markdownToPlain(input)

	for _, banned := range []string{"**", "# ", "```", "](", "> "} {
		if strings.Contains(got, banned) {
			t.Fatalf("output still contains %q:\n%s", banned, got)
		}
	}
	for _, want := range []string{"标题", "重点内容和代码", "链接 (https://example.com)", "1. 苹果\n2. 香蕉", "引用一句", `fmt.Println("hi")`} {
		if !strings.Contains(got, want) {
			t.Fatalf("output missing %q:\n%s", want, got)
		}
	}
}

// TestMarkdownToPlainKeepsBotBrAndPlainText 验证对应功能场景。
func TestMarkdownToPlainKeepsBotBrAndPlainText(t *testing.T) {
	input := "第一段" + notificationSplitMarker + "第二段，2*3=6，纯文本不应被改动。"
	if got := markdownToPlain(input); got != input {
		t.Fatalf("plain text changed: %q -> %q", input, got)
	}
}

// TestNormalizeReplyDowngradesMarkdown 验证对应功能场景。
func TestNormalizeReplyDowngradesMarkdown(t *testing.T) {
	got := normalizeReply("**你好**，看[这里](https://a.cn)", 100, true)
	if got != "你好，看这里 (https://a.cn)" {
		t.Fatalf("normalizeReply = %q", got)
	}
	// 关闭降级时保留原始 Markdown。
	raw := normalizeReply("**你好**", 100, false)
	if raw != "**你好**" {
		t.Fatalf("markdown kept = %q", raw)
	}
}

// TestTruncateForChat 验证对应功能场景。
func TestTruncateForChat(t *testing.T) {
	if got := truncateForChat("短文本", 10); got != "短文本" {
		t.Fatalf("short = %q", got)
	}
	long := strings.Repeat("错", 200)
	got := truncateForChat(long, 160)
	if runes := []rune(got); len(runes) != 161 || !strings.HasSuffix(got, "…") {
		t.Fatalf("truncated len = %d, got %q", len([]rune(got)), got[:30])
	}
}

// 无序列表在纯文本平台编成 1. 2. 3.，不发 QQ 里又小又不对齐的「•」。
func TestMarkdownToPlainNumbersBullets(t *testing.T) {
	input := "要点：\n- 甲\n  补充说明\n  - 子项\n- 乙\n\n另一组：\n* 丙\n- 第 2 步启动"
	want := "要点：\n1. 甲\n  补充说明\n  - 子项\n2. 乙\n\n另一组：\n1. 丙\n第 2 步启动"
	if got := markdownToPlain(input); got != want {
		t.Fatalf("markdownToPlain = %q, want %q", got, want)
	}
}

// 线上回复：介绍、列表、追问之间用空行隔开，开着换行分条。列表要编号并整块留在一条。
func TestPlainReplyListNumberedAndKeptTogether(t *testing.T) {
	raw := "上周刚发售的游戏，热乎着呢～\n\n- 10 月 28 日发售，登陆 PS5 和 PC\n- Steam 标准版 $59.99，豪华版 $69.99\n- 本体约 145G，必须装 SSD\n\n主人是打算入还是已经在玩了？"
	cfg := BotConfig{MarkdownToPlain: boolPointer(true), ReplyLineSplitEnabled: boolPointer(true), MaxReplyChars: 8000}.WithDefaults()
	event := MessageEvent{Kind: EventKindPrivate}
	got := splitEventChatReply(normalizeReply(raw, 8000, true), cfg, event)
	want := "1. 10 月 28 日发售，登陆 PS5 和 PC\n2. Steam 标准版 $59.99，豪华版 $69.99\n3. 本体约 145G，必须装 SSD"
	found := false
	for _, bubble := range got {
		if strings.Contains(bubble, "•") {
			t.Fatalf("bullet leaked: %q", got)
		}
		found = found || bubble == want
	}
	if !found {
		t.Fatalf("list bubble missing, got %q", got)
	}
}
