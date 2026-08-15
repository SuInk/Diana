package assistant

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SuInk/diana/model/llm"
)

func TestCacheMessageEventVideosPersistsFrames(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg is not installed")
	}
	t.Setenv("DIANA_HISTORY_MEDIA_DIR", t.TempDir())
	videoPath := filepath.Join(t.TempDir(), "incoming.mp4")
	cmd := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-y", "-f", "lavfi", "-i", "testsrc2=size=320x180:rate=10:duration=3", "-pix_fmt", "yuv420p", videoPath)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("create sample video: %v: %s", err, output)
	}

	event := cacheMessageEventVideos(context.Background(), MessageEvent{
		Kind:      EventKindGroup,
		GroupID:   "group-1",
		UserID:    "user-2",
		MessageID: "video-1",
		Segments:  []MessageSegment{{Type: "video", Data: map[string]string{"url": videoPath}}},
	})
	frames := cachedVideoFrameURLs(event.Segments)
	if len(frames) != 4 {
		t.Fatalf("cached frame count = %d, want 4: %#v", len(frames), event.Segments)
	}
	for _, frame := range frames {
		if info, err := os.Stat(frame); err != nil || info.Size() == 0 {
			t.Fatalf("cached frame invalid: path=%q info=%v err=%v", frame, info, err)
		}
	}
	again := cacheMessageEventVideos(context.Background(), event)
	if len(cachedVideoFrameURLs(again.Segments)) != len(frames) {
		t.Fatalf("cache duplicated frames: %#v", again.Segments)
	}
}

func TestCacheMessageEventVideosWaitsForNapCatPath(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg is not installed")
	}
	t.Setenv("DIANA_HISTORY_MEDIA_DIR", t.TempDir())
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "source.mp4")
	pendingPath := filepath.Join(dir, "pending.mp4")
	cmd := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-y", "-f", "lavfi", "-i", "testsrc2=size=320x180:rate=10:duration=1", "-pix_fmt", "yuv420p", sourcePath)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("create sample video: %v: %s", err, output)
	}
	moveDone := make(chan error, 1)
	go func() {
		time.Sleep(150 * time.Millisecond)
		moveDone <- os.Rename(sourcePath, pendingPath)
	}()

	event := cacheMessageEventVideos(context.Background(), MessageEvent{
		Kind:      EventKindGroup,
		GroupID:   "group-1",
		UserID:    "user-2",
		MessageID: "video-delayed",
		Segments:  []MessageSegment{{Type: "video", Data: map[string]string{"file": pendingPath}}},
	})
	if err := <-moveDone; err != nil {
		t.Fatal(err)
	}
	if frames := cachedVideoFrameURLs(event.Segments); len(frames) == 0 {
		t.Fatalf("delayed NapCat video was not cached: %#v", event.Segments)
	}
}

func TestCacheMessageEventVideosIgnoresNapCatThumbnailAndWaitsForMP4(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg is not installed")
	}
	t.Setenv("DIANA_HISTORY_MEDIA_DIR", t.TempDir())
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "source.mp4")
	pendingPath := filepath.Join(dir, "Ori", "incoming.mp4")
	thumbnailPath := filepath.Join(dir, "Thumb", "incoming_0.png")
	if err := os.MkdirAll(filepath.Dir(pendingPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(thumbnailPath), 0o700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-y", "-f", "lavfi", "-i", "testsrc2=size=320x180:rate=10:duration=1", "-pix_fmt", "yuv420p", sourcePath)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("create sample video: %v: %s", err, output)
	}
	if err := os.WriteFile(thumbnailPath, tinyJPEGBytes(t), 0o600); err != nil {
		t.Fatal(err)
	}
	moveDone := make(chan error, 1)
	go func() {
		time.Sleep(150 * time.Millisecond)
		moveDone <- os.Rename(sourcePath, pendingPath)
	}()

	event := cacheMessageEventVideos(context.Background(), MessageEvent{
		Kind:      EventKindPrivate,
		UserID:    "user-1",
		MessageID: "napcat-thumbnail-first",
		Segments: []MessageSegment{{Type: "video", Data: map[string]string{
			"file": "incoming.mp4",
			"url":  pendingPath,
			"path": thumbnailPath,
		}}},
	})
	if err := <-moveDone; err != nil {
		t.Fatal(err)
	}
	if frames := cachedVideoFrameURLs(event.Segments); len(frames) != 4 {
		t.Fatalf("cached frame count = %d, want 4: %#v", len(frames), event.Segments)
	}
}

func TestRuntimeResolvesImageWithoutURLAndCachesIt(t *testing.T) {
	t.Setenv("DIANA_HISTORY_MEDIA_DIR", t.TempDir())
	body := tinyJPEGBytes(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(body)
	}))
	defer server.Close()
	channel := &recordingChannel{apiResponses: map[string]map[string]any{
		"get_image": {"url": server.URL + "/image.jpg"},
	}}
	runtime := NewRuntime(BotConfig{}, channel, NewPluginManager(), nil, nil, nil, nil)
	event := runtime.enrichMediaReferences(context.Background(), MessageEvent{
		Kind:      EventKindGroup,
		GroupID:   "group-1",
		MessageID: "image-no-url",
		Segments: []MessageSegment{{Type: "image", Data: map[string]string{
			"file": "CEC014F0C4280214A9F672B17116581B.png",
		}}},
	})
	event = cacheMessageEventImages(context.Background(), event)
	if event.Segments[0].Data["url"] == "" || event.Segments[0].Data["cached_file"] == "" {
		t.Fatalf("image source was not resolved and cached: %#v", event.Segments)
	}
}

