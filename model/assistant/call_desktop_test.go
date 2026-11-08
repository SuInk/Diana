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

	"github.com/SuInk/diana/model/callbridge"
	"github.com/SuInk/diana/model/desktopctl"
)

// fakeDesktopAgent 是只会回桌面指令的 agent：一个 客户端窗口，截图回固定内容，点击记下来。
func fakeDesktopAgent(t *testing.T) (*bridgeCallLink, <-chan desktopctl.Command) {
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
			var msg callbridge.Message
			if json.Unmarshal(data, &msg) != nil || msg.Type != callbridge.TypeDesktop {
				continue
			}
			var cmd desktopctl.Command
			_ = json.Unmarshal(msg.Params, &cmd)
			var payload any
			switch msg.Op {
			case desktopctl.OpWindowsList:
				payload = desktopctl.WindowsPayload{Windows: []desktopctl.WindowInfo{{ID: "100", AppName: "Chat", BundleID: "x11.chat", Title: "Chat", Active: true}}}
			case desktopctl.OpWindowScreenshot:
				payload = desktopctl.ScreenshotPayload{WindowID: cmd.WindowID, Mime: "image/png", Data: "iVBORw0KGgo=", Observation: 3}
			case desktopctl.OpWindowClick:
				clicks <- cmd
				payload = desktopctl.ActionResult{WindowID: cmd.WindowID, Op: msg.Op, OK: true}
			}
			body, _ := json.Marshal(payload)
			reply, _ := json.Marshal(callbridge.Message{Type: callbridge.TypeDesktopResult, ID: msg.ID, Data: body})
			_ = conn.WriteMessage(websocket.TextMessage, reply)
		}
	}))
	t.Cleanup(srv.Close)
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	link := &bridgeCallLink{conn: conn}
	return link, clicks
}

func TestCallDesktopBridgeRoundTrip(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	link, clicks := fakeDesktopAgent(t)
	calls := newBridgeCallSet(NewCallPlugin(), link)
	// agent 的回执由读循环交给 handle；这里直接把连接上的帧转过去。
	go func() {
		for {
			_, data, err := link.conn.ReadMessage()
			if err != nil {
				return
			}
			var msg callbridge.Message
			if json.Unmarshal(data, &msg) == nil {
				calls.handle(ctx, msg)
			}
		}
	}()
	var shot desktopctl.ScreenshotPayload
	if err := calls.desktop.call(ctx, desktopctl.OpWindowScreenshot, desktopctl.Command{WindowID: "100"}, &shot); err != nil || shot.Data != "iVBORw0KGgo=" {
		t.Fatalf("screenshot = %+v, %v", shot, err)
	}
	x, y := 740.0, 67.0
	if err := calls.desktop.call(ctx, desktopctl.OpWindowClick, desktopctl.Command{WindowID: "100", X: &x, Y: &y}, nil); err != nil {
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
}

func TestCallScreenToolNeedsPlugin(t *testing.T) {
	runtime, _, _ := newVoiceCallTestRuntime(t, "")
	tool := &callScreenTool{runtime: runtime, event: MessageEvent{ProfileID: "bot"}}
	if _, err := tool.Run(context.Background(), map[string]any{"action": "screenshot", "window_id": "1"}); err == nil || !strings.Contains(err.Error(), "通话插件没开") {
		t.Fatalf("err = %v", err)
	}
	if _, err := tool.Run(context.Background(), map[string]any{"action": "rm"}); err == nil {
		t.Fatal("unknown action should fail")
	}
}

func TestBridgeCallOutgoingOnlyClaimsRegisteredDials(t *testing.T) {
	link, _ := fakeBridgeCallAgent(t)
	calls := newBridgeCallSet(NewCallPlugin(), link)
	// 没登记过：有人在 noVNC 里拨的，不归机器人管。
	calls.outgoing(callbridge.Message{Type: callbridge.TypeOutgoing, Call: "o1"})
	if _, ok := calls.answered["o1"]; ok {
		t.Fatal("unregistered outgoing call was claimed")
	}
	calls.expectOutgoing(bridgeCallTarget{profileID: "bot", ownerID: "10001", groupID: "20002", dialed: true, reason: "提醒开会"})
	calls.outgoing(callbridge.Message{Type: callbridge.TypeOutgoing, Call: "o2"})
	target, ok := calls.answered["o2"]
	if !ok || !target.dialed || target.groupID != "20002" {
		t.Fatalf("registered dial not claimed: %+v", target)
	}
	// 登记只用一次。
	calls.outgoing(callbridge.Message{Type: callbridge.TypeOutgoing, Call: "o3"})
	if _, ok := calls.answered["o3"]; ok {
		t.Fatal("one intent claimed two calls")
	}
	// 过期的登记不算。
	calls.expectOutgoing(bridgeCallTarget{profileID: "bot", dialed: true, reason: "x"})
	calls.intent.at = time.Now().Add(-bridgeCallIntentTTL - time.Second)
	calls.outgoing(callbridge.Message{Type: callbridge.TypeOutgoing, Call: "o4"})
	if _, ok := calls.answered["o4"]; ok {
		t.Fatal("stale intent claimed a call")
	}
	if opener := bridgeCallOpener(target); !strings.Contains(opener, "提醒开会") || !strings.Contains(opener, "先开口") {
		t.Fatalf("opener = %q", opener)
	}
}

func TestPhoneCallToolNeedsPlugin(t *testing.T) {
	runtime, _, _ := newVoiceCallTestRuntime(t, "")
	tool := &phoneCallTool{runtime: runtime, event: MessageEvent{ProfileID: "bot", Kind: EventKindPrivate}}
	if _, err := tool.Run(context.Background(), map[string]any{"reason": "想你了"}); err == nil || !strings.Contains(err.Error(), "插件没开") {
		t.Fatalf("err = %v", err)
	}
	if _, err := tool.Run(context.Background(), map[string]any{}); err == nil {
		t.Fatal("reason is required")
	}
}
