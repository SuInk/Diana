// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"html"
	"image"
	"image/color"
	"image/png"
	"math"
	"sort"
	"strings"
)

// 关系图的聊天版渲染：照 WebUI 的 GroupRelationChart.vue 画「互动圈」——中心是
// 机器人，成员按和它的互动次数从里往外排进三圈，越靠里越亲近，只画头像不写名字。
//
// 网页版靠悬停看名字和连线，位图没有悬停，所以这两样都不画；圈层、头像大小和配色
// 跟网页版一致，改了一边记得对齐另一边。
//
// 真正不能有两份的是「谁和谁算一次互动」，那一份在存储层，两边都从它取数。
const (
	relationImageWidth  = 1000
	relationImageHeight = 860
	// relationImageDefaultSeats 是没配置时画多少人。
	relationImageDefaultSeats = 24
	relationImageMinSeats     = 6
	relationImageMaxSeats     = 40
)

// relationRings 从里到外三圈，数值照抄网页版：圈越外半径越大、头像越小。
var relationRings = []struct {
	radius, node float64
	capacity     int
}{
	{98, 27, 8}, // 内圈只留最亲近的几个人
	{166, 20, 0},
	{228, 15, 0},
}

// relationRingFillPercent 是有来往的人在各圈的底色深浅，和网页版的 color-mix 一致。
var relationRingFillPercent = []float64{100, 62, 34}

func relationRingCapacity(ring int) int {
	spec := relationRings[ring]
	capacity := int(math.Floor(2 * math.Pi * spec.radius / (spec.node*2 + 6)))
	if spec.capacity > 0 && spec.capacity < capacity {
		capacity = spec.capacity
	}
	return capacity
}

type relationMember struct {
	node   GroupRelationNode
	x, y   float64
	radius float64
	ring   int
	weight int
	fill   color.NRGBA
}

type relationBand struct {
	radius  float64
	opacity float64
}

// relationCircle 是一张互动圈的完整布局，HTML 和位图两条路都照它画。
type relationCircle struct {
	scale                          float64
	centerX, centerY, centerRadius float64
	center                         GroupRelationNode
	// bands 由外到内，先画外圈底盘。
	bands   []relationBand
	members []relationMember
}

func layoutRelationCircle(graph GroupRelationGraph, maxSeats int) relationCircle {
	area := float64(relationImageHeight - relationHeaderHeight)
	layout := relationCircle{
		centerX: float64(relationImageWidth) / 2,
		centerY: float64(relationHeaderHeight) + area/2,
		center:  relationCenterNode(graph),
	}
	weights := relationWeightsTo(graph, layout.center.UserID)
	members := make([]GroupRelationNode, 0, len(graph.Nodes))
	for _, node := range graph.Nodes {
		if node.UserID != layout.center.UserID && !(layout.center.UserID == "" && node.IsBot) {
			members = append(members, node)
		}
	}
	sort.SliceStable(members, func(left, right int) bool {
		if weights[members[left].UserID] != weights[members[right].UserID] {
			return weights[members[left].UserID] > weights[members[right].UserID]
		}
		return members[left].Messages > members[right].Messages
	})
	if len(members) > maxSeats {
		members = members[:maxSeats]
	}
	var slices [][]GroupRelationNode
	for ring, cursor := 0, 0; ring < len(relationRings) && cursor < len(members); ring++ {
		end := min(len(members), cursor+relationRingCapacity(ring))
		slices = append(slices, members[cursor:end])
		cursor = end
	}
	// 网页版能缩放，位图不能：按实际用到的最外圈把整个圈放大到占满画面，人少时
	// 也不至于缩在中间一小团，但最多放大两倍，免得一两个人的头像大得离谱。
	layout.scale = 2
	if deepest := len(slices) - 1; deepest >= 0 {
		spec := relationRings[deepest]
		layout.scale = math.Min(2, (area/2-16)/(spec.radius+spec.node+8))
	}
	scale := layout.scale
	layout.centerRadius = 46 * scale
	for ring, slice := range slices {
		spec := relationRings[ring]
		// 每圈错开半格起点，相邻两圈的头像不会正好排成一条射线。
		offset := 0.0
		if ring%2 == 1 {
			offset = math.Pi / float64(len(slice))
		}
		// 人少的圈头像放大到把一圈铺满，最多放大到 1.4 倍。
		nodeRadius := math.Min(spec.node*1.4, math.Pi*spec.radius/float64(len(slice))-3)
		for index, node := range slice {
			angle := -math.Pi/2 + offset + float64(index)*2*math.Pi/float64(len(slice))
			weight := weights[node.UserID]
			fill := relationColorSeatFill
			if weight > 0 {
				fill = mixRelationColor(relationColorCenter, relationColorSeatFill, relationRingFillPercent[ring]/100)
			}
			layout.members = append(layout.members, relationMember{
				node:   node,
				x:      layout.centerX + spec.radius*scale*math.Cos(angle),
				y:      layout.centerY + spec.radius*scale*math.Sin(angle),
				radius: nodeRadius * scale,
				ring:   ring,
				weight: weight,
				fill:   fill,
			})
		}
	}
	for index := len(slices) - 1; index >= 0; index-- {
		spec := relationRings[index]
		layout.bands = append(layout.bands, relationBand{
			radius:  (spec.radius + spec.node + 8) * scale,
			opacity: 0.35 + 0.2*float64(len(slices)-1-index),
		})
	}
	return layout
}

