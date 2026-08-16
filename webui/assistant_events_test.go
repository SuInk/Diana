// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
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
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/assistant/events?range=24h&result=not_replied&page=1&limit=20", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var response assistantEventsResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Result != "not_replied" || response.Total != 1 || response.FilteredTotal != 1 || response.NotReplied != 1 || response.HasMore || len(response.Events) != 1 {
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

func TestAssistantEventImageEndpointServesCachedImageWithoutLeakingSource(t *testing.T) {
	store, err := storage.NewSQLiteStore(filepath.Join(t.TempDir(), "assistant-event-image.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()

	imagePath := filepath.Join(t.TempDir(), "cached.png")
	file, err := os.Create(imagePath)
	if err != nil {
		t.Fatal(err)
	}
	picture := image.NewRGBA(image.Rect(0, 0, 2, 2))
	picture.Set(0, 0, color.RGBA{R: 220, G: 40, B: 80, A: 255})
	if err := png.Encode(file, picture); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	event := assistant.MessageEvent{
		Platform: assistant.PlatformOneBotV11, Kind: assistant.EventKindGroup,
		GroupID: "group-1", UserID: "user-1", MessageID: "image-message", Time: time.Now().Unix(),
		RawMessage: "[CQ:image,summary=&#91;测试图片&#93;,url=https://expired.example/image.png]",
		Segments: []assistant.MessageSegment{{Type: "image", Data: map[string]string{
			"summary": "[测试图片]", "cached_file": imagePath, "url": "https://expired.example/image.png",
		}}},
	}
	eventID, inserted, err := store.EnqueueInboundEvent(context.Background(), "group:group-1", event)
	if err != nil || !inserted {
		t.Fatalf("enqueue inserted=%v err=%v", inserted, err)
	}

	gin.SetMode(gin.TestMode)
	router := gin.New()
	handler := &QQBotHandler{sqlite: store}
	router.GET("/api/assistant/events", handler.listEvents)
	router.GET("/api/assistant/events/:id/images/:index", handler.eventImage)

	listRecorder := httptest.NewRecorder()
	router.ServeHTTP(listRecorder, httptest.NewRequest(http.MethodGet, "/api/assistant/events?range=24h", nil))
	if listRecorder.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", listRecorder.Code, listRecorder.Body.String())
	}
	if strings.Contains(listRecorder.Body.String(), imagePath) || strings.Contains(listRecorder.Body.String(), "expired.example") || strings.Contains(listRecorder.Body.String(), "CQ:image") {
		t.Fatalf("event list leaked raw image source: %s", listRecorder.Body.String())
	}
	var response assistantEventsResponse
	if err := json.Unmarshal(listRecorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Events) != 1 || response.Events[0].Text != "" || len(response.Events[0].Images) != 1 {
		t.Fatalf("response=%+v", response)
	}

	imageRecorder := httptest.NewRecorder()
	imageURL := "/api/assistant/events/" + eventID + "/images/1"
	router.ServeHTTP(imageRecorder, httptest.NewRequest(http.MethodGet, imageURL, nil))
	if imageRecorder.Code != http.StatusOK || imageRecorder.Header().Get("Content-Type") != "image/png" {
		t.Fatalf("image status=%d type=%q body=%q", imageRecorder.Code, imageRecorder.Header().Get("Content-Type"), imageRecorder.Body.String())
	}
	if imageRecorder.Header().Get("X-Content-Type-Options") != "nosniff" || !strings.Contains(imageRecorder.Header().Get("Cache-Control"), "private") {
		t.Fatalf("image headers=%v", imageRecorder.Header())
	}
	want, err := os.ReadFile(imagePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(imageRecorder.Body.Bytes()) != string(want) {
		t.Fatal("event image body differs from cached file")
	}

	thumbnailRecorder := httptest.NewRecorder()
	router.ServeHTTP(thumbnailRecorder, httptest.NewRequest(http.MethodGet, imageURL+"?thumbnail=1", nil))
	if thumbnailRecorder.Code != http.StatusOK || thumbnailRecorder.Header().Get("Content-Type") != "image/png" {
		t.Fatalf("thumbnail status=%d type=%q body=%q", thumbnailRecorder.Code, thumbnailRecorder.Header().Get("Content-Type"), thumbnailRecorder.Body.String())
	}
	thumbnail, _, err := image.Decode(bytes.NewReader(thumbnailRecorder.Body.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if got := thumbnail.Bounds().Size(); got.X != 2 || got.Y != 2 {
		t.Fatalf("thumbnail size=%v", got)
	}

	missingRecorder := httptest.NewRecorder()
	router.ServeHTTP(missingRecorder, httptest.NewRequest(http.MethodGet, "/api/assistant/events/"+eventID+"/images/2", nil))
	if missingRecorder.Code != http.StatusNotFound {
		t.Fatalf("missing image status=%d body=%s", missingRecorder.Code, missingRecorder.Body.String())
	}
}

func TestEventRasterImageContentTypeRejectsSVG(t *testing.T) {
	for _, contentType := range []string{"image/png", "image/gif", "image/webp", "image/avif; charset=binary"} {
		if got, ok := eventRasterImageContentType(contentType); !ok || !strings.HasPrefix(contentType, got) {
			t.Fatalf("eventRasterImageContentType(%q) = %q, %v", contentType, got, ok)
		}
	}
	for _, contentType := range []string{"image/svg+xml", "text/html", "application/octet-stream", ""} {
		if got, ok := eventRasterImageContentType(contentType); ok || got != "" {
			t.Fatalf("eventRasterImageContentType(%q) = %q, %v", contentType, got, ok)
		}
	}
}

func TestEventImageThumbnailPreservesAspectRatioAndBounds(t *testing.T) {
	source := image.NewRGBA(image.Rect(0, 0, 640, 320))
	source.Set(0, 0, color.RGBA{R: 220, G: 40, B: 80, A: 255})
	var original bytes.Buffer
	if err := png.Encode(&original, source); err != nil {
		t.Fatal(err)
	}
	thumbnail, contentType, err := eventImageThumbnail(original.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if contentType != "image/png" || len(thumbnail) >= original.Len() {
		t.Fatalf("contentType=%q thumbnail=%d original=%d", contentType, len(thumbnail), original.Len())
	}
	decoded, _, err := image.Decode(bytes.NewReader(thumbnail))
	if err != nil {
		t.Fatal(err)
	}
	if got := decoded.Bounds().Size(); got.X != 192 || got.Y != 96 {
		t.Fatalf("thumbnail size=%v, want 192x96", got)
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

func TestAssistantEventsRejectsUnsupportedResultFilter(t *testing.T) {
	store, err := storage.NewSQLiteStore(filepath.Join(t.TempDir(), "assistant-events-filter.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()

	gin.SetMode(gin.TestMode)
	router := gin.New()
	handler := &QQBotHandler{sqlite: store}
	router.GET("/api/assistant/events", handler.listEvents)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/assistant/events?range=24h&result=failed", nil))
	if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "result") {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestAssistantEventsErrorFilterClassifiesLegacyProcessingError(t *testing.T) {
	store, err := storage.NewSQLiteStore(filepath.Join(t.TempDir(), "assistant-events-legacy-error.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()

	ctx := context.Background()
	event := assistant.MessageEvent{
		Kind: assistant.EventKindGroup, GroupID: "group-1", UserID: "user-1",
		MessageID: "legacy-error", Time: time.Now().Unix(), RawMessage: "帮我看这张图",
	}
	if _, inserted, err := store.EnqueueInboundEvent(ctx, "group:group-1", event); err != nil || !inserted {
		t.Fatalf("enqueue inserted=%v err=%v", inserted, err)
	}
	item, ok, err := store.ClaimNextInboundEvent(ctx, "events-test", time.Now().Add(time.Minute))
	if err != nil || !ok {
		t.Fatalf("claim ok=%v err=%v", ok, err)
	}
	if err := store.CompleteInboundEvent(ctx, item.ID, "events-test", "ignored"); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordInboundEventAudit(ctx, assistant.EventRecord{
		Kind: event.Kind, GroupID: event.GroupID, UserID: event.UserID,
		MessageID: event.MessageID, Error: "图片源不可读取",
	}); err != nil {
		t.Fatal(err)
	}

	gin.SetMode(gin.TestMode)
	router := gin.New()
	handler := &QQBotHandler{sqlite: store}
	router.GET("/api/assistant/events", handler.listEvents)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/assistant/events?range=24h&result=error", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var response assistantEventsResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.FilteredTotal != 1 || len(response.Events) != 1 || response.Events[0].Decision != "error" || !strings.Contains(response.Events[0].Reason, "图片源不可读取") {
		t.Fatalf("response=%+v", response)
	}
}

func TestAssistantEventsDoesNotReportUnconfirmedLegacyErrorReplyAsReplied(t *testing.T) {
	store, err := storage.NewSQLiteStore(filepath.Join(t.TempDir(), "assistant-events-unconfirmed-error.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	ctx := context.Background()
	event := assistant.MessageEvent{
		Kind: assistant.EventKindGroup, GroupID: "group-1", UserID: "user-1",
		MessageID: "legacy-error-reply", Time: time.Now().Unix(), RawMessage: "帮我看图",
	}
	if _, inserted, err := store.EnqueueInboundEvent(ctx, "group:group-1", event); err != nil || !inserted {
		t.Fatalf("enqueue inserted=%v err=%v", inserted, err)
	}
	item, ok, err := store.ClaimNextInboundEvent(ctx, "events-test", time.Now().Add(time.Minute))
	if err != nil || !ok {
		t.Fatalf("claim ok=%v err=%v", ok, err)
	}
	if err := store.CompleteInboundEvent(ctx, item.ID, "events-test", "error_replied"); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordInboundEventAudit(ctx, assistant.EventRecord{
		Kind: event.Kind, GroupID: event.GroupID, UserID: event.UserID, MessageID: event.MessageID,
		Decision: "replied", Reason: "旧版直接记为已回复", Error: "图片读取失败",
	}); err != nil {
		t.Fatal(err)
	}

	gin.SetMode(gin.TestMode)
	router := gin.New()
	handler := &QQBotHandler{sqlite: store}
	router.GET("/api/assistant/events", handler.listEvents)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/assistant/events?range=24h&result=error", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var response assistantEventsResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Replied != 0 || response.Errors != 1 || len(response.Events) != 1 || response.Events[0].Handled || response.Events[0].Decision != "error" {
		t.Fatalf("response=%+v", response)
	}
	if !strings.Contains(response.Events[0].Reason, "没有可核验的 ACK") {
		t.Fatalf("reason=%q", response.Events[0].Reason)
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
