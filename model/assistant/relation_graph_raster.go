// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math"
	"strings"

	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/font/sfnt"
	"golang.org/x/image/math/fixed"
	"golang.org/x/image/vector"
)

// 关系图的第二条出图路径：不经浏览器，直接在 Go 里栅格化。
//
// 第一条路径（HTML + 无头浏览器截图）上写着当初的理由：位图要自己做字体光栅化，
// 中文名字第一个就过不去。那条理由现在只剩一半——x/image/font 就是干光栅化的，
// 真正缺的是一个字型文件，而浏览器画得出中文本身就说明这台机器上有中文字体。
//
// 两条路径都留着，因为各有各的死法：这条要能找到中文字体，那条要装得起浏览器。
// 一台机器上通常至少满足一个，出图成功率比只押一条高得多。
//
// 形状是照着 renderGroupRelationSVG 一比一画的：同一份 relationSeats 布局、同一
// 组颜色。两边各画各的，视觉上要对得住，改了一边记得对齐另一边。
const (
	relationTitleFontSize    = 30
	relationSubtitleFontSize = 17
	relationSeatFontSize     = 19
	relationCenterFontSize   = 21
	// relationHeaderHeight 是标题区高度，和 HTML 版的 110px 对齐。
	relationHeaderHeight = 110
)

var (
	relationColorBackground = color.NRGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff}
	relationColorTitle      = color.NRGBA{R: 0x25, G: 0x1d, B: 0x23, A: 0xff}
	relationColorSubtitle   = color.NRGBA{R: 0x85, G: 0x79, B: 0x82, A: 0xff}
	relationColorPeerEdge   = color.NRGBA{R: 0x85, G: 0x79, B: 0x82, A: 0xff}
	relationColorCenterEdge = color.NRGBA{R: 0xe0, G: 0x57, B: 0x8f, A: 0xff}
	relationColorSeatFill   = color.NRGBA{R: 0xf4, G: 0xef, B: 0xf2, A: 0xff}
	relationColorSeatEdge   = color.NRGBA{R: 0xd9, G: 0xd2, B: 0xd7, A: 0xff}
	relationColorSeatLabel  = color.NRGBA{R: 0x57, G: 0x4d, B: 0x55, A: 0xff}
	relationColorCenter     = color.NRGBA{R: 0xe0, G: 0x57, B: 0x8f, A: 0xff}
	relationColorCenterText = color.NRGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff}
)

// RenderGroupRelationPNG 直接把关系图画成 PNG，不需要无头浏览器。
// 找不到能画中文的字体时返回错误，让调用方退回浏览器那条路。
func RenderGroupRelationPNG(graph GroupRelationGraph, title, rangeLabel string, maxSeats int) ([]byte, error) {
	sfntFont, _, err := LoadCJKFont()
	if err != nil {
		return nil, err
	}
	faces, err := newRelationFaces(sfntFont)
	if err != nil {
		return nil, err
	}
	defer faces.close()

	canvas := newRasterCanvas(relationImageWidth, relationImageHeight, relationColorBackground)
	canvas.text(strings.TrimSpace(title), 40, 66, faces.title, relationColorTitle, textAlignLeft)
	canvas.text(relationSubtitle(graph, rangeLabel), 40, 94, faces.subtitle, relationColorSubtitle, textAlignLeft)

	drawRelationRing(canvas, graph, faces, clampRelationSeats(maxSeats))

	var buffer bytes.Buffer
	if err := png.Encode(&buffer, canvas.img); err != nil {
		return nil, fmt.Errorf("编码关系图 PNG：%w", err)
	}
	return buffer.Bytes(), nil
}

