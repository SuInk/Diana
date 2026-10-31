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

func TestQQOfficialReadTimeoutCoversTwoHeartbeats(t *testing.T) {
	if got := qqOfficialHeartbeatInterval(0); got != 30*time.Second {
		t.Fatalf("missing interval = %v, want 30s", got)
	}
	if got := qqOfficialHeartbeatInterval(41250 * time.Millisecond); got != 41250*time.Millisecond {
		t.Fatalf("gateway interval = %v, want 41.25s", got)
	}
	if got := qqOfficialReadTimeout(30 * time.Second); got != 65*time.Second {
		t.Fatalf("read timeout = %v, want 65s", got)
	}
}

// 网关握手后不再发任何报文（半开连接）时，runSession 应按读超时退出而不是一直卡住。
func TestQQOfficialRunSessionExitsOnSilentGateway(t *testing.T) {
	upgrader := websocket.Upgrader{}
	release := make(chan struct{})
	mux := http.NewServeMux()
	var server *httptest.Server
	mux.HandleFunc("/gateway", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"url":"ws` + strings.TrimPrefix(server.URL, "http") + `/ws"}`))
	})
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.WriteJSON(map[string]any{"op": qqOpHello, "d": map[string]any{"heartbeat_interval": 60000}})
		var identify map[string]any
		_ = conn.ReadJSON(&identify)
		<-release
	})
	server = httptest.NewServer(mux)
	defer server.Close()
	defer close(release)

	channel := NewQQOfficialChannel(QQOfficialConfig{AppID: "app", AppSecret: "secret"})
	channel.apiBaseOverride = server.URL
	channel.readTimeoutOverride = 200 * time.Millisecond
	channel.tokens = &platformTokenCache{fetch: func(context.Context) (string, time.Duration, error) {
		return "token", time.Hour, nil
	}}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- channel.runSession(ctx) }()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "网关连接中断") {
			t.Fatalf("runSession error = %v, want gateway read timeout", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("runSession did not exit on a silent gateway")
	}
}

// 读循环卡在某次处理里时，网关早断了也不会出错返回；状态要按收消息停滞
// 改报离线，不能一直停在 READY 时设的「已连接」。
func TestQQOfficialStuckReadLoopReportsOffline(t *testing.T) {
	upgrader := websocket.Upgrader{}
	mux := http.NewServeMux()
	var server *httptest.Server
	mux.HandleFunc("/gateway", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"url":"ws` + strings.TrimPrefix(server.URL, "http") + `/ws"}`))
	})
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.WriteJSON(map[string]any{"op": qqOpHello, "d": map[string]any{"heartbeat_interval": 60000}})
		var identify map[string]any
		_ = conn.ReadJSON(&identify)
		_ = conn.WriteJSON(map[string]any{"op": qqOpDispatch, "s": 1, "t": "READY", "d": map[string]any{"session_id": "s1", "user": map[string]any{"id": "bot-1"}}})
		_ = conn.WriteJSON(map[string]any{"op": qqOpDispatch, "s": 2, "t": "C2C_MESSAGE_CREATE", "d": map[string]any{"id": "m-1", "content": "hi", "author": map[string]any{"user_openid": "u-1"}}})
	})
	server = httptest.NewServer(mux)
	defer server.Close()

	channel := NewQQOfficialChannel(QQOfficialConfig{AppID: "app", AppSecret: "secret"})
	channel.apiBaseOverride = server.URL
	channel.tokens = &platformTokenCache{fetch: func(context.Context) (string, time.Duration, error) {
		return "token", time.Hour, nil
	}}
	stuck := make(chan struct{})
	release := make(chan struct{})
	defer close(release)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	go func() {
		_ = channel.Connect(ctx, func(context.Context, MessageEvent) error {
			close(stuck)
			<-release
			return nil
		})
	}()
	select {
	case <-stuck:
	case <-time.After(3 * time.Second):
		t.Fatal("message was never dispatched")
	}
	if !channel.Status().Connected {
		t.Fatal("status should still be connected right after READY")
	}
	// 线上时限是读超时加一个心跳周期（一分多钟），这里缩短免得干等。
	channel.live.setLimit(200 * time.Millisecond)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if status := channel.Status(); !status.Connected {
			if !strings.Contains(status.LastError, "停滞") {
				t.Fatalf("LastError = %q, want the stall explained", status.LastError)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("status stayed connected while the read loop was stuck")
}

func TestReceiveLivenessOnlyFlagsStalledConnections(t *testing.T) {
	var live receiveLiveness
	now := time.Now()
	up := ChannelStatus{Connected: true}
	if got := live.apply("test", up, now); !got.Connected {
		t.Fatal("before any progress is recorded the status must pass through")
	}
	live.setLimit(time.Minute)
	live.mark(now)
	if got := live.apply("test", up, now.Add(59*time.Second)); !got.Connected {
		t.Fatal("progress within the limit must stay connected")
	}
	if got := live.apply("test", up, now.Add(2*time.Minute)); got.Connected || got.LastError == "" {
		t.Fatalf("stalled status = %+v, want offline with a reason", got)
	}
	if got := live.apply("test", ChannelStatus{LastError: "auth failed"}, now.Add(2*time.Minute)); got.LastError != "auth failed" {
		t.Fatalf("an already-offline status keeps its own error, got %q", got.LastError)
	}
	live.reset()
	if got := live.apply("test", up, now.Add(2*time.Minute)); !got.Connected {
		t.Fatal("after reset the next connection starts fresh")
	}
}
