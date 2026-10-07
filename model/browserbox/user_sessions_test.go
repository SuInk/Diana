// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package browserbox

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SuInk/diana/model/agent"
)

func TestUserBrowserScopesAndCancelledLeases(t *testing.T) {
	pool := NewUserSessionPool(t.TempDir(), nil)
	defer pool.Stop()
	one := pool.NewRequest("bot", "telegram", "1001", []string{"example.com"})
	same := pool.NewRequest("bot", "telegram", "1001", []string{"login.example.com"})
	if one.gate != same.gate || one.dataDir != same.dataDir {
		t.Fatal("same sender did not share a profile lease")
	}
	for _, scope := range [][3]string{{"bot", "telegram", "1002"}, {"other", "telegram", "1001"}, {"bot", "qq", "1001"}, {"bot", "telegram", "../1001"}} {
		other := pool.NewRequest(scope[0], scope[1], scope[2], nil)
		if other.dataDir == one.dataDir || other.gate == one.gate {
			t.Fatalf("scope collision: %v", scope)
		}
		if filepath.Dir(other.dataDir) != pool.root {
			t.Fatalf("scope escaped data directory: %s", other.dataDir)
		}
	}
	if _, err := os.Stat(one.dataDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("browser started without a tool invocation")
	}
	one.gate <- struct{}{}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := same.Endpoint(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("lease did not respect cancellation: %v", err)
	}
	<-one.gate
	if len(pool.slots) != 0 {
		t.Fatal("cancelled request leaked a process slot")
	}
	_ = same.Close()
	if _, err := same.Endpoint(context.Background()); err == nil {
		t.Fatal("closed session reopened")
	}
}

func TestUserBrowserPoolEvictsIdleProfilesWithinProcessLimit(t *testing.T) {
	pool := NewUserSessionPool(t.TempDir(), nil)
	defer pool.Stop()
	var oldest *userBrowserProfile
	for i := 0; i < 4; i++ {
		pool.slots <- struct{}{}
		entry := &userBrowserProfile{gate: make(chan struct{}, 1), manager: New(context.Background(), nil, t.TempDir()).Bot("personal"), lastUsed: time.Now().Add(time.Duration(i) * time.Minute)}
		pool.profiles[string(rune('a'+i))] = entry
		if i == 0 {
			oldest = entry
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := pool.acquireSlot(ctx, make(chan struct{})); err != nil {
		t.Fatal(err)
	}
	if oldest.manager != nil || len(pool.slots) != 4 {
		t.Fatal("pool exceeded cap or failed to evict oldest idle profile")
	}
	<-pool.slots // The newly reserved slot has no process in this fixture.
}

func TestUserProxyMatchesExactHostsAndRejectsPrivateOptIn(t *testing.T) {
	t.Setenv("DIANA_ALLOW_PRIVATE_HTTP_FETCHES", "1")
	proxy, err := newUserBrowserProxy([]string{"example.com", "127.0.0.1:1234"})
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.Close()
	for _, raw := range []string{"http://example.com", "https://example.com:443/login", "http://example.com:80/"} {
		if !proxy.allowed(raw) {
			t.Fatalf("valid host denied: %s", raw)
		}
	}
	for _, raw := range []string{"https://other.example.com", "https://example.com.evil.test", "https://example.com:444", "https://example.com@other.test", "file:///example.com", "https://example.com@127.0.0.1"} {
		if proxy.allowed(raw) {
			t.Fatalf("host bypass: %s", raw)
		}
	}
	for _, request := range []*http.Request{
		httptest.NewRequest(http.MethodGet, "http://127.0.0.1:1234", nil),
		httptest.NewRequest(http.MethodGet, "http://unlisted.example", nil),
		httptest.NewRequest(http.MethodConnect, "http://127.0.0.1:1234", nil),
	} {
		request.Host = request.URL.Host
		response := httptest.NewRecorder()
		proxy.ServeHTTP(response, request)
		if response.Code != http.StatusForbidden {
			t.Fatalf("private or unlisted destination status=%d", response.Code)
		}
	}
}

func TestUserProxyChecksLoginRedirectsAndBackgroundRequests(t *testing.T) {
	var calls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Host == "93.184.216.34" {
			http.Redirect(w, r, "http://1.1.1.1/login", http.StatusFound)
			return
		}
		_, _ = w.Write([]byte("login page"))
	}))
	defer upstream.Close()
	proxy, err := newUserBrowserProxy([]string{"93.184.216.34"})
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.Close()
	// Route public-IP fixtures to a local HTTP server only in this test.
	proxy.transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, strings.TrimPrefix(upstream.URL, "http://"))
	}
	proxyURL, _ := url.Parse(proxy.url)
	transport := &http.Transport{Proxy: http.ProxyURL(proxyURL)}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: time.Second}
	response, err := client.Get("http://93.184.216.34/start")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusForbidden || calls.Load() != 1 {
		t.Fatalf("unlisted redirect connected: status=%d calls=%d", response.StatusCode, calls.Load())
	}
	proxy.hosts = append(proxy.hosts, "1.1.1.1")
	response, err = client.Get("http://93.184.216.34/start")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK || calls.Load() != 3 {
		t.Fatalf("listed login redirect failed: status=%d calls=%d", response.StatusCode, calls.Load())
	}
	response, err = client.Get("http://8.8.8.8/background")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusForbidden || calls.Load() != 3 {
		t.Fatal("background request bypassed allowlist")
	}
}

func TestUserBrowserProfileIsolationIntegration(t *testing.T) {
	if os.Getenv("DIANA_HEADLESS_BROWSER_INTEGRATION") != "1" {
		t.Skip("set DIANA_HEADLESS_BROWSER_INTEGRATION=1 to run Chrome")
	}
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
