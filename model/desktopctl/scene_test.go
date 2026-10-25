// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.
package desktopctl

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"testing"
)

func scenePNG(t *testing.T, w, h int, paint func(*image.RGBA)) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{250, 250, 250, 255})
		}
	}
	if paint != nil {
		paint(img)
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

func fill(img *image.RGBA, x0, y0, x1, y1 int, c color.RGBA) {
	for y := y0; y < y1; y++ {
		for x := x0; x < x1; x++ {
			img.Set(x, y, c)
		}
	}
}

// 输入框里闪烁的光标不算画面变化，否则文本框永远打不进字；稍大一点的真实变化照样拒绝。
func TestSameSceneForgivesOnlyACaret(t *testing.T) {
	black := color.RGBA{0, 0, 0, 255}
	base := scenePNG(t, 200, 100, nil)
	caret := scenePNG(t, 200, 100, func(img *image.RGBA) { fill(img, 50, 20, 52, 38, black) })
	char := scenePNG(t, 200, 100, func(img *image.RGBA) { fill(img, 50, 20, 58, 36, black) })
	checkbox := scenePNG(t, 200, 100, func(img *image.RGBA) { fill(img, 10, 10, 26, 26, black) })
	twoPlaces := scenePNG(t, 200, 100, func(img *image.RGBA) {
		fill(img, 50, 20, 52, 38, black)
		fill(img, 150, 20, 152, 38, black)
	})
	noise := scenePNG(t, 200, 100, func(img *image.RGBA) { fill(img, 0, 0, 200, 100, color.RGBA{240, 242, 238, 255}) })
	resized := scenePNG(t, 201, 100, nil)
	cases := []struct {
		name  string
		after string
		same  bool
	}{
		{"identical", base, true},
		{"blinking caret", caret, true},
		{"antialias noise", noise, true},
		{"typed character", char, false},
		{"toggled checkbox", checkbox, false},
		{"changes in two places", twoPlaces, false},
		{"resized window", resized, false},
		{"not a png", "bm90IGEgcG5n", false},
	}
	for _, tc := range cases {
		if got := sameScene(base, tc.after); got != tc.same {
			t.Errorf("%s: sameScene = %v, want %v", tc.name, got, tc.same)
		}
	}
}
