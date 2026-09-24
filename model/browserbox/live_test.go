// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package browserbox

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// fakeLiveTab 是一个只会几条画面命令的假标签页。stuck 为真时 Page.enable 永远不回，
// 模拟页面主线程卡死；crashAfterStart 为真时开始推帧之后发一条 Inspector.targetCrashed。
func fakeLiveTab(t *testing.T, stuck, crashAfterStart bool) (string, <-chan string) {
	t.Helper()
	navigated := make(chan string, 4)
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			var msg struct {
				ID     int    `json:"id"`
				Method string `json:"method"`
				Params struct {
					URL string `json:"url"`
				} `json:"params"`
			}
			if err := conn.ReadJSON(&msg); err != nil {
				return
			}
			if stuck && msg.Method == "Page.enable" {
				continue
			}
			if msg.Method == "Page.navigate" {
				navigated <- msg.Params.URL
			}
			if err := conn.WriteJSON(map[string]any{"id": msg.ID, "result": map[string]any{}}); err != nil {
				return
			}
			if crashAfterStart && msg.Method == "Page.startScreencast" {
				_ = conn.WriteJSON(map[string]any{"method": "Inspector.targetCrashed", "params": map[string]any{}})
			}
		}
	}))
	t.Cleanup(server.Close)
	return "ws" + strings.TrimPrefix(server.URL, "http"), navigated
}

// 标签页卡死时连画面按时收手并说清原因，WebUI 不再一直停在「正在连接画面……」。
func TestStartLiveGivesUpOnUnresponsiveTab(t *testing.T) {
	previous := liveStartTimeout
	liveStartTimeout = 200 * time.Millisecond
	t.Cleanup(func() { liveStartTimeout = previous })
	started := time.Now()
	tab, _ := fakeLiveTab(t, true, false)
	_, err := StartLive(context.Background(), tab, "https://example.com/", 1280, 800)
	if !errors.Is(err, ErrLivePageUnresponsive) {
		t.Fatalf("卡死的标签页应当报没有响应：%v", err)
	}
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("连画面没有按时收手：%s", elapsed)
	}
}

// 调用方自己取消（WebUI 关掉了这一页）不算标签页卡死。
func TestStartLiveKeepsCallerCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(100*time.Millisecond, cancel)
	tab, _ := fakeLiveTab(t, true, false)
	_, err := StartLive(ctx, tab, "https://example.com/", 1280, 800)
	if err == nil || errors.Is(err, ErrLivePageUnresponsive) {
		t.Fatalf("取消应当原样交回：%v", err)
	}
}

// 画面对应的标签页崩了：画面流结束并交代原因，崩掉的页换回空白页，下次连上就有画面。
func TestLiveReportsCrashedTab(t *testing.T) {
	tab, navigated := fakeLiveTab(t, false, true)
	live, err := StartLive(context.Background(), tab, "https://example.com/", 1280, 800)
	if err != nil {
		t.Fatal(err)
	}
	defer live.Close()
	select {
	case _, ok := <-live.Frames():
		if ok {
			t.Fatal("崩溃之后不该还有画面")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("标签页崩溃之后画面流没有结束")
	}
	if !errors.Is(live.Err(), ErrLivePageCrashed) {
		t.Fatalf("应当交代标签页崩溃：%v", live.Err())
	}
	select {
	case got := <-navigated:
		if got != "about:blank" {
			t.Fatalf("崩掉的页应当换回空白页：%s", got)
		}
	default:
		t.Fatal("崩掉的页没有被换回空白页")
	}
}
