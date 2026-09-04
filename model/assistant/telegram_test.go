// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type telegramPartialFailureChannel struct {
	mu    sync.Mutex
	calls []OutgoingMessage
}

func (c *telegramPartialFailureChannel) Connect(context.Context, EventHandler) error { return nil }
func (c *telegramPartialFailureChannel) Close() error                                { return nil }
func (c *telegramPartialFailureChannel) Status() ChannelStatus                       { return ChannelStatus{Connected: true} }
func (c *telegramPartialFailureChannel) CallAPI(context.Context, string, map[string]any) (map[string]any, error) {
	return map[string]any{}, nil
}
func (c *telegramPartialFailureChannel) Send(_ context.Context, msg OutgoingMessage) error {
	c.mu.Lock()
	c.calls = append(c.calls, msg)
	c.mu.Unlock()
	if len(msg.ImageURLs)+len(msg.VideoURLs) > 0 {
		return fmt.Errorf("media rejected")
	}
	return nil
}

// fakeTelegramAPI 是一个最小的 Bot API 桩，按方法名返回预置结果并记录调用。
type fakeTelegramAPI struct {
	mu      sync.Mutex
	calls   []telegramCall
	replies map[string]any
	server  *httptest.Server
}

type telegramCall struct {
	Method string
	Params map[string]any
}

func newFakeTelegramAPI(t *testing.T, replies map[string]any) *fakeTelegramAPI {
	t.Helper()
	api := &fakeTelegramAPI{replies: replies}
	api.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/file/bot") {
			api.mu.Lock()
			body, _ := api.replies["download:"+strings.TrimPrefix(r.URL.Path, "/file/bottest-token/")].([]byte)
			api.mu.Unlock()
			if len(body) == 0 {
				http.NotFound(w, r)
				return
			}
			_, _ = w.Write(body)
			return
		}
		// 路径形如 /bot<token>/<method>
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		method := parts[len(parts)-1]
		params := map[string]any{}
		if r.Header.Get("Content-Type") == "application/json" {
			_ = json.NewDecoder(r.Body).Decode(&params)
		}
		api.mu.Lock()
		api.calls = append(api.calls, telegramCall{Method: method, Params: params})
		result, ok := api.replies[method]
		api.mu.Unlock()
		if !ok {
			result = map[string]any{}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": result})
	}))
	t.Cleanup(api.server.Close)
	return api
}

func (a *fakeTelegramAPI) channel() *TelegramChannel {
	return NewTelegramChannel(TelegramConfig{BotToken: "test-token", APIBaseURL: a.server.URL})
}

func (a *fakeTelegramAPI) callsOf(method string) []telegramCall {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := []telegramCall{}
	for _, call := range a.calls {
		if call.Method == method {
			out = append(out, call)
		}
	}
	return out
}

func TestTelegramSendsTextToChat(t *testing.T) {
	api := newFakeTelegramAPI(t, nil)
	ch := api.channel()

	if err := ch.Send(context.Background(), OutgoingMessage{GroupID: "-100123", Text: "你好"}); err != nil {
		t.Fatalf("发送失败：%v", err)
	}
	calls := api.callsOf("sendMessage")
	if len(calls) != 1 {
		t.Fatalf("期望 1 次 sendMessage，实际 %d", len(calls))
	}
	if calls[0].Params["chat_id"] != "-100123" {
		t.Fatalf("chat_id 错误：%v", calls[0].Params["chat_id"])
	}
	if calls[0].Params["text"] != "你好" {
		t.Fatalf("text 错误：%v", calls[0].Params["text"])
	}
}

func TestTelegramSendsRepliesIntoForumTopic(t *testing.T) {
	api := newFakeTelegramAPI(t, nil)
	err := api.channel().Send(context.Background(), OutgoingMessage{
		GroupID:         "-100123",
		MessageThreadID: "77",
		Text:            "话题内回复",
		ImageURLs:       []string{"https://example.com/image.jpg"},
	})
	if err != nil {
		t.Fatalf("发送失败：%v", err)
	}
	textParams := api.callsOf("sendMessage")[0].Params
	if textParams["message_thread_id"] != "77" {
		t.Fatalf("文本未保留话题 ID：%+v", textParams)
	}
	photoParams := api.callsOf("sendPhoto")[0].Params
	if photoParams["message_thread_id"] != "77" {
		t.Fatalf("图片未保留话题 ID：%+v", photoParams)
	}
}

