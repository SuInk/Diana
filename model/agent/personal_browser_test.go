// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

type personalBrowserStub struct {
	endpoint string
	err      error
	tab      string
}

func (b *personalBrowserStub) Endpoint(context.Context) (string, error) { return b.endpoint, b.err }
func (b *personalBrowserStub) CurrentTab() string                       { return b.tab }
func (b *personalBrowserStub) SetCurrentTab(id string)                  { b.tab = id }

func TestPersonalBrowserNeverFallsBackToOwnerCDP(t *testing.T) {
	var calls atomic.Int64
	owner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(500) }))
	defer owner.Close()
	for _, personal := range []*personalBrowserStub{{err: errors.New("personal unavailable")}, {}} {
		base := browserToolBase{cdpURL: owner.URL, personal: personal, timeout: time.Second, allowedHosts: []string{"example.com"}}
		tools := []Tool{&BrowserOpenTool{base: base}, &BrowserTextTool{base: base}, &BrowserClickTool{base: base}, &BrowserTypeTool{base: base}, &PersonalBrowserTabsTool{base: base}, &BrowserScreenshotTool{base: base, restricted: true, allowedHosts: []string{"example.com"}}}
		for _, tool := range tools {
			if _, err := tool.Run(context.Background(), map[string]any{"url": "https://example.com", "selector": "button", "text": "value", "user_id": "owner", "profile_id": "owner"}); err == nil {
				t.Fatalf("%s succeeded without own browser", tool.Name())
			}
		}
	}
	if calls.Load() != 0 {
		t.Fatal("member contacted owner's CDP")
	}
}

func TestPersonalScreenshotChecksBothOperationAndScreenshotSites(t *testing.T) {
	server, captures := screenshotCDPServer(t, screenshotCDPOptions{})
	base := browserToolBase{personal: &personalBrowserStub{endpoint: server.URL}, timeout: time.Second, allowedHosts: []string{"login.example.com"}}
	tool := &BrowserScreenshotTool{base: base, restricted: true, allowedHosts: []string{"example.com"}}
	if _, err := tool.Run(context.Background(), map[string]any{"url": "https://example.com"}); err == nil || captures.Load() != 0 {
		t.Fatal("screenshot exceeded operation scope")
	}
	tool.base.allowedHosts = []string{"example.com"}
	if _, err := tool.Run(context.Background(), map[string]any{"url": "https://example.com", "user_id": "other"}); err != nil || captures.Load() != 1 {
		t.Fatalf("own screenshot failed: %v", err)
	}
}
