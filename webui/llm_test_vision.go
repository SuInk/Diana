// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"sync"

	"github.com/SuInk/diana/model/llm"
)

// 视觉测试附的图：白底红色实心圆。答得出「红色」「圆」才说明图真的进了模型，
// 只发文字的话，不认图的通道也会回一句话，看起来照样「测试通过」。
var visionProbeImage = sync.OnceValue(func() string {
	const size = 128
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	red := color.RGBA{R: 220, G: 30, B: 30, A: 255}
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			dx, dy := x-size/2, y-size/2
			if dx*dx+dy*dy <= 40*40 {
				img.Set(x, y, red)
			} else {
				img.Set(x, y, color.White)
			}
		}
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(buf.Bytes())
})

func visionProbeMessage(text string) llm.Message {
	return llm.Message{
		Role:    llm.RoleUser,
		Content: text,
		Parts: []llm.ContentPart{
			{Type: llm.ContentPartText, Text: text},
			{Type: llm.ContentPartImageURL, ImageURL: visionProbeImage()},
		},
	}
}
