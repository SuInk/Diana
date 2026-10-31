// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"fmt"
	"image"
	"math"
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
	avatar := image.NewNRGBA(image.Rect(0, 0, 8, 8))
	page := RenderGroupRelationHTML(sampleRelationGraph(), "群 555 · 关系图", "最近 7 天", relationImageDefaultSeats, map[string]image.Image{"1001": avatar})
	// 沙箱里取不到外部资源，页面必须自包含：头像只能内联成 data URI。查的是真的会
	// 发起请求的东西——SVG 的 xmlns 里也有 http://，那是命名空间标识，不是地址。
	for _, forbidden := range []string{"<script", "<link", "src=", "@import", "url(", `href="http`} {
		if strings.Contains(page, forbidden) {
			t.Fatalf("页面引用了外部资源或脚本 %q", forbidden)
		}
	}
	for _, want := range []string{"群 555 · 关系图", "最近 7 天", "Dian", "<svg", `href="data:image/png;base64,`} {
		if !strings.Contains(page, want) {
			t.Fatalf("页面缺少 %q", want)
		}
	}
	// 位图没有悬停，成员名字不画。
	if strings.Contains(page, "Alice") {
		t.Fatalf("成员名字不该出现在图上")
	}
}

// 中心没有头像时圆里写名字，名字是用户可控的文本，必须转义。
func TestRenderGroupRelationHTMLEscapesText(t *testing.T) {
	graph := sampleRelationGraph()
	graph.Nodes[0].DisplayName = `<b>x`
	page := RenderGroupRelationHTML(graph, `群 <b>555</b>`, "", relationImageDefaultSeats, nil)
	if strings.Contains(page, "<b>") {
		t.Fatalf("文本里的标签没有转义：%s", page)
	}
	if !strings.Contains(page, "&lt;b&gt;x") {
		t.Fatalf("中心名字应当被转义成实体：%s", page)
	}
}

// 中心圆里只放得下四个字，和网页版一致。
func TestRelationCenterLabelKeepsFourRunes(t *testing.T) {
	if got := relationCenterLabel(GroupRelationNode{DisplayName: "Diana"}); got != "Dian" {
		t.Fatalf("center = %q, want Dian", got)
	}
	if got := relationCenterLabel(GroupRelationNode{}); got != "Diana" {
		t.Fatalf("没有名字时 center = %q, want Diana", got)
	}
}

// 没有成员时不该画出一个空环，也不该 panic。
func TestRenderGroupRelationHTMLHandlesEmptyGraph(t *testing.T) {
	page := RenderGroupRelationHTML(GroupRelationGraph{GroupID: "555"}, "群 555 · 关系图", "最近 7 天", relationImageDefaultSeats, nil)
	if !strings.Contains(page, "<svg") {
		t.Fatalf("空图也要有画布：%s", page)
	}
	if layout := layoutRelationCircle(GroupRelationGraph{GroupID: "555"}, relationImageDefaultSeats); len(layout.bands) != 0 {
		t.Fatalf("没人时不该画底盘：%+v", layout.bands)
	}
}

// 互动越多越靠里：内圈满了才往外排，内圈最多八个人。
func TestLayoutRelationCircleOrdersByInteraction(t *testing.T) {
	graph := GroupRelationGraph{GroupID: "555", BotID: "42", Nodes: []GroupRelationNode{{UserID: "42", IsBot: true}}}
	for index := 0; index < 20; index++ {
		id := fmt.Sprintf("u%02d", index)
		graph.Nodes = append(graph.Nodes, GroupRelationNode{UserID: id, Messages: 1})
		graph.Edges = append(graph.Edges, GroupRelationEdge{Source: id, Target: "42", Weight: 100 - index})
	}
	layout := layoutRelationCircle(graph, relationImageDefaultSeats)
	if len(layout.members) != 20 {
		t.Fatalf("members = %d", len(layout.members))
	}
	inner := 0
	for index, member := range layout.members {
		if member.ring == 0 {
			inner++
		}
		if index > 0 && member.ring < layout.members[index-1].ring {
			t.Fatalf("%s 互动更少却排得更靠里", member.node.UserID)
		}
		distance := math.Hypot(member.x-layout.centerX, member.y-layout.centerY)
		if math.Abs(distance-relationRings[member.ring].radius*layout.scale) > 0.5 {
			t.Fatalf("%s 不在第 %d 圈上", member.node.UserID, member.ring)
		}
	}
	if inner != 8 {
		t.Fatalf("内圈 %d 人，想要 8", inner)
	}
	if len(layout.bands) != 2 {
		t.Fatalf("用到两圈就只画两圈底盘，实际 %d", len(layout.bands))
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

// 人数上限来自插件设置，超出可画范围时收回来而不是照单全收。
func TestClampRelationSeats(t *testing.T) {
	cases := map[int]int{0: relationImageDefaultSeats, -5: relationImageDefaultSeats, 3: relationImageMinSeats, 99: relationImageMaxSeats, 20: 20}
	for input, want := range cases {
		if got := clampRelationSeats(input); got != want {
			t.Fatalf("clampRelationSeats(%d) = %d, want %d", input, got, want)
		}
	}
}

// 设置里的人数上限要真的少画几个人：互动最多的 Alice 留下。
func TestLayoutRelationCircleHonoursSeatLimit(t *testing.T) {
	layout := layoutRelationCircle(sampleRelationGraph(), 1)
	if len(layout.members) != 1 || layout.members[0].node.UserID != "1001" {
		t.Fatalf("members = %+v", layout.members)
	}
	if layout.center.UserID != "42" {
		t.Fatalf("center = %+v", layout.center)
	}
}
