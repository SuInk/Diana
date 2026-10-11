// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
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
		} else if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
			if err := r.ParseMultipartForm(1 << 20); err != nil {
				t.Error(err)
				return
			}
			defer r.MultipartForm.RemoveAll()
			for k, v := range r.MultipartForm.Value {
				params[k] = v[0]
			}
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

func TestTelegramTransportErrorDoesNotLeakBotToken(t *testing.T) {
	const token = "super-secret-telegram-token"
	channel := NewTelegramChannel(TelegramConfig{BotToken: token})
	channel.client = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("network unavailable")
	})}

	_, err := channel.CallAPI(context.Background(), "getMe", nil)
	if err == nil {
		t.Fatal("expected transport error")
	}
	if strings.Contains(err.Error(), token) || strings.Contains(err.Error(), "/bot"+token) {
		t.Fatalf("transport error leaked bot token: %v", err)
	}
	if !strings.Contains(err.Error(), "network unavailable") {
		t.Fatalf("transport error lost its cause: %v", err)
	}
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

func TestTelegramSendsPrivateTextDraft(t *testing.T) {
	api := newFakeTelegramAPI(t, nil)
	err := api.channel().SendTextDraft(context.Background(), OutgoingMessage{UserID: "555", Text: "正在生成"}, 73)
	if err != nil {
		t.Fatalf("发送草稿失败：%v", err)
	}
	calls := api.callsOf("sendMessageDraft")
	if len(calls) != 1 {
		t.Fatalf("应调用一次 sendMessageDraft，实际 %+v", calls)
	}
	if calls[0].Params["chat_id"] != "555" || calls[0].Params["draft_id"].(float64) != 73 || calls[0].Params["text"] != "正在生成" {
		t.Fatalf("草稿参数错误：%+v", calls[0].Params)
	}
}

func TestTelegramSkipsGroupTextDraft(t *testing.T) {
	api := newFakeTelegramAPI(t, nil)
	if err := api.channel().SendTextDraft(context.Background(), OutgoingMessage{GroupID: "-1001", Text: "draft"}, 1); err != nil {
		t.Fatalf("群聊草稿应无操作而非报错：%v", err)
	}
	if calls := api.callsOf("sendMessageDraft"); len(calls) != 0 {
		t.Fatalf("Telegram 群聊不应调用 sendMessageDraft：%+v", calls)
	}
}