// 私聊没有 GroupID，要回落到 UserID 当 chat_id。
func TestTelegramFallsBackToUserIDForPrivateChat(t *testing.T) {
	api := newFakeTelegramAPI(t, nil)
	if err := api.channel().Send(context.Background(), OutgoingMessage{UserID: "555", Text: "hi"}); err != nil {
		t.Fatalf("发送失败：%v", err)
	}
	calls := api.callsOf("sendMessage")
	if len(calls) != 1 || calls[0].Params["chat_id"] != "555" {
		t.Fatalf("私聊应使用 user id 作为 chat_id，实际 %+v", calls)
	}
}

// 被回复的消息可能已删除，必须允许降级为普通发送，否则整条回复会丢。
func TestTelegramReplyAllowsMissingTarget(t *testing.T) {
	api := newFakeTelegramAPI(t, nil)
	err := api.channel().Send(context.Background(), OutgoingMessage{GroupID: "1", Text: "x", ReplyMessageID: "42"})
	if err != nil {
		t.Fatalf("发送失败：%v", err)
	}
	params := api.callsOf("sendMessage")[0].Params
	if params["reply_to_message_id"] != "42" {
		t.Fatalf("reply_to_message_id 错误：%v", params["reply_to_message_id"])
	}
	if params["allow_sending_without_reply"] != true {
		t.Fatal("被回复消息不存在时应仍然发送")
	}
}

func TestTelegramSendsMediaByURL(t *testing.T) {
	api := newFakeTelegramAPI(t, nil)
	err := api.channel().Send(context.Background(), OutgoingMessage{
		GroupID:   "1",
		ImageURLs: []string{"https://example.com/a.jpg"},
		VideoURLs: []string{"https://example.com/b.mp4"},
	})
	if err != nil {
		t.Fatalf("发送失败：%v", err)
	}
	if photos := api.callsOf("sendPhoto"); len(photos) != 1 || photos[0].Params["photo"] != "https://example.com/a.jpg" {
		t.Fatalf("图片发送错误：%+v", photos)
	}
	if videos := api.callsOf("sendVideo"); len(videos) != 1 || videos[0].Params["video"] != "https://example.com/b.mp4" {
		t.Fatalf("视频发送错误：%+v", videos)
	}
}

func TestTelegramRetryDoesNotResendSuccessfulText(t *testing.T) {
	channel := &telegramPartialFailureChannel{}
	runtime := NewRuntime(BotConfig{Platform: PlatformTelegram}, channel, NewPluginManager(), nil, nil, nil, nil)
	_, err := runtime.sendChannelWithRetry(context.Background(), OutgoingMessage{
		Platform:  PlatformTelegram,
		UserID:    "5",
		Text:      "图片生成完成",
		ImageURLs: []string{"/tmp/generated.png"},
	}, 3)
	if err == nil {
		t.Fatal("media failure should be returned")
	}
	channel.mu.Lock()
	defer channel.mu.Unlock()
	if len(channel.calls) != 4 {
		t.Fatalf("calls = %#v", channel.calls)
	}
	if channel.calls[0].Text != "图片生成完成" || len(channel.calls[0].ImageURLs) != 0 {
		t.Fatalf("first step = %#v", channel.calls[0])
	}
	for index, call := range channel.calls[1:] {
		if call.Text != "" || len(call.ImageURLs) != 1 {
			t.Fatalf("media retry %d = %#v", index, call)
		}
	}
}

