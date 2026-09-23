// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package llm

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/gif"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

func testGIFBytes(t *testing.T) []byte {
	t.Helper()
	palette := color.Palette{color.Black, color.White}
	first := image.NewPaletted(image.Rect(0, 0, 4, 3), palette)
	first.SetColorIndex(1, 1, 1)
	second := image.NewPaletted(image.Rect(0, 0, 4, 3), palette)
	var buf bytes.Buffer
	if err := gif.EncodeAll(&buf, &gif.GIF{Image: []*image.Paletted{first, second}, Delay: []int{10, 10}}); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// 群里拿来改的表情包大多是 GIF，图片接口会直接判成 invalid_image_file：
// 读源图时就要取第一帧转成 PNG。
func TestImageEditInputConvertsGIFFirstFrameToPNG(t *testing.T) {
	path := filepath.Join(t.TempDir(), "meme.gif")
	if err := os.WriteFile(path, testGIFBytes(t), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := imageEditInputFrom(context.Background(), nil, path, 0)
	if err != nil {
		t.Fatalf("imageEditInputFrom() error = %v", err)
	}
	if got.mediaType != "image/png" || filepath.Ext(got.filename) != ".png" {
		t.Fatalf("mediaType=%q filename=%q, want png", got.mediaType, got.filename)
	}
	decoded, err := png.Decode(bytes.NewReader(got.data))
	if err != nil {
		t.Fatalf("converted data is not png: %v", err)
	}
	if bounds := decoded.Bounds(); bounds.Dx() != 4 || bounds.Dy() != 3 {
		t.Fatalf("bounds = %v", bounds)
	}
	if r, _, _, _ := decoded.At(1, 1).RGBA(); r == 0 {
		t.Fatal("first frame pixel lost; converted a later frame")
	}
}

// 扩展名说是 PNG、内容其实是 JPEG 时，按字节报类型，不照着扩展名报。
func TestImageEditInputUsesSniffedMediaType(t *testing.T) {
	var jpegData bytes.Buffer
	jpegData.Write([]byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 'J', 'F', 'I', 'F', 0x00})
	path := filepath.Join(t.TempDir(), "photo.png")
	if err := os.WriteFile(path, jpegData.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := imageEditInputFrom(context.Background(), nil, path, 0)
	if err != nil {
		t.Fatalf("imageEditInputFrom() error = %v", err)
	}
	if got.mediaType != "image/jpeg" || got.filename != "photo.jpg" {
		t.Fatalf("mediaType=%q filename=%q", got.mediaType, got.filename)
	}
	if !bytes.Equal(got.data, jpegData.Bytes()) {
		t.Fatal("non-gif input bytes must stay untouched")
	}
}
