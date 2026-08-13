package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// fakeCDPServer 模拟 Chrome DevTools 端点：/json/new 建标签、/json/close 关标签、
// websocket 上应答 Runtime.evaluate。
func fakeCDPServer(t *testing.T, closed *atomic.Bool) *httptest.Server {
	t.Helper()
	upgrader := websocket.Upgrader{}
	mux := http.NewServeMux()
	var server *httptest.Server
	mux.HandleFunc("/json/new", func(w http.ResponseWriter, _ *http.Request) {
		wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/devtools/page/tab1"
		_ = json.NewEncoder(w).Encode(map[string]string{
			"id":                   "tab1",
			"webSocketDebuggerUrl": wsURL,
		})
	})
	mux.HandleFunc("/json/close/tab1", func(w http.ResponseWriter, _ *http.Request) {
		closed.Store(true)
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/devtools/page/tab1", func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			var req struct {
				ID     int64          `json:"id"`
				Method string         `json:"method"`
				Params map[string]any `json:"params"`
			}
			if err := conn.ReadJSON(&req); err != nil {
				return
			}
			resp := map[string]any{"id": req.ID, "result": map[string]any{}}
			if req.Method == "Runtime.evaluate" {
				expression, _ := req.Params["expression"].(string)
				value := any(true)
				switch {
				case strings.Contains(expression, "readyState"):
					// 就绪轮询：直接报告已完成加载。
					value = map[string]any{"href": "https://www.xiaohongshu.com/discovery/item/1", "ready": "complete"}
				case strings.Contains(expression, "og:title"):
					// 提取脚本：返回渲染后的页面元数据。
					value = map[string]any{
						"url":         "https://www.xiaohongshu.com/discovery/item/1",
						"title":       "渲染后的笔记标题",
						"description": "渲染后的摘要",
						"text":        "正文内容",
					}
				}
				resp["result"] = map[string]any{"result": map[string]any{"value": value}}
			}
			if err := conn.WriteJSON(resp); err != nil {
				return
			}
		}
	})
	server = httptest.NewServer(mux)
	return server
}

// TestFetchRenderedPageExtractsMetaAndClosesTab 验证对应功能场景。
func TestFetchRenderedPageExtractsMetaAndClosesTab(t *testing.T) {
	var closed atomic.Bool
	server := fakeCDPServer(t, &closed)
	defer server.Close()

	page, err := FetchRenderedPage(context.Background(), server.URL, "https://www.xiaohongshu.com/discovery/item/1", 5*time.Second, 4000)
	if err != nil {
		t.Fatalf("FetchRenderedPage() error = %v", err)
	}
	if page.Title != "渲染后的笔记标题" || page.Description != "渲染后的摘要" {
		t.Fatalf("page = %#v", page)
	}
	if page.URL != "https://www.xiaohongshu.com/discovery/item/1" {
		t.Fatalf("URL = %q", page.URL)
	}
	// 等待 defer 的关闭调用落地。
	deadline := time.Now().Add(2 * time.Second)
	for !closed.Load() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !closed.Load() {
		t.Fatal("tab was not closed after fetch")
	}
}

// TestFetchRenderedPageRejectsBadURL 验证对应功能场景。
func TestFetchRenderedPageRejectsBadURL(t *testing.T) {
	if _, err := FetchRenderedPage(context.Background(), "", "ftp://x", time.Second, 100); err == nil {
		t.Fatal("ftp url accepted")
	}
}