func TestRuntimeRefreshesExpiredImageURLThroughGetImage(t *testing.T) {
	t.Setenv("DIANA_HISTORY_MEDIA_DIR", t.TempDir())
	t.Setenv("DIANA_ALLOW_PRIVATE_HTTP_FETCHES", "true")
	body := tinyJPEGBytes(t)
	var expiredRequests, refreshedRequests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/expired.jpg":
			expiredRequests++
			http.Error(w, "expired rkey", http.StatusBadRequest)
		case "/refreshed.jpg":
			refreshedRequests++
			w.Header().Set("Content-Type", "image/jpeg")
			_, _ = w.Write(body)
		default:
			http.NotFound(w, req)
		}
	}))
	defer server.Close()

	channel := &recordingChannel{apiResponses: map[string]map[string]any{
		"get_image": {"file": server.URL + "/refreshed.jpg"},
	}}
	provider := &capturingLLMProvider{reply: "图片已读取"}
	runtime := NewRuntime(BotConfig{}, channel, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
		return provider, nil
	})
	event, text, handled, outcome := runtime.prepareMessageEvent(context.Background(), MessageEvent{
		Kind:       EventKindPrivate,
		UserID:     "user-1",
		MessageID:  "expired-rkey",
		RawMessage: "[图片]",
		Segments: []MessageSegment{{Type: "image", Data: map[string]string{
			"file": "CEC014F0C4280214A9F672B17116581B.jpg",
			"url":  server.URL + "/expired.jpg",
		}}},
	})
	if !handled || outcome != "replied" {
		t.Fatalf("prepare handled=%v outcome=%q", handled, outcome)
	}
	if event.imageLoadErr != nil {
		t.Fatalf("image fallback failed: %v", event.imageLoadErr)
	}
	if event.Segments[0].Data["cached_file"] == "" {
		t.Fatalf("refreshed image was not cached: %#v", event.Segments)
	}
	if expiredRequests != 1 || refreshedRequests != 1 {
		t.Fatalf("image requests expired=%d refreshed=%d", expiredRequests, refreshedRequests)
	}
	getImageCalls := recordedCallsByAction(channel.callsSnapshot(), "get_image")
	if len(getImageCalls) != 1 || getImageCalls[0].params["file"] != "CEC014F0C4280214A9F672B17116581B.jpg" {
		t.Fatalf("get_image calls = %#v", getImageCalls)
	}
	if getMsgCalls := recordedCallsByAction(channel.callsSnapshot(), "get_msg"); len(getMsgCalls) != 0 {
		t.Fatalf("successful get_image fallback should not refresh the message: %#v", getMsgCalls)
	}
	if _, err := runtime.replyTo(context.Background(), event, text); err != nil {
		t.Fatal(err)
	}
	wantImageURL := "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(body)
	request := provider.requestSnapshot()
	if !requestHasImageURL(request, wantImageURL) || requestHasImageURL(request, server.URL+"/expired.jpg") {
		t.Fatalf("vision request did not use refreshed bytes: %#v", request.Messages)
	}
}

func TestRuntimeRefreshesExpiredImageURLThroughGetImageLocalPath(t *testing.T) {
	t.Setenv("DIANA_HISTORY_MEDIA_DIR", t.TempDir())
	t.Setenv("DIANA_ALLOW_PRIVATE_HTTP_FETCHES", "true")
	body := tinyJPEGBytes(t)
	localPath := filepath.Join(t.TempDir(), "napcat-image.jpg")
	if err := os.WriteFile(localPath, body, 0o600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "expired rkey", http.StatusBadRequest)
	}))
	defer server.Close()

	channel := &recordingChannel{apiResponses: map[string]map[string]any{
		"get_image": {"file": localPath},
	}}
	runtime := NewRuntime(BotConfig{}, channel, NewPluginManager(), nil, nil, nil, nil)
	event := runtime.prepareEventImages(context.Background(), MessageEvent{
		Kind:      EventKindPrivate,
		UserID:    "user-1",
		MessageID: "expired-rkey-local",
		Segments: []MessageSegment{{Type: "image", Data: map[string]string{
			"file": "napcat-image-token",
			"url":  server.URL + "/expired.jpg",
		}}},
	})
	if event.imageLoadErr != nil {
		t.Fatal(event.imageLoadErr)
	}
	if event.Segments[0].Data["path"] != localPath || event.Segments[0].Data["cached_file"] == "" {
		t.Fatalf("NapCat local image was not cached: %#v", event.Segments[0].Data)
	}
	if calls := recordedCallsByAction(channel.callsSnapshot(), "get_image"); len(calls) != 1 {
		t.Fatalf("get_image calls = %#v", calls)
	}
}

