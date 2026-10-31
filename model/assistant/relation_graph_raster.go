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
// 形状照着 renderGroupRelationSVG 画：同一份 layoutRelationCircle 布局、同一组颜色。
const (
	relationTitleFontSize    = 30
	relationSubtitleFontSize = 17
	relationCenterFontSize   = 18
	// relationHeaderHeight 是标题区高度，和 HTML 版的 110px 对齐。
	relationHeaderHeight = 110
)

var (
	relationColorBackground = color.NRGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff}
	relationColorTitle      = color.NRGBA{R: 0x25, G: 0x1d, B: 0x23, A: 0xff}
	relationColorSubtitle   = color.NRGBA{R: 0x85, G: 0x79, B: 0x82, A: 0xff}
	relationColorSeatFill   = color.NRGBA{R: 0xf4, G: 0xef, B: 0xf2, A: 0xff}
	relationColorCenter     = color.NRGBA{R: 0xe0, G: 0x57, B: 0x8f, A: 0xff}
	relationColorCenterText = color.NRGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff}
)

// RenderGroupRelationPNG 直接把关系图画成 PNG，不需要无头浏览器。avatars 是解码好
// 的头像，按账号索引。找不到能画中文的字体时返回错误，让调用方退回浏览器那条路。
func RenderGroupRelationPNG(graph GroupRelationGraph, title, rangeLabel string, maxSeats int, avatars map[string]image.Image) ([]byte, error) {
	sfntFont, _, err := LoadCJKFont()
	if err != nil {
		return nil, err
	}
	layout := layoutRelationCircle(graph, clampRelationSeats(maxSeats))
	texts := []string{title, relationSubtitle(graph, rangeLabel)}
	if avatars[layout.center.UserID] == nil {
		texts = append(texts, relationCenterLabel(layout.center))
	}
	if relationTextNeedsBrowser(sfntFont, strings.Join(texts, " ")) {
		return nil, fmt.Errorf("关系图包含需要多字体或复杂排版的文字，使用浏览器渲染")
	}
	faces, err := newRelationFaces(sfntFont, layout.scale)
	if err != nil {
		return nil, err
	}
	defer faces.close()

	canvas := newRasterCanvas(relationImageWidth, relationImageHeight, relationColorBackground)
	canvas.text(strings.TrimSpace(title), 40, 66, faces.title, relationColorTitle, textAlignLeft)
	canvas.text(relationSubtitle(graph, rangeLabel), 40, 94, faces.subtitle, relationColorSubtitle, textAlignLeft)

	drawRelationCircle(canvas, layout, faces, avatars)

	var buffer bytes.Buffer
	if err := png.Encode(&buffer, canvas.img); err != nil {
		return nil, fmt.Errorf("编码关系图 PNG：%w", err)
	}
	return buffer.Bytes(), nil
}

// drawRelationCircle 画互动圈本体：由外到内的底盘，成员头像，最后是中心。
func drawRelationCircle(canvas *rasterCanvas, layout relationCircle, faces *relationFaces, avatars map[string]image.Image) {
	band := relationBandColor()
	for _, ring := range layout.bands {
		canvas.fillCircle(layout.centerX, layout.centerY, ring.radius, withAlpha(band, ring.opacity))
	}
	white := relationColorBackground
	for _, member := range layout.members {
		canvas.fillCircle(member.x, member.y, member.radius, member.fill)
		canvas.drawAvatar(avatars[member.node.UserID], member.x, member.y, member.radius)
		canvas.strokeCircle(member.x, member.y, member.radius, 2.5*layout.scale, white)
	}
	canvas.fillCircle(layout.centerX, layout.centerY, layout.centerRadius, relationColorCenter)
	if avatar := avatars[layout.center.UserID]; avatar != nil {
		canvas.drawAvatar(avatar, layout.centerX, layout.centerY, layout.centerRadius)
	} else {
		canvas.text(relationCenterLabel(layout.center), layout.centerX, layout.centerY+6*layout.scale, faces.center, relationColorCenterText, textAlignCenter)
	}
	canvas.strokeCircle(layout.centerX, layout.centerY, layout.centerRadius, 4*layout.scale, white)
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
	center   font.Face
}

func newRelationFaces(source *sfnt.Font, scale float64) (*relationFaces, error) {
	sizes := []float64{relationTitleFontSize, relationSubtitleFontSize, math.Round(relationCenterFontSize * scale)}
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
	return &relationFaces{title: made[0], subtitle: made[1], center: made[2]}, nil
}

func (f *relationFaces) close() {
	for _, face := range []font.Face{f.title, f.subtitle, f.center} {
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

// strokeCircle 画圆环：外圆正向、内圆反向，叠在一起只剩环。
func (c *rasterCanvas) strokeCircle(centerX, centerY, radius, width float64, stroke color.NRGBA) {
	if width <= 0 || radius <= 0 {
		return
	}
	c.fillPath(func(r *vector.Rasterizer) {
		appendCircle(r, centerX, centerY, radius+width/2)
		inner := math.Max(0, radius-width/2)
		r.MoveTo(float32(centerX+inner), float32(centerY))
		for step := circlePolygonSegments - 1; step >= 0; step-- {
			angle := 2 * math.Pi * float64(step) / circlePolygonSegments
			r.LineTo(float32(centerX+inner*math.Cos(angle)), float32(centerY+inner*math.Sin(angle)))
		}
		r.ClosePath()
	}, stroke)
}

// drawAvatar 把头像裁成圆贴到以 (centerX, centerY) 为圆心、radius 为半径的位置。
func (c *rasterCanvas) drawAvatar(avatar image.Image, centerX, centerY, radius float64) {
	if avatar == nil {
		return
	}
	size := int(math.Round(radius * 2))
	avatar = circleAvatar(avatar, size)
	origin := image.Pt(int(math.Round(centerX-radius)), int(math.Round(centerY-radius)))
	draw.Draw(c.img, image.Rectangle{Min: origin, Max: origin.Add(image.Pt(size, size))}, avatar, avatar.Bounds().Min, draw.Over)
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