// relationAvatarIDs 是这张图上要画头像的人：中心和排进圈里的成员。
func relationAvatarIDs(graph GroupRelationGraph, maxSeats int) []string {
	layout := layoutRelationCircle(graph, clampRelationSeats(maxSeats))
	ids := make([]string, 0, len(layout.members)+1)
	if layout.center.UserID != "" {
		ids = append(ids, layout.center.UserID)
	}
	for _, member := range layout.members {
		ids = append(ids, member.node.UserID)
	}
	return ids
}

// RenderGroupRelationHTML 把关系图渲染成一张自包含的 HTML，交给无头浏览器截图。
// avatars 是解码好的头像，按账号索引；没有的人画成纯色圆点。
func RenderGroupRelationHTML(graph GroupRelationGraph, title string, rangeLabel string, maxSeats int, avatars map[string]image.Image) string {
	var body strings.Builder
	body.WriteString(`<!doctype html><meta charset="utf-8"><style>
html,body{margin:0;padding:0;background:#ffffff}
.wrap{position:relative;display:flow-root;width:` + fmt.Sprint(relationImageWidth) + `px;height:` + fmt.Sprint(relationImageHeight) + `px;
  font-family:"Noto Sans CJK SC","Source Han Sans SC","Microsoft YaHei","PingFang SC",sans-serif;color:#251d23}
h1{font-size:30px;margin:32px 40px 4px}
.sub{font-size:17px;color:#857982;margin:0 40px 8px}
svg{position:absolute;left:0;top:0}
</style><div class="wrap">`)
	body.WriteString(`<h1>` + html.EscapeString(strings.TrimSpace(title)) + `</h1>`)
	body.WriteString(`<p class="sub">` + html.EscapeString(relationSubtitle(graph, rangeLabel)) + `</p>`)
	body.WriteString(renderGroupRelationSVG(graph, clampRelationSeats(maxSeats), avatars))
	body.WriteString(`</div>`)
	return body.String()
}

func relationSubtitle(graph GroupRelationGraph, rangeLabel string) string {
	parts := []string{}
	if label := strings.TrimSpace(rangeLabel); label != "" {
		parts = append(parts, label)
	}
	parts = append(parts, fmt.Sprintf("%d 条发言 · %d 人发过言", graph.Messages, graph.Participants))
	parts = append(parts, "越靠里互动越多")
	return strings.Join(parts, " · ")
}

