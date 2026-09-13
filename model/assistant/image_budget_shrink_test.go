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

// 本来就小的图，超预算时也要标 low。
//
// 线上 v0.8.108 的一次失败：一轮 15 张图，描述掉 3 张，剩 12 张全按 high 的 8192
// 计，图片 98304 加文字 74395 超出 126848，报「当前问题本身就超出预算」整轮丢弃。
// 这些图不超过 512 像素，缩放函数如实返回「不用缩」，而上一版在那里直接 continue，
// 连 low 标签也不打了。改之前是无条件标 low，12 张按 1024 只要 12288，本来装得下。
// 小图标 low 并不虚报：它的尺寸本来就在低细节档位以内。
func TestSmallImagesStillMarkedLowWhenOverBudget(t *testing.T) {
	parts := []llm.ContentPart{{Type: llm.ContentPartText, Text: "这些图里哪张是网卡设置"}}
	for i := 0; i < 12; i++ {
		parts = append(parts, llm.ContentPart{Type: llm.ContentPartImageURL, ImageURL: testImageDataURL(t, 400, 300), Detail: "high"})
	}
	req := llm.GenerateRequest{Messages: []llm.Message{{Role: llm.RoleUser, Parts: parts}}}
	before := llm.PlanInputBudget(req, 20000)
	if before.ImageExcess <= 0 {
		t.Fatalf("前提不成立：图片应当超出预算，ImageExcess=%d", before.ImageExcess)
	}
	got := lowerOverBudgetImageDetail(req, 20000)
	// 只降到够用为止，不要求每张都降；要求的是降完不再超。上一版一张都不降。
	lowered := 0
	for _, part := range got.Messages[0].Parts[1:] {
		if part.Detail == "low" {
			lowered++
		}
	}
	if lowered == 0 {
		t.Fatal("本来就小的图一张都没标 low，仍全部按 high 的 8192 计")
	}
	if after := llm.PlanInputBudget(got, 20000); after.ImageExcess > 0 {
		t.Fatalf("标了 %d 张仍然超出：ImageExcess=%d", lowered, after.ImageExcess)
	}
}

// 解不开的图（格式不认、数据损坏）也照旧标 low，不能比改之前更差。
func TestUndecodableImagesStillMarkedLowWhenOverBudget(t *testing.T) {
	broken := "data:image/png;base64,bm90LWFuLWltYWdl"
	parts := []llm.ContentPart{{Type: llm.ContentPartText, Text: "看图"}}
	for i := 0; i < 8; i++ {
		parts = append(parts, llm.ContentPart{Type: llm.ContentPartImageURL, ImageURL: broken, Detail: "high"})
	}
	got := lowerOverBudgetImageDetail(llm.GenerateRequest{Messages: []llm.Message{{Role: llm.RoleUser, Parts: parts}}}, 20000)
	lowered := 0
	for _, part := range got.Messages[0].Parts[1:] {
		if part.Detail == "low" {
			lowered++
		}
		if part.ImageURL != broken {
			t.Fatal("解不开的图不该被改写字节")
		}
	}
	if lowered == 0 {
		t.Fatal("解不开的图一张都没标 low，比改之前还差")
	}
	if after := llm.PlanInputBudget(got, 20000); after.ImageExcess > 0 {
		t.Fatalf("仍然超出：ImageExcess=%d", after.ImageExcess)
	}
}

// 标 low 之后还超，就丢图直到装得下：先丢旧消息里的，再从当前这条的最后一张往前丢。
// 线上一轮十几张截图整轮失败，用户只收到一句「超出输入预算，未能发出」。
func TestDropOverBudgetImagesOrderAndPlaceholder(t *testing.T) {
	image := func(label string) llm.ContentPart {
		return llm.ContentPart{Type: llm.ContentPartImageURL, ImageURL: "https://example.invalid/" + label + ".png", Detail: "low"}
	}
	req := llm.GenerateRequest{Messages: []llm.Message{
		{Role: llm.RoleUser, Parts: []llm.ContentPart{{Type: llm.ContentPartText, Text: "早先的图"}, image("old")}},
		{Role: llm.RoleAssistant, Content: "看到了"},
		{Role: llm.RoleUser, Parts: []llm.ContentPart{{Type: llm.ContentPartText, Text: "这几张呢"}, image("first"), image("second"), image("third")}},
	}}
	// 预算只够一张低细节图加少量文字。
	got, dropped := dropOverBudgetImages(req, 1400)
	if dropped == 0 {
		t.Fatal("超预算却一张没丢")
	}
	if llm.PlanInputBudget(got, 1400).OverBudget() {
		t.Fatalf("丢了 %d 张仍然超", dropped)
	}
	// 旧消息里的图最先丢。
	if got.Messages[0].Parts[1].Type != llm.ContentPartText || got.Messages[0].Parts[1].Text != budgetDroppedImagePlaceholder {
		t.Fatal("旧消息里的图应当最先被丢")
	}
	// 当前这条保住的是第一张，丢的是后面的。
	current := got.Messages[2].Parts
	if current[1].Type != llm.ContentPartImageURL {
		t.Fatal("当前消息的第一张图应当最后才丢")
	}
	if current[3].Type != llm.ContentPartText || current[3].Text != budgetDroppedImagePlaceholder {
		t.Fatal("当前消息的最后一张应当先于前面的被丢")
	}
	// 原请求不被改写。
	if req.Messages[2].Parts[3].Type != llm.ContentPartImageURL {
		t.Fatal("丢图改到了调用方传进来的请求")
	}
}

// 没超就一张不丢。
func TestDropOverBudgetImagesNoopWithinBudget(t *testing.T) {
	req := llm.GenerateRequest{Messages: []llm.Message{{Role: llm.RoleUser, Parts: []llm.ContentPart{
		{Type: llm.ContentPartImageURL, ImageURL: "https://example.invalid/a.png", Detail: "low"},
	}}}}
	if _, dropped := dropOverBudgetImages(req, 1_000_000); dropped != 0 {
		t.Fatalf("预算充足却丢了 %d 张", dropped)
	}
}