func TestTelegramGeneratedBase64ImageUsesLocalUploadPath(t *testing.T) {
	runtime := NewRuntime(BotConfig{Platform: PlatformTelegram}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	encoded := base64.StdEncoding.EncodeToString(tinyJPEGBytes(t))
	images, cleanup, err := runtime.shareAgentImages(context.Background(), PlatformTelegram, []string{"data:image/jpeg;base64," + encoded})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		for _, path := range cleanup {
			cleanupLocalMediaFile(path)
		}
	}()
	if len(images) != 1 || !filepath.IsAbs(images[0]) {
		t.Fatalf("images = %#v", images)
	}
	if _, err := os.Stat(images[0]); err != nil {
		t.Fatalf("local upload file = %q: %v", images[0], err)
	}
}

// 空文本不该产生一次空的 sendMessage 调用。
func TestTelegramSkipsEmptyText(t *testing.T) {
	api := newFakeTelegramAPI(t, nil)
	if err := api.channel().Send(context.Background(), OutgoingMessage{GroupID: "1", Text: "   "}); err != nil {
		t.Fatalf("发送失败：%v", err)
	}
	if calls := api.callsOf("sendMessage"); len(calls) != 0 {
		t.Fatalf("空文本不该发送，实际 %d 次", len(calls))
	}
}

func TestTelegramRequiresChatID(t *testing.T) {
	api := newFakeTelegramAPI(t, nil)
	if err := api.channel().Send(context.Background(), OutgoingMessage{Text: "x"}); err == nil {
		t.Fatal("缺少 chat id 时应报错")
	}
}

func TestTelegramSurfacesAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok": false, "error_code": 401, "description": "Unauthorized",
		})
	}))
	defer server.Close()

	ch := NewTelegramChannel(TelegramConfig{BotToken: "bad", APIBaseURL: server.URL})
	_, err := ch.CallAPI(context.Background(), "getMe", nil)
	if err == nil || !strings.Contains(err.Error(), "Unauthorized") {
		t.Fatalf("应透出 Bot API 的错误描述，实际：%v", err)
	}
}

func TestTelegramConnectRequiresToken(t *testing.T) {
	ch := NewTelegramChannel(TelegramConfig{})
	err := ch.Connect(context.Background(), func(context.Context, MessageEvent) error { return nil })
	if err == nil {
		t.Fatal("没有 token 时 Connect 应失败")
	}
	if ch.Status().Connected {
		t.Fatal("失败后不该标记为已连接")
	}
}

// —— 事件映射 ——

func TestTelegramGroupMessageMapping(t *testing.T) {
	msg := &telegramMessage{
		MessageID: 7,
		Date:      1700000000,
		Text:      "@diana_bot 你好",
		From:      &telegramUser{ID: 42, FirstName: "小", LastName: "明"},
		Chat:      &telegramChat{ID: -100999, Type: "supergroup", Title: "测试群"},
		Entities:  []telegramEntity{{Type: "mention", Offset: 0, Length: 10}},
	}
	event := telegramMessageToEvent(msg, "8888", "diana_bot")

	if event.Kind != EventKindGroup {
		t.Fatalf("应识别为群聊，实际 %q", event.Kind)
	}
	if event.GroupID != "-100999" {
		t.Fatalf("GroupID 错误：%q", event.GroupID)
	}
	// Bot API 没有「列出我加入的群」，群名只能从消息里带出来；丢了它，
	// 控制台的群管理页就只能显示一串群号。
	if event.GroupName != "测试群" {
		t.Fatalf("GroupName 错误：%q", event.GroupName)
	}
	if event.UserID != "42" {
		t.Fatalf("UserID 错误：%q", event.UserID)
	}
	if event.SenderName != "小 明" {
		t.Fatalf("SenderName 错误：%q", event.SenderName)
	}
	if event.MessageID != "7" {
		t.Fatalf("MessageID 错误：%q", event.MessageID)
	}
	if !event.ToMe {
		t.Fatal("带 mention 实体应视为 @机器人")
	}
	// Telegram 没有 群等级，等级门槛会按「读不到即放行」处理。
	if event.SenderLevel != 0 {
		t.Fatalf("Telegram 不应有群等级，实际 %d", event.SenderLevel)
	}
}

