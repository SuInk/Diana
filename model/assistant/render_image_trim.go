// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
)

// 截图的高度是命令行给死的窗口高度，不是内容高度。一张两行的表格照样出一整屏，
// 底下全是白的——发到群里就是一张平白很高的图。
//
// 与其去问浏览器内容多高（那得走 CDP，比现在这条「开一次、拍一张、退出」重得多），
// 不如拍完之后把底部纯背景色的部分裁掉。背景色是模板里自己定的，已知量。
const renderTrimPaddingPx = 24

// trimRenderScreenshot 裁掉底部和右侧的纯背景区域。
//
// 裁不动就原样返回：这一步是让图好看，不该因为它失败就让整张图发不出去。
func trimRenderScreenshot(raw []byte) []byte {
	decoded, err := png.Decode(bytes.NewReader(raw))
	if err != nil {
		return raw
	}
	bounds := decoded.Bounds()
	if bounds.Empty() {
		return raw
	}
	// 左上角必然是页面背景：模板给 .render-root 留了内边距，那一格一定是空白。
	background := decoded.At(bounds.Min.X, bounds.Min.Y)
	bottom := lastContentRow(decoded, bounds, background)
	right := lastContentColumn(decoded, bounds, background)
	if bottom < bounds.Min.Y || right < bounds.Min.X {
		// 整张都是背景色，说明这次渲染什么也没画出来。原样返回，让调用方
		// 拿到一张能看出「是空的」的图，而不是一张被裁成 0 像素的图。
		return raw
	}
	cropped := image.Rect(
		bounds.Min.X,
		bounds.Min.Y,
		min(right+1+renderTrimPaddingPx, bounds.Max.X),
		min(bottom+1+renderTrimPaddingPx, bounds.Max.Y),
	)
	if cropped == bounds {
		return raw
	}
	sub, ok := decoded.(interface {
		SubImage(r image.Rectangle) image.Image
	})
	if !ok {
		return raw
	}
	var buffer bytes.Buffer
	if err := png.Encode(&buffer, sub.SubImage(cropped)); err != nil {
		return raw
	}
	return buffer.Bytes()
}

func lastContentRow(img image.Image, bounds image.Rectangle, background color.Color) int {
	for y := bounds.Max.Y - 1; y >= bounds.Min.Y; y-- {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			if !sameRenderPixel(img.At(x, y), background) {
				return y
			}
		}
	}
	return bounds.Min.Y - 1
}

func lastContentColumn(img image.Image, bounds image.Rectangle, background color.Color) int {
	for x := bounds.Max.X - 1; x >= bounds.Min.X; x-- {
		for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
			if !sameRenderPixel(img.At(x, y), background) {
				return x
			}
		}
	}
	return bounds.Min.X - 1
}

func sameRenderPixel(a, b color.Color) bool {
	ar, ag, ab, aa := a.RGBA()
	br, bg, bb, ba := b.RGBA()
	return ar == br && ag == bg && ab == bb && aa == ba
}
