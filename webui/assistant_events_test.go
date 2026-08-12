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

func TestAssistantEventsEndpointReturnsDurableDecisionReasons(t *testing.T) {
	store, err := storage.NewSQLiteStore(filepath.Join(t.TempDir(), "assistant-events.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()

	ctx := context.Background()
	event := assistant.MessageEvent{
		Kind:       assistant.EventKindGroup,
		GroupID:    "10001",
		UserID:     "20002",
		MessageID:  "30003",
		SenderName: "测试成员",
		Time:       time.Now().Add(-time.Minute).Unix(),
		RawMessage: "为什么不回复",
	}
	if _, inserted, err := store.EnqueueInboundEvent(ctx, "group:10001", event); err != nil || !inserted {
		t.Fatalf("enqueue inserted=%v err=%v", inserted, err)
	}
	item, ok, err := store.ClaimNextInboundEvent(ctx, "events-test", time.Now().Add(time.Minute))
	if err != nil || !ok {
		t.Fatalf("claim ok=%v err=%v", ok, err)
	}
	if err := store.CompleteInboundEvent(ctx, item.ID, "events-test", "ignored_member_level"); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordInboundEventAudit(ctx, assistant.EventRecord{
		Kind:      event.Kind,
		GroupID:   event.GroupID,
		UserID:    event.UserID,
		MessageID: event.MessageID,
		Decision:  "not_replied",
		Reason:    "发送者群等级为 2，低于本群最低回复等级 5",
		Duration:  87,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendLog(ctx, storage.AppLogEntry{
		Action:    "qqbot.llm_usage",
		Target:    event.MessageID,
		CreatedAt: time.Now().UTC(),
		Metadata:  map[string]any{"input_tokens": 60, "output_tokens": 15},
	}); err != nil {
		t.Fatal(err)
	}

	gin.SetMode(gin.TestMode)
	router := gin.New()
	handler := &QQBotHandler{sqlite: store}
	router.GET("/api/assistant/events", handler.listEvents)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/assistant/events?range=24h&page=1&limit=20", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var response assistantEventsResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Total != 1 || response.NotReplied != 1 || len(response.Events) != 1 {
		t.Fatalf("response=%+v", response)
	}
	if response.LLMCalls != 1 || response.InputTokens != 60 || response.OutputTokens != 15 || response.TotalTokens != 75 {
		t.Fatalf("response token totals=%+v", response)
	}
	detail := response.Events[0]
	if detail.Decision != "not_replied" || detail.Handled || detail.Reason != "发送者群等级为 2，低于本群最低回复等级 5" || detail.DurationMS != 87 {
		t.Fatalf("detail=%+v", detail)
	}
	if detail.SenderName != "测试成员" || detail.Text != "为什么不回复" {
		t.Fatalf("message detail=%+v", detail)
	}
	if detail.LLMCalls != 1 || detail.TotalTokens != 75 {
		t.Fatalf("message token usage=%+v", detail)
	}
}

func TestAssistantEventsRangeValidation(t *testing.T) {
	now := time.Now()
	for _, rangeID := range []string{"1h", "24h", "7d", "30d", "all"} {
		since, ok := assistantEventsSince(rangeID, now)
		if !ok || (rangeID == "all") != since.IsZero() {
			t.Fatalf("range %q since=%v ok=%v", rangeID, since, ok)
		}
	}
	if _, ok := assistantEventsSince("90m", now); ok {
		t.Fatal("unsupported range accepted")
	}
}

func TestAssistantEventTraceEndpointReturnsDebugSteps(t *testing.T) {
	store, err := storage.NewSQLiteStore(filepath.Join(t.TempDir(), "assistant-event-trace.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	ctx := context.Background()
	event := assistant.MessageEvent{
		Platform:  "napcat",
		ProfileID: "qq-main",
		Kind:      assistant.EventKindGroup,
		GroupID:   "group-1",
		UserID:    "user-1",
		MessageID: "message-1",
		Time:      time.Now().Unix(),
	}
	eventID, inserted, err := store.EnqueueInboundEvent(ctx, "group:group-1", event)
	if err != nil || !inserted {
		t.Fatalf("enqueue inserted=%v err=%v", inserted, err)
	}
	if err := store.AppendLog(ctx, storage.AppLogEntry{
		Kind: storage.LogKindDebug, Action: "qqbot.debug_trace", Target: event.MessageID, Message: "模型请求完成",
		Metadata: map[string]any{
			"phase": "model_request", "platform": event.Platform, "profile_id": event.ProfileID,
			"kind": "group", "group_id": event.GroupID, "user_id": event.UserID, "message_id": event.MessageID,
		},
	}); err != nil {
		t.Fatal(err)
	}
	gin.SetMode(gin.TestMode)
	router := gin.New()
	handler := &QQBotHandler{sqlite: store}
	router.GET("/api/assistant/events/:id/trace", handler.eventTrace)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/assistant/events/"+eventID+"/trace", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var response assistantEventTraceResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.EventID != eventID || response.MessageID != event.MessageID || len(response.Steps) != 1 {
		t.Fatalf("response=%+v", response)
	}
}
