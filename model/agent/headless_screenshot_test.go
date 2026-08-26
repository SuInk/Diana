// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestCaptureHTMLScreenshotAcceptsCompletedPNGFromHangingBrowser(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake browser uses a POSIX shell script")
	}
	dir := t.TempDir()
	fixture := filepath.Join(dir, "fixture.png")
	file, err := os.Create(fixture)
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(file, image.NewRGBA(image.Rect(0, 0, 4, 4))); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	browser := filepath.Join(dir, "chrome")
	writeExecutable(t, browser, "#!/bin/sh\n"+
		"if [ \"$1\" = \"--version\" ]; then echo 'Chromium test'; exit 0; fi\n"+
		"for arg in \"$@\"; do case \"$arg\" in --screenshot=*) cp '"+fixture+"' \"${arg#--screenshot=}\";; esac; done\n"+
		"while :; do :; done\n")

	started := time.Now()
	raw, err := CaptureHTMLScreenshot(context.Background(), ScreenshotRequest{
		HTML:       "<body>ok</body>",
		Executable: browser,
		Timeout:    3 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(started) >= 2*time.Second {
		t.Fatalf("截图已写完却仍等待浏览器退出：%s", time.Since(started))
	}
	if _, err := png.DecodeConfig(strings.NewReader(string(raw))); err != nil {
		t.Fatalf("returned invalid PNG: %v", err)
	}
}

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

func TestReadCompletedScreenshotRejectsInvalidPNG(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shot.png")
	if err := os.WriteFile(path, []byte("not a png"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readCompletedScreenshot(path); err == nil {
		t.Fatal("invalid PNG was accepted")
	}

	img := image.NewRGBA(image.Rect(0, 0, 1, 1))
	img.Set(0, 0, color.Black)
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(file, img); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := readCompletedScreenshot(path); err != nil {
		t.Fatalf("valid PNG was rejected: %v", err)
	}
}