func TestRuntimeUsesStableImageIDAndNapCatSourcePath(t *testing.T) {
	t.Setenv("DIANA_HISTORY_MEDIA_DIR", t.TempDir())
	body := tinyJPEGBytes(t)
	localPath := filepath.Join(t.TempDir(), "napcat-source.jpg")
	if err := os.WriteFile(localPath, body, 0o600); err != nil {
		t.Fatal(err)
	}
	channel := &recordingChannel{apiResponses: map[string]map[string]any{
		"get_image": {
			"data": map[string]any{
				"picElement": map[string]any{
					"filePath":   "",
					"sourcePath": localPath,
				},
			},
		},
	}}
	runtime := NewRuntime(BotConfig{}, channel, NewPluginManager(), nil, nil, nil, nil)
	event := runtime.prepareEventImages(context.Background(), MessageEvent{
		Kind:      EventKindPrivate,
		UserID:    "user-1",
		MessageID: "stable-image-id",
		Segments: []MessageSegment{{Type: "image", Data: map[string]string{
			"file":    "display-name.jpg",
			"file_id": "napcat-stable-image-id",
		}}},
	})
	if event.imageLoadErr != nil {
		t.Fatal(event.imageLoadErr)
	}
	if event.Segments[0].Data["path"] != localPath || event.Segments[0].Data["cached_file"] == "" {
		t.Fatalf("NapCat sourcePath was not cached: %#v", event.Segments[0].Data)
	}
	calls := recordedCallsByAction(channel.callsSnapshot(), "get_image")
	if len(calls) != 1 || calls[0].params["file"] != "napcat-stable-image-id" {
		t.Fatalf("get_image calls = %#v", calls)
	}
}