// renderGroupRelationSVG 画整张画布大小的互动圈，标题区留空给 HTML 的标题。
func renderGroupRelationSVG(graph GroupRelationGraph, maxSeats int, avatars map[string]image.Image) string {
	layout := layoutRelationCircle(graph, maxSeats)
	var svg strings.Builder
	fmt.Fprintf(&svg, `<svg width="%d" height="%d" viewBox="0 0 %d %d" xmlns="http://www.w3.org/2000/svg">`,
		relationImageWidth, relationImageHeight, relationImageWidth, relationImageHeight)
	band := relationBandColor()
	for _, ring := range layout.bands {
		fmt.Fprintf(&svg, `<circle cx="%.1f" cy="%.1f" r="%.1f" fill="%s" fill-opacity="%.2f"/>`,
			layout.centerX, layout.centerY, ring.radius, hexRelationColor(band), ring.opacity)
	}
	stroke := 2.5 * layout.scale
	for _, member := range layout.members {
		fmt.Fprintf(&svg, `<circle cx="%.1f" cy="%.1f" r="%.1f" fill="%s"/>`, member.x, member.y, member.radius, hexRelationColor(member.fill))
		svg.WriteString(relationAvatarSVG(avatars[member.node.UserID], member.x, member.y, member.radius))
		fmt.Fprintf(&svg, `<circle cx="%.1f" cy="%.1f" r="%.1f" fill="none" stroke="#ffffff" stroke-width="%.1f"/>`, member.x, member.y, member.radius, stroke)
	}
	fmt.Fprintf(&svg, `<circle cx="%.1f" cy="%.1f" r="%.1f" fill="%s"/>`, layout.centerX, layout.centerY, layout.centerRadius, hexRelationColor(relationColorCenter))
	if avatar := avatars[layout.center.UserID]; avatar != nil {
		svg.WriteString(relationAvatarSVG(avatar, layout.centerX, layout.centerY, layout.centerRadius))
	} else {
		fmt.Fprintf(&svg, `<text x="%.1f" y="%.1f" font-size="%.0f" font-weight="600" fill="#ffffff" text-anchor="middle">%s</text>`,
			layout.centerX, layout.centerY+6*layout.scale, 15*layout.scale, html.EscapeString(relationCenterLabel(layout.center)))
	}
	fmt.Fprintf(&svg, `<circle cx="%.1f" cy="%.1f" r="%.1f" fill="none" stroke="#ffffff" stroke-width="%.1f"/>`,
		layout.centerX, layout.centerY, layout.centerRadius, 4*layout.scale)
	svg.WriteString(`</svg>`)
	return svg.String()
}

// relationAvatarSVG 把头像裁成圆、内联成 data URI，页面不发任何外部请求。
func relationAvatarSVG(avatar image.Image, x, y, radius float64) string {
	if avatar == nil {
		return ""
	}
	var buffer bytes.Buffer
	if err := png.Encode(&buffer, circleAvatar(avatar, int(math.Round(radius*2)))); err != nil {
		return ""
	}
	return fmt.Sprintf(`<image href="data:image/png;base64,%s" x="%.1f" y="%.1f" width="%.1f" height="%.1f"/>`,
		base64.StdEncoding.EncodeToString(buffer.Bytes()), x-radius, y-radius, radius*2, radius*2)
}

// relationCenterNode 找中心：默认是机器人，WebUI 换过中心时以 center_id 为准。
func relationCenterNode(graph GroupRelationGraph) GroupRelationNode {
	id := firstNonEmpty(strings.TrimSpace(graph.CenterID), strings.TrimSpace(graph.BotID))
	for _, node := range graph.Nodes {
		if id != "" && node.UserID == id {
			return node
		}
	}
	for _, node := range graph.Nodes {
		if node.IsBot {
			return node
		}
	}
	return GroupRelationNode{UserID: id, IsBot: true}
}

// relationCenterLabel 是中心没有头像时圆里写的字，和网页版一样只留四个字。
func relationCenterLabel(center GroupRelationNode) string {
	if name := strings.TrimSpace(center.DisplayName); name != "" {
		runes := []rune(name)
		return string(runes[:min(4, len(runes))])
	}
	return "Diana"
}

func relationWeightsTo(graph GroupRelationGraph, centerID string) map[string]int {
	weights := map[string]int{}
	centerID = strings.TrimSpace(centerID)
	if centerID == "" {
		return weights
	}
	for _, edge := range graph.Edges {
		switch centerID {
		case edge.Source:
			weights[edge.Target] = edge.Weight
		case edge.Target:
			weights[edge.Source] = edge.Weight
		}
	}
	return weights
}

// mixRelationColor 对应 CSS 的 color-mix(in srgb, a p%, b)。
func mixRelationColor(a, b color.NRGBA, p float64) color.NRGBA {
	mix := func(x, y uint8) uint8 { return uint8(math.Round(float64(x)*p + float64(y)*(1-p))) }
	return color.NRGBA{R: mix(a.R, b.R), G: mix(a.G, b.G), B: mix(a.B, b.B), A: 0xff}
}

// relationBandColor 是圈层底盘色：网页版的 color-mix(accent 18%, surface)。
func relationBandColor() color.NRGBA {
	return mixRelationColor(relationColorCenter, relationColorBackground, 0.18)
}

func hexRelationColor(c color.NRGBA) string {
	return fmt.Sprintf("#%02x%02x%02x", c.R, c.G, c.B)
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
