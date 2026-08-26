// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"bytes"
	"image"
	"image/png"
	"testing"
)

func relationTestGraph() GroupRelationGraph {
	names := []string{"查无此人", "枞と哀傷", "Winter", "笨笨喵"}
	graph := GroupRelationGraph{
		GroupID: "1049765710", BotID: "bot", Messages: 5800, Participants: len(names) + 1,
		Nodes: []GroupRelationNode{{UserID: "bot", DisplayName: "Diana", IsBot: true, Messages: 900}},
	}
	for index, name := range names {
		graph.Nodes = append(graph.Nodes, GroupRelationNode{UserID: name, DisplayName: name, Messages: 900 - index*90})
		graph.Edges = append(graph.Edges, GroupRelationEdge{Source: "bot", Target: name, Weight: 80 - index*8})
		if index > 0 {
			graph.Edges = append(graph.Edges, GroupRelationEdge{Source: names[index-1], Target: name, Weight: 30 - index*3})
		}
	}
	return graph
}

// TestRenderGroupRelationPNGWithoutBrowser 关系图不经浏览器也能出图。
//
// 这条路是为了绕开无头浏览器：一台机器上装不装得起 Chrome，和有没有中文字体，
// 是两件独立的事，两条路都能出图，才不至于缺一个环境件就彻底没图。
func TestRenderGroupRelationPNGWithoutBrowser(t *testing.T) {
	if _, _, err := LoadCJKFont(); err != nil {
		t.Skipf("这台机器上没有能画中文的字体：%v", err)
	}
	raw, err := RenderGroupRelationPNG(relationTestGraph(), "群 1049765710 · 关系图", "近 7 天", relationImageDefaultSeats)
	if err != nil {
		t.Fatalf("RenderGroupRelationPNG() error = %v", err)
	}
	img, err := png.Decode(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("产出的不是合法 PNG：%v", err)
	}
	bounds := img.Bounds()
	if bounds.Dx() != relationImageWidth || bounds.Dy() != relationImageHeight {
		t.Fatalf("尺寸 %dx%d，想要 %dx%d", bounds.Dx(), bounds.Dy(), relationImageWidth, relationImageHeight)
	}

	// 不能只验「是张图」：全白的空画布同样是合法 PNG，也同样能通过尺寸检查。
	// 中心节点那一坨粉色必须真的落在画布上。
	centerX, centerY := bounds.Dx()/2, relationHeaderHeight+(bounds.Dy()-relationHeaderHeight)/2
	r, g, b, _ := img.At(centerX, centerY).RGBA()
	if r>>8 != 0xe0 || g>>8 != 0x57 || b>>8 != 0x8f {
		t.Fatalf("中心节点没画上：中心像素是 #%02x%02x%02x", r>>8, g>>8, b>>8)
	}
	if painted := paintedPixelRatio(img); painted < 0.02 {
		t.Fatalf("画布几乎是空的：非背景像素只占 %.3f%%", painted*100)
	}
}

// TestRenderGroupRelationPNGDrawsCJKGlyphs 盯住中文真的被画出来了。
//
// 这条路当初被否掉的理由就是「中文名字第一个就过不去」：字体挑错、.notdef 顶上，
// 出来的是一排豆腐块——那看起来像成功了，比报错更难发现。标题区只有文字，没有
// 图形，正好拿来验墨。
func TestRenderGroupRelationPNGDrawsCJKGlyphs(t *testing.T) {
	if _, _, err := LoadCJKFont(); err != nil {
		t.Skipf("这台机器上没有能画中文的字体：%v", err)
	}
	withTitle, err := RenderGroupRelationPNG(relationTestGraph(), "关系图标题", "近 7 天", relationImageDefaultSeats)
	if err != nil {
		t.Fatal(err)
	}
	blank, err := RenderGroupRelationPNG(relationTestGraph(), "", "", relationImageDefaultSeats)
	if err != nil {
		t.Fatal(err)
	}
	titled, plain := decodePNG(t, withTitle), decodePNG(t, blank)
	if inkInHeader(titled) <= inkInHeader(plain) {
		t.Fatal("标题区没有落墨：中文标题没被画出来")
	}
}

func decodePNG(t *testing.T, raw []byte) image.Image {
	t.Helper()
	img, err := png.Decode(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("解码 PNG：%v", err)
	}
	return img
}

// inkInHeader 数标题区里有多少非背景像素。
func inkInHeader(img image.Image) int {
	bounds := img.Bounds()
	count := 0
	for y := bounds.Min.Y; y < bounds.Min.Y+relationHeaderHeight; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			if !isBackgroundPixel(img, x, y) {
				count++
			}
		}
	}
	return count
}

func paintedPixelRatio(img image.Image) float64 {
	bounds := img.Bounds()
	count := 0
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			if !isBackgroundPixel(img, x, y) {
				count++
			}
		}
	}
	return float64(count) / float64(bounds.Dx()*bounds.Dy())
}

func isBackgroundPixel(img image.Image, x, y int) bool {
	r, g, b, _ := img.At(x, y).RGBA()
	return r>>8 == 0xff && g>>8 == 0xff && b>>8 == 0xff
}

// TestCJKFontRejectsFontsWithoutGlyphs 挑字体必须验货。文件名像中文字体不代表
// 真有中文字形：发行版里 .ttc 的点阵分卷、补全字体都能骗过文件名匹配。
func TestCJKFontRejectsFontsWithoutGlyphs(t *testing.T) {
	if fontHasCJKGlyphs(nil) {
		t.Fatal("空字体被判成能画中文")
	}
	font, path, err := LoadCJKFont()
	if err != nil {
		t.Skipf("这台机器上没有能画中文的字体：%v", err)
	}
	if !fontHasCJKGlyphs(font) {
		t.Fatalf("挑中的字体 %s 画不了中文", path)
	}
}