func TestOneBotImageResolutionMatchesTheRequestedSegment(t *testing.T) {
	firstPath := filepath.Join(t.TempDir(), "first.jpg")
	secondPath := filepath.Join(t.TempDir(), "second.jpg")
	for _, path := range []string{firstPath, secondPath} {
		if err := os.WriteFile(path, tinyJPEGBytes(t), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	response := map[string]any{
		"message_id": 42,
		"message": []any{
			map[string]any{"type": "image", "data": map[string]any{
				"file": "first.jpg", "file_id": "first-stable-id", "sourcePath": firstPath,
			}},
			map[string]any{"type": "image", "data": map[string]any{
				"file": "second.jpg", "file_id": "second-stable-id", "sourcePath": secondPath,
			}},
		},
	}
	target := MessageSegment{Type: "image", Data: map[string]string{"file": "second.jpg"}}
	if got := mediaFileTokenFromOneBotData(response, target); got != "second-stable-id" {
		t.Fatalf("media token = %q, want second-stable-id", got)
	}
	if got, _ := mediaSourceFromOneBotData(response, target); got != secondPath {
		t.Fatalf("media source = %q, want %q", got, secondPath)
	}
}

func TestRememberOutgoingPersistsSharedImage(t *testing.T) {
	historyDir := t.TempDir()
	t.Setenv("DIANA_HISTORY_MEDIA_DIR", historyDir)
	body := tinyJPEGBytes(t)
	sourcePath := filepath.Join(t.TempDir(), "agent-output.jpg")
	if err := os.WriteFile(sourcePath, body, 0o600); err != nil {
		t.Fatal(err)
	}
	store := NewLocalMediaStore("http://127.0.0.1:18080/api/qqbot/media")
	sharedURL, ok := store.Share(sourcePath, time.Minute)
	if !ok {
		t.Fatal("Share() returned false")
	}
	runtime := NewRuntime(BotConfig{BotQQ: "bot-1"}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.SetLocalMediaSharer(store)
	source := MessageEvent{Kind: EventKindPrivate, UserID: "user-1", MessageID: "edit-request"}
	runtime.rememberOutgoingWithMessageID(context.Background(), source, OutgoingMessage{
		Text:      "改好了。",
		ImageURLs: []string{sharedURL},
	}, "bot-image-1")
	if err := os.Remove(sourcePath); err != nil {
		t.Fatal(err)
	}
	history := runtime.contextHistory(source)
	if len(history) != 1 || history[0].MessageID != "bot-image-1" {
		t.Fatalf("history = %#v", history)
	}
	cachedPath := history[0].Segments[1].Data["cached_file"]
	if cachedPath == "" || cachedPath == sourcePath {
		t.Fatalf("outgoing image was not persisted: %#v", history[0].Segments)
	}
	if got, err := os.ReadFile(cachedPath); err != nil || string(got) != string(body) {
		t.Fatalf("cached outgoing image invalid: bytes=%d err=%v", len(got), err)
	}
}

func TestRememberOutgoingRecoversSentImageThroughOneBot(t *testing.T) {
	t.Setenv("DIANA_HISTORY_MEDIA_DIR", t.TempDir())
	body := tinyJPEGBytes(t)
	localPath := filepath.Join(t.TempDir(), "onebot-sent-image.jpg")
	if err := os.WriteFile(localPath, body, 0o600); err != nil {
		t.Fatal(err)
	}
	channel := &recordingChannel{apiResponses: map[string]map[string]any{
		"get_msg": {
			"message": []any{map[string]any{
				"type": "image",
				"data": map[string]any{"file": "sent-image.jpg", "file_id": "sent-image-stable-id"},
			}},
		},
		"get_image": {"sourcePath": localPath},
	}}
	runtime := NewRuntime(BotConfig{BotQQ: "bot-1"}, channel, NewPluginManager(), nil, nil, nil, nil)
	source := MessageEvent{Kind: EventKindPrivate, UserID: "user-1", MessageID: "edit-request"}
	runtime.rememberOutgoingWithMessageID(context.Background(), source, OutgoingMessage{
		Text:      "改好了。",
		ImageURLs: []string{"https://expired.example.invalid/generated.jpg"},
	}, "sent-message-1")
	history := runtime.contextHistory(source)
	if len(history) != 1 || history[0].Segments[1].Data["cached_file"] == "" {
		t.Fatalf("sent image was not recovered: %#v", history)
	}
	getMsgCalls := recordedCallsByAction(channel.callsSnapshot(), "get_msg")
	getImageCalls := recordedCallsByAction(channel.callsSnapshot(), "get_image")
	if len(getMsgCalls) == 0 || len(getImageCalls) == 0 || getImageCalls[len(getImageCalls)-1].params["file"] != "sent-image-stable-id" {
		t.Fatalf("OneBot recovery calls: get_msg=%#v get_image=%#v", getMsgCalls, getImageCalls)
	}
}

func TestAgentHistorySummarizesImagesBeforeLazyToolSkipsBrokenImage(t *testing.T) {
	t.Setenv("DIANA_HISTORY_MEDIA_DIR", t.TempDir())
	body := tinyJPEGBytes(t)
	goodPath := filepath.Join(t.TempDir(), "new-image.jpg")
	if err := os.WriteFile(goodPath, body, 0o600); err != nil {
		t.Fatal(err)
	}
	channel := &recordingChannel{}
	runtime := NewRuntime(BotConfig{}, channel, NewPluginManager(), nil, nil, nil, nil)
	history := []MessageEvent{
		{
			Kind:      EventKindPrivate,
			UserID:    "user-1",
			MessageID: "expired-bot-image",
			Segments: []MessageSegment{{Type: "image", Data: map[string]string{
				"file":               "missing-image-id",
				imageUnavailableKey:  "true",
				imageSourceFailedKey: "true",
			}}},
		},
		{
			Kind:      EventKindPrivate,
			UserID:    "user-1",
			MessageID: "new-user-image",
			Segments:  []MessageSegment{{Type: "image", Data: map[string]string{"path": goodPath}}},
		},
	}
	message, err := runtime.agentHistoryImageBatchMessage(context.Background(), history, map[int]bool{0: true, 1: true}, time.Now().Unix())
	if err != nil {
		t.Fatal(err)
	}
	if len(message.Parts) != 0 {
		t.Fatalf("history summary eagerly attached images: %#v", message.Parts)
	}
	for _, want := range []string{"message_id=expired-bot-image", "message_id=new-user-image", "当前未附加原图", dianaHistoryImagesToolName} {
		if !strings.Contains(message.Content, want) {
			t.Fatalf("history summary = %q, missing %q", message.Content, want)
		}
	}
	if strings.Contains(message.Content, "[图片]") {
		t.Fatalf("history media notice = %q", message.Content)
	}
	if calls := recordedCallsByAction(channel.callsSnapshot(), "get_image"); len(calls) != 0 {
		t.Fatalf("history summary loaded media: %#v", calls)
	}
	for _, event := range history {
		runtime.remember(event)
	}
	tool := newDianaHistoryImagesTool(runtime, MessageEvent{Kind: EventKindPrivate, UserID: "user-1"})
	output, err := tool.Run(context.Background(), map[string]any{
		"message_ids": []any{"expired-bot-image", "new-user-image"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output, `"loaded":1`) || !strings.Contains(output, `"failed":1`) {
		t.Fatalf("tool output = %s", output)
	}
	parts := tool.ToolResultParts(output)
	want := "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(body)
	if len(parts) != 1 || parts[0].Type != llm.ContentPartImageURL || parts[0].ImageURL != want {
		t.Fatalf("lazy history image parts = %#v", parts)
	}
	if calls := recordedCallsByAction(channel.callsSnapshot(), "get_image"); len(calls) != 0 {
		t.Fatalf("known broken history image was retried: %#v", calls)
	}
}

func TestAgentHistoryPersistsNewImageFailureAndDoesNotRetry(t *testing.T) {
	t.Setenv("DIANA_HISTORY_MEDIA_DIR", t.TempDir())
	channel := &recordingChannel{apiResponses: map[string]map[string]any{
		"get_image": {},
		"get_msg":   {},
	}}
	runtime := NewRuntime(BotConfig{}, channel, NewPluginManager(), nil, nil, nil, nil)
	broken := MessageEvent{
		Kind:       EventKindPrivate,
		UserID:     "user-1",
		MessageID:  "legacy-broken-image",
		RawMessage: "[图片]",
		Segments:   []MessageSegment{{Type: "image", Data: map[string]string{"file": "missing-image-id"}}},
	}
	runtime.remember(broken)
	message, err := runtime.agentHistoryImageBatchMessage(context.Background(), []MessageEvent{broken}, map[int]bool{0: true}, time.Now().Unix())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(message.Content, "message_id=legacy-broken-image") || !strings.Contains(message.Content, "当前未附加原图") || strings.Contains(message.Content, "[图片]") {
		t.Fatalf("history media notice = %q", message.Content)
	}
	if got := len(channel.callsSnapshot()); got != 0 {
		t.Fatalf("summary unexpectedly resolved legacy image: calls=%d", got)
	}
	tool := newDianaHistoryImagesTool(runtime, broken)
	if _, err := tool.Run(context.Background(), map[string]any{"message_ids": []any{"legacy-broken-image"}}); err == nil {
		t.Fatal("broken historical image tool call succeeded")
	}
	firstCallCount := len(channel.callsSnapshot())
	if firstCallCount == 0 {
		t.Fatal("legacy image was not resolved on first explicit detail request")
	}
	stored := runtime.contextHistory(broken)
	if len(stored) != 1 || stored[0].Segments[0].Data[imageUnavailableKey] != "true" {
		t.Fatalf("failed history state was not persisted: %#v", stored)
	}
	if _, err := tool.Run(context.Background(), map[string]any{"message_ids": []any{"legacy-broken-image"}}); err == nil {
		t.Fatal("known failed historical image tool call succeeded")
	}
	if got := len(channel.callsSnapshot()); got != firstCallCount {
		t.Fatalf("known failed image was retried: calls before=%d after=%d", firstCallCount, got)
	}
}

func TestRuntimeRefreshesExpiredGetImageSourceThroughGetMsg(t *testing.T) {
	t.Setenv("DIANA_HISTORY_MEDIA_DIR", t.TempDir())
	t.Setenv("DIANA_ALLOW_PRIVATE_HTTP_FETCHES", "true")
	body := tinyJPEGBytes(t)
	expiredServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "expired rkey", http.StatusBadRequest)
	}))
	defer expiredServer.Close()
	refreshedServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(body)
	}))
	defer refreshedServer.Close()

	channel := &stagedNapCatImageChannel{
		imageToken:   "original-image-token",
		expiredURL:   expiredServer.URL + "/expired.jpg",
		refreshedURL: refreshedServer.URL + "/refreshed.jpg",
	}
	runtime := NewRuntime(BotConfig{}, channel, NewPluginManager(), nil, nil, nil, nil)
	event := runtime.prepareEventImages(context.Background(), MessageEvent{
		Kind:      EventKindPrivate,
		UserID:    "user-1",
		MessageID: "refresh-through-get-msg",
		Segments: []MessageSegment{{Type: "image", Data: map[string]string{
			"file": channel.imageToken,
			"url":  expiredServer.URL + "/initial-expired.jpg",
		}}},
	})
	if event.imageLoadErr != nil || event.Segments[0].Data["cached_file"] == "" {
		t.Fatalf("get_msg fallback failed: error=%v segment=%#v", event.imageLoadErr, event.Segments[0])
	}
	if got := strings.Join(channel.calls, ","); got != "get_image:"+channel.imageToken+",get_msg:refresh-through-get-msg,get_image:"+channel.imageToken {
		t.Fatalf("fallback calls = %q", got)
	}
}

