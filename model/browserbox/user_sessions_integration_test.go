// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

//go:build browser_integration

package browserbox

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/SuInk/diana/model/agent"
)

func TestUserBrowserProfileIsolationIntegration(t *testing.T) {
	pool := NewUserSessionPool(t.TempDir(), nil)
	defer pool.Stop()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	open := func(user string) (*UserBrowserSession, *Session) {
		t.Helper()
		browser := pool.NewRequest("bot", "telegram", user, []string{"example.com"})
		endpoint, err := browser.Endpoint(ctx)
		if err != nil {
			t.Fatal(err)
		}
		ws, err := browserWebSocket(ctx, endpoint)
		if err != nil {
			t.Fatal(err)
		}
		cdp, err := Dial(ctx, ws)
		if err != nil {
			t.Fatal(err)
		}
		return browser, cdp
	}
	one, cdp := open("one")
	registry, err := agent.NewDefaultToolRegistry(agent.Config{WorkDir: t.TempDir(), PersonalBrowser: one, BrowserOperationHosts: []string{"example.com"}, BrowserTimeoutMS: 20000, BrowserScreenshotRestricted: true, BrowserScreenshotHosts: []string{"example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	defer registry.Close()
	tool, _ := registry.Get("browser_open")
	if output, err := tool.Run(ctx, map[string]any{"url": "https://example.com"}); err != nil || !strings.Contains(output, "Example Domain") {
		t.Fatalf("public website via proxy=%s err=%v", output, err)
	}
	if _, err := tool.Run(ctx, map[string]any{"url": "https://www.example.com"}); err == nil {
		t.Fatal("unlisted host opened")
	}
	if err := cdp.Call(ctx, "Storage.setCookies", map[string]any{"cookies": []any{map[string]any{"name": "isolated_login", "value": "one", "domain": "example.com", "path": "/", "expires": float64(time.Now().Add(time.Hour).Unix())}}}, nil); err != nil {
		t.Fatal(err)
	}
	cdp.Close()
	_ = one.Close()
	resumed, resumedCDP := open("one")
	if resumed.manager != one.manager {
		t.Fatal("QR login browser did not survive reply close")
	}
	resumedCDP.Close()
	_ = resumed.Close()
	pool.Stop()
	two, other := open("two")
	var cookies struct {
		Cookies []struct{ Name, Value string } `json:"cookies"`
	}
	if err := other.Call(ctx, "Storage.getCookies", nil, &cookies); err != nil {
		t.Fatal(err)
	}
	for _, cookie := range cookies.Cookies {
		if cookie.Name == "isolated_login" {
			t.Fatal("another user's cookie leaked")
		}
	}
	other.Close()
	_ = two.Close()
	restored, again := open("one")
	defer restored.Close()
	defer again.Close()
	if err := again.Call(ctx, "Storage.getCookies", nil, &cookies); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, cookie := range cookies.Cookies {
		if cookie.Name == "isolated_login" && cookie.Value == "one" {
			found = true
		}
	}
	if !found {
		t.Fatal("own persistent login cookie was lost")
	}
}
