// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/llm"
)

func testImageDataURL(t *testing.T, width, height int) string {
	t.Helper()
	canvas := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			// 有花纹才压得出体积差，纯色会被压成几百字节看不出效果。
			canvas.Set(x, y, color.RGBA{R: uint8(x * 7 % 256), G: uint8(y * 13 % 256), B: uint8((x + y) * 3 % 256), A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, canvas); err != nil {
		t.Fatal(err)
	}
	return imageBytesAsDataURL(buf.Bytes(), "image/png")
}

// 大图按长边缩到目标尺寸，比例保持，字节确实变小。
func TestShrinkDataURLImageActuallyShrinks(t *testing.T) {
	original := testImageDataURL(t, 1600, 900)
	shrunk, ok := shrinkDataURLImageLongSide(original, budgetLowDetailLongSide)
	if !ok {
		t.Fatal("大图没有被缩小")
	}
	decoded, err := decodeDataURLImage(shrunk)
	if err != nil {
		t.Fatal(err)
	}
	bounds := decoded.Bounds()
	if bounds.Dx() != budgetLowDetailLongSide {
		t.Fatalf("长边 = %d，应当是 %d", bounds.Dx(), budgetLowDetailLongSide)
	}
	if bounds.Dy() != 900*budgetLowDetailLongSide/1600 {
		t.Fatalf("比例没保持：%dx%d", bounds.Dx(), bounds.Dy())
	}
}

// 已经比目标小的不动，也不谎称缩过。
func TestShrinkDataURLImageLeavesSmallImages(t *testing.T) {
	original := testImageDataURL(t, 200, 120)
	shrunk, ok := shrinkDataURLImageLongSide(original, budgetLowDetailLongSide)
	if ok || shrunk != original {
		t.Fatal("小图不该被改动")
	}
	// 不是 data URI 的一律改不动。
	if _, ok := shrinkDataURLImageLongSide("https://example.invalid/a.png", budgetLowDetailLongSide); ok {
		t.Fatal("远程链接不该报告已缩小")
	}
}

// 图片超预算时先真缩再标 low：标签是给估算看的，字节没变就不该标。
func TestOverBudgetImagesAreShrunkBeforeBeingMarkedLow(t *testing.T) {
	original := testImageDataURL(t, 1600, 900)
	req := llm.GenerateRequest{Messages: []llm.Message{{Role: llm.RoleUser, Parts: []llm.ContentPart{
		{Type: llm.ContentPartText, Text: "这张图讲了什么"},
		{Type: llm.ContentPartImageURL, ImageURL: original, Detail: "high"},
	}}}}
	got := lowerOverBudgetImageDetail(req, 2048)
	part := got.Messages[0].Parts[1]
	if part.Detail != "low" {
		t.Fatalf("detail = %q，超预算时应当降到 low", part.Detail)
	}
	if part.ImageURL == original {
		t.Fatal("只改了标签没缩图：Gemini 不认 detail，这种省法是假的")
	}
	if !strings.HasPrefix(part.ImageURL, "data:image/jpeg;base64,") {
		t.Fatalf("缩完的图不是 JPEG data URI：%.40s", part.ImageURL)
	}
	decoded, err := decodeDataURLImage(part.ImageURL)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Bounds().Dx() != budgetLowDetailLongSide {
		t.Fatalf("图没被缩到目标尺寸：%dx%d", decoded.Bounds().Dx(), decoded.Bounds().Dy())
	}
}

// 没超预算就一张都别动。
func TestWithinBudgetImagesUntouched(t *testing.T) {
	original := testImageDataURL(t, 1600, 900)
	req := llm.GenerateRequest{Messages: []llm.Message{{Role: llm.RoleUser, Parts: []llm.ContentPart{
		{Type: llm.ContentPartImageURL, ImageURL: original, Detail: "high"},
	}}}}
	got := lowerOverBudgetImageDetail(req, 1_000_000)
	if got.Messages[0].Parts[0].ImageURL != original || got.Messages[0].Parts[0].Detail != "high" {
		t.Fatal("预算充足时不该动图片")
	}
}