type stagedNapCatImageChannel struct {
	imageToken    string
	expiredURL    string
	refreshedURL  string
	messageLoaded bool
	calls         []string
}

func (c *stagedNapCatImageChannel) Connect(context.Context, EventHandler) error { return nil }
func (c *stagedNapCatImageChannel) Send(context.Context, OutgoingMessage) error { return nil }
func (c *stagedNapCatImageChannel) Status() ChannelStatus                       { return ChannelStatus{} }
func (c *stagedNapCatImageChannel) Close() error                                { return nil }
func (c *stagedNapCatImageChannel) CallAPI(_ context.Context, action string, params map[string]any) (map[string]any, error) {
	switch action {
	case "get_image":
		token := stringFromAny(params["file"])
		c.calls = append(c.calls, action+":"+token)
		if c.messageLoaded {
			return map[string]any{"url": c.refreshedURL}, nil
		}
		return map[string]any{"url": c.expiredURL}, nil
	case "get_msg":
		messageID := stringFromAny(params["message_id"])
		c.calls = append(c.calls, action+":"+messageID)
		c.messageLoaded = true
		return map[string]any{"message": []any{map[string]any{
			"type": "image",
			"data": map[string]any{"file": c.imageToken},
		}}}, nil
	default:
		return map[string]any{}, nil
	}
}

