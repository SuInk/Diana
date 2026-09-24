// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// NoneBot 连上以后不再读，TCP 缓冲区一满，没有期限的写会一直挂着，收消息的主链路
// 跟着卡在 writeMu 上。写要按期限失败，并把这条坏掉的连接关掉。
func TestNoneBotBridgeForwardEventDoesNotBlockOnStalledPeer(t *testing.T) {
	original := nonebotBridgeWriteTimeout
	nonebotBridgeWriteTimeout = 100 * time.Millisecond
	t.Cleanup(func() { nonebotBridgeWriteTimeout = original })

	release := make(chan struct{})
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		<-release // 不读任何帧
	}))
	defer server.Close()
	defer close(release)

	bridge := NewNoneBotBridge(NoneBotBridgeConfig{Enabled: true, Endpoint: "ws" + strings.TrimPrefix(server.URL, "http")}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	bridge.Start(ctx)
	defer bridge.Stop()
	waitForCondition(t, 3*time.Second, func() bool { return bridge.Status().Connected })

	event := MessageEvent{Kind: EventKindPrivate, UserID: "10001", MessageID: "big", RawMessage: strings.Repeat("x", 1<<20)}
	// 64 帧、每帧 1 MiB，远超本机回环的发送缓冲区，一定会有写不出去的时候。
	// 每一次转发都必须在写期限附近返回；写失败后连接会被关掉、读循环再重连。
	slow := make(chan time.Duration, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 64; i++ {
			start := time.Now()
			bridge.ForwardEvent(event)
			if elapsed := time.Since(start); elapsed > 2*time.Second {
				slow <- elapsed
				return
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("ForwardEvent blocked on a peer that stopped reading")
	}
	select {
	case elapsed := <-slow:
		t.Fatalf("one ForwardEvent took %s", elapsed)
	default:
	}
}
