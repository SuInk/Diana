package assistant

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/SuInk/diana/model/llm"
)

func TestHistoryImagesToolSelectsExactIndexesInSourceOrder(t *testing.T) {
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.remember(MessageEvent{
		Kind:      EventKindGroup,
		GroupID:   "group-1",
		UserID:    "user-1",
		MessageID: "images-1",
		Segments: []MessageSegment{
			{Type: "image", Data: map[string]string{"url": "data:image/png;base64,YQ=="}},
			{Type: "image", Data: map[string]string{"url": "data:image/png;base64,Yg=="}},
			{Type: "image", Data: map[string]string{"url": "data:image/png;base64,Yw=="}},
		},
	})
	tool := newDianaHistoryImagesTool(runtime, MessageEvent{Kind: EventKindGroup, GroupID: "group-1", UserID: "reader"})
	output, err := tool.Run(context.Background(), map[string]any{
		"items": []any{map[string]any{
			"message_id":    "images-1",
			"image_indexes": []any{3, 1},
		}},
		"detail": "high",
	})
	if err != nil {
		t.Fatal(err)
	}
	parts := tool.ToolResultParts(output)
	if len(parts) != 2 || parts[0].ImageURL != "data:image/png;base64,YQ==" || parts[1].ImageURL != "data:image/png;base64,Yw==" {
		t.Fatalf("parts = %#v", parts)
	}
	for _, part := range parts {
		if part.Type != llm.ContentPartImageURL || part.Detail != "high" {
			t.Fatalf("part = %#v", part)
		}
	}
	if !strings.Contains(output, `"image_index":1`) || !strings.Contains(output, `"image_index":3`) || !strings.Contains(output, `"loaded":2`) {
		t.Fatalf("output = %s", output)
	}
}

func TestHistoryImagesToolCannotReadAnotherSession(t *testing.T) {
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.remember(MessageEvent{
		Kind:      EventKindGroup,
		GroupID:   "group-secret",
		UserID:    "user-1",
		MessageID: "secret-image",
		Segments:  []MessageSegment{{Type: "image", Data: map[string]string{"url": "data:image/png;base64,c2VjcmV0"}}},
	})
	tool := newDianaHistoryImagesTool(runtime, MessageEvent{Kind: EventKindGroup, GroupID: "group-public", UserID: "reader"})
	if _, err := tool.Run(context.Background(), map[string]any{"message_ids": []any{"secret-image"}}); err == nil || !strings.Contains(err.Error(), "均不可用") {
		t.Fatalf("cross-session read error = %v", err)
	}
	if parts := tool.ToolResultParts(""); len(parts) != 0 {
		t.Fatalf("cross-session tool leaked image parts: %#v", parts)
	}
}

