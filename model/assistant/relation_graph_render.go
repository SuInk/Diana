// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"fmt"
	"html"
	"math"
	"sort"
	"strings"
)

// 关系图的聊天版渲染。
//
// 这份布局和 WebUI 里那个 Vue 组件是两套实现，共用的是数据层（GroupRelationGraphFor）。
// 看起来像重复，但两边的约束不一样：网页版能悬停看明细、能缩放，所以敢画细线和小字；
// 发到群里的是一张定死的位图，没有悬停，字必须更大、连线必须更粗、还得自带标题说明
// 这是哪个群哪段时间。把网页那份直接截图会得到一张给鼠标看的图。
//
// 真正不能有两份的是「谁和谁算一次互动」，那一份在存储层，两边都从它取数。
const (
	relationImageWidth  = 1000
	relationImageHeight = 860
	// relationImageDefaultSeats 是没配置时画多少人。位图没有悬停也不能缩放，
	// 收得比网页版紧。
	relationImageDefaultSeats = 24
	relationImageMinSeats     = 6
	relationImageMaxSeats     = 40
)

// RenderGroupRelationHTML 把关系图渲染成一张自包含的 HTML，交给无头浏览器截图。
//
// 用 HTML+SVG 而不是在 Go 里直接画位图：位图要自己做字体光栅化，中文名字第一个
// 就过不去；SVG 的文字交给浏览器排版，中文、emoji、生僻字都不用操心。
func RenderGroupRelationHTML(graph GroupRelationGraph, title string, rangeLabel string, maxSeats int) string {
	var body strings.Builder
	body.WriteString(`<!doctype html><meta charset="utf-8"><style>
html,body{margin:0;padding:0;background:#ffffff}
.wrap{width:` + fmt.Sprint(relationImageWidth) + `px;height:` + fmt.Sprint(relationImageHeight) + `px;
  font-family:"Noto Sans CJK SC","Source Han Sans SC","Microsoft YaHei","PingFang SC",sans-serif;color:#251d23}
h1{font-size:30px;margin:32px 40px 4px}
.sub{font-size:17px;color:#857982;margin:0 40px 8px}
</style><div class="wrap">`)
	body.WriteString(`<h1>` + html.EscapeString(strings.TrimSpace(title)) + `</h1>`)
	body.WriteString(`<p class="sub">` + html.EscapeString(relationSubtitle(graph, rangeLabel)) + `</p>`)
	body.WriteString(renderGroupRelationSVG(graph, clampRelationSeats(maxSeats)))
	body.WriteString(`</div>`)
	return body.String()
}

func relationSubtitle(graph GroupRelationGraph, rangeLabel string) string {
	parts := []string{}
	if label := strings.TrimSpace(rangeLabel); label != "" {
		parts = append(parts, label)
	}
	parts = append(parts, fmt.Sprintf("%d 条发言 · %d 人发过言", graph.Messages, graph.Participants))
	parts = append(parts, "圆点大小是发言量，连线粗细是互动次数")
	return strings.Join(parts, " · ")
}

type relationSeat struct {
	node   GroupRelationNode
	x, y   float64
	radius float64
	weight int
	label  string
	anchor string
	labelX float64
}

// renderGroupRelationSVG 画环形图：机器人在圆心，成员按与它的互动次数从正上方
// 顺时针铺开。
func renderGroupRelationSVG(graph GroupRelationGraph, maxSeats int) string {
	const (
		width        = relationImageWidth
		height       = relationImageHeight - 110
		centerRadius = 44
	)
	centerX, centerY := float64(width)/2, float64(height)/2
	ringRadius := math.Min(width, height)/2 - 140

	weights := relationWeightsToBot(graph)
	seats := relationSeats(graph, weights, centerX, centerY, ringRadius, maxSeats)

	var svg strings.Builder
	fmt.Fprintf(&svg, `<svg width="%d" height="%d" viewBox="0 0 %d %d" xmlns="http://www.w3.org/2000/svg">`, width, height, width, height)

	// 成员之间的弦先画，压在中心边下面。
	maxPeer := 1
	peers := relationPeerEdges(graph, seats)
	for _, edge := range peers {
		if edge.weight > maxPeer {
			maxPeer = edge.weight
		}
	}
	for _, edge := range peers {
		ratio := float64(edge.weight) / float64(maxPeer)
		midX, midY := (edge.from.x+edge.to.x)/2, (edge.from.y+edge.to.y)/2
		controlX := midX + (centerX-midX)*0.55
		controlY := midY + (centerY-midY)*0.55
		fmt.Fprintf(&svg, `<path d="M %.1f %.1f Q %.1f %.1f %.1f %.1f" fill="none" stroke="#857982" stroke-width="%.2f" stroke-opacity="%.2f" stroke-linecap="round"/>`,
			edge.from.x, edge.from.y, controlX, controlY, edge.to.x, edge.to.y, 1.0+2.0*ratio, 0.16+0.24*ratio)
	}

	// 中心光环：接近对径的弦中点落在圆心，会从中心节点上划过去。
	fmt.Fprintf(&svg, `<circle cx="%.1f" cy="%.1f" r="%d" fill="#ffffff"/>`, centerX, centerY, centerRadius+16)

	maxCenter := 1
	for _, seat := range seats {
		if seat.weight > maxCenter {
			maxCenter = seat.weight
		}
	}
	for _, seat := range seats {
		if seat.weight <= 0 {
			continue
		}
		ratio := float64(seat.weight) / float64(maxCenter)
		fmt.Fprintf(&svg, `<line x1="%.1f" y1="%.1f" x2="%.1f" y2="%.1f" stroke="#e0578f" stroke-width="%.2f" stroke-opacity="%.2f" stroke-linecap="round"/>`,
			centerX, centerY, seat.x, seat.y, 2.0+8.0*ratio, 0.35+0.5*ratio)
	}

	for _, seat := range seats {
		stroke := "#d9d2d7"
		strokeWidth := "2"
		if seat.weight > 0 {
			stroke = "#e0578f"
			strokeWidth = "3"
		}
		fmt.Fprintf(&svg, `<circle cx="%.1f" cy="%.1f" r="%.1f" fill="#f4eff2" stroke="%s" stroke-width="%s"/>`,
			seat.x, seat.y, seat.radius, stroke, strokeWidth)
		fmt.Fprintf(&svg, `<text x="%.1f" y="%.1f" font-size="19" fill="#574d55" text-anchor="%s">%s</text>`,
			seat.labelX, seat.y+7, seat.anchor, html.EscapeString(seat.label))
	}

	fmt.Fprintf(&svg, `<circle cx="%.1f" cy="%.1f" r="%d" fill="#e0578f"/>`, centerX, centerY, centerRadius)
	fmt.Fprintf(&svg, `<text x="%.1f" y="%.1f" font-size="21" font-weight="600" fill="#ffffff" text-anchor="middle">%s</text>`,
		centerX, centerY+8, html.EscapeString(relationCenterLabel(graph)))
	svg.WriteString(`</svg>`)
	return svg.String()
}