// drawRelationRing 画环形图本体。层次和 SVG 版一致：成员之间的弦在最下面，
// 中心白环压住穿过圆心的弦，然后是辐条、成员节点，最后是中心节点。
func drawRelationRing(canvas *rasterCanvas, graph GroupRelationGraph, faces *relationFaces, maxSeats int) {
	const centerRadius = 44
	width := float64(relationImageWidth)
	height := float64(relationImageHeight - relationHeaderHeight)
	centerX := width / 2
	centerY := float64(relationHeaderHeight) + height/2
	ringRadius := math.Min(width, height)/2 - 140

	weights := relationWeightsToBot(graph)
	seats := relationSeats(graph, weights, centerX, centerY, ringRadius, maxSeats)
	if len(seats) == 0 {
		return
	}

	peers := relationPeerEdges(graph, seats)
	maxPeer := 1
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
		canvas.strokeQuad(edge.from.x, edge.from.y, controlX, controlY, edge.to.x, edge.to.y,
			1.0+2.0*ratio, withAlpha(relationColorPeerEdge, 0.16+0.24*ratio))
	}

	canvas.fillCircle(centerX, centerY, centerRadius+16, relationColorBackground)

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
		canvas.strokeLine(centerX, centerY, seat.x, seat.y,
			2.0+8.0*ratio, withAlpha(relationColorCenterEdge, 0.35+0.5*ratio))
	}

	for _, seat := range seats {
		// 描边靠「大圆填描边色、小圆填底色」叠出来：rasterizer 只会填充，
		// 画不了真正的 stroke。
		edge, edgeWidth := relationColorSeatEdge, 2.0
		if seat.weight > 0 {
			edge, edgeWidth = relationColorCenterEdge, 3.0
		}
		canvas.fillCircle(seat.x, seat.y, seat.radius+edgeWidth/2, edge)
		canvas.fillCircle(seat.x, seat.y, seat.radius-edgeWidth/2, relationColorSeatFill)
		canvas.text(seat.label, seat.labelX, seat.y+7, faces.seat, relationColorSeatLabel, textAlignFor(seat.anchor))
	}

	canvas.fillCircle(centerX, centerY, centerRadius, relationColorCenter)
	canvas.text(relationCenterLabel(graph), centerX, centerY+8, faces.center, relationColorCenterText, textAlignCenter)
}

// withAlpha 把 SVG 里的 stroke-opacity 换算成颜色自带的 alpha：栅格化这边没有
// 独立的不透明度通道，只能把它乘进颜色里。
func withAlpha(base color.NRGBA, opacity float64) color.NRGBA {
	opacity = math.Max(0, math.Min(1, opacity))
	base.A = uint8(math.Round(255 * opacity))
	return base
}

type relationFaces struct {
	title    font.Face
	subtitle font.Face
	seat     font.Face
	center   font.Face
}

func newRelationFaces(source *sfnt.Font) (*relationFaces, error) {
	sizes := []float64{relationTitleFontSize, relationSubtitleFontSize, relationSeatFontSize, relationCenterFontSize}
	made := make([]font.Face, 0, len(sizes))
	for _, size := range sizes {
		face, err := opentype.NewFace(source, &opentype.FaceOptions{Size: size, DPI: 72, Hinting: font.HintingFull})
		if err != nil {
			for _, open := range made {
				_ = open.Close()
			}
			return nil, fmt.Errorf("创建 %.0fpx 字体：%w", size, err)
		}
		made = append(made, face)
	}
	return &relationFaces{title: made[0], subtitle: made[1], seat: made[2], center: made[3]}, nil
}

func (f *relationFaces) close() {
	for _, face := range []font.Face{f.title, f.subtitle, f.seat, f.center} {
		if face != nil {
			_ = face.Close()
		}
	}
}

type textAlign int

const (
	textAlignLeft textAlign = iota
	textAlignCenter
	textAlignRight
)

func textAlignFor(anchor string) textAlign {
	switch strings.TrimSpace(anchor) {
	case "middle":
		return textAlignCenter
	case "end":
		return textAlignRight
	default:
		return textAlignLeft
	}
}

type rasterCanvas struct {
	img *image.RGBA
}

func newRasterCanvas(width, height int, background color.NRGBA) *rasterCanvas {
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	draw.Draw(img, img.Bounds(), image.NewUniform(background), image.Point{}, draw.Src)
	return &rasterCanvas{img: img}
}

// fillPath 把一条路径按覆盖率混到画布上。vector.Rasterizer 只做填充，所以下面
// 那些「描边」原语都是自己算出轮廓多边形再交给它。
func (c *rasterCanvas) fillPath(build func(*vector.Rasterizer), fill color.NRGBA) {
	if fill.A == 0 {
		return
	}
	bounds := c.img.Bounds()
	rasterizer := vector.NewRasterizer(bounds.Dx(), bounds.Dy())
	build(rasterizer)
	rasterizer.Draw(c.img, bounds, image.NewUniform(fill), image.Point{})
}

// circlePolygonSegments 是圆的折线段数。关系图上最大的圆是中心那个 60px 半径，
// 96 段的边长不到 4px，肉眼看不出棱角。
const circlePolygonSegments = 96

func (c *rasterCanvas) fillCircle(centerX, centerY, radius float64, fill color.NRGBA) {
	if radius <= 0 {
		return
	}
	c.fillPath(func(r *vector.Rasterizer) {
		appendCircle(r, centerX, centerY, radius)
	}, fill)
}