func TestTelegramPrivateMessageAlwaysToMe(t *testing.T) {
	msg := &telegramMessage{
		MessageID: 1,
		Text:      "在吗",
		From:      &telegramUser{ID: 5, Username: "someone"},
		Chat:      &telegramChat{ID: 5, Type: "private"},
	}
	event := telegramMessageToEvent(msg, "8888", "diana_bot")
	if event.Kind != EventKindPrivate {
		t.Fatalf("应识别为私聊，实际 %q", event.Kind)
	}
	if event.GroupID != "" {
		t.Fatalf("私聊不该有 GroupID，实际 %q", event.GroupID)
	}
	if !event.ToMe {
		t.Fatal("私聊天然是对机器人说的")
	}
	if event.SenderName != "someone" {
		t.Fatalf("没有姓名时应回落 username，实际 %q", event.SenderName)
	}
}

func TestTelegramUsesCaptionWhenTextEmpty(t *testing.T) {
	msg := &telegramMessage{
		MessageID: 2,
		Caption:   "图片说明",
		From:      &telegramUser{ID: 5},
		Chat:      &telegramChat{ID: 5, Type: "private"},
	}
	event := telegramMessageToEvent(msg, "", "diana_bot")
	if event.RawMessage != "图片说明" {
		t.Fatalf("纯图片消息应取 caption，实际 %q", event.RawMessage)
	}
}

func TestTelegramCaptionMentionMarksGroupMessageToMe(t *testing.T) {
	msg := &telegramMessage{
		MessageID:       3,
		MessageThreadID: 77,
		Caption:         "@diana_bot 看图",
		CaptionEntities: []telegramEntity{{Type: "mention", Offset: 0, Length: 10}},
		From:            &telegramUser{ID: 5},
		Chat:            &telegramChat{ID: -100999, Type: "supergroup"},
	}
	event := telegramMessageToEvent(msg, "8888", "diana_bot")
	if !event.ToMe {
		t.Fatal("caption_entities 中的 @机器人 应视为直接呼叫")
	}
	if event.MessageThreadID != "77" {
		t.Fatalf("话题 ID 丢失：%q", event.MessageThreadID)
	}
	if routed := routeOutgoingToEvent(event, OutgoingMessage{Text: "收到"}); routed.MessageThreadID != "77" {
		t.Fatalf("出站消息未继承话题 ID：%+v", routed)
	}
}

func TestTelegramMapsAndCachesIncomingPhoto(t *testing.T) {
	t.Setenv("DIANA_HISTORY_MEDIA_DIR", t.TempDir())
	body := tinyJPEGBytes(t)
	api := newFakeTelegramAPI(t, map[string]any{
		"getFile":               map[string]any{"file_path": "photos/a.jpg"},
		"download:photos/a.jpg": body,
	})
	msg := &telegramMessage{
		MessageID: 9,
		Date:      1700000000,
		Caption:   "miku 看图",
		From:      &telegramUser{ID: 5},
		Chat:      &telegramChat{ID: -100999, Type: "supergroup"},
		Photo:     []telegramPhoto{{FileID: "small"}, {FileID: "largest", FileSize: int64(len(body))}},
	}
	event := telegramMessageToEvent(msg, "8888", "mikuabot")
	if len(event.Segments) != 2 || event.Segments[1].Type != "image" || event.Segments[1].Data["file_id"] != "largest" {
		t.Fatalf("photo mapping = %#v", event.Segments)
	}
	event = api.channel().resolveIncomingMedia(context.Background(), event, msg)
	path := event.Segments[1].Data["cached_file"]
	if !strings.Contains(filepath.ToSlash(path), "/telegram/") || !strings.Contains(filepath.ToSlash(path), "/image/group_-100999/9/") {
		t.Fatalf("classified media path = %q", path)
	}
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, body) {
		t.Fatalf("cached photo: bytes=%d err=%v", len(got), err)
	}
}

