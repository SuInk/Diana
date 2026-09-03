// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"encoding/json"
	"strings"
	"testing"
)

// 大白话消息不该被认成 Markdown：换消息类型会改掉气泡样式和回复挂载方式，白换。
func TestPlatformTextHasMarkdown(t *testing.T) {
	for _, plain := range []string{"今天天气不错，出去走走吧", "面积是 a*b*c", "变量叫 user_name_id", "3 - 2 = 1"} {
		if platformTextHasMarkdown(plain) {
			t.Fatalf("%q 被误认成 Markdown", plain)
		}
	}
	for _, marked := range []string{"这是**重点**", "## 小结", "- 第一条", "见[文档](https://a.b)", "> 引用", "`code`", "| a | b |\n|---|---|"} {
		if !platformTextHasMarkdown(marked) {
			t.Fatalf("%q 没被认出来", marked)
		}
	}
}

// 平台不认的标记要就地降级，不能以字面量漏出去——这正是纯文本时代 ** 的老毛病。
func TestDowngradeMarkdownDropsUnsupportedMarkup(t *testing.T) {
	source := "## 标题\n**粗**和*斜*和~~删~~\n```go\ncode here\n```\n行内 `x`\n见[文档](https://a.b)"
	out := downgradeMarkdownFor(source, platformMarkdownFeatures{Bold: true, Links: true})

	if strings.Contains(out, "~~") || strings.Contains(out, "```") || strings.Contains(out, "`x`") {
		t.Fatalf("不支持的标记漏出去了：%q", out)
	}
	if strings.Contains(out, "## ") {
		t.Fatalf("标题没降级：%q", out)
	}
	// 标题和斜体都该降成粗体：平台认粗体，语气不该在降级里丢掉。
	if !strings.Contains(out, "**标题**") || !strings.Contains(out, "**斜**") {
		t.Fatalf("没降级成粗体：%q", out)
	}
	// 内容本身必须留着。
	for _, keep := range []string{"code here", "行内 x", "粗", "删"} {
		if !strings.Contains(out, keep) {
			t.Fatalf("内容 %q 被降级弄丢了：%q", keep, out)
		}
	}
	if !strings.Contains(out, "[文档](https://a.b)") {
		t.Fatalf("平台认链接，不该摊平：%q", out)
	}
}

// 平台不认链接时地址不能整个丢掉，否则用户看到一句话却点不到东西。
func TestDowngradeMarkdownFlattensLinksWhenUnsupported(t *testing.T) {
	out := downgradeMarkdownFor("见[文档](https://a.b)", platformMarkdownFeatures{})
	if !strings.Contains(out, "文档") || !strings.Contains(out, "https://a.b") {
		t.Fatalf("链接地址丢了：%q", out)
	}
	if strings.Contains(out, "](") {
		t.Fatalf("链接标记没摊平：%q", out)
	}
}

// 三家都不认表格，一律转成等宽对齐的行；认代码块的顺便裹一层保住等宽。
func TestDowngradeMarkdownConvertsTables(t *testing.T) {
	table := "| 平台 | 状态 |\n|---|---|\n| 钉钉 | 待办 |"

	fenced := downgradeMarkdownFor(table, platformMarkdownFeatures{CodeFence: true})
	if !strings.HasPrefix(strings.TrimSpace(fenced), "```") {
		t.Fatalf("认代码块的平台该裹一层：%q", fenced)
	}
	plain := downgradeMarkdownFor(table, platformMarkdownFeatures{})
	if strings.Contains(plain, "|") || strings.Contains(plain, "```") {
		t.Fatalf("表格标记没清干净：%q", plain)
	}
	if !strings.Contains(plain, "钉钉") || !strings.Contains(plain, "待办") {
		t.Fatalf("表格内容丢了：%q", plain)
	}
}

// 支持什么就原样留着，别做多余的改写。
func TestDowngradeMarkdownKeepsSupportedMarkup(t *testing.T) {
	source := "**粗**和~~删~~\n```go\ncode\n```"
	full := platformMarkdownFeatures{Bold: true, Strike: true, CodeFence: true, Lists: true}
	if out := downgradeMarkdownFor(source, full); out != source {
		t.Fatalf("支持的标记被改写了：%q", out)
	}
}

func TestDingTalkMarkdownPayload(t *testing.T) {
	if _, ok := dingTalkMarkdownPayload("今天天气不错"); ok {
		t.Fatal("纯文本不该走 markdown 消息")
	}
	payload, ok := dingTalkMarkdownPayload("## 小结\n**重点**在这里")
	if !ok {
		t.Fatal("带标记的文本应当走 markdown 消息")
	}
	if payload["msgtype"] != "markdown" {
		t.Fatalf("msgtype = %v", payload["msgtype"])
	}
	markdown, _ := payload["markdown"].(map[string]string)
	// 钉钉要求 markdown 消息必须带 title，缺了会被接口拒收。
	if markdown == nil || strings.TrimSpace(markdown["title"]) == "" {
		t.Fatalf("title 不能为空：%#v", payload)
	}
	if !strings.Contains(markdown["text"], "**重点**") {
		t.Fatalf("正文丢了标记：%q", markdown["text"])
	}
}

// 标题取正文开头，而且不能带着 # 和 - 这些标记进通知栏。
func TestDingTalkMarkdownTitle(t *testing.T) {
	if title := dingTalkMarkdownTitle("## 今日小结\n正文"); title != "今日小结" {
		t.Fatalf("title = %q", title)
	}
	if title := dingTalkMarkdownTitle(""); title == "" {
		t.Fatal("空正文也得给个标题，否则钉钉拒收")
	}
}

func TestFeishuMarkdownCard(t *testing.T) {
	if _, ok := feishuMarkdownCard("今天天气不错"); ok {
		t.Fatal("纯文本不该包成卡片")
	}
	raw, ok := feishuMarkdownCard("**重点**在这里")
	if !ok {
		t.Fatal("带标记的文本应当包成卡片")
	}
	var card struct {
		Elements []struct {
			Tag     string `json:"tag"`
			Content string `json:"content"`
		} `json:"elements"`
	}
	if err := json.Unmarshal([]byte(raw), &card); err != nil {
		t.Fatalf("卡片不是合法 JSON：%v", err)
	}
	if len(card.Elements) != 1 || card.Elements[0].Tag != "markdown" {
		t.Fatalf("卡片结构不对：%s", raw)
	}
	if !strings.Contains(card.Elements[0].Content, "**重点**") {
		t.Fatalf("正文丢了标记：%q", card.Elements[0].Content)
	}
}

// 飞书卡片不认 # 标题，得先降成粗体再进卡片。
func TestFeishuMarkdownCardDowngradesHeadings(t *testing.T) {
	raw, ok := feishuMarkdownCard("## 小结\n正文")
	if !ok {
		t.Fatal("应当包成卡片")
	}
	if strings.Contains(raw, "## ") {
		t.Fatalf("标题没降级：%s", raw)
	}
}