func TestTelegramSendsTypingActionToTopic(t *testing.T) {
	api := newFakeTelegramAPI(t, nil)
	err := api.channel().SendChatAction(context.Background(), OutgoingMessage{GroupID: "-1001", MessageThreadID: "88"}, "typing")
	if err != nil {
		t.Fatalf("发送输入状态失败：%v", err)
	}
	calls := api.callsOf("sendChatAction")
	if len(calls) != 1 || calls[0].Params["chat_id"] != "-1001" || calls[0].Params["action"] != "typing" || calls[0].Params["message_thread_id"] != "88" {
		t.Fatalf("输入状态参数错误：%+v", calls)
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
	if got := telegramTestReplyID(t, params); got != "42" {
		t.Fatalf("reply id = %q", got)
	}
}

func telegramTestReplyID(t *testing.T, params map[string]any) string {
	t.Helper()
	for _, key := range []string{"reply_to_message_id", "allow_sending_without_reply"} {
		if _, ok := params[key]; ok {
			t.Errorf("legacy field %s present", key)
		}
	}
	value, exists := params["reply_parameters"]
	if !exists {
		return ""
	}
	if text, ok := value.(string); ok {
		if err := json.Unmarshal([]byte(text), &value); err != nil {
			t.Errorf("invalid reply JSON: %v", err)
			return ""
		}
	}
	reply, ok := value.(map[string]any)
	if !ok {
		t.Errorf("reply_parameters must be object: %#v", value)
		return ""
	}
	id, ok := reply["message_id"].(float64)
	if !ok || id <= 0 || id != float64(int64(id)) || reply["allow_sending_without_reply"] != true || len(reply) != 2 {
		t.Errorf("bad reply_parameters: %#v", reply)
		return ""
	}
	return fmt.Sprintf("%.0f", id)
}

func TestTelegramVideoPreservesReplyTarget(t *testing.T) {
	api := newFakeTelegramAPI(t, nil)
	if err := api.channel().Send(context.Background(), OutgoingMessage{GroupID: "-10001", ReplyMessageID: "7", VideoURLs: []string{"https://example.com/video.mp4"}}); err != nil {
		t.Fatal(err)
	}
	p := api.callsOf("sendVideo")[0].Params
	if telegramTestReplyID(t, p) != "7" {
		t.Fatalf("video lost reply: %#v", p)
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
		Photo:     []telegramPhoto{{FileID: "small"}, {FileID: "largest", FileUniqueID: "stable-photo", FileSize: int64(len(body))}},
	}
	event := telegramMessageToEvent(msg, "8888", "examplebot")
	if len(event.Segments) != 2 || event.Segments[1].Type != "image" || event.Segments[1].Data["file_id"] != "largest" {
		t.Fatalf("photo mapping = %#v", event.Segments)
	}
	event = api.channel().resolveIncomingMedia(context.Background(), event, msg)
	path := event.Segments[1].Data["cached_file"]
	if filepath.Base(filepath.Dir(path)) != imageBytesSHA256(body) {
		t.Fatalf("content-addressed media path = %q", path)
	}
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, body) {
		t.Fatalf("cached photo: bytes=%d err=%v", len(got), err)
	}
	// A fresh channel and changed download ID reuse the durable unique-id index.
	msg.MessageID++
	msg.Photo[1].FileID = "largest-refreshed"
	again := telegramMessageToEvent(msg, "8888", "examplebot")
	again = api.channel().resolveIncomingMedia(context.Background(), again, msg)
	if again.Segments[1].Data["cached_file"] != path || len(api.callsOf("getFile")) != 1 {
		t.Fatalf("repeated Telegram file downloaded again: %#v", again.Segments)
	}
	if again.Segments[1].Data[imageContentSHA256Key] != imageBytesSHA256(body) {
		t.Fatal("Telegram media is missing its content hash")
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
	event := telegramMessageToEvent(msg, "8888", "examplebot")
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
	if filepath.Base(filepath.Dir(path)) != imageBytesSHA256(body) {
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
	event := telegramMessageToEvent(msg, "8888", "examplebot")
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
	event := telegramMessageToEvent(msg, "8888", "examplebot")
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

// 编辑更新要在 handler 成功后才推进 offset；否则这次唯一的新 revision
// 还没写进持久层就会被 Telegram 永久确认掉。
func TestTelegramAcknowledgesEditedUpdateOffsetAfterDispatch(t *testing.T) {
	api := newFakeTelegramAPI(t, map[string]any{
		"getUpdates": []map[string]any{
			{"update_id": 100, "edited_message": map[string]any{
				"message_id": 1, "date": 10, "edit_date": 20, "text": "a",
				"chat": map[string]any{"id": 5, "type": "private"},
				"from": map[string]any{"id": 5},
			}},
			{"update_id": 101, "edited_message": map[string]any{
				"message_id": 2, "date": 11, "edit_date": 21, "text": "b",
				"chat": map[string]any{"id": 5, "type": "private"},
				"from": map[string]any{"id": 5},
			}},
		},
	})
	ch := api.channel()

	updates, err := ch.fetchUpdates(context.Background())
	if err != nil {
		t.Fatalf("首次拉取失败：%v", err)
	}
	ch.handler = func(context.Context, MessageEvent) error { return errors.New("database busy") }
	if err := ch.dispatch(context.Background(), updates[0]); err == nil {
		t.Fatal("handler 失败应保留更新待重试")
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
	if _, ok := calls[1].Params["offset"]; ok {
		t.Fatal("失败的 update 不该推进 offset")
	}

	ch.handler = func(context.Context, MessageEvent) error { return nil }
	for _, update := range updates {
		if err := ch.dispatch(context.Background(), update); err != nil {
			t.Fatalf("成功重试失败：%v", err)
		}
		ch.acknowledgeUpdate(update.UpdateID)
	}
	if _, err := ch.fetchUpdates(context.Background()); err != nil {
		t.Fatalf("三次拉取失败：%v", err)
	}
	calls = api.callsOf("getUpdates")
	if got := calls[2].Params["offset"]; got != float64(102) {
		t.Fatalf("成功处理后 offset 应推进到 102，实际 %v", got)
	}
}

func TestTelegramDispatchAcknowledgesNonEditedHandlerFailures(t *testing.T) {
	tests := []struct {
		name   string
		update telegramUpdate
	}{
		{
			name: "ordinary message",
			update: telegramUpdate{UpdateID: 200, Message: &telegramMessage{
				MessageID: 1,
				Date:      10,
				Text:      "ordinary",
				Chat:      &telegramChat{ID: 5, Type: "private"},
				From:      &telegramUser{ID: 5},
			}},
		},
		{
			name: "reaction notice",
			update: telegramUpdate{UpdateID: 201, MessageReaction: &telegramReaction{
				Chat:      &telegramChat{ID: -1005, Type: "supergroup"},
				MessageID: 2,
				User:      &telegramUser{ID: 5},
				Date:      11,
				NewReaction: []telegramReactionType{{
					Type:  "emoji",
					Emoji: "👍",
				}},
			}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ch := NewTelegramChannel(TelegramConfig{})
			calls := 0
			ch.handler = func(context.Context, MessageEvent) error {
				calls++
				return errors.New("downstream rejected event")
			}
			if err := ch.dispatch(context.Background(), tt.update); err != nil {
				t.Fatalf("non-edited handler failure should not retain Telegram update: %v", err)
			}
			if calls != 1 {
				t.Fatalf("handler calls=%d, want 1", calls)
			}
			ch.acknowledgeUpdate(tt.update.UpdateID)
			ch.mu.RLock()
			offset := ch.offset
			ch.mu.RUnlock()
			if offset != tt.update.UpdateID+1 {
				t.Fatalf("offset=%d, want %d", offset, tt.update.UpdateID+1)
			}
		})
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

func TestTelegramDispatchMarksEditedMessages(t *testing.T) {
	tests := []struct {
		name       string
		raw        string
		wantKind   EventKind
		wantGroup  string
		wantUser   string
		wantText   string
		wantTime   int64
		wantEditAt int64
		wantUpdate int64
	}{
		{
			name: "edited private message",
			raw: `{
				"update_id": 1,
				"edited_message": {
					"message_id": 9,
					"date": 100,
					"edit_date": 150,
					"text": "edited private text",
					"chat": {"id": 77, "type": "private"},
					"from": {"id": 77, "first_name": "A"}
				}
			}`,
			wantKind:   EventKindPrivate,
			wantUser:   "77",
			wantText:   "edited private text",
			wantTime:   100,
			wantEditAt: 150,
			wantUpdate: 1,
		},
		{
			name: "edited channel post",
			raw: `{
				"update_id": 2,
				"edited_channel_post": {
					"message_id": 10,
					"date": 200,
					"edit_date": 275,
					"text": "edited channel text",
					"chat": {"id": -10077, "type": "channel", "title": "updates"}
				}
			}`,
			wantKind:   EventKindGroup,
			wantGroup:  "-10077",
			wantText:   "edited channel text",
			wantTime:   200,
			wantEditAt: 275,
			wantUpdate: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var update telegramUpdate
			if err := json.Unmarshal([]byte(tt.raw), &update); err != nil {
				t.Fatal(err)
			}
			ch := NewTelegramChannel(TelegramConfig{})
			var got []MessageEvent
			ch.handler = func(_ context.Context, event MessageEvent) error {
				got = append(got, event)
				return nil
			}

			ch.dispatch(context.Background(), update)

			if len(got) != 1 {
				t.Fatalf("编辑更新投递了 %d 条事件，期望 1 条：%+v", len(got), got)
			}
			event := got[0]
			if !event.Edited || event.EditTime != tt.wantEditAt {
				t.Fatalf("编辑标记错误：Edited=%v EditTime=%d，期望 true/%d", event.Edited, event.EditTime, tt.wantEditAt)
			}
			if event.TelegramUpdateID != tt.wantUpdate {
				t.Fatalf("TelegramUpdateID=%d，期望 %d", event.TelegramUpdateID, tt.wantUpdate)
			}
			if event.Time != tt.wantTime {
				t.Fatalf("原始发送时间被编辑时间覆盖：Time=%d，期望 %d", event.Time, tt.wantTime)
			}
			if event.Kind != tt.wantKind || event.GroupID != tt.wantGroup || event.UserID != tt.wantUser || event.RawMessage != tt.wantText {
				t.Fatalf("编辑消息映射错误：%+v", event)
			}
		})
	}
}

func TestTelegramPassesBotIdentityToSemanticReplyGate(t *testing.T) {
	ch := NewTelegramChannel(TelegramConfig{})
	ch.botUsername = "diana_bot"
	var got []MessageEvent
	ch.handler = func(_ context.Context, event MessageEvent) error {
		got = append(got, event)
		return nil
	}

	ch.dispatch(context.Background(), telegramUpdate{Message: &telegramMessage{
		MessageID: 1,
		Text:      "机器人自动播报",
		From:      &telegramUser{ID: 42, IsBot: true, Username: "other_bot"},
		Chat:      &telegramChat{ID: -1001, Type: "supergroup"},
	}})

	if len(got) != 1 || !got[0].SenderIsBot || got[0].Platform != PlatformTelegram {
		t.Fatalf("Bot 消息应携带身份进入语义判断，实际 %+v", got)
	}
}

func TestTelegramAllowsBotGroupMessageWhenExplicitlyMentioned(t *testing.T) {
	ch := NewTelegramChannel(TelegramConfig{})
	ch.botUsername = "diana_bot"
	var got []MessageEvent
	ch.handler = func(_ context.Context, event MessageEvent) error {
		got = append(got, event)
		return nil
	}

	ch.dispatch(context.Background(), telegramUpdate{Message: &telegramMessage{
		MessageID: 2,
		Text:      "@diana_bot 请处理",
		Entities:  []telegramEntity{{Type: "mention", Offset: 0, Length: 10}},
		From:      &telegramUser{ID: 42, IsBot: true, Username: "other_bot"},
		Chat:      &telegramChat{ID: -1001, Type: "supergroup"},
	}})

	if len(got) != 1 || !got[0].ToMe {
		t.Fatalf("明确提及本机器人的 Bot 群消息应放行，实际 %+v", got)
	}
}

func TestTelegramDoesNotClassifyAnonymousSenderAsBot(t *testing.T) {
	ch := NewTelegramChannel(TelegramConfig{})
	var got []MessageEvent
	ch.handler = func(_ context.Context, event MessageEvent) error {
		got = append(got, event)
		return nil
	}

	ch.dispatch(context.Background(), telegramUpdate{Message: &telegramMessage{
		MessageID:  3,
		SenderChat: &telegramChat{ID: -1001, Type: "supergroup"},
		Text:       "机器人自动播报",
		From:       &telegramUser{ID: 42, IsBot: true, Username: "other_bot"},
		Chat:       &telegramChat{ID: -1001, Type: "supergroup"},
	}})

	if len(got) != 1 || got[0].SenderIsBot {
		t.Fatalf("匿名发送者不应被识别为 Bot，实际 %+v", got)
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

	// 等 getMe 的结果真正写进状态，说明已经进入轮询。假 API 在收到请求时就记下
	// 调用，客户端解析响应、写 SelfID 还在另一个协程里；只等「调用过」再立刻读
	// 状态，会偶发读到空的 SelfID。
	deadline := time.After(3 * time.Second)
	for ch.Status().SelfID != "4242" {
		select {
		case <-deadline:
			t.Fatalf("应从 getMe 取得 self id，实际 %q（getMe 调用 %d 次）", ch.Status().SelfID, len(api.callsOf("getMe")))
		case <-time.After(10 * time.Millisecond):
		}
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

// /cmd@username 是 Telegram 群里指定机器人的写法，Bot API 只发 bot_command
// entity；不认它的话，发给自己的命令会被路由当成「别人的指令」而忽略。
func TestTelegramBotCommandAddressedToSelfCountsAsMention(t *testing.T) {
	cases := []struct {
		name     string
		text     string
		entities []telegramEntity
		want     bool
	}{
		{"命令点名本机器人", "/status@diana_bot", []telegramEntity{{Type: "bot_command", Offset: 0, Length: 17}}, true},
		{"命令点名别的机器人", "/status@otherbot", []telegramEntity{{Type: "bot_command", Offset: 0, Length: 16}}, false},
		{"命令没点名", "/status", []telegramEntity{{Type: "bot_command", Offset: 0, Length: 7}}, false},
		{"大小写不敏感", "/Status@Diana_Bot", []telegramEntity{{Type: "bot_command", Offset: 0, Length: 17}}, true},
		{"中文前缀按 UTF-16 取位", "你好 /status@diana_bot", []telegramEntity{{Type: "bot_command", Offset: 3, Length: 17}}, true},
		{"实体越界不误判", "/status@diana_bot", []telegramEntity{{Type: "bot_command", Offset: 0, Length: 99}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := telegramMentionsBot(tc.text, tc.entities, "8888", "diana_bot"); got != tc.want {
				t.Fatalf("%q 期望 %v，实际 %v", tc.text, tc.want, got)
			}
		})
	}
}

func TestTelegramGroupCommandToSelfSetsToMe(t *testing.T) {
	msg := &telegramMessage{
		MessageID: 113954,
		Date:      1789054343,
		Text:      "/status@examplebot",
		Entities:  []telegramEntity{{Type: "bot_command", Offset: 0, Length: 18}},
		From:      &telegramUser{ID: 70002},
		Chat:      &telegramChat{ID: -100200400, Type: "supergroup"},
	}
	event := telegramMessageToEvent(msg, "99999", "examplebot")
	if !event.ToMe {
		t.Fatal("群里 /status@examplebot 是发给本机器人的命令，应判为 ToMe")
	}
	if event.SelfUsername != "examplebot" {
		t.Fatalf("事件应带上本机器人用户名，实际 %q", event.SelfUsername)
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

func TestTelegramReplyParametersAcrossTransports(t *testing.T) {
	for _, local := range []bool{false, true} {
		for _, reply := range []string{"7", "", "0", "-7", "im_unknown", "7oops", "9223372036854775808"} {
			t.Run(fmt.Sprintf("local=%v/reply=%s", local, reply), func(t *testing.T) {
				api := newFakeTelegramAPI(t, map[string]any{"sendMediaGroup": []map[string]any{{"message_id": 11}, {"message_id": 12}}})
				source := "https://example.com/media.jpg"
				if local {
					source = filepath.Join(t.TempDir(), "media.jpg")
					if err := os.WriteFile(source, tinyJPEGBytes(t), 0600); err != nil {
						t.Fatal(err)
					}
				}
				ch := api.channel()
				msg := OutgoingMessage{GroupID: "-10001", MessageThreadID: "3", ReplyMessageID: reply, Text: "text", ImageURLs: []string{source}, VideoURLs: []string{source}, AudioURLs: []string{source}, Segments: []MessageSegment{{Type: "file", Data: map[string]string{"file": source}}}}
				if err := ch.Send(context.Background(), msg); err != nil {
					t.Fatal(err)
				}
				for _, media := range []struct{ method, field string }{{"sendAnimation", "animation"}, {"sendSticker", "sticker"}} {
					if _, err := ch.sendMedia(context.Background(), msg.GroupID, msg.MessageThreadID, media.method, media.field, source, reply); err != nil {
						t.Fatal(err)
					}
				}
				if err := ch.Send(context.Background(), OutgoingMessage{GroupID: msg.GroupID, MessageThreadID: msg.MessageThreadID, ReplyMessageID: reply, ImageAlbum: true, ImageURLs: []string{source, source}}); err != nil {
					t.Fatal(err)
				}
				for _, method := range []string{"sendMessage", "sendPhoto", "sendVideo", "sendAudio", "sendDocument", "sendAnimation", "sendSticker", "sendMediaGroup"} {
					calls := api.callsOf(method)
					if len(calls) != 1 {
						t.Fatalf("%s calls=%#v", method, calls)
					}
					p := calls[0].Params
					want := ""
					if reply == "7" {
						want = "7"
					}
					if got := telegramTestReplyID(t, p); got != want {
						t.Errorf("%s reply=%s want=%s", method, got, want)
					}
					if p["chat_id"] != "-10001" || p["message_thread_id"] != "3" {
						t.Errorf("routing changed: %#v", p)
					}
				}
			})
		}
	}
}