func TestHistoryImagesToolUsesCurrentSemanticSourcesByDefault(t *testing.T) {
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	for index, source := range []string{"data:image/png;base64,YQ==", "data:image/png;base64,Yg=="} {
		runtime.remember(MessageEvent{
			Kind:      EventKindPrivate,
			UserID:    "user-1",
			MessageID: []string{"source-1", "source-2"}[index],
			Segments:  []MessageSegment{{Type: "image", Data: map[string]string{"url": source}}},
		})
	}
	event := MessageEvent{Kind: EventKindPrivate, UserID: "user-1"}
	setEventSemanticSourceMessageIDs(&event, []string{"source-1", "source-2"})
	tool := newDianaHistoryImagesTool(runtime, event)
	output, err := tool.Run(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if parts := tool.ToolResultParts(output); len(parts) != 2 {
		t.Fatalf("default semantic source parts = %#v", parts)
	}
}

func TestHistoryImagesToolUsesCurrentQuotedPayloadWithoutStoredEvent(t *testing.T) {
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	event := MessageEvent{
		Kind:   EventKindPrivate,
		UserID: "user-1",
		Quoted: &QuotedMessage{
			MessageID: "quoted-only",
			UserID:    "user-2",
			Segments:  []MessageSegment{{Type: "image", Data: map[string]string{"url": "data:image/png;base64,cXVvdGVk"}}},
		},
	}
	tool := newDianaHistoryImagesTool(runtime, event)
	output, err := tool.Run(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	parts := tool.ToolResultParts(output)
	if len(parts) != 1 || parts[0].ImageURL != "data:image/png;base64,cXVvdGVk" {
		t.Fatalf("quoted-only parts = %#v", parts)
	}
	if parts[0].Detail != "auto" {
		t.Fatalf("historical quoted image detail = %q, want auto", parts[0].Detail)
	}
}

func TestHistoryImagesToolPrefersFreshCurrentQuoteOverBrokenStoredCopy(t *testing.T) {
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.remember(MessageEvent{
		Kind:      EventKindPrivate,
		UserID:    "user-1",
		MessageID: "quoted-image",
		Segments: []MessageSegment{{Type: "image", Data: map[string]string{
			"file":              "expired-image",
			imageUnavailableKey: "true",
		}}},
	})
	event := MessageEvent{
		Kind:   EventKindPrivate,
		UserID: "user-1",
		Quoted: &QuotedMessage{
			MessageID: "quoted-image",
			UserID:    "user-2",
			Segments: []MessageSegment{{Type: "image", Data: map[string]string{
				"url":               "data:image/png;base64,ZnJlc2g=",
				imageUnavailableKey: "true",
			}}},
		},
	}
	tool := newDianaHistoryImagesTool(runtime, event)
	output, err := tool.Run(context.Background(), map[string]any{"message_ids": []any{"quoted-image"}})
	if err != nil {
		t.Fatal(err)
	}
	parts := tool.ToolResultParts(output)
	if len(parts) != 1 || parts[0].ImageURL != "data:image/png;base64,ZnJlc2g=" {
		t.Fatalf("fresh quote parts = %#v", parts)
	}
}

func TestHistoryImagesToolMergesFreshQuoteWithStoredLocalCache(t *testing.T) {
	localPath := filepath.Join(t.TempDir(), "stored.png")
	if err := os.WriteFile(localPath, tinyJPEGBytes(t), 0o600); err != nil {
		t.Fatal(err)
	}
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.remember(MessageEvent{
		Kind:      EventKindPrivate,
		UserID:    "user-1",
		MessageID: "merged-image",
		Segments:  []MessageSegment{{Type: "image", Data: map[string]string{"cached_file": localPath, "url": "https://expired.invalid/old.png"}}},
	})
	event := MessageEvent{
		Kind:   EventKindPrivate,
		UserID: "user-1",
		Quoted: &QuotedMessage{
			MessageID: "merged-image",
			Segments:  []MessageSegment{{Type: "image", Data: map[string]string{"file": "fresh-onebot-id", "url": "https://expired.invalid/new.png"}}},
		},
	}
	tool := newDianaHistoryImagesTool(runtime, event)
	output, err := tool.Run(context.Background(), map[string]any{"message_ids": []any{"merged-image"}})
	if err != nil {
		t.Fatal(err)
	}
	parts := tool.ToolResultParts(output)
	if len(parts) != 1 || !strings.HasPrefix(parts[0].ImageURL, "data:image/") {
		t.Fatalf("merged quote parts = %#v", parts)
	}
}

func TestHistoryImagesToolOnlyResolvesRequestedIndex(t *testing.T) {
	t.Setenv("DIANA_HISTORY_MEDIA_DIR", t.TempDir())
	t.Setenv("DIANA_ALLOW_PRIVATE_HTTP_FETCHES", "true")
	var firstCalls atomic.Int32
	var secondCalls atomic.Int32
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		firstCalls.Add(1)
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00})
	}))
	defer first.Close()
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		secondCalls.Add(1)
		http.Error(w, "must not be called", http.StatusInternalServerError)
	}))
	defer second.Close()

	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	event := MessageEvent{
		Kind:      EventKindPrivate,
		UserID:    "user-1",
		MessageID: "two-images",
		Segments: []MessageSegment{
			{Type: "image", Data: map[string]string{"url": first.URL + "/first.png"}},
			{Type: "image", Data: map[string]string{"url": second.URL + "/second.png"}},
		},
	}
	runtime.remember(event)
	tool := newDianaHistoryImagesTool(runtime, event)
	output, err := tool.Run(context.Background(), map[string]any{
		"items": []any{map[string]any{"message_id": "two-images", "image_indexes": []any{1}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if firstCalls.Load() == 0 || secondCalls.Load() != 0 || !strings.Contains(output, `"loaded":1`) {
		t.Fatalf("calls first=%d second=%d output=%s", firstCalls.Load(), secondCalls.Load(), output)
	}
}

func TestHistoryImagesToolAllFailedErrorIdentifiesSourceImage(t *testing.T) {
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.remember(MessageEvent{
		Kind:      EventKindPrivate,
		UserID:    "user-1",
		MessageID: "broken-image",
		Segments: []MessageSegment{{Type: "image", Data: map[string]string{
			imageUnavailableKey: "true",
		}}},
	})
	tool := newDianaHistoryImagesTool(runtime, MessageEvent{Kind: EventKindPrivate, UserID: "user-1"})
	_, err := tool.Run(context.Background(), map[string]any{"message_ids": []any{"broken-image"}})
	if err == nil || !strings.Contains(err.Error(), "message_id=broken-image image_index=1") || !strings.Contains(err.Error(), "原始图片已失效") {
		t.Fatalf("all-failed diagnostic = %v", err)
	}
}
