// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

//go:build browser_integration

package agent

import (
	"bytes"
	"context"
	"image/png"
	"testing"
)

func TestProbeRenderBrowserRenderingIntegration(t *testing.T) {
	status := ProbeRenderBrowserRendering(context.Background(), "")
	if !status.Available {
		t.Fatalf("installed browser failed real screenshot probe: %#v", status)
	}
	t.Logf("browser=%s version=%s", status.Path, status.Version)
}

func TestScreenshotPixelDensityIntegration(t *testing.T) {
	for _, scale := range []float64{0, 2} {
		raw, err := CaptureHTMLScreenshot(context.Background(), ScreenshotRequest{HTML: `<style>body{margin:0}div{width:100px;height:40px;background:black}</style><div></div>`, Width: 300, Height: 200, DeviceScaleFactor: scale})
		if err != nil {
			t.Fatal(err)
		}
		img, err := png.Decode(bytes.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		factor := 1
		if scale == 2 {
			factor = 2
		}
		if img.Bounds().Dx() != 300*factor || img.Bounds().Dy() != 200*factor {
			t.Fatalf("scale %v: bounds %v", scale, img.Bounds())
		}
		// CSS geometry must remain fixed while its rasterized dimensions double.
		r, _, _, _ := img.At(99*factor, 20*factor).RGBA()
		if r != 0 {
			t.Fatal("content shrank")
		}
		r, _, _, _ = img.At(101*factor, 20*factor).RGBA()
		if r != 65535 {
			t.Fatal("layout expanded")
		}
	}
}