func TestRuntimeReportsImageErrorWhenURLAndGetImageFail(t *testing.T) {
	t.Setenv("DIANA_HISTORY_MEDIA_DIR", t.TempDir())
	t.Setenv("DIANA_ALLOW_PRIVATE_HTTP_FETCHES", "true")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "expired rkey", http.StatusBadRequest)
	}))
	defer server.Close()

	channel := &recordingChannel{apiResponses: map[string]map[string]any{
		"get_image": {},
		"get_msg":   {},
	}}
	provider := &capturingLLMProvider{reply: "不应调用模型"}
	runtime := NewRuntime(BotConfig{}, channel, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
		return provider, nil
	})
	event, text, handled, successOutcome := runtime.prepareMessageEvent(context.Background(), MessageEvent{
		Kind:       EventKindPrivate,
		UserID:     "user-1",
		MessageID:  "broken-image",
		RawMessage: "[图片]",
		Segments: []MessageSegment{{Type: "image", Data: map[string]string{
			"file": "broken.jpg",
			"url":  server.URL + "/expired.jpg",
		}}},
	})
	if !handled || successOutcome != "replied" {
		t.Fatalf("prepare handled=%v outcome=%q", handled, successOutcome)
	}
	if !errors.Is(event.imageLoadErr, errImageMediaUnavailable) {
		t.Fatalf("image error = %v", event.imageLoadErr)
	}
	outcome, err := runtime.replyAndRecord(context.Background(), event, text, successOutcome)
	if err != nil || outcome != "error_replied" {
		t.Fatalf("reply outcome = %q error = %v", outcome, err)
	}
	if len(provider.requestSnapshot().Messages) != 0 {
		t.Fatalf("vision model received an unavailable image: %#v", provider.requestSnapshot())
	}
	if len(channel.sent) != 1 || !strings.Contains(channel.sent[0].Text, "图片读取失败") || strings.Contains(channel.sent[0].Text, "[图片]") {
		t.Fatalf("public error reply = %#v", channel.sent)
	}
	if len(recordedCallsByAction(channel.callsSnapshot(), "get_image")) == 0 {
		t.Fatalf("get_image fallback was not attempted: %#v", channel.callsSnapshot())
	}
}

func TestRuntimeCachesMP4FileWithoutReplying(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg is not installed")
	}
	t.Setenv("DIANA_HISTORY_MEDIA_DIR", t.TempDir())
	videoPath := filepath.Join(t.TempDir(), "IMG_1939.mp4")
	cmd := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-y", "-f", "lavfi", "-i", "testsrc2=size=320x180:rate=10:duration=1", "-pix_fmt", "yuv420p", videoPath)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("create sample video: %v: %s", err, output)
	}
	channel := &recordingChannel{apiResponses: map[string]map[string]any{
		"get_group_file_url": {"path": videoPath},
	}}
	runtime := NewRuntime(BotConfig{}, channel, NewPluginManager(), nil, nil, nil, nil)
	event, _, handled, outcome := runtime.prepareMessageEvent(context.Background(), MessageEvent{
		Kind:       EventKindGroup,
		GroupID:    "group-1",
		UserID:     "user-1",
		MessageID:  "file-video",
		RawMessage: "[文件:IMG_1939.mp4]",
		Segments: []MessageSegment{{Type: "file", Data: map[string]string{
			"file": "IMG_1939.mp4", "file_id": "/video-id",
		}}},
	})
	if handled || outcome != "ignored_video" {
		t.Fatalf("video file triggered chat: handled=%v outcome=%q", handled, outcome)
	}
	if frames := cachedVideoFrameURLs(event.Segments); len(frames) == 0 {
		t.Fatalf("video file frames were not cached: %#v", event.Segments)
	}
}

func TestRuntimeReplacesUnavailableNapCatVideoPath(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg is not installed")
	}
	t.Setenv("DIANA_HISTORY_MEDIA_DIR", t.TempDir())
	videoPath := filepath.Join(t.TempDir(), "downloaded.mp4")
	cmd := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-y", "-f", "lavfi", "-i", "testsrc2=size=320x180:rate=10:duration=1", "-pix_fmt", "yuv420p", videoPath)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("create sample video: %v: %s", err, output)
	}
	missingPath := filepath.Join(t.TempDir(), "QQ", "Video", "Ori", "video.mp4")
	channel := &recordingChannel{apiResponses: map[string]map[string]any{
		"get_file": {"path": videoPath},
	}}
	runtime := NewRuntime(BotConfig{}, channel, NewPluginManager(), nil, nil, nil, nil)
	event := runtime.enrichMediaReferences(context.Background(), MessageEvent{
		Kind:      EventKindGroup,
		GroupID:   "group-1",
		MessageID: "napcat-video",
		Segments: []MessageSegment{{Type: "video", Data: map[string]string{
			"file": "video.mp4", "url": missingPath,
		}}},
	})
	if got := event.Segments[0].Data["path"]; got != videoPath {
		t.Fatalf("resolved path = %q, want %q: %#v", got, videoPath, event.Segments)
	}
	if len(channel.calls) != 1 || channel.calls[0].action != "get_file" {
		t.Fatalf("OneBot fallback calls = %#v", channel.calls)
	}
	event = cacheMessageEventVideos(context.Background(), event)
	if frames := cachedVideoFrameURLs(event.Segments); len(frames) != 4 {
		t.Fatalf("cached frame count = %d, want 4: %#v", len(frames), event.Segments)
	}
}

