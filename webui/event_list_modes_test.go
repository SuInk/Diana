package webui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/SuInk/diana/model/assistant"
	"github.com/SuInk/diana/model/storage"
	"github.com/gin-gonic/gin"
)

func TestEventListModesAndSummaryCache(t *testing.T) {
	s, err := storage.NewSQLiteStore(filepath.Join(t.TempDir(), "events.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	for _, id := range []string{"a", "b", "c"} {
		_, _, err := s.EnqueueInboundEvent(ctx, "group:g", assistant.MessageEvent{ProfileID: id, Kind: assistant.EventKindGroup, GroupID: "g", UserID: "u", MessageID: id, Time: time.Now().Unix()})
		if err != nil {
			t.Fatal(err)
		}
	}
	h := &BotHandler{sqlite: s}
	r := gin.New()
	r.GET("/events", h.listEvents)
	get := func(query string) assistantEventsResponse {
		t.Helper()
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/events?range=24h&"+query, nil))
		if w.Code != 200 {
			t.Fatalf("%d %s", w.Code, w.Body.String())
		}
		var v assistantEventsResponse
		if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	light := get("mode=list&limit=2")
	if len(light.Events) != 2 || !light.HasMore || len(light.Groups) != 0 || light.Total != 0 {
		t.Fatalf("not lightweight: %+v", light)
	}
	if last := get("mode=list&limit=2&page=2"); len(last.Events) != 1 || last.HasMore {
		t.Fatalf("wrong final page: %+v", last)
	}
	summary := get("mode=summary")
	if len(summary.Events) != 0 || summary.Total != 3 || len(summary.Groups) != 1 {
		t.Fatalf("bad summary: %+v", summary)
	}
	if scoped := get("mode=summary&profile=a"); scoped.Total != 1 {
		t.Fatalf("cache scope leak: %+v", scoped)
	}
	if scoped := get("mode=summary&profile=missing"); scoped.Total != 0 {
		t.Fatalf("cache scope leak: %+v", scoped)
	}
	_, _, err = s.EnqueueInboundEvent(ctx, "group:g", assistant.MessageEvent{Kind: assistant.EventKindGroup, GroupID: "g", UserID: "u", MessageID: "new", Time: time.Now().Unix()})
	if err != nil {
		t.Fatal(err)
	}
	if cached := get("mode=summary"); cached.Total != 3 {
		t.Fatal("summary did not use bounded cache")
	}
	h.eventSummaryMu.Lock()
	for k, entry := range h.eventSummaryCache {
		entry.expires = time.Now().Add(-time.Second)
		h.eventSummaryCache[k] = entry
	}
	h.eventSummaryMu.Unlock()
	if fresh := get("mode=summary"); fresh.Total != 4 {
		t.Fatal("expired summary not refreshed")
	}
	if full := get("mode=full"); full.Total != 4 || len(full.Events) != 4 {
		t.Fatal("legacy full mode changed")
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/events?mode=bad", nil))
	if w.Code != 400 {
		t.Fatalf("invalid mode accepted: %d", w.Code)
	}
}