func TestTelegramMapsAndCachesStaticStickerAsImage(t *testing.T) {
	t.Setenv("DIANA_HISTORY_MEDIA_DIR", t.TempDir())
	body := tinyJPEGBytes(t)
	api := newFakeTelegramAPI(t, map[string]any{
		"getFile":                    map[string]any{"file_path": "stickers/cat.webp"},
		"download:stickers/cat.webp": body,
	})
	msg := &telegramMessage{
		MessageID: 11,
		Date:      1700000000,
		From:      &telegramUser{ID: 5},
		Chat:      &telegramChat{ID: -100999, Type: "supergroup"},
		Sticker: &telegramSticker{
			FileID: "sticker-file", FileUniqueID: "sticker-unique", Emoji: "😾", SetName: "cats", Type: "regular", FileSize: int64(len(body)),
		},
	}
	event := telegramMessageToEvent(msg, "8888", "mikuabot")
	if len(event.Segments) != 2 {
		t.Fatalf("sticker mapping = %#v", event.Segments)
	}
	segment := event.Segments[1]
	if segment.Type != "image" || segment.Data["file_id"] != "sticker-file" || segment.Data["sub_type"] != "telegram_sticker" || segment.Data["summary"] != "😾" {
		t.Fatalf("sticker segment = %#v", segment)
	}
	if label, ok := StickerSegmentLabel(segment); !ok || label != "😾" {
		t.Fatalf("sticker label = %q, %v", label, ok)
	}
	event = api.channel().resolveIncomingMedia(context.Background(), event, msg)
	path := event.Segments[1].Data["cached_file"]
	if !strings.Contains(filepath.ToSlash(path), "/image/group_-100999/11/") {
		t.Fatalf("cached sticker path = %q", path)
	}
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, body) {
		t.Fatalf("cached sticker: bytes=%d err=%v", len(got), err)
	}
}

func TestTelegramUsesAnimatedStickerThumbnailForVision(t *testing.T) {
	msg := &telegramMessage{
		MessageID: 12,
		Chat:      &telegramChat{ID: 5, Type: "private"},
		Sticker: &telegramSticker{
			FileID: "animated-tgs", FileUniqueID: "animated-unique", IsAnimated: true, Emoji: "🥺",
			Thumbnail: &telegramPhoto{FileID: "animated-preview", FileSize: 123},
		},
	}
	event := telegramMessageToEvent(msg, "8888", "mikuabot")
	segment := event.Segments[1]
	if segment.Type != "image" || segment.Data["file_id"] != "animated-preview" || segment.Data["sticker_file_id"] != "animated-tgs" {
		t.Fatalf("animated sticker preview = %#v", segment)
	}
}

func TestTelegramMapsAllSupportedIncomingMedia(t *testing.T) {
	msg := &telegramMessage{
		MessageID: 10,
		Chat:      &telegramChat{ID: 5, Type: "private"},
		Video:     &telegramFile{FileID: "video", FileName: "a.mp4"},
		Animation: &telegramFile{FileID: "animation", FileName: "a.gif"},
		Voice:     &telegramFile{FileID: "voice", MimeType: "audio/ogg"},
		Audio:     &telegramFile{FileID: "audio", FileName: "a.mp3"},
		Document:  &telegramFile{FileID: "document", FileName: "a.pdf"},
	}
	event := telegramMessageToEvent(msg, "8888", "mikuabot")
	var kinds []string
	for _, segment := range event.Segments {
		if segment.Type != "text" {
			kinds = append(kinds, segment.Type)
		}
	}
	if got := strings.Join(kinds, ","); got != "video,video,record,record,file" {
		t.Fatalf("media kinds = %q", got)
	}
}

func TestTelegramIgnoresUnknownChatType(t *testing.T) {
	msg := &telegramMessage{Chat: &telegramChat{ID: 1, Type: "unknown"}}
	if event := telegramMessageToEvent(msg, "", "diana_bot"); event.Kind != "" {
		t.Fatalf("未知会话类型应被忽略，实际 %q", event.Kind)
	}
	if event := telegramMessageToEvent(nil, "", "diana_bot"); event.Kind != "" {
		t.Fatal("nil 消息应被忽略")
	}
}

