package webui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/SuInk/diana/model/assistant"

	"github.com/gin-gonic/gin"
)

// TestEventHubPublishAndUnsubscribe 验证对应功能场景。
func TestEventHubPublishAndUnsubscribe(t *testing.T) {
	hub := NewEventHub()
	sub := hub.Subscribe()
	if hub.SubscriberCount() != 1 {
		t.Fatalf("SubscriberCount = %d, want 1", hub.SubscriberCount())
	}

	hub.PublishBotEvent(assistant.EventRecord{Kind: assistant.EventKindGroup, Text: "hello"})
	select {
	case message := <-sub:
		if message.Event != "bot_event" {
			t.Fatalf("event = %q, want bot_event", message.Event)
		}
		if !strings.Contains(string(message.Data), "hello") {
			t.Fatalf("data = %s", message.Data)
		}
	case <-time.After(time.Second):
		t.Fatal("no message received")
	}

	hub.Unsubscribe(sub)
	if hub.SubscriberCount() != 0 {
		t.Fatalf("SubscriberCount = %d, want 0", hub.SubscriberCount())
	}
	// 重复退订不应 panic。
	hub.Unsubscribe(sub)
}

// TestEventHubDropsWhenSubscriberSlow 验证对应功能场景。
func TestEventHubDropsWhenSubscriberSlow(t *testing.T) {
	hub := NewEventHub()
	sub := hub.Subscribe()
	defer hub.Unsubscribe(sub)

	// 写满缓冲后继续 Publish 不应阻塞。
	done := make(chan struct{})
	go func() {
		for i := 0; i < 100; i++ {
			hub.Publish("bot_event", gin.H{"seq": i})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Publish blocked on slow subscriber")
	}
}

// TestEventStreamSendsInitialSnapshot 验证对应功能场景。
func TestEventStreamSendsInitialSnapshot(t *testing.T) {
	hub := NewEventHub()
	collector := NewStatsCollector()
	provider := &fakeStatusProvider{status: assistant.RuntimeStatus{
		Running: true,
		Channel: assistant.ChannelStatus{Connected: true},
	}}
	handler := NewEventStreamHandler(hub, provider, collector)

	gin.SetMode(gin.TestMode)
	router := gin.New()
	handler.Register(router)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest(http.MethodGet, "/api/events", nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if contentType := rec.Header().Get("Content-Type"); !strings.HasPrefix(contentType, "text/event-stream") {
		t.Fatalf("Content-Type = %q", contentType)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "event: status") {
		t.Fatalf("body missing status event: %s", body)
	}
	if !strings.Contains(body, "event: stats") {
		t.Fatalf("body missing stats event: %s", body)
	}
	if !strings.Contains(body, `"running":true`) {
		t.Fatalf("body missing running flag: %s", body)
	}
}
