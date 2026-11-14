// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/SuInk/diana/model/agent"
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
			"media_indexes": []any{3, 1},
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
	if !strings.Contains(output, `"media_index":1`) || !strings.Contains(output, `"media_index":3`) || !strings.Contains(output, `"loaded":2`) {
		t.Fatalf("output = %s", output)
	}
}

func TestHistoryImagesToolLoadsPersistedVideoFrames(t *testing.T) {
	framePath := filepath.Join(t.TempDir(), "frame.jpg")
	frameBody := tinyJPEGBytes(t)
	if err := os.WriteFile(framePath, frameBody, 0o600); err != nil {
		t.Fatal(err)
	}
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.remember(MessageEvent{
		Kind:      EventKindGroup,
		GroupID:   "group-1",
		UserID:    "user-1",
		MessageID: "video-1",
		Segments: []MessageSegment{
			{Type: "video", Data: map[string]string{"file": "expired.mp4"}},
			{Type: "image", Data: map[string]string{"cached_file": framePath, "cached_mime": "image/jpeg", "source_type": "video_frame"}},
		},
	})

	tool := newDianaHistoryImagesTool(runtime, MessageEvent{Kind: EventKindGroup, GroupID: "group-1", UserID: "reader"})
	output, err := tool.Run(context.Background(), map[string]any{"message_ids": []any{"video-1"}})
	if err != nil {
		t.Fatal(err)
	}
	parts := tool.ToolResultParts(output)
	want := "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(frameBody)
	if len(parts) != 1 || parts[0].ImageURL != want {
		t.Fatalf("video frame parts = %#v", parts)
	}
	if !strings.Contains(output, `"media_type":"video_frame"`) || !strings.Contains(output, `"loaded":1`) {
		t.Fatalf("video frame output = %s", output)
	}
}

