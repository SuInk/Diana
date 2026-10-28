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
