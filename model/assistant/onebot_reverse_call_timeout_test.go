// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

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

// 反向连接的桥收下请求却一直不回时，没带期限的调用（WebUI 直接传请求 ctx）以前会
// 一直等下去；正向连接一直有 30 秒上限，这里补上同样的默认值。
func TestReverseCallAPIDefaultTimeoutWhenBridgeNeverAnswers(t *testing.T) {
	original := oneBotReverseCallTimeout
	oneBotReverseCallTimeout = 100 * time.Millisecond
	t.Cleanup(func() { oneBotReverseCallTimeout = original })

	release := make(chan struct{})
	upgrader := websocket.Upgrader{}
	bridge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		// 读走请求，但从不回响应。
		go func() {
			for {
				if _, _, err := conn.ReadMessage(); err != nil {
					return
				}
			}
		}()
		<-release
	}))
	defer bridge.Close()
	defer close(release)

	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(bridge.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	server := NewOneBotReverseServer(OneBotConfig{Endpoint: "ws://127.0.0.1:18080/onebot/v11/ws"})
	server.connMu.Lock()
	server.conn = conn
	server.connMu.Unlock()

	started := time.Now()
	_, err = server.CallAPI(context.Background(), "get_status", nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("CallAPI error = %v, want deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("CallAPI waited %s", elapsed)
	}

	// 调用方自己给了期限就按它的来，不被默认值截短。
	oneBotReverseCallTimeout = 10 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	started = time.Now()
	_, _ = server.CallAPI(ctx, "get_status", nil)
	if elapsed := time.Since(started); elapsed < 250*time.Millisecond {
		t.Fatalf("caller deadline was cut short to %s", elapsed)
	}
}