func TestHistoryMediaToolReturnsVoiceAndFileText(t *testing.T) {
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.remember(MessageEvent{
		Kind:      EventKindPrivate,
		UserID:    "user-1",
		MessageID: "documents-1",
		Segments: []MessageSegment{
			{Type: "record", Data: map[string]string{voiceSTTTranscriptKey: "明天下午三点开会"}},
			{Type: "file", Data: map[string]string{"name": "会议纪要.pdf", "summary": "项目周五发布"}},
		},
	})

	tool := newDianaHistoryImagesTool(runtime, MessageEvent{Kind: EventKindPrivate, UserID: "user-1"})
	output, err := tool.Run(context.Background(), map[string]any{"message_ids": []any{"documents-1"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(tool.ToolResultParts(output)) != 0 || !strings.Contains(output, "明天下午三点开会") || !strings.Contains(output, "项目周五发布") {
		t.Fatalf("history media text output = %s", output)
	}
}

func TestHistoryMediaToolParsesHistoricalFileBody(t *testing.T) {
	notePath := filepath.Join(t.TempDir(), "作业要求.txt")
	if err := os.WriteFile(notePath, []byte("第三题要写推导过程，周五前交"), 0o600); err != nil {
		t.Fatal(err)
	}
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(NewFileParserPlugin(nil)), nil, nil, nil, nil)
	runtime.remember(MessageEvent{
		Kind:      EventKindGroup,
		GroupID:   "10001",
		UserID:    "20001",
		MessageID: "file-1",
		Segments:  []MessageSegment{{Type: "file", Data: map[string]string{"name": "作业要求.txt", "file": notePath}}},
	})

	tool := newDianaHistoryImagesTool(runtime, MessageEvent{Kind: EventKindGroup, GroupID: "10001", UserID: "20002"})
	output, err := tool.Run(context.Background(), map[string]any{"message_id": "file-1"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output, "第三题要写推导过程") || !strings.Contains(output, "message_id=file-1") || strings.Contains(output, "正文尚未解析") {
		t.Fatalf("history file output = %s", output)
	}
}

func TestHistoryMediaToolKeepsFileSummaryWithoutParser(t *testing.T) {
	notePath := filepath.Join(t.TempDir(), "作业要求.txt")
	if err := os.WriteFile(notePath, []byte("第三题要写推导过程"), 0o600); err != nil {
		t.Fatal(err)
	}
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.remember(MessageEvent{
		Kind:      EventKindPrivate,
		UserID:    "20001",
		MessageID: "file-1",
		Segments:  []MessageSegment{{Type: "file", Data: map[string]string{"name": "作业要求.txt", "file": notePath}}},
	})

	tool := newDianaHistoryImagesTool(runtime, MessageEvent{Kind: EventKindPrivate, UserID: "20001"})
	output, err := tool.Run(context.Background(), map[string]any{"message_id": "file-1"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output, "第三题要写推导过程") || !strings.Contains(output, "文件名：作业要求.txt") {
		t.Fatalf("disabled parser output = %s", output)
	}
}

func TestHistoryImagesToolAddsOverlappingCropsForLargeHighDetailImage(t *testing.T) {
	large := image.NewRGBA(image.Rect(0, 0, 3000, 1600))
	var encoded bytes.Buffer
	if err := jpeg.Encode(&encoded, large, &jpeg.Options{Quality: 80}); err != nil {
		t.Fatal(err)
	}
	imageURL := "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(encoded.Bytes())
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.remember(MessageEvent{
		Kind:      EventKindPrivate,
		UserID:    "user-1",
		MessageID: "large-image",
		Segments:  []MessageSegment{{Type: "image", Data: map[string]string{"url": imageURL}}},
	})
	tool := newDianaHistoryImagesTool(runtime, MessageEvent{Kind: EventKindPrivate, UserID: "user-1"})
	output, err := tool.Run(context.Background(), map[string]any{"message_ids": []any{"large-image"}, "detail": "high"})
	if err != nil {
		t.Fatal(err)
	}
	parts := tool.ToolResultParts(output)
	if len(parts) != 3 {
		t.Fatalf("parts = %d, want original plus two crops", len(parts))
	}
	if parts[0].ImageURL != imageURL || !strings.Contains(output, `"focus_crops":2`) {
		t.Fatalf("output=%s parts=%#v", output, parts)
	}
	for index, part := range parts {
		if part.Detail != "high" {
			t.Fatalf("part %d detail = %q", index, part.Detail)
		}
	}
	crop, err := decodeDataURLImage(parts[1].ImageURL)
	if err != nil {
		t.Fatal(err)
	}
	if crop.Bounds().Dx() != 1800 || crop.Bounds().Dy() != 1600 {
		t.Fatalf("first crop bounds=%v", crop.Bounds())
	}
}

func TestHistoryImagesToolUsesThreeReadableCropsForLongScreenshot(t *testing.T) {
	large := image.NewRGBA(image.Rect(0, 0, 1200, 4500))
	var encoded bytes.Buffer
	if err := jpeg.Encode(&encoded, large, &jpeg.Options{Quality: 80}); err != nil {
		t.Fatal(err)
	}
	imageURL := "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(encoded.Bytes())
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.remember(MessageEvent{
		Kind:      EventKindPrivate,
		UserID:    "user-1",
		MessageID: "long-screenshot",
		Segments:  []MessageSegment{{Type: "image", Data: map[string]string{"url": imageURL}}},
	})
	tool := newDianaHistoryImagesTool(runtime, MessageEvent{Kind: EventKindPrivate, UserID: "user-1"})
	output, err := tool.Run(context.Background(), map[string]any{"message_ids": []any{"long-screenshot"}, "detail": "high"})
	if err != nil {
		t.Fatal(err)
	}
	parts := tool.ToolResultParts(output)
	if len(parts) != 4 || !strings.Contains(output, `"focus_crops":3`) {
		t.Fatalf("output=%s parts=%d, want original plus three crops", output, len(parts))
	}
	for index, part := range parts[1:] {
		crop, err := decodeDataURLImage(part.ImageURL)
		if err != nil {
			t.Fatalf("decode crop %d: %v", index, err)
		}
		if crop.Bounds().Dx() != 1200 || crop.Bounds().Dy() != 1800 {
			t.Fatalf("crop %d bounds=%v", index, crop.Bounds())
		}
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
		"items": []any{map[string]any{"message_id": "two-images", "media_indexes": []any{1}}},
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
	if err == nil || !strings.Contains(err.Error(), "message_id=broken-image media_index=1") || !strings.Contains(err.Error(), "原始图片已失效") {
		t.Fatalf("all-failed diagnostic = %v", err)
	}
}

// 弱模型最常写的单条形式：顶层 message_id，而不是 message_ids 数组。
func TestHistoryImagesToolAcceptsTopLevelMessageID(t *testing.T) {
	t.Setenv("DIANA_HISTORY_MEDIA_DIR", t.TempDir())
	t.Setenv("DIANA_ALLOW_PRIVATE_HTTP_FETCHES", "true")
	var secondCalls atomic.Int32
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
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
	properties := tool.InputSchema()["properties"].(map[string]any)
	if _, ok := properties["message_id"]; !ok {
		t.Fatalf("schema must declare message_id: %v", properties)
	}
	output, err := tool.Run(context.Background(), map[string]any{"message_id": "two-images", "media_indexes": []any{1}})
	if err != nil {
		t.Fatal(err)
	}
	if secondCalls.Load() != 0 || !strings.Contains(output, `"loaded":1`) {
		t.Fatalf("second=%d output=%s", secondCalls.Load(), output)
	}
}

func TestChatHistoryItemListsFileNamesWithoutFileIDs(t *testing.T) {
	item := chatHistoryItem(MessageEvent{Segments: []MessageSegment{
		{Type: "file", Data: map[string]string{"name": "作业要求.txt", "file": "a1b2c3d4", "file_id": "/a1b2c3d4"}},
		{Type: "file", Data: map[string]string{"file": "e5f6a7b8"}},
	}})
	if item.FileCount != 2 || len(item.FileNames) != 2 || item.FileNames[0] != "作业要求.txt" || item.FileNames[1] != "未命名文件" {
		t.Fatalf("item = %#v", item)
	}
}

// 官方 Bot API 只给下载 20MB 以内的文件。超限的视频不是格式问题，报错要说清楚，
// 否则模型会把它转述成「格式或者源文件的问题」。
func TestHistoryMediaToolExplainsOversizedTelegramVideo(t *testing.T) {
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.remember(MessageEvent{
		Platform:  PlatformTelegram,
		Kind:      EventKindGroup,
		GroupID:   "-10012345",
		UserID:    "user-1",
		MessageID: "705",
		Segments: []MessageSegment{
			{Type: "text", Data: map[string]string{"text": "看这个"}},
			{Type: "video", Data: map[string]string{"file_id": "BAAC", "file_size": "23227364", "mime": "video/mp4"}},
		},
	})
	tool := newDianaHistoryImagesTool(runtime, MessageEvent{Platform: PlatformTelegram, Kind: EventKindGroup, GroupID: "-10012345", UserID: "reader"})
	_, err := tool.Run(context.Background(), map[string]any{"message_ids": []any{"705"}})
	if err == nil || !strings.Contains(err.Error(), "20MB 以内") || !strings.Contains(err.Error(), "22.2MB") {
		t.Fatalf("err = %v", err)
	}
}

func TestTelegramDownloadSkipsFilesOverCloudLimit(t *testing.T) {
	segment := MessageSegment{Type: "video", Data: map[string]string{"file_id": "BAAC", "file_size": "23227364"}}
	if !telegramSegmentOverCloudLimit(segment) {
		t.Fatal("23MB video should be over the Bot API download limit")
	}
	segment.Data["file_size"] = "1048576"
	if telegramSegmentOverCloudLimit(segment) {
		t.Fatal("1MB video should be downloadable")
	}
	delete(segment.Data, "file_size")
	if telegramSegmentOverCloudLimit(segment) {
		t.Fatal("unknown size must not block the download")
	}
}

func rememberManyHistoryImages(t *testing.T, count int) *Runtime {
	t.Helper()
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	segments := make([]MessageSegment, 0, count)
	for index := 0; index < count; index++ {
		segments = append(segments, MessageSegment{Type: "image", Data: map[string]string{"url": "data:image/png;base64," + base64.StdEncoding.EncodeToString([]byte{byte('a' + index)})}})
	}
	runtime.remember(MessageEvent{Kind: EventKindGroup, GroupID: "group-1", UserID: "user-1", MessageID: "many-images", Segments: segments})
	return runtime
}

// 以前一次最多 8 张，第 9 张起标成 skipped 计入失败；现在没有条数上限。
func TestHistoryImagesToolHasNoImageCountCap(t *testing.T) {
	runtime := rememberManyHistoryImages(t, 12)
	tool := newDianaHistoryImagesTool(runtime, MessageEvent{Kind: EventKindGroup, GroupID: "group-1", UserID: "reader"})
	items := tool.InputSchema()["properties"].(map[string]any)["items"].(map[string]any)
	if _, capped := items["maxItems"]; capped {
		t.Fatalf("items schema must not cap the count: %#v", items)
	}
	output, err := tool.Run(context.Background(), map[string]any{"message_ids": []any{"many-images"}})
	if err != nil {
		t.Fatal(err)
	}
	if parts := tool.ToolResultParts(output); len(parts) != 12 {
		t.Fatalf("parts = %d, want 12", len(parts))
	}
	if !strings.Contains(output, `"loaded":12`) || !strings.Contains(output, `"failed":0`) || strings.Contains(output, "next_items") || strings.Contains(output, "skipped") {
		t.Fatalf("output = %s", output)
	}
}

// 结果字数装不下时按整张分页：每页的附件和 media 一一对应，next_items 原样传回
// items 能把剩下的读完，不重不漏。
func TestHistoryImagesToolPagesWholeImagesWithinBudget(t *testing.T) {
	const total = 10
	runtime := rememberManyHistoryImages(t, total)
	tool := newDianaHistoryImagesTool(runtime, MessageEvent{Kind: EventKindGroup, GroupID: "group-1", UserID: "reader"})
	const budget = 1000
	ctx := agent.WithToolOutputBudget(context.Background(), budget)
	input := map[string]any{"message_ids": []any{"many-images"}}
	seen := make(map[int]int)
	for page := 0; ; page++ {
		if page > total {
			t.Fatal("pagination did not finish")
		}
		output, err := tool.Run(ctx, input)
		if err != nil {
			t.Fatal(err)
		}
		var result dianaHistoryImagesResult
		if err := json.Unmarshal([]byte(output), &result); err != nil {
			t.Fatal(err)
		}
		if result.Loaded > 1 && len([]rune(output)) > budget {
			t.Fatalf("page %d is %d chars, over budget %d", page, len([]rune(output)), budget)
		}
		parts := tool.ToolResultParts(output)
		if len(parts) != result.Loaded || len(result.Media) != result.Loaded || result.Failed != 0 {
			t.Fatalf("page %d parts=%d result=%s", page, len(parts), output)
		}
		for i, status := range result.Media {
			seen[status.ImageIndex]++
			want := "data:image/png;base64," + base64.StdEncoding.EncodeToString([]byte{byte('a' + status.ImageIndex - 1)})
			if parts[i].ImageURL != want {
				t.Fatalf("page %d part %d = %q, want %q", page, i, parts[i].ImageURL, want)
			}
		}
		if !result.HasMore {
			if page == 0 {
				t.Fatalf("budget %d should need more than one page: %s", budget, output)
			}
			break
		}
		if result.RemainingCount != total-len(seen) || len(result.NextItems) != 1 || !strings.Contains(result.Message, "next_items") {
			t.Fatalf("page %d continuation = %s", page, output)
		}
		next := make([]any, 0, len(result.NextItems[0].MediaIndexes))
		for _, index := range result.NextItems[0].MediaIndexes {
			next = append(next, index)
		}
		input = map[string]any{"items": []any{map[string]any{"message_id": result.NextItems[0].MessageID, "media_indexes": next}}}
	}
	for index := 1; index <= total; index++ {
		if seen[index] != 1 {
			t.Fatalf("image %d returned %d times: %v", index, seen[index], seen)
		}
	}
}