// getUpdates 的 offset 必须推进，否则同一条消息会被反复投递。
func TestTelegramAdvancesUpdateOffset(t *testing.T) {
	api := newFakeTelegramAPI(t, map[string]any{
		"getUpdates": []map[string]any{
			{"update_id": 100, "message": map[string]any{
				"message_id": 1, "text": "a",
				"chat": map[string]any{"id": 5, "type": "private"},
				"from": map[string]any{"id": 5},
			}},
			{"update_id": 101, "message": map[string]any{
				"message_id": 2, "text": "b",
				"chat": map[string]any{"id": 5, "type": "private"},
				"from": map[string]any{"id": 5},
			}},
		},
	})
	ch := api.channel()

	if _, err := ch.fetchUpdates(context.Background()); err != nil {
		t.Fatalf("首次拉取失败：%v", err)
	}
	if _, err := ch.fetchUpdates(context.Background()); err != nil {
		t.Fatalf("二次拉取失败：%v", err)
	}

	calls := api.callsOf("getUpdates")
	if len(calls) != 2 {
		t.Fatalf("期望 2 次 getUpdates，实际 %d", len(calls))
	}
	if _, ok := calls[0].Params["offset"]; ok {
		t.Fatal("首次拉取不该带 offset")
	}
	if got := calls[1].Params["offset"]; got != float64(102) {
		t.Fatalf("offset 应推进到 102，实际 %v", got)
	}
}

func TestTelegramDispatchesUpdatesToHandler(t *testing.T) {
	api := newFakeTelegramAPI(t, map[string]any{
		"getUpdates": []map[string]any{
			{"update_id": 1, "message": map[string]any{
				"message_id": 9, "text": "hello",
				"chat": map[string]any{"id": 77, "type": "private"},
				"from": map[string]any{"id": 77, "first_name": "A"},
			}},
		},
	})
	ch := api.channel()

	var got []MessageEvent
	ch.handler = func(_ context.Context, event MessageEvent) error {
		got = append(got, event)
		return nil
	}
	updates, err := ch.fetchUpdates(context.Background())
	if err != nil {
		t.Fatalf("拉取失败：%v", err)
	}
	for _, update := range updates {
		ch.dispatch(context.Background(), update)
	}
	if len(got) != 1 || got[0].RawMessage != "hello" || got[0].UserID != "77" {
		t.Fatalf("事件投递错误：%+v", got)
	}
}

func TestTelegramLocalPathDetection(t *testing.T) {
	if telegramLocalPath("https://example.com/a.jpg") != "" {
		t.Fatal("远程 URL 不该当成本地文件")
	}
	if got := telegramLocalPath("/tmp/a.mp4"); got != "/tmp/a.mp4" {
		t.Fatalf("绝对路径应识别为本地文件，实际 %q", got)
	}
	if got := telegramLocalPath("file:///tmp/b.mp4"); got != "/tmp/b.mp4" {
		t.Fatalf("file:// 应剥掉前缀，实际 %q", got)
	}
}

func TestTelegramStatusReflectsConfig(t *testing.T) {
	ch := NewTelegramChannel(TelegramConfig{BotToken: "t"})
	if !strings.Contains(ch.Status().Endpoint, telegramDefaultAPIBase) {
		t.Fatalf("未配置时应用官方地址，实际 %q", ch.Status().Endpoint)
	}
	ch.SetConfig(TelegramConfig{BotToken: "t", APIBaseURL: "https://tg.example.com"})
	if !strings.Contains(ch.Status().Endpoint, "tg.example.com") {
		t.Fatalf("自建地址未生效：%q", ch.Status().Endpoint)
	}
	_ = ch.Close()
	if ch.Status().Connected {
		t.Fatal("Close 后不该是已连接")
	}
}

func TestTelegramCloseStopsPolling(t *testing.T) {
	api := newFakeTelegramAPI(t, map[string]any{
		"getMe":      map[string]any{"id": 4242},
		"getUpdates": []map[string]any{},
	})
	ch := api.channel()

	done := make(chan error, 1)
	go func() {
		done <- ch.Connect(context.Background(), func(context.Context, MessageEvent) error { return nil })
	}()

	// 等 getMe 完成，说明已经进入轮询。
	deadline := time.After(3 * time.Second)
	for len(api.callsOf("getMe")) == 0 {
		select {
		case <-deadline:
			t.Fatal("等待 getMe 超时")
		case <-time.After(10 * time.Millisecond):
		}
	}
	if ch.Status().SelfID != "4242" {
		t.Fatalf("应从 getMe 取得 self id，实际 %q", ch.Status().SelfID)
	}

	_ = ch.Close()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Close 后轮询未退出")
	}
}

