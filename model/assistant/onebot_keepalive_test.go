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

// 反向监听器现在强制要 token，握手时得带上。
const keepaliveTestToken = "0123456789abcdef"

// shrinkOneBotKeepalive 把心跳参数缩到毫秒级，用完还原。
func shrinkOneBotKeepalive(t *testing.T, read, ping time.Duration) {
	t.Helper()
	readWas, pingWas := oneBotReadTimeout, oneBotPingInterval
	oneBotReadTimeout, oneBotPingInterval = read, ping
	t.Cleanup(func() { oneBotReadTimeout, oneBotPingInterval = readWas, pingWas })
}

// startReverseTestServer 起一个真实的 HTTP server 接反向连接。
func startReverseTestServer(t *testing.T) (*OneBotReverseServer, string) {
	t.Helper()
	server := NewOneBotReverseServer(OneBotConfig{AccessToken: keepaliveTestToken})
	server.mu.Lock()
	server.handler = func(context.Context, MessageEvent) error { return nil }
	server.mu.Unlock()
	httpServer := httptest.NewServer(http.HandlerFunc(server.ServeHTTP))
	t.Cleanup(httpServer.Close)
	t.Cleanup(func() { _ = server.Close() })
	return server, "ws" + strings.TrimPrefix(httpServer.URL, "http") + "/onebot/v11/ws"
}

func dialReverse(t *testing.T, endpoint string) *websocket.Conn {
	t.Helper()
	header := http.Header{}
	header.Set("X-Self-ID", "10001")
	header.Set("Authorization", "Bearer "+keepaliveTestToken)
	conn, _, err := websocket.DefaultDialer.Dial(endpoint, header)
	if err != nil {
		t.Fatalf("握手失败：%v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func waitUntil(t *testing.T, timeout time.Duration, want func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if want() {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return want()
}

// 对端被硬断（容器被 kill、宿主机休眠、NAT 表项过期）时 TCP 不会来 FIN，
// ReadMessage 就一直阻塞。反向模式只有一个连接位，位子被这条死连接占着，NapCat
// 重连上来只拿得到 409，机器人整段时间收不到消息——线上就是这么停的。
// 现在心跳会把它判死并交出连接位。
func TestReverseKeepaliveFreesSlotWhenPeerGoesSilent(t *testing.T) {
	shrinkOneBotKeepalive(t, 150*time.Millisecond, 30*time.Millisecond)
	server, endpoint := startReverseTestServer(t)

	// 连上之后再也不读：客户端不读就不会自动回 pong，等同于一条死掉的连接。
	dead := dialReverse(t, endpoint)
	if !waitUntil(t, time.Second, func() bool { return server.Status().Connected }) {
		t.Fatal("连接没有登记上")
	}

	if !waitUntil(t, 3*time.Second, func() bool { return !server.Status().Connected }) {
		t.Fatal("死连接没有被判死，连接位仍然被占着")
	}
	if got := server.Status().LastError; !strings.Contains(got, "没有任何响应") {
		t.Fatalf("状态里看不出是心跳超时：%q", got)
	}
	_ = dead.Close()

	// 位子交出来了，重连要能接上。
	dialReverse(t, endpoint)
	if !waitUntil(t, time.Second, func() bool { return server.Status().Connected }) {
		t.Fatal("重连没有被接受，连接位没有真正释放")
	}
}

// 反过来不能误杀：对端只是闲着没消息，但 ping 有回应，就该一直连着。
func TestReverseKeepaliveKeepsIdleButLiveClientConnected(t *testing.T) {
	shrinkOneBotKeepalive(t, 150*time.Millisecond, 30*time.Millisecond)
	server, endpoint := startReverseTestServer(t)

	conn := dialReverse(t, endpoint)
	// 客户端读循环：gorilla 在读的时候自动回 pong，一条消息都不发。
	go func() {
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}()
	if !waitUntil(t, time.Second, func() bool { return server.Status().Connected }) {
		t.Fatal("连接没有登记上")
	}

	time.Sleep(600 * time.Millisecond) // 远超读超时，全靠 pong 续命
	if !server.Status().Connected {
		t.Fatalf("空闲但活着的连接被误杀了：%q", server.Status().LastError)
	}
}

// 不回 pong 的对端只要还在推心跳元事件，也算活着——协议要求回 pong，但没必要赌。
func TestReverseKeepaliveAcceptsHeartbeatInsteadOfPong(t *testing.T) {
	shrinkOneBotKeepalive(t, 200*time.Millisecond, time.Hour) // 不发 ping，只看数据帧
	server, endpoint := startReverseTestServer(t)

	conn := dialReverse(t, endpoint)
	stop := make(chan struct{})
	t.Cleanup(func() { close(stop) })
	go func() {
		ticker := time.NewTicker(40 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				frame := map[string]any{"post_type": "meta_event", "meta_event_type": "heartbeat", "self_id": 10001}
				if err := conn.WriteJSON(frame); err != nil {
					return
				}
			}
		}
	}()

	time.Sleep(700 * time.Millisecond)
	if !server.Status().Connected {
		t.Fatalf("还在推心跳的连接被误杀了：%q", server.Status().LastError)
	}
}
