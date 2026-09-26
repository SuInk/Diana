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

// 前端没来取帧时，浏览器推来的帧只留最新一张，过时的当场替它回执，浏览器才会接着出帧；
// 留着的那张要等前端取走、调用方 Ack 之后才回执——帧率跟着看的人走，不在缓冲里排队。
func TestLiveKeepsOnlyLatestFrameAndAcksStale(t *testing.T) {
	const pushed = 5
	acks := make(chan int, pushed)
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
					SessionID int `json:"sessionId"`
				} `json:"params"`
			}
			if err := conn.ReadJSON(&msg); err != nil {
				return
			}
			if msg.Method == "Page.screencastFrameAck" {
				acks <- msg.Params.SessionID
			}
			if err := conn.WriteJSON(map[string]any{"id": msg.ID, "result": map[string]any{}}); err != nil {
				return
			}
			if msg.Method == "Page.startScreencast" {
				for id := 1; id <= pushed; id++ {
					_ = conn.WriteJSON(map[string]any{"method": "Page.screencastFrame", "params": map[string]any{
						"data": "/9j/", "sessionId": id,
						"metadata": map[string]any{"deviceWidth": 1280, "deviceHeight": 800, "timestamp": 1.5},
					}})
				}
			}
		}
	}))
	t.Cleanup(server.Close)
	live, err := StartLive(context.Background(), "ws"+strings.TrimPrefix(server.URL, "http"), "https://example.com/", 1280, 800)
	if err != nil {
		t.Fatal(err)
	}
	defer live.Close()

	for want := 1; want < pushed; want++ {
		select {
		case got := <-acks:
			if got != want {
				t.Fatalf("过时的帧应按顺序回执，期望 %d，得到 %d", want, got)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("过时的第 %d 帧没有回执，浏览器会停在这里不再出帧", want)
		}
	}
	select {
	case got := <-acks:
		t.Fatalf("最新一帧还没被取走就回执了：%d", got)
	case <-time.After(100 * time.Millisecond):
	}
	frame := <-live.Frames()
	if frame.ackID != pushed || len(frame.JPEG) != 3 || frame.Width != 1280 || frame.Timestamp != 1.5 {
		t.Fatalf("应拿到最新一帧并解好 JPEG：%+v", frame)
	}
	live.Ack(frame)
	if got := <-acks; got != pushed {
		t.Fatalf("取走之后应回执最新一帧，得到 %d", got)
	}
}

// 地址栏和标题跟着主框架走：子框架（广告、嵌入页）的跳转不算，加载完读一次标题。
func TestLiveTracksMainFramePage(t *testing.T) {
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		title := "旧标题"
		for {
			var msg struct {
				ID     int    `json:"id"`
				Method string `json:"method"`
			}
			if err := conn.ReadJSON(&msg); err != nil {
				return
			}
			result := map[string]any{}
			switch msg.Method {
			case "Page.getFrameTree":
				result = map[string]any{"frameTree": map[string]any{"frame": map[string]any{"id": "main", "url": "https://a.example/"}}}
			case "Runtime.evaluate":
				result = map[string]any{"result": map[string]any{"type": "string", "value": title}}
			}
			if err := conn.WriteJSON(map[string]any{"id": msg.ID, "result": result}); err != nil {
				return
			}
			if msg.Method == "Page.startScreencast" {
				title = "新页面"
				for _, event := range []map[string]any{
					{"method": "Page.frameNavigated", "params": map[string]any{"frame": map[string]any{"id": "ad", "parentId": "main", "url": "https://ads.example/"}}},
					{"method": "Page.frameStartedLoading", "params": map[string]any{"frameId": "main"}},
					{"method": "Page.frameNavigated", "params": map[string]any{"frame": map[string]any{"id": "main", "url": "https://b.example/"}}},
					{"method": "Page.frameStoppedLoading", "params": map[string]any{"frameId": "main"}},
				} {
					_ = conn.WriteJSON(event)
				}
			}
		}
	}))
	t.Cleanup(server.Close)
	live, err := StartLive(context.Background(), "ws"+strings.TrimPrefix(server.URL, "http"), "", 1280, 800)
	if err != nil {
		t.Fatal(err)
	}
	defer live.Close()

	want := PageInfo{URL: "https://b.example/", Title: "新页面"}
	deadline := time.After(3 * time.Second)
	for {
		select {
		case page := <-live.Pages():
			if page.URL == "https://ads.example/" {
				t.Fatalf("子框架的跳转不该改地址栏：%+v", page)
			}
			if page == want {
				return
			}
		case <-deadline:
			t.Fatalf("页面信息没有跟到新页面，最后是 %+v", live.Page())
		}
	}
}