// —— @提及 精确匹配 ——

// 只要看到「有 @ 就算」的话，群里任何带邮箱或 @别人的消息都会误触发。
func TestTelegramMentionMustMatchBotUsername(t *testing.T) {
	cases := []struct {
		name     string
		text     string
		entities []telegramEntity
		want     bool
	}{
		{"@本机器人", "@diana_bot 在吗", []telegramEntity{{Type: "mention", Offset: 0, Length: 10}}, true},
		{"@别人", "@someone_else 在吗", []telegramEntity{{Type: "mention", Offset: 0, Length: 13}}, false},
		{"纯邮箱不算", "联系 a@b.com", nil, false},
		{"文本里有@但无实体", "价格 @ 100", nil, false},
		{"大小写不敏感", "@Diana_Bot hi", []telegramEntity{{Type: "mention", Offset: 0, Length: 10}}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := telegramMentionsBot(tc.text, tc.entities, "8888", "diana_bot")
			if got != tc.want {
				t.Fatalf("%q 期望 %v，实际 %v", tc.text, tc.want, got)
			}
		})
	}
}

// entity 的 offset/length 以 UTF-16 码元计，中文前缀不能按字节切。
func TestTelegramMentionHandlesUTF16Offsets(t *testing.T) {
	text := "你好呀 @diana_bot"
	// "你好呀 " 是 4 个 UTF-16 码元，@diana_bot 是 10 个。
	entities := []telegramEntity{{Type: "mention", Offset: 4, Length: 10}}
	if !telegramMentionsBot(text, entities, "8888", "diana_bot") {
		t.Fatal("中文前缀后的 @提及 应能正确识别")
	}
}

func TestTelegramTextMentionMatchesBySelfID(t *testing.T) {
	entities := []telegramEntity{{Type: "text_mention", Offset: 0, Length: 2, User: &telegramUser{ID: 8888}}}
	if !telegramMentionsBot("机器人 hi", entities, "8888", "") {
		t.Fatal("text_mention 应按用户 ID 匹配")
	}
	other := []telegramEntity{{Type: "text_mention", Offset: 0, Length: 2, User: &telegramUser{ID: 9999}}}
	if telegramMentionsBot("别人 hi", other, "8888", "") {
		t.Fatal("text_mention 指向别人时不该触发")
	}
}

// —— 入群通知 ——

// 不映射 new_chat_members 的话，欢迎语在 Telegram 上永远不触发。
func TestTelegramMapsNewChatMemberToNotice(t *testing.T) {
	msg := &telegramMessage{
		MessageID:      9,
		Date:           1700000000,
		Chat:           &telegramChat{ID: -100777, Type: "supergroup"},
		From:           &telegramUser{ID: 1, FirstName: "邀请人"},
		NewChatMembers: []telegramUser{{ID: 4242, FirstName: "新", LastName: "人"}},
	}
	event := telegramMessageToEvent(msg, "8888", "diana_bot")

	if event.Kind != EventKindNotice {
		t.Fatalf("应映射为 notice，实际 %q", event.Kind)
	}
	if event.SubType != "group_increase" {
		t.Fatalf("SubType 应为 group_increase，实际 %q", event.SubType)
	}
	if event.GroupID != "-100777" {
		t.Fatalf("GroupID 错误：%q", event.GroupID)
	}
	// UserID 必须是新加入的人，不是发出通知的人。
	if event.UserID != "4242" {
		t.Fatalf("UserID 应为新成员，实际 %q", event.UserID)
	}
	if event.SenderName != "新 人" {
		t.Fatalf("SenderName 错误：%q", event.SenderName)
	}
}

func TestTelegramDisplayNameFallsBackToUsername(t *testing.T) {
	if got := telegramDisplayName(&telegramUser{Username: "nick"}); got != "nick" {
		t.Fatalf("无姓名时应回落 username，实际 %q", got)
	}
	if got := telegramDisplayName(nil); got != "" {
		t.Fatalf("nil 用户应返回空串，实际 %q", got)
	}
}

