// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestForwardMediaRecoveryKeepsMediaOrdinals(t *testing.T) {
	t.Setenv("DIANA_HISTORY_MEDIA_DIR", t.TempDir())
	first := filepath.Join(t.TempDir(), "first.mp4")
	second := filepath.Join(t.TempDir(), "second.mp4")
	for _, path := range []string{first, second} {
		if err := os.WriteFile(path, []byte("video"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	channel := &recordingChannel{apiResponses: map[string]map[string]any{
		"get_forward_msg": {"messages": []any{map[string]any{
			"message_id": 0,
			"message": []any{
				map[string]any{"type": "video", "data": map[string]any{"url": first}},
				map[string]any{"type": "video", "data": map[string]any{"url": second}},
			},
		}}},
	}}
	runtime := NewRuntime(BotConfig{}, channel, NewPluginManager(), nil, nil, nil, nil)
	event := runtime.enrichMediaReferences(context.Background(), MessageEvent{
		MessageID: "123",
		Segments: []MessageSegment{
			{Type: "video", Data: map[string]string{"forward_id": "inner", "source_message_id": "0", "url": "http://host.docker.internal:18080/media/resolver/old-first"}},
			{Type: "video", Data: map[string]string{"forward_id": "inner", "source_message_id": "0", "url": "http://host.docker.internal:18080/media/resolver/old-second"}},
		},
	})
	if event.Segments[0].Data["url"] != first || event.Segments[1].Data["url"] != second {
		t.Fatalf("recovered media = %#v", event.Segments)
	}
	if calls := channel.callsSnapshot(); len(calls) != 1 || calls[0].action != "get_forward_msg" || calls[0].params["id"] != "inner" {
		t.Fatalf("calls = %#v", calls)
	}
}

func TestForwardMediaRecoveryRefreshesImageBeforeGetImage(t *testing.T) {
	t.Setenv("DIANA_HISTORY_MEDIA_DIR", t.TempDir())
	path := filepath.Join(t.TempDir(), "image.jpg")
	if err := os.WriteFile(path, tinyJPEGBytes(t), 0o600); err != nil {
		t.Fatal(err)
	}
	channel := &recordingChannel{apiResponses: map[string]map[string]any{
		"get_forward_msg": {"messages": []any{map[string]any{"message_id": 0, "message": []any{
			map[string]any{"type": "image", "data": map[string]any{
				"file": "qq-image-token", "url": "http://host.docker.internal/media/resolver/expired",
			}},
		}}}},
		"get_image": {"path": path},
	}}
	runtime := NewRuntime(BotConfig{}, channel, NewPluginManager(), nil, nil, nil, nil)
	event := runtime.prepareEventImages(context.Background(), MessageEvent{
		MessageID: "123", Segments: []MessageSegment{{Type: "image", Data: map[string]string{
			"forward_id": "inner", "source_message_id": "0", "sub_type": "0",
		}}},
	})
	if event.imageLoadErr != nil || event.Segments[0].Data["cached_file"] == "" {
		t.Fatalf("image = %#v, err = %v", event.Segments, event.imageLoadErr)
	}
	for _, call := range channel.callsSnapshot() {
		if call.action == "get_msg" {
			t.Fatalf("unexpected get_msg: %#v", call)
		}
	}
}

func TestForwardMediaUnavailableDoesNotRequestZeroOrBlameOriginal(t *testing.T) {
	t.Setenv("DIANA_HISTORY_MEDIA_DIR", t.TempDir())
	channel := &recordingChannel{}
	runtime := NewRuntime(BotConfig{}, channel, NewPluginManager(), nil, nil, nil, nil)
	event := runtime.prepareEventImages(context.Background(), MessageEvent{
		MessageID: "123", Segments: []MessageSegment{{Type: "image", Data: map[string]string{
			"forward_id": "inner", "source_message_id": "0",
		}}},
	})
	if !errors.Is(event.imageLoadErr, errForwardMediaUnavailable) {
		t.Fatalf("error = %v", event.imageLoadErr)
	}
	for _, call := range channel.callsSnapshot() {
		if call.action == "get_msg" {
			t.Fatalf("requested outer or placeholder message: %#v", call)
		}
	}
	message := publicChatErrorMessage(event.imageLoadErr)
	if !strings.Contains(message, "合并转发") || strings.Contains(message, "重新发送原图") || strings.Contains(message, "inner") {
		t.Fatalf("public error = %s", message)
	}
}

func TestMediaRecoveryDoesNotUseVideoAsImage(t *testing.T) {
	response := map[string]any{"message": []any{
		map[string]any{"type": "video", "data": map[string]any{"url": "https://example.com/video.mp4", "file_id": "video-token"}},
	}}
	target := MessageSegment{Type: "image"}
	if source, _ := mediaSourceFromOneBotData(response, target); source != "" {
		t.Fatalf("video used as image: %s", source)
	}
	if token := mediaFileTokenFromOneBotData(response, target); token != "" {
		t.Fatalf("video token used as image: %s", token)
	}
}

func TestForwardMediaPartialFailureKeepsReadableImages(t *testing.T) {
	t.Setenv("DIANA_HISTORY_MEDIA_DIR", t.TempDir())
	path := filepath.Join(t.TempDir(), "image.jpg")
	if err := os.WriteFile(path, tinyJPEGBytes(t), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, forwarded := range []bool{true, false} {
		t.Run(map[bool]string{true: "forwarded", false: "direct-upload"}[forwarded], func(t *testing.T) {
			channel := &recordingChannel{}
			runtime := NewRuntime(BotConfig{}, channel, NewPluginManager(), nil, nil, nil, nil)
			missing := map[string]string{"source_message_id": "0"}
			if forwarded {
				missing["forward_id"] = "inner"
			}
			event := runtime.prepareEventImages(context.Background(), MessageEvent{
				MessageID: "123", Segments: []MessageSegment{
					{Type: "image", Data: missing},
					{Type: "image", Data: map[string]string{"forward_id": "inner", "path": path}},
				},
			})
			if !forwarded {
				if event.imageLoadErr == nil {
					t.Fatal("direct upload failure was hidden")
				}
				return
			}
			if event.imageLoadErr != nil || !strings.Contains(PlainText(event.Segments), "1 张图片未能读取") {
				t.Fatalf("partial result = %#v, err = %v", event.Segments, event.imageLoadErr)
			}
			if len(availableImageURLs(event.Segments)) != 1 {
				t.Fatalf("unreadable media was exposed: %#v", event.Segments)
			}
			// A failed historical URL must not re-enter the model image loader.
			event.Segments[0].Data["url"] = "http://host.docker.internal/media/resolver/expired"
			_, failures := llmMessageFromEventWithImagesForContextDiagnostics(context.Background(), event, PlainText(event.Segments), nil)
			if len(failures) != 0 {
				t.Fatalf("unavailable forward image reached model loader: %v", failures)
			}
		})
	}
}

func TestIncomingForwardResolvesOwnShareAfterRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "video.mp4")
	if err := os.WriteFile(path, []byte("video"), 0o600); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	store := NewLocalMediaStore("http://host.docker.internal:18080/media/resolver")
	if err := store.SetIndexDir(dir); err != nil {
		t.Fatal(err)
	}
	shared, ok := store.Share(path, time.Hour)
	if !ok {
		t.Fatal("share failed")
	}
	restarted := NewLocalMediaStore("http://127.0.0.1:18080/media/resolver")
	if err := restarted.SetIndexDir(dir); err != nil {
		t.Fatal(err)
	}
	channel := &recordingChannel{}
	runtime := NewRuntime(BotConfig{}, channel, NewPluginManager(), nil, nil, nil, nil)
	runtime.SetLocalMediaSharer(restarted)
	event := runtime.enrichMediaReferences(context.Background(), MessageEvent{
		Segments: []MessageSegment{{Type: "video", Data: map[string]string{"forward_id": "inner", "url": shared}}},
	})
	if event.Segments[0].Data["url"] != path || len(channel.callsSnapshot()) != 0 {
		t.Fatalf("local share not resolved: %#v", event.Segments)
	}
}

func TestForwardMediaPartialFailureKeepsReadableVideo(t *testing.T) {
	t.Setenv("DIANA_HISTORY_MEDIA_DIR", t.TempDir())
	path := createTimelineVideo(t)
	channel := &recordingChannel{}
	runtime := NewRuntime(BotConfig{}, channel, NewPluginManager(), nil, nil, nil, nil)
	event := runtime.prepareEventImages(context.Background(), MessageEvent{
		MessageID: "forward-video", Segments: []MessageSegment{
			{Type: "image", Data: map[string]string{"forward_id": "inner", "source_message_id": "0"}},
			{Type: "video", Data: map[string]string{"forward_id": "inner", "path": path}},
		},
	})
	if event.imageLoadErr != nil || len(cachedVideoFrameURLs(event.Segments)) == 0 {
		t.Fatalf("readable video was blocked: frames=%v err=%v", cachedVideoFrameURLs(event.Segments), event.imageLoadErr)
	}
	if !strings.Contains(PlainText(event.Segments), "图片未能读取") {
		t.Fatal("partial-media warning missing")
	}
}
