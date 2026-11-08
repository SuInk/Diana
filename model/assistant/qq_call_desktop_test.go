// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/SuInk/diana/model/desktopctl"
	"github.com/SuInk/diana/model/qqcall"
)

// fakeDesktopAgent 是只会回桌面指令的 agent：一个 QQ 窗口，截图回固定内容，点击记下来。
func fakeDesktopAgent(t *testing.T) (*qqCallLink, <-chan desktopctl.Command) {
	t.Helper()
	clicks := make(chan desktopctl.Command, 4)
	upgrader := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var msg qqcall.Message
			if json.Unmarshal(data, &msg) != nil || msg.Type != qqcall.TypeDesktop {
				continue
			}
			var cmd desktopctl.Command
			_ = json.Unmarshal(msg.Params, &cmd)
			var payload any
			switch msg.Op {
			case desktopctl.OpWindowsList:
				payload = desktopctl.WindowsPayload{Windows: []desktopctl.WindowInfo{{ID: "100", AppName: "QQ", BundleID: "x11.qq", Title: "QQ", Active: true}}}
			case desktopctl.OpWindowScreenshot:
				payload = desktopctl.ScreenshotPayload{WindowID: cmd.WindowID, Mime: "image/png", Data: "iVBORw0KGgo=", Observation: 3}
			case desktopctl.OpWindowClick:
				clicks <- cmd
				payload = desktopctl.ActionResult{WindowID: cmd.WindowID, Op: msg.Op, OK: true}
			}
			body, _ := json.Marshal(payload)
			reply, _ := json.Marshal(qqcall.Message{Type: qqcall.TypeDesktopResult, ID: msg.ID, Data: body})
			_ = conn.WriteMessage(websocket.TextMessage, reply)
		}
	}))
	t.Cleanup(srv.Close)
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	link := &qqCallLink{conn: conn}
	return link, clicks
}

func TestQQCallAgentScreenJoinsDesktopControl(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	registry := desktopctl.NewRegistry(ctx, nil)
	if _, err := registry.SetPolicy(ctx, desktopctl.Policy{Enabled: true, WriteEnabled: true, AllowedApps: []string{"x11.qq"}}); err != nil {
		t.Fatal(err)
	}
	hub := desktopctl.NewHub(registry)
	runtime := &Runtime{}
	runtime.SetDesktopControl(hub)

	link, clicks := fakeDesktopAgent(t)
	calls := newQQCallSet(NewQQVoiceCallPlugin(), link)
	// agent 的回执由读循环交给 handle；这里直接把连接上的帧转过去。
	go func() {
		for {
			_, data, err := link.conn.ReadMessage()
			if err != nil {
				return
			}
			var msg qqcall.Message
			if json.Unmarshal(data, &msg) == nil {
				calls.handle(ctx, msg)
			}
		}
	}()
	calls.desktop.attach(ctx, runtime)
	if conns := hub.Connections(); len(conns) != 1 || conns[0].HelperName != "QQ 容器" {
		t.Fatalf("connections = %+v", conns)
	}
	result, err := hub.Dispatch(ctx, desktopctl.Command{Op: desktopctl.OpWindowScreenshot, WindowID: "100"})
	if err != nil || !strings.Contains(string(result.Data), "iVBORw0KGgo=") {
		t.Fatalf("screenshot = %s, %v", result.Data, err)
	}
	x, y := 740.0, 67.0
	if _, err := hub.Dispatch(ctx, desktopctl.Command{Op: desktopctl.OpWindowClick, WindowID: "100", X: &x, Y: &y}); err != nil {
		t.Fatal(err)
	}
	select {
	case cmd := <-clicks:
		if *cmd.X != 740 || *cmd.Y != 67 {
			t.Fatalf("click = %+v", cmd)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("click never reached the agent")
	}
	// agent 断线：连接从桌面控制里注销。
	calls.desktop.detach()
	if conns := hub.Connections(); len(conns) != 0 {
		t.Fatalf("connection should be gone after detach: %+v", conns)
	}
}

func TestQQCallOutgoingOnlyClaimsRegisteredDials(t *testing.T) {
	link, _ := fakeQQCallAgent(t)
	calls := newQQCallSet(NewQQVoiceCallPlugin(), link)
	// 没登记过：有人在 noVNC 里拨的，不归机器人管。
	calls.outgoing(qqcall.Message{Type: qqcall.TypeOutgoing, Call: "o1"})
	if _, ok := calls.answered["o1"]; ok {
		t.Fatal("unregistered outgoing call was claimed")
	}
	calls.expectOutgoing(qqCallTarget{profileID: "bot", ownerID: "10001", groupID: "20002", dialed: true, reason: "提醒开会"})
	calls.outgoing(qqcall.Message{Type: qqcall.TypeOutgoing, Call: "o2"})
	target, ok := calls.answered["o2"]
	if !ok || !target.dialed || target.groupID != "20002" {
		t.Fatalf("registered dial not claimed: %+v", target)
	}
	// 登记只用一次。
	calls.outgoing(qqcall.Message{Type: qqcall.TypeOutgoing, Call: "o3"})
	if _, ok := calls.answered["o3"]; ok {
		t.Fatal("one intent claimed two calls")
	}
	// 过期的登记不算。
	calls.expectOutgoing(qqCallTarget{profileID: "bot", dialed: true, reason: "x"})
	calls.intent.at = time.Now().Add(-qqCallIntentTTL - time.Second)
	calls.outgoing(qqcall.Message{Type: qqcall.TypeOutgoing, Call: "o4"})
	if _, ok := calls.answered["o4"]; ok {
		t.Fatal("stale intent claimed a call")
	}
	if opener := qqCallOpener(target); !strings.Contains(opener, "提醒开会") || !strings.Contains(opener, "先开口") {
		t.Fatalf("opener = %q", opener)
	}
}

func TestQQCallToolNeedsPluginAndDesktop(t *testing.T) {
	runtime, _, _ := newVoiceCallTestRuntime(t, "")
	tool := &qqCallTool{runtime: runtime, event: MessageEvent{ProfileID: "bot", Kind: EventKindPrivate}}
	if _, err := tool.Run(context.Background(), map[string]any{"reason": "想你了"}); err == nil || !strings.Contains(err.Error(), "插件没开") {
		t.Fatalf("err = %v", err)
	}
	if _, err := tool.Run(context.Background(), map[string]any{}); err == nil {
		t.Fatal("reason is required")
	}
}
