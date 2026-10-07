// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SuInk/diana/model/llm"
	"github.com/gorilla/websocket"
)

func TestRestrictedBrowserScreenshotCannotSpecifyPath(t *testing.T) {
	tool := &BrowserScreenshotTool{base: browserToolBase{root: t.TempDir()}, restricted: true}
	if _, err := tool.Run(context.Background(), map[string]any{"path": ".extension-overrides.json"}); err == nil || !strings.Contains(err.Error(), "不能指定") {
		t.Fatalf("restricted screenshot accepted a file path: %v", err)
	}
}

func TestBrowserScreenshotCannotOverwriteProtectedConfig(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, ".extension-overrides.json")
	if err := os.WriteFile(path, []byte(`{"bot-a":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	tool := &BrowserScreenshotTool{base: browserToolBase{root: root}, protected: agentProtectedFiles(Config{WorkDir: root})}
	if _, err := tool.Run(context.Background(), map[string]any{"path": ".extension-overrides.json"}); err == nil || !strings.Contains(err.Error(), "受保护") {
		t.Fatalf("screenshot accepted protected config: %v", err)
	}
	data, _ := os.ReadFile(path)
	if string(data) != `{"bot-a":{}}` {
		t.Fatal("protected config was modified")
	}
}

type screenshotCDPOptions struct {
	beforeURL  string
	afterURL   string
	childURL   string
	navigation bool
	png        []byte
}

func screenshotTestPNG(t *testing.T) []byte {
	t.Helper()
	var output bytes.Buffer
	if err := png.Encode(&output, image.NewRGBA(image.Rect(0, 0, 16, 16))); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func screenshotCDPServer(t *testing.T, options screenshotCDPOptions) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	if options.beforeURL == "" {
		options.beforeURL = "https://example.com"
	}
	if options.afterURL == "" {
		options.afterURL = options.beforeURL
	}
	if options.png == nil {
		options.png = screenshotTestPNG(t)
	}
	calls := &atomic.Int64{}
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/json/list" {
			_ = json.NewEncoder(w).Encode([]browserTarget{{ID: "1", Type: "page", URL: "https://example.com", WebSocketDebuggerURL: "ws" + strings.TrimPrefix(server.URL, "http") + "/page"}})
			return
		}
		if r.URL.Path != "/page" {
			t.Errorf("screenshot tried to open a page: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		frameCalls := 0
		for {
			var request struct {
				ID     int    `json:"id"`
				Method string `json:"method"`
			}
			if err := conn.ReadJSON(&request); err != nil {
				return
			}
			result := map[string]any{}
			switch request.Method {
			case "Page.enable":
			case "Page.getFrameTree":
				frameCalls++
				frameURL := options.beforeURL
				if frameCalls > 1 {
					frameURL = options.afterURL
				}
				tree := map[string]any{"frame": map[string]any{"url": frameURL}}
				if options.childURL != "" {
					tree["childFrames"] = []any{map[string]any{"frame": map[string]any{"url": options.childURL}}}
				}
				result["frameTree"] = tree
			case "Page.captureScreenshot":
				calls.Add(1)
				if options.navigation {
					_ = conn.WriteJSON(map[string]any{"method": "Page.navigatedWithinDocument", "params": map[string]any{"url": "https://secret.example"}})
				}
				result["data"] = base64.StdEncoding.EncodeToString(options.png)
			default:
				t.Errorf("unexpected screenshot operation: %s", request.Method)
			}
			if err := conn.WriteJSON(map[string]any{"id": request.ID, "result": result}); err != nil {
				return
			}
		}
	}))
	t.Cleanup(server.Close)
	return server, calls
}

func TestRestrictedBrowserScreenshotReturnsOnlyCapturedImage(t *testing.T) {
	server, calls := screenshotCDPServer(t, screenshotCDPOptions{})
	root := t.TempDir()
	tool := &BrowserScreenshotTool{base: browserToolBase{root: root, cdpURL: server.URL, timeout: time.Second}, restricted: true, allowedHosts: []string{"example.com"}}
	for range 2 {
		output, err := tool.Run(context.Background(), map[string]any{"url": "https://example.com"})
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(output, "path") || strings.Contains(output, "data:image") {
			t.Fatalf("unexpected file or base64 output: %s", output)
		}
		parts := tool.ToolResultParts(output)
		if len(parts) != 1 || parts[0].Type != llm.ContentPartImageURL || !strings.HasPrefix(parts[0].ImageURL, "data:image/png;base64,") {
			t.Fatalf("missing image evidence: %#v", parts)
		}
	}
	files, err := os.ReadDir(root)
	if err != nil || len(files) != 0 {
		t.Fatalf("member screenshot persisted files: %#v %v", files, err)
	}
	if calls.Load() != 2 {
		t.Fatal("screenshots were not captured")
	}
	if _, err := tool.Run(context.Background(), map[string]any{"url": "https://secret.example"}); err == nil {
		t.Fatal("unapproved host accepted")
	}
	if len(tool.ToolResultParts("")) != 0 {
		t.Fatal("stale image evidence after rejection")
	}
}

func TestBrowserScreenshotRejectsAmbiguousTargets(t *testing.T) {
	targets := []browserTarget{
		{ID: "1", Type: "page", URL: "https://example.com", WebSocketDebuggerURL: "ws://1"},
		{ID: "2", Type: "page", URL: "https://example.com", WebSocketDebuggerURL: "ws://2"},
		{ID: "3", Type: "page", URL: "about:blank", WebSocketDebuggerURL: "ws://3"},
	}
	for _, rawURL := range []string{"", "https://example.com", "https://other.example"} {
		if _, err := screenshotTarget(targets, rawURL, ""); err == nil {
			t.Fatal("non-unique target accepted")
		}
	}
	if got, err := screenshotTarget(targets, "https://example.com", "2"); err != nil || got.ID != "2" {
		t.Fatalf("explicit target failed: %#v %v", got, err)
	}
	if _, err := screenshotTarget(targets, "https://other.example", "2"); err == nil {
		t.Fatal("tab ID bypassed the exact URL")
	}
}

func TestRestrictedScreenshotDropsChangedAndUnapprovedPages(t *testing.T) {
	for _, tc := range []struct {
		name         string
		options      screenshotCDPOptions
		wantCaptures int64
	}{
		{"changed-before-capture", screenshotCDPOptions{beforeURL: "https://secret.example"}, 0},
		{"unapproved-frame", screenshotCDPOptions{childURL: "https://secret.example"}, 0},
		{"changed-after-capture", screenshotCDPOptions{afterURL: "https://secret.example"}, 1},
		{"navigation-back-to-same-url", screenshotCDPOptions{navigation: true}, 1},
		{"invalid-png", screenshotCDPOptions{png: []byte("not an image")}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server, calls := screenshotCDPServer(t, tc.options)
			tool := &BrowserScreenshotTool{base: browserToolBase{root: t.TempDir(), cdpURL: server.URL, timeout: time.Second}, restricted: true, allowedHosts: []string{"example.com"}}
			if _, err := tool.Run(context.Background(), map[string]any{"url": "https://example.com"}); err == nil {
				t.Fatal("unsafe screenshot accepted")
			}
			if calls.Load() != tc.wantCaptures || len(tool.ToolResultParts("")) != 0 {
				t.Fatal("rejected screenshot retained evidence or captured an unauthorized page")
			}
		})
	}
}

func TestBrowserScreenshotHostMatching(t *testing.T) {
	for _, rawURL := range []string{"https://example.com/account", "http://EXAMPLE.COM/account"} {
		if !browserScreenshotHostAllowed(rawURL, []string{" Example.COM "}) {
			t.Fatal("exact host not matched")
		}
	}
	for _, rawURL := range []string{"https://sub.example.com", "https://example.com:8443", "https://example.com.evil.test", "https://user:secret@example.com", "file:///example.com", "https://example.com@evil.test"} {
		if browserScreenshotHostAllowed(rawURL, []string{"example.com"}) {
			t.Fatalf("host scope bypass: %s", rawURL)
		}
	}
	for _, host := range []string{"*.example.com", "https://example.com", "example.com/path", "example.com?", "example.com#", "user@example.com"} {
		if ValidBrowserScreenshotHost(host) {
			t.Fatalf("invalid host accepted: %s", host)
		}
	}
}
