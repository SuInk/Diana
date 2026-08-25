// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"strings"
	"testing"
	"time"
)

func sampleRelationGraph() GroupRelationGraph {
	graph := GroupRelationGraph{GroupID: "555", BotID: "42", Messages: 120, Participants: 4}
	graph.Nodes = []GroupRelationNode{
		{UserID: "42", DisplayName: "Diana", IsBot: true, Messages: 40},
		{UserID: "1001", DisplayName: "Alice", Messages: 30, Favorability: 90},
		{UserID: "1002", DisplayName: "Bob", Messages: 20},
		{UserID: "1003", Messages: 10},
	}
	graph.Edges = []GroupRelationEdge{
		{Source: "1001", Target: "42", Weight: 12},
		{Source: "1002", Target: "42", Weight: 3},
		{Source: "1001", Target: "1002", Weight: 4},
	}
	return graph
}

func TestRenderGroupRelationHTMLIsSelfContained(t *testing.T) {
	page := RenderGroupRelationHTML(sampleRelationGraph(), "群 555 · 关系图", "最近 7 天")
	// 沙箱里取不到外部资源，页面必须自包含。查的是真的会发起请求的东西——
	// SVG 的 xmlns 里也有 http://，那是命名空间标识，不是地址。
	for _, forbidden := range []string{"<script", "<link", "src=", "@import", "url("} {
		if strings.Contains(page, forbidden) {
			t.Fatalf("页面引用了外部资源或脚本 %q", forbidden)
		}
	}
	for _, want := range []string{"群 555 · 关系图", "最近 7 天", "Alice", "Diana", "<svg"} {
		if !strings.Contains(page, want) {
			t.Fatalf("页面缺少 %q", want)
		}
	}
	// 查不到昵称的人退回显示账号，而不是空白圆点。
	if !strings.Contains(page, "1003") {
		t.Fatalf("没有昵称的成员没有回退到账号：%s", page)
	}
}

// 昵称是用户可控的文本，必须转义后再进 SVG，否则一个带尖括号的群名片就能改变
// 这张图的结构。
func TestRenderGroupRelationHTMLEscapesNames(t *testing.T) {
	graph := sampleRelationGraph()
	graph.Nodes[1].DisplayName = `<tspan fill="red">x`
	page := RenderGroupRelationHTML(graph, `群 <b>555</b>`, "")
	if strings.Contains(page, `<tspan fill="red">`) {
		t.Fatalf("昵称里的标签没有转义：%s", page)
	}
	if strings.Contains(page, "<b>555</b>") {
		t.Fatalf("标题没有转义：%s", page)
	}
	if !strings.Contains(page, "&lt;tspan") {
		t.Fatalf("昵称应当被转义成实体：%s", page)
	}
}

// 中心节点的名字不能被截断到看不出是谁：圆圈直径放得下六个字。
func TestRelationCenterLabelKeepsShortNames(t *testing.T) {
	graph := sampleRelationGraph()
	if got := relationCenterLabel(graph); got != "Diana" {
		t.Fatalf("center = %q, want Diana", got)
	}
	graph.Nodes[0].DisplayName = "一个特别长的机器人名字"
	if got := relationCenterLabel(graph); !strings.HasSuffix(got, "…") || len([]rune(got)) != 7 {
		t.Fatalf("过长的名字应当截断成六个字加省略号，实际 %q", got)
	}
}

// 没有成员时不该画出一个空环，也不该 panic。
func TestRenderGroupRelationHTMLHandlesEmptyGraph(t *testing.T) {
	page := RenderGroupRelationHTML(GroupRelationGraph{GroupID: "555"}, "群 555 · 关系图", "最近 7 天")
	if !strings.Contains(page, "<svg") {
		t.Fatalf("空图也要有画布：%s", page)
	}
}

func TestRelationRangeParsing(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	cases := map[string]string{"": "7d", "7d": "7d", "24h": "24h", "1d": "24h", "30d": "30d", "ALL": "all"}
	for input, want := range cases {
		if got := normalizeRelationRange(input); got != want {
			t.Fatalf("normalizeRelationRange(%q) = %q, want %q", input, got, want)
		}
	}
	if _, ok := relationRangeSince("nonsense", now); ok {
		t.Fatal("非法区间应当被拒绝")
	}
	// all 表示不设起点，扫全部历史。
	since, ok := relationRangeSince("all", now)
	if !ok || !since.IsZero() {
		t.Fatalf("all = %v / %v", since, ok)
	}
	if since, ok := relationRangeSince("24h", now); !ok || now.Sub(since) != 24*time.Hour {
		t.Fatalf("24h = %v / %v", since, ok)
	}
}