func TestTelegramMessageToEventMapsReplyToNeutralSegment(t *testing.T) {
	// Telegram 的 reply_to_message 要落成与 OneBot 同一种 reply 段，
	// 模型才能在两个平台看到同样的引用标记，并用同样的写法引用回去。
	event := telegramMessageToEvent(&telegramMessage{
		MessageID: 200,
		Chat:      &telegramChat{ID: -100200300, Type: "supergroup"},
		From:      &telegramUser{ID: 42},
		Text:      "就是这张",
		ReplyTo:   &telegramMessage{MessageID: 199},
	}, "7", "diana_bot")

	if len(event.Segments) != 2 || event.Segments[0].Type != "reply" {
		t.Fatalf("segments = %#v, want a leading reply segment", event.Segments)
	}
	if event.Segments[0].Data["id"] != "199" {
		t.Fatalf("reply segment = %#v", event.Segments[0])
	}
	if got := PlainText(event.Segments); got != "[diana-reply:199]就是这张" {
		t.Fatalf("PlainText = %q", got)
	}
}

func TestTelegramMessageToEventWithoutReplyKeepsTextOnly(t *testing.T) {
	event := telegramMessageToEvent(&telegramMessage{
		MessageID: 201,
		Chat:      &telegramChat{ID: -100200300, Type: "supergroup"},
		From:      &telegramUser{ID: 42},
		Text:      "普通消息",
	}, "7", "diana_bot")
	if len(event.Segments) != 1 || event.Segments[0].Type != "text" {
		t.Fatalf("segments = %#v, want text only", event.Segments)
	}
}

// Telegram 侧的提及：正文写「@昵称」，另附一条 text_mention entity 指向用户 id。
// 这是给「没有 username 的人」准备的提及方式，等于把提及虚拟出来。
func TestTelegramRendersMentionMarkerAsTextMention(t *testing.T) {
	api := newFakeTelegramAPI(t, nil)
	msg := OutgoingMessage{
		GroupID:      "-100123",
		Text:         "[diana-at:10002] 看下这个",
		MentionNames: map[string]string{"10002": "Alice"},
	}
	if err := api.channel().Send(context.Background(), msg); err != nil {
		t.Fatalf("发送失败：%v", err)
	}
	calls := api.callsOf("sendMessage")
	if len(calls) != 1 {
		t.Fatalf("期望 1 次 sendMessage，实际 %d", len(calls))
	}
	// 标记本身绝不能出现在正文里——那正是这套机制要消灭的东西。
	text, _ := calls[0].Params["text"].(string)
	if text != "@Alice 看下这个" {
		t.Fatalf("text = %q", text)
	}
	entities, ok := calls[0].Params["entities"].([]any)
	if !ok || len(entities) != 1 {
		t.Fatalf("entities = %#v", calls[0].Params["entities"])
	}
	entity, _ := entities[0].(map[string]any)
	if entity["type"] != "text_mention" || entity["offset"] != float64(0) || entity["length"] != float64(6) {
		t.Fatalf("entity = %#v", entity)
	}
	user, _ := entity["user"].(map[string]any)
	if user["id"] != float64(10002) {
		t.Fatalf("entity user = %#v", entity["user"])
	}
	// 只传 entities、不传 parse_mode：正文仍按纯文本发，* # ` 不会被当格式标记。
	if _, has := calls[0].Params["parse_mode"]; has {
		t.Fatalf("不该设置 parse_mode：%#v", calls[0].Params)
	}
}

// 没有提及的普通消息不该多带一个空的 entities 参数。
func TestTelegramOmitsEntitiesWithoutMentions(t *testing.T) {
	api := newFakeTelegramAPI(t, nil)
	if err := api.channel().Send(context.Background(), OutgoingMessage{GroupID: "-100123", Text: "你好"}); err != nil {
		t.Fatalf("发送失败：%v", err)
	}
	calls := api.callsOf("sendMessage")
	if _, has := calls[0].Params["entities"]; has {
		t.Fatalf("不该出现 entities：%#v", calls[0].Params)
	}
}
