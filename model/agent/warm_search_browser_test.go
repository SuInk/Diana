// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// 常驻搜索浏览器要复用进程，但每次渲染之间不能共享 Cookie。
func TestWarmSearchBrowserReusesProcessAndIsolatesCookies(t *testing.T) {
	if os.Getenv("DIANA_HEADLESS_BROWSER_INTEGRATION") != "1" {
		t.Skip("set DIANA_HEADLESS_BROWSER_INTEGRATION=1 to run Chrome integration")
	}
	t.Cleanup(func() {
		warmSearchBrowser.mu.Lock()
		defer warmSearchBrowser.mu.Unlock()
		if warmSearchBrowser.timer != nil {
			warmSearchBrowser.timer.Stop()
		}
		if warmSearchBrowser.session != nil {
			warmSearchBrowser.session.close()
			warmSearchBrowser.session = nil
		}
	})
	renderer := NewSandboxedHeadlessBrowser(SandboxedBrowserConfig{Timeout: 30 * time.Second, ReuseProcess: true})
	started := time.Now()
	if _, err := renderer.Render(context.Background(), "https://httpbin.org/cookies/set?diana_isolation=leaked"); err != nil {
		t.Fatal(err)
	}
	first := time.Since(started)
	warmSearchBrowser.mu.Lock()
	session := warmSearchBrowser.session
	warmSearchBrowser.mu.Unlock()
	if session == nil {
		t.Fatal("warm browser was not kept")
	}
	started = time.Now()
	page, err := renderer.Render(context.Background(), "https://httpbin.org/cookies")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("first=%s second=%s text=%q", first, time.Since(started), truncateText(page.Text, 200))
	warmSearchBrowser.mu.Lock()
	reused := warmSearchBrowser.session == session
	warmSearchBrowser.mu.Unlock()
	if !reused {
		t.Fatal("second render started a new browser")
	}
	if strings.Contains(page.Text, "leaked") {
		t.Fatalf("cookie leaked between renders: %q", page.Text)
	}
}

func TestWarmBrowserIdleShutdownAndRelaunch(t *testing.T) {
	w := &warmBrowser{idle: 10 * time.Millisecond}
	session := &warmBrowserSession{root: context.Background(), cancel: func() {}, process: &sandboxedChromeProcess{cancel: func() {}, done: make(chan error)}}
	w.session, w.users = session, 1
	w.release(session)
	time.Sleep(50 * time.Millisecond)
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.session != nil || w.users != 0 {
		t.Fatalf("idle browser kept: session=%v users=%d", w.session, w.users)
	}
}

func TestWarmBrowserDoesNotReuseDifferentActiveConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name, executable string
		window           BrowserWindow
	}{
		{"executable", "other-chrome", BrowserWindowVisible},
		{"window", "chrome", BrowserWindowHidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			session := &warmBrowserSession{executable: "chrome", requestedWindow: BrowserWindowVisible, root: context.Background(), process: &sandboxedChromeProcess{done: make(chan error)}}
			w := &warmBrowser{session: session, users: 1}
			got, release, err := w.acquire(context.Background(), tc.executable, SandboxedBrowserConfig{Window: tc.window})
			if err == nil || got != nil || release != nil || w.users != 1 {
				t.Fatalf("incompatible browser reused: session=%v users=%d err=%v", got, w.users, err)
			}
		})
	}
}