func TestRuntimeDownloadsVideoWithNapCatFileToken(t *testing.T) {
	videoPath := filepath.Join(t.TempDir(), "downloaded.mp4")
	if err := os.WriteFile(videoPath, []byte("video"), 0o600); err != nil {
		t.Fatal(err)
	}
	const token = "napcat-video-token"
	channel := &recordingChannel{apiResponses: map[string]map[string]any{
		"get_file": {"file": videoPath},
	}}
	runtime := NewRuntime(BotConfig{}, channel, NewPluginManager(), nil, nil, nil, nil)
	event := runtime.enrichMediaReferences(context.Background(), MessageEvent{
		Kind:      EventKindPrivate,
		UserID:    "user-1",
		MessageID: "video-token",
		Segments: []MessageSegment{{Type: "video", Data: map[string]string{
			"file": token,
		}}},
	})
	if got := event.Segments[0].Data["path"]; got != videoPath {
		t.Fatalf("resolved path = %q, want %q: %#v", got, videoPath, event.Segments)
	}
	if len(channel.calls) != 1 || channel.calls[0].action != "get_file" || channel.calls[0].params["file"] != token {
		t.Fatalf("get_file calls = %#v", channel.calls)
	}
}

func TestRuntimeRefreshesNapCatVideoTokenThroughGetMsg(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg is not installed")
	}
	t.Setenv("DIANA_HISTORY_MEDIA_DIR", t.TempDir())
	videoPath := filepath.Join(t.TempDir(), "refreshed.mp4")
	cmd := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-y", "-f", "lavfi", "-i", "testsrc2=size=320x180:rate=10:duration=1", "-pix_fmt", "yuv420p", videoPath)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("create sample video: %v: %s", err, output)
	}
	channel := &stagedNapCatVideoChannel{
		fileName:  "incoming.mp4",
		videoPath: videoPath,
	}
	runtime := NewRuntime(BotConfig{}, channel, NewPluginManager(), nil, nil, nil, nil)
	event := runtime.enrichMediaReferences(context.Background(), MessageEvent{
		Kind:      EventKindPrivate,
		UserID:    "user-1",
		MessageID: "30007",
		Segments: []MessageSegment{{Type: "video", Data: map[string]string{
			"file": "incoming.mp4",
			"url":  "/missing/QQ/Video/Ori/incoming.mp4",
		}}},
	})
	if got := event.Segments[0].Data["path"]; got != videoPath {
		t.Fatalf("resolved path = %q, want %q: %#v", got, videoPath, event.Segments)
	}
	if len(channel.calls) < 3 || channel.calls[len(channel.calls)-2].action != "get_msg" || channel.calls[len(channel.calls)-1].action != "get_file" {
		t.Fatalf("expected get_msg followed by get_file, calls = %#v", channel.calls)
	}
	event = cacheMessageEventVideos(context.Background(), event)
	if frames := cachedVideoFrameURLs(event.Segments); len(frames) != 4 {
		t.Fatalf("cached frame count = %d, want 4: %#v", len(frames), event.Segments)
	}
}

type stagedNapCatVideoChannel struct {
	recordingChannel
	fileName  string
	videoPath string
	refreshed bool
}

func (c *stagedNapCatVideoChannel) CallAPI(_ context.Context, action string, params map[string]any) (map[string]any, error) {
	c.calls = append(c.calls, recordingAPICall{action: action, params: params})
	switch action {
	case "get_msg":
		c.refreshed = true
		return map[string]any{
			"message": []any{map[string]any{
				"type": "video",
				"data": map[string]any{"file": c.fileName},
			}},
		}, nil
	case "get_file":
		if c.refreshed && params["file"] == c.fileName {
			return map[string]any{"file": c.videoPath}, nil
		}
		return nil, errors.New("file not found")
	default:
		return map[string]any{}, nil
	}
}

func TestLLMMessageUsesPersistedVideoFramesAfterSourceDisappears(t *testing.T) {
	framePath := filepath.Join(t.TempDir(), "frame.jpg")
	if err := os.WriteFile(framePath, tinyJPEGBytes(t), 0o600); err != nil {
		t.Fatal(err)
	}
	msg := llmMessageFromEventWithVideoFrames(context.Background(), MessageEvent{
		RawMessage: "这是什么",
		Quoted: &QuotedMessage{
			Semantic: true,
			Segments: []MessageSegment{
				{Type: "video", Data: map[string]string{"file": "expired-video.mp4"}},
				{Type: "image", Data: map[string]string{"cached_file": framePath, "source_type": "video_frame"}},
			},
		},
	}, "这是什么", nil)
	if len(msg.Parts) < 2 || msg.Parts[1].Type != "image_url" {
		t.Fatalf("persisted frame missing from LLM message: %#v", msg)
	}
}

