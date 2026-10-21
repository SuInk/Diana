// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestProbeHeadlessBrowserRenderingChecksRealScreenshot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake browser uses a POSIX shell script")
	}
	browser := filepath.Join(t.TempDir(), "chrome")
	writeExecutable(t, browser, "#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then echo 'Chromium test'; fi\n")

	status := ProbeHeadlessBrowserRendering(context.Background(), browser)
	if status.Available || !strings.Contains(status.Detail, "真实截图失败") {
		t.Fatalf("status=%#v", status)
	}
}

func TestProbeHeadlessBrowserRenderingIntegration(t *testing.T) {
	if os.Getenv("DIANA_HEADLESS_BROWSER_PROBE_INTEGRATION") != "1" {
		t.Skip("set DIANA_HEADLESS_BROWSER_PROBE_INTEGRATION=1 to probe the installed browser")
	}
	status := ProbeHeadlessBrowserRendering(context.Background(), "")
	if !status.Available {
		t.Fatalf("installed browser failed real screenshot probe: %#v", status)
	}
	t.Logf("browser=%s version=%s", status.Path, status.Version)
}

func TestBrowserProbeRejectsScreenshotOnlySuccess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX fake browser")
	}
	dir := t.TempDir()
	fixture := filepath.Join(dir, "image.png")
	f, err := os.Create(fixture)
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(f, image.NewRGBA(image.Rect(0, 0, 4, 4))); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	browser := filepath.Join(dir, "chromium")
	writeExecutable(t, browser, "#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then echo 'Chromium test'; exit 0; fi\nfor arg in \"$@\"; do case \"$arg\" in --screenshot=*) cp '"+fixture+"' \"${arg#--screenshot=}\";; esac; done\n")
	status := ProbeHeadlessBrowserRendering(context.Background(), browser)
	if status.Available || !strings.Contains(status.Detail, "真实截图失败") {
		t.Fatalf("screenshot-only browser incorrectly available: %#v", status)
	}
}

func TestScreenshotPixelDensityIntegration(t *testing.T) {
	if os.Getenv("DIANA_HEADLESS_BROWSER_PROBE_INTEGRATION") != "1" {
		t.Skip("requires installed browser")
	}
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