func appendCircle(r *vector.Rasterizer, centerX, centerY, radius float64) {
	r.MoveTo(float32(centerX+radius), float32(centerY))
	for step := 1; step <= circlePolygonSegments; step++ {
		angle := 2 * math.Pi * float64(step) / circlePolygonSegments
		r.LineTo(float32(centerX+radius*math.Cos(angle)), float32(centerY+radius*math.Sin(angle)))
	}
	r.ClosePath()
}

// strokeLine 画一条带圆头的粗线：中间一个矩形，两端各一个圆。
func (c *rasterCanvas) strokeLine(x1, y1, x2, y2, width float64, stroke color.NRGBA) {
	if width <= 0 {
		return
	}
	dx, dy := x2-x1, y2-y1
	length := math.Hypot(dx, dy)
	if length == 0 {
		c.fillCircle(x1, y1, width/2, stroke)
		return
	}
	// 法线方向偏移半个线宽，得到矩形的四个角。
	offsetX, offsetY := -dy/length*width/2, dx/length*width/2
	c.fillPath(func(r *vector.Rasterizer) {
		r.MoveTo(float32(x1+offsetX), float32(y1+offsetY))
		r.LineTo(float32(x2+offsetX), float32(y2+offsetY))
		r.LineTo(float32(x2-offsetX), float32(y2-offsetY))
		r.LineTo(float32(x1-offsetX), float32(y1-offsetY))
		r.ClosePath()
		appendCircle(r, x1, y1, width/2)
		appendCircle(r, x2, y2, width/2)
	}, stroke)
}

// quadSampleSegments 是二次贝塞尔采样成折线的段数。弦最长也就画面对角线的一半，
// 32 段够平滑了。
const quadSampleSegments = 32

// strokeQuad 画粗的二次贝塞尔曲线：采样成折线，逐段画粗线段。
//
// 一次 fillPath 画完整条曲线，而不是每段各画一次：曲线是半透明的，分段画的话
// 相邻段的重叠处会叠出更深的颜色，一条线看起来像串珠子。
func (c *rasterCanvas) strokeQuad(x1, y1, controlX, controlY, x2, y2, width float64, stroke color.NRGBA) {
	if width <= 0 {
		return
	}
	points := make([][2]float64, 0, quadSampleSegments+1)
	for step := 0; step <= quadSampleSegments; step++ {
		t := float64(step) / quadSampleSegments
		inverse := 1 - t
		x := inverse*inverse*x1 + 2*inverse*t*controlX + t*t*x2
		y := inverse*inverse*y1 + 2*inverse*t*controlY + t*t*y2
		points = append(points, [2]float64{x, y})
	}
	c.fillPath(func(r *vector.Rasterizer) {
		for index := 0; index+1 < len(points); index++ {
			from, to := points[index], points[index+1]
			dx, dy := to[0]-from[0], to[1]-from[1]
			length := math.Hypot(dx, dy)
			if length == 0 {
				continue
			}
			offsetX, offsetY := -dy/length*width/2, dx/length*width/2
			r.MoveTo(float32(from[0]+offsetX), float32(from[1]+offsetY))
			r.LineTo(float32(to[0]+offsetX), float32(to[1]+offsetY))
			r.LineTo(float32(to[0]-offsetX), float32(to[1]-offsetY))
			r.LineTo(float32(from[0]-offsetX), float32(from[1]-offsetY))
			r.ClosePath()
			// 关节处补个圆，否则折线的外侧转角会缺一个楔形口子。
			appendCircle(r, to[0], to[1], width/2)
		}
		appendCircle(r, points[0][0], points[0][1], width/2)
	}, stroke)
}

// text 按基线位置画一行字。x 的含义随对齐方式变：左对齐是起点，居中是中点，
// 右对齐是终点——和 SVG 的 text-anchor 一致。
func (c *rasterCanvas) text(value string, x, y float64, face font.Face, fill color.NRGBA, align textAlign) {
	value = strings.TrimSpace(value)
	if value == "" || face == nil {
		return
	}
	width := float64(font.MeasureString(face, value)) / 64
	switch align {
	case textAlignCenter:
		x -= width / 2
	case textAlignRight:
		x -= width
	}
	drawer := &font.Drawer{
		Dst:  c.img,
		Src:  image.NewUniform(fill),
		Face: face,
		Dot:  fixed.Point26_6{X: fixed.Int26_6(math.Round(x * 64)), Y: fixed.Int26_6(math.Round(y * 64))},
	}
	drawer.DrawString(value)
}
