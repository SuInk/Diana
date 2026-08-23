// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"strings"
	"testing"
)

// TestMarkdownToPlainStripsCommonSyntax 验证对应功能场景。
func TestMarkdownToPlainStripsCommonSyntax(t *testing.T) {
	input := "# 标题\n\n**重点**内容和`代码`，还有[链接](https://example.com)。\n\n- 第一项\n- 第二项\n\n> 引用一句\n\n---\n\n```go\nfmt.Println(\"hi\")\n```\n"
	got := markdownToPlain(input)

	for _, banned := range []string{"**", "# ", "```", "](", "> "} {
		if strings.Contains(got, banned) {
			t.Fatalf("output still contains %q:\n%s", banned, got)
		}
	}
	for _, want := range []string{"标题", "重点内容和代码", "链接 (https://example.com)", "• 第一项", "引用一句", `fmt.Println("hi")`} {
		if !strings.Contains(got, want) {
			t.Fatalf("output missing %q:\n%s", want, got)
		}
	}
}

// TestMarkdownToPlainKeepsBotBrAndPlainText 验证对应功能场景。
func TestMarkdownToPlainKeepsBotBrAndPlainText(t *testing.T) {
	input := "第一段<dianabr>第二段，2*3=6，纯文本不应被改动。"
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