func TestRecentHistoryImageIndexesIgnoreVideoFrames(t *testing.T) {
	history := []MessageEvent{
		{
			Kind:      EventKindPrivate,
			Time:      100,
			UserID:    "user-1",
			MessageID: "video-1",
			Segments: []MessageSegment{
				{Type: "video", Data: map[string]string{"file": "video.mp4"}},
				{Type: "image", Data: map[string]string{"cached_file": "/tmp/frame.jpg", "source_type": "video_frame"}},
			},
		},
		{Kind: EventKindPrivate, Time: 101, UserID: "user-1", MessageID: "question", Segments: []MessageSegment{{Type: "text", Data: map[string]string{"text": "刚才的视频是什么"}}}},
	}
	if selected := recentHistoryImageIndexes(history, "question"); len(selected) != 0 {
		t.Fatalf("video frames selected as historical still images: %#v", selected)
	}
}

func TestEventWithoutQuotedImagesKeepsCachedVideoFrames(t *testing.T) {
	framePath := filepath.Join(t.TempDir(), "frame.jpg")
	if err := os.WriteFile(framePath, tinyJPEGBytes(t), 0o600); err != nil {
		t.Fatal(err)
	}
	event := eventWithoutQuotedImages(MessageEvent{
		RawMessage: "看引用",
		Quoted: &QuotedMessage{Segments: []MessageSegment{
			{Type: "video", Data: map[string]string{"file": "expired-video.mp4"}},
			{Type: "image", Data: map[string]string{"cached_file": framePath, "source_type": "video_frame"}},
			{Type: "image", Data: map[string]string{"cached_file": filepath.Join(t.TempDir(), "still.jpg")}},
		}},
	})
	if event.Quoted == nil || len(event.Quoted.Segments) != 2 || event.Quoted.Segments[1].Data["source_type"] != "video_frame" {
		t.Fatalf("quoted video context was stripped with still images: %#v", event.Quoted)
	}
	message := llmMessageFromEventWithVideoFrames(context.Background(), event, "看引用", nil)
	if requestImageCount(llm.GenerateRequest{Messages: []llm.Message{message}}) != 1 {
		t.Fatalf("cached quoted video frame missing after lazy still-image filtering: %#v", message)
	}
}

func TestRuntimeCachesIncomingVideoThenRoutesFollowupToItsFrames(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg is not installed")
	}
	t.Setenv("DIANA_HISTORY_MEDIA_DIR", t.TempDir())
	videoPath := filepath.Join(t.TempDir(), "incoming.mp4")
	cmd := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-y", "-f", "lavfi", "-i", "testsrc2=size=320x180:rate=10:duration=3", "-pix_fmt", "yuv420p", videoPath)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("create sample video: %v: %s", err, output)
	}

	channel := &recordingChannel{}
	provider := &sequenceLLMProvider{replies: []string{
		`{"message_id":"video-1","confidence":0.98,"reason":"当前问题指向群友刚才发送的视频"}`,
		`{"action":"none","prompt":""}`,
		"视频里是测试画面。",
	}}
	runtime := NewRuntime(BotConfig{BotQQ: "42", RecentContextLimit: 20}, channel, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
		return provider, nil
	})
	prepared, _, handled, _ := runtime.prepareMessageEvent(context.Background(), MessageEvent{
		Kind:       EventKindGroup,
		GroupID:    "group-1",
		UserID:     "other-user",
		SenderName: "群友",
		MessageID:  "video-1",
		RawMessage: "[视频]",
		Segments:   []MessageSegment{{Type: "video", Data: map[string]string{"file": videoPath}}},
	})
	if handled {
		t.Fatal("video-only message should be cached without an immediate reply")
	}
	if frames := cachedVideoFrameURLs(prepared.Segments); len(frames) == 0 {
		t.Fatalf("incoming video was not cached: %#v", prepared.Segments)
	}
	if err := os.Remove(videoPath); err != nil {
		t.Fatal(err)
	}

	reply, err := runtime.replyTo(context.Background(), MessageEvent{
		Kind:       EventKindGroup,
		GroupID:    "group-1",
		UserID:     "current-user",
		SenderName: "提问者",
		MessageID:  "question-1",
		ToMe:       true,
		RawMessage: "这是什么",
		Segments:   []MessageSegment{{Type: "text", Data: map[string]string{"text": "这是什么"}}},
	}, "这是什么")
	if err != nil {
		t.Fatal(err)
	}
	if reply != "视频里是测试画面。" || len(channel.sent) != 1 {
		t.Fatalf("reply=%q sent=%#v", reply, channel.sent)
	}
	if len(provider.requests) != 3 || requestImageCount(provider.requests[2]) != 4 {
		t.Fatalf("selected video frames missing from final request: %#v", provider.requests)
	}
}

func requestHasAnyImage(req llm.GenerateRequest) bool {
	return requestImageCount(req) > 0
}

func requestImageCount(req llm.GenerateRequest) int {
	count := 0
	for _, message := range req.Messages {
		for _, part := range message.Parts {
			if part.Type == llm.ContentPartImageURL && part.ImageURL != "" {
				count++
			}
		}
	}
	return count
}

func tinyJPEGBytes(t *testing.T) []byte {
	t.Helper()
	path := filepath.Join(t.TempDir(), "tiny.jpg")
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg is not installed")
	}
	cmd := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-y", "-f", "lavfi", "-i", "color=c=green:size=16x16:duration=0.1", "-frames:v", "1", path)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("create JPEG: %v: %s", err, output)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