func relationCenterLabel(graph GroupRelationGraph) string {
	for _, node := range graph.Nodes {
		if node.IsBot {
			if name := strings.TrimSpace(node.DisplayName); name != "" {
				return truncateRelationLabel(name, 6)
			}
		}
	}
	return "Diana"
}

func relationWeightsToBot(graph GroupRelationGraph) map[string]int {
	weights := map[string]int{}
	botID := strings.TrimSpace(graph.BotID)
	if botID == "" {
		return weights
	}
	for _, edge := range graph.Edges {
		switch botID {
		case edge.Source:
			weights[edge.Target] = edge.Weight
		case edge.Target:
			weights[edge.Source] = edge.Weight
		}
	}
	return weights
}

func relationSeats(graph GroupRelationGraph, weights map[string]int, centerX, centerY, ringRadius float64, maxSeats int) []relationSeat {
	members := make([]GroupRelationNode, 0, len(graph.Nodes))
	for _, node := range graph.Nodes {
		if !node.IsBot {
			members = append(members, node)
		}
	}
	sort.SliceStable(members, func(left, right int) bool {
		if weights[members[left].UserID] != weights[members[right].UserID] {
			return weights[members[left].UserID] > weights[members[right].UserID]
		}
		return members[left].Messages > members[right].Messages
	})
	// 位图没有悬停也不能缩放，人太多就只剩一圈看不清的小字，比网页版收得更紧。
	if len(members) > maxSeats {
		members = members[:maxSeats]
	}
	if len(members) == 0 {
		return nil
	}
	maxMessages := 1
	for _, node := range members {
		if node.Messages > maxMessages {
			maxMessages = node.Messages
		}
	}
	seats := make([]relationSeat, 0, len(members))
	for index, node := range members {
		angle := -math.Pi/2 + float64(index)*2*math.Pi/float64(len(members))
		x := centerX + ringRadius*math.Cos(angle)
		y := centerY + ringRadius*math.Sin(angle)
		// 面积随发言量走，所以半径开方；线性缩放会把话痨画得夸张。
		radius := 9 + 20*math.Sqrt(float64(node.Messages)/float64(maxMessages))
		anchor, labelX := "start", x+radius+10
		if math.Cos(angle) < 0 {
			anchor, labelX = "end", x-radius-10
		}
		name := strings.TrimSpace(node.DisplayName)
		if name == "" {
			name = node.UserID
		}
		seats = append(seats, relationSeat{
			node: node, x: x, y: y, radius: radius,
			weight: weights[node.UserID], label: truncateRelationLabel(name, 8),
			anchor: anchor, labelX: labelX,
		})
	}
	return seats
}

type relationPeerEdge struct {
	from, to relationSeat
	weight   int
}

// relationPeerEdges 只保留最强的一批成员间连线。全画出来是一团毛线，中心那圈
// 主线反而看不见——这张图的主语是机器人。
func relationPeerEdges(graph GroupRelationGraph, seats []relationSeat) []relationPeerEdge {
	const maxPeerEdges = 12
	positions := make(map[string]relationSeat, len(seats))
	for _, seat := range seats {
		positions[seat.node.UserID] = seat
	}
	botID := strings.TrimSpace(graph.BotID)
	edges := make([]relationPeerEdge, 0, maxPeerEdges)
	for _, edge := range graph.Edges {
		if edge.Source == botID || edge.Target == botID {
			continue
		}
		from, okFrom := positions[edge.Source]
		to, okTo := positions[edge.Target]
		if !okFrom || !okTo {
			continue
		}
		edges = append(edges, relationPeerEdge{from: from, to: to, weight: edge.Weight})
		if len(edges) >= maxPeerEdges {
			break
		}
	}
	return edges
}

func truncateRelationLabel(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit]) + "…"
}

// clampRelationSeats 把配置里的人数上限收进可画的范围。配置层已经限过一遍，
// 这里再收一次是因为渲染也可以被直接调用。
func clampRelationSeats(value int) int {
	if value <= 0 {
		return relationImageDefaultSeats
	}
	if value < relationImageMinSeats {
		return relationImageMinSeats
	}
	if value > relationImageMaxSeats {
		return relationImageMaxSeats
	}
	return value
}
