// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	defaultScreenshotTimeout = 25 * time.Second
	maxScreenshotBytes       = 8 << 20
)

// ScreenshotRequest describes one isolated local HTML-to-PNG render.
type ScreenshotRequest struct {
	WaitForFonts bool
	FontFiles    []string
	HTML         string
	Width        int
	Height       int
	// DeviceScaleFactor increases pixel density without changing CSS layout (default 1).
	DeviceScaleFactor float64
	Executable        string
	Timeout           time.Duration
	// VirtualTimeBudget lets scripts and asynchronous SVG/canvas rendering settle before capture.
	VirtualTimeBudget time.Duration
}

// CaptureHTMLScreenshot renders local HTML to PNG in the same headed-hidden browser path used by
// webpage rendering. If the host cannot provide a real or virtual display, browser launch falls
// back to headless automatically. A missing browser remains an explicit error.
func CaptureHTMLScreenshot(ctx context.Context, req ScreenshotRequest) ([]byte, error) {
	if strings.TrimSpace(req.HTML) == "" {
		return nil, errors.New("screenshot: empty html")
	}
	release, err := disposableBrowsers.acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("screenshot: %w", err)
	}
	defer release()
	return captureHTMLScreenshot(ctx, req)
}

// captureHTMLScreenshot skips the concurrency queue for availability probes, which already have
// a short deadline and must not report a busy browser as an unavailable installation.
func captureHTMLScreenshot(ctx context.Context, req ScreenshotRequest) ([]byte, error) {
	if strings.TrimSpace(req.HTML) == "" {
		return nil, errors.New("screenshot: empty html")
	}
	if req.Width <= 0 {
		req.Width = 1000
	}
	if req.Height <= 0 {
		req.Height = 900
	}
	if req.DeviceScaleFactor <= 0 {
		req.DeviceScaleFactor = 1
	}
	req.DeviceScaleFactor = min(req.DeviceScaleFactor, 2)
	if req.Timeout <= 0 {
		req.Timeout = defaultScreenshotTimeout
	}
	return captureFontReadyScreenshot(ctx, req)
}
