// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package browserbox

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestSandboxProxyAllowsAnyPublicHostButNotPrivate(t *testing.T) {
	t.Setenv("DIANA_ALLOW_PRIVATE_HTTP_FETCHES", "1")
	proxy, err := newUserBrowserProxy(nil, true)
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.Close()
	for _, raw := range []string{"https://www.bilibili.com/toy/x", "http://example.com:8080/", "https://www.bilibilitoy.com"} {
		if !proxy.allowed(raw) {
			t.Fatalf("public host denied: %s", raw)
		}
	}
	for _, raw := range []string{"https://user@example.com", "file:///etc/passwd", "ftp://example.com"} {
		if proxy.allowed(raw) {
			t.Fatalf("non-web or credentialed url allowed: %s", raw)
		}
	}
	for _, request := range []*http.Request{
		httptest.NewRequest(http.MethodGet, "http://127.0.0.1:1234", nil),
		httptest.NewRequest(http.MethodGet, "http://192.168.1.1/admin", nil),
		httptest.NewRequest(http.MethodConnect, "http://10.0.0.1:443", nil),
	} {
		request.Host = request.URL.Host
		response := httptest.NewRecorder()
		proxy.ServeHTTP(response, request)
		if response.Code != http.StatusForbidden {
			t.Fatalf("private destination %s status=%d", request.URL, response.Code)
		}
	}
	// The personal proxy keeps its allowlist semantics: no hosts means nothing.
	strict, err := newUserBrowserProxy(nil, false)
	if err != nil {
		t.Fatal(err)
	}
	defer strict.Close()
	if strict.allowed("https://example.com") {
		t.Fatal("empty personal allowlist opened every host")
	}
}

func TestSandboxPoolScopesByConversationAndForgetsProfiles(t *testing.T) {
	dir := t.TempDir()
	leftover := filepath.Join(dir, "sandbox-browsers", "stale")
	if err := os.MkdirAll(leftover, 0o700); err != nil {
		t.Fatal(err)
	}
	pool := NewSandboxSessionPool(dir, nil)
	defer pool.Stop()
	if _, err := os.Stat(leftover); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("sandbox profiles from a previous run survived startup")
	}
	if !pool.NewSandboxRequest("bot", "qq", "group:1").PublicSandbox() {
		t.Fatal("sandbox session not marked public")
	}
	if NewUserSessionPool(dir, nil).NewRequest("bot", "qq", "1", nil).PublicSandbox() {
		t.Fatal("personal session marked as public sandbox")
	}

	first := pool.NewSandboxRequest("bot", "qq", "group:1")
	first.SetCurrentTab("tab-1")
	_ = first.Close()
	// The next reply in the same group continues on the same page.
	next := pool.NewSandboxRequest("bot", "qq", "group:1")
	if next.CurrentTab() != "tab-1" || next.dataDir != first.dataDir {
		t.Fatalf("same conversation lost its page: tab=%q", next.CurrentTab())
	}
	other := pool.NewSandboxRequest("bot", "qq", "group:2")
	if other.CurrentTab() != "" || other.dataDir == first.dataDir || other.gate == first.gate {
		t.Fatal("conversations share a sandbox")
	}
	_ = other.Close()

	// Closing the process deletes the profile and forgets the tab.
	if err := os.MkdirAll(next.dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	next.gate <- struct{}{}
	pool.closeProfile(next.entry)
	<-next.gate
	if _, err := os.Stat(next.dataDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("sandbox profile kept after the browser closed")
	}
	_ = next.Close()
	if next.CurrentTab() != "" || pool.NewSandboxRequest("bot", "qq", "group:1").CurrentTab() != "" {
		t.Fatal("closed sandbox still points at a dead tab")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := pool.NewSandboxRequest("bot", "qq", "group:3").Endpoint(ctx); err == nil {
		t.Fatal("cancelled request started a browser")
	}
}
