package assistant

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestOneBotHTTPTransport(t *testing.T) {
	calls := make(chan map[string]any, 4)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.Header.Get("Authorization") != "Bearer api-token" || r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("unexpected API request: %s %v", r.Method, r.Header)
		}
		switch r.URL.Path {
		case "/prefix/get_login_info":
			_, _ = w.Write([]byte(`{"status":"ok","retcode":0,"data":{"user_id":42}}`))
		case "/prefix/send_group_msg":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			calls <- body
			_, _ = w.Write([]byte(`{"status":"ok","retcode":0,"data":{"message_id":123}}`))
		case "/prefix/get_group_list":
			_, _ = w.Write([]byte(`{"status":"ok","retcode":0,"data":[{"group_id":1}]}`))
		case "/prefix/fail":
			_, _ = w.Write([]byte(`{"status":"failed","retcode":100,"wording":"denied"}`))
		case "/prefix/bad_json":
			_, _ = w.Write([]byte(`not json`))
		default:
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	}))
	defer api.Close()
	c := NewOneBotHTTPChannel(OneBotConfig{Endpoint: api.URL + "/prefix", AccessToken: "api-token", HTTPSecret: "event-secret"})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := make(chan MessageEvent, 1)
	done := make(chan error, 1)
	go func() {
		done <- c.Connect(ctx, func(_ context.Context, e MessageEvent) error { events <- e; return nil })
	}()
	deadline := time.Now().Add(3 * time.Second)
	for !c.Status().Connected && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !c.Status().Connected || c.Status().SelfID != "42" {
		t.Fatalf("status: %+v", c.Status())
	}
	body := `{"post_type":"message","message_type":"group","self_id":42,"group_id":123,"user_id":456,"message_id":789,"message":[{"type":"text","data":{"text":"hello"}}]}`
	post := func(raw, signature string) int {
		req := httptest.NewRequest("POST", "/onebot/v11/http", strings.NewReader(raw))
		req.Header.Set("X-Signature", signature)
		w := httptest.NewRecorder()
		c.ServeHTTP(w, req)
		return w.Code
	}
	sign := func(raw string) string {
		mac := hmac.New(sha1.New, []byte("event-secret"))
		_, _ = mac.Write([]byte(raw))
		return "sha1=" + hex.EncodeToString(mac.Sum(nil))
	}
	if got := post(body, ""); got != 401 {
		t.Fatalf("unsigned event status=%d", got)
	}
	if got := post(body+" ", sign(body)); got != 401 {
		t.Fatalf("tampered event status=%d", got)
	}
	if got := post("{", sign("{")); got != 400 {
		t.Fatalf("malformed event status=%d", got)
	}
	if got := post(body, sign(body)); got != 204 {
		t.Fatalf("valid event status=%d", got)
	}
	select {
	case event := <-events:
		if event.GroupID != "123" || event.UserID != "456" {
			t.Fatalf("event=%+v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("missing HTTP event")
	}
	result, err := c.SendWithResult(ctx, OutgoingMessage{GroupID: "123", Text: "reply", ReplyMessageID: "789"})
	if err != nil || stringifyID(result["message_id"]) != "123" {
		t.Fatalf("result=%v err=%v", result, err)
	}
	call := <-calls
	if call["group_id"] != float64(123) || len(call["message"].([]any)) != 2 {
		t.Fatalf("message=%v", call)
	}
	list, err := c.CallAPI(ctx, "get_group_list", nil)
	if err != nil || len(list["items"].([]any)) != 1 {
		t.Fatalf("list=%v err=%v", list, err)
	}
	for _, action := range []string{"fail", "bad_json", "unavailable", "../get_login_info"} {
		if _, err := c.CallAPI(ctx, action, nil); err == nil {
			t.Errorf("%s should fail", action)
		}
	}
	_ = c.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("HTTP close did not stop channel")
	}
	if got := post(body, sign(body)); got != 503 {
		t.Fatalf("stopped event status=%d", got)
	}
}

func TestOneBotHTTPConfigRoundTrip(t *testing.T) {
	cfg := DefaultBotConfig()
	cfg.OneBotTransport, cfg.OneBotHTTPURL, cfg.OneBotHTTPSecret = "http", "http://localhost:5700", "secret"
	payload := PayloadFromConfig(cfg)
	if payload.OneBotHTTPSecret != "" || !payload.OneBotHTTPSecretConfigured {
		t.Fatal("secret not masked")
	}
	restored := ConfigFromPayload(payload, cfg)
	if restored.OneBotHTTPSecret != "secret" || restored.OneBotTransport != "http" || restored.OneBotHTTPURL != cfg.OneBotHTTPURL {
		t.Fatalf("transport fields not preserved")
	}
	if PayloadFromConfigWithSecrets(cfg).OneBotHTTPSecret != "secret" {
		t.Fatal("explicit reveal missing secret")
	}
	if err := restored.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"forward_ws", "http", "invalid"} {
		bad := DefaultBotConfig()
		bad.Enabled = true
		bad.OneBotTransport = mode
		if err := bad.Validate(); err == nil {
			t.Errorf("incomplete %s config accepted", mode)
		}
	}
	if DefaultBotConfig().WithDefaults().OneBotTransport != OneBotTransportReverseWS {
		t.Fatal("legacy default changed")
	}
}

func TestOneBotForwardWSRoundTripAndCancellation(t *testing.T) {
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer ws-token" {
			t.Error("missing WS token")
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"post_type":"message","message_type":"private","self_id":42,"user_id":456,"message_id":789,"message":"hello"}`))
		for {
			var req map[string]any
			if err := conn.ReadJSON(&req); err != nil {
				return
			}
			if req["action"] == "wait" {
				continue
			}
			_ = conn.WriteJSON(map[string]any{"status": "ok", "retcode": 0, "data": map[string]any{"message_id": 123}, "echo": req["echo"]})
		}
	}))
	defer server.Close()
	c := NewOneBotChannel(OneBotConfig{Endpoint: "ws" + strings.TrimPrefix(server.URL, "http"), AccessToken: "ws-token"})
	for i := 0; i < 2; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		events := make(chan MessageEvent, 1)
		done := make(chan error, 1)
		go func() {
			done <- c.Connect(ctx, func(_ context.Context, e MessageEvent) error { events <- e; return nil })
		}()
		select {
		case <-events:
		case <-time.After(time.Second):
			cancel()
			t.Fatal("WS event missing")
		}
		result, err := c.SendWithResult(ctx, OutgoingMessage{UserID: "456", Text: "hello"})
		if err != nil || stringifyID(result["message_id"]) != "123" {
			cancel()
			t.Fatalf("result=%v err=%v", result, err)
		}
		cancel()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("idle read did not unblock on cancellation")
		}
		if c.Status().Connected {
			t.Fatal("WS remained connected")
		}
	}
}

func TestOneBotHTTPRejectsOversizedCallback(t *testing.T) {
	c := NewOneBotHTTPChannel(OneBotConfig{HTTPSecret: "secret"})
	req := httptest.NewRequest("POST", "/", bytes.NewReader(make([]byte, maxOneBotWebSocketFrameBytes+1)))
	w := httptest.NewRecorder()
	c.ServeHTTP(w, req)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status=%d", w.Code)
	}
}
