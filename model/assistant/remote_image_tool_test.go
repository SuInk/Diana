package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/agent"
)

func TestRemoteImageViewThenTelegramUpload(t *testing.T) {
	t.Setenv("DIANA_ALLOW_PRIVATE_HTTP_FETCHES", "true")
	var data bytes.Buffer
	if err := png.Encode(&data, image.NewRGBA(image.Rect(0, 0, 20, 20))); err != nil {
		t.Fatal(err)
	}
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(data.Bytes())
	}))
	defer source.Close()
	var uploaded []byte
	var chatID, threadID string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Error(err)
			http.Error(w, "invalid upload", 400)
			return
		}
		defer r.MultipartForm.RemoveAll()
		chatID, threadID = r.FormValue("chat_id"), r.FormValue("message_thread_id")
		file, _, err := r.FormFile("photo")
		if err != nil {
			t.Error(err)
			http.Error(w, "no photo", 400)
			return
		}
		defer file.Close()
		uploaded, _ = io.ReadAll(file)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":42}}`))
	}))
	defer api.Close()
	event := MessageEvent{Platform: PlatformTelegram, Kind: EventKindGroup, GroupID: "-100123", MessageThreadID: "77", UserID: "123"}
	r := NewRuntime(BotConfig{Platform: PlatformTelegram}, NewTelegramChannel(TelegramConfig{BotToken: "test", APIBaseURL: api.URL}), NewPluginManager(), nil, nil, nil, nil)
	tool := newDianaRemoteImageTool(r, event)
	ctx := context.Background()
	if _, err := tool.Run(ctx, map[string]any{"action": "send", "image_id": "remote_image_1"}); err == nil {
		t.Fatal("unread image accepted")
	}
	output, err := tool.Run(ctx, map[string]any{"action": "view", "url": source.URL})
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatal(err)
	}
	// 回复工具也叫 send，view 的结果要说清图还没发、发图得再调 action=send。
	if message, _ := result["message"].(string); !strings.Contains(message, "还没发到聊天") || !strings.Contains(message, "action=send、image_id="+result["image_id"].(string)) {
		t.Fatalf("view message does not explain how to send: %q", message)
	}
	parts := tool.ToolResultParts(output)
	if len(parts) != 1 || parts[0].ImageURL == "" {
		t.Fatal("missing visual attachment")
	}
	if _, err := tool.Run(ctx, map[string]any{"action": "send", "image_id": result["image_id"]}); err != nil {
		t.Fatal(err)
	}
	if len(uploaded) == 0 || chatID != "-100123" || threadID != "77" {
		t.Fatalf("upload bytes=%d chat=%s thread=%s", len(uploaded), chatID, threadID)
	}
	if len(tool.ToolResultParts("")) != 0 {
		t.Fatal("stale image attached after send")
	}
}

func TestRemoteImageRejectsNonImageAndPrivateURL(t *testing.T) {
	t.Setenv("DIANA_ALLOW_PRIVATE_HTTP_FETCHES", "false")
	tool := newDianaRemoteImageTool(nil, MessageEvent{})
	for _, url := range []string{"file:///etc/passwd", "http://127.0.0.1/a.png"} {
		if _, err := tool.Run(context.Background(), map[string]any{"action": "view", "url": url}); err == nil {
			t.Fatalf("accepted %s", url)
		}
	}
	t.Setenv("DIANA_ALLOW_PRIVATE_HTTP_FETCHES", "true")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("not an image")) }))
	defer server.Close()
	if _, err := tool.Run(context.Background(), map[string]any{"action": "view", "url": server.URL}); err == nil {
		t.Fatal("accepted text as image")
	}
}

func TestAgentToolBudgetDefaultsToUnlimited(t *testing.T) {
	if (agent.Config{}).WithDefaults().MaxSteps != 0 {
		t.Fatal("default must be unlimited")
	}
}

// Telegram 头像没有公开链接，模型以前只能说「看不到自己的头像」。
func TestRemoteImageViewAvatarShowsTelegramBotOwnAvatarAndSendsIt(t *testing.T) {
	var avatar bytes.Buffer
	if err := png.Encode(&avatar, image.NewRGBA(image.Rect(0, 0, 32, 32))); err != nil {
		t.Fatal(err)
	}
	api := newFakeTelegramAPI(t, map[string]any{
		"getUserProfilePhotos":    map[string]any{"photos": [][]any{{map[string]any{"file_id": "bot-photo", "width": 320, "height": 320}}}},
		"getFile":                 map[string]any{"file_path": "photos/bot.png"},
		"download:photos/bot.png": avatar.Bytes(),
		"sendPhoto":               map[string]any{"message_id": 7},
	})
	event := MessageEvent{Platform: PlatformTelegram, Kind: EventKindGroup, GroupID: "-100123", UserID: "22222"}
	r := NewRuntime(BotConfig{Platform: PlatformTelegram, BotAccount: "12345"}, api.channel(), NewPluginManager(), nil, nil, nil, nil)
	tool := newDianaRemoteImageTool(r, event)
	ctx := context.Background()

	output, err := tool.Run(ctx, map[string]any{"action": "view_avatar", "avatar_source": avatarSourceBot})
	if err != nil {
		t.Fatal(err)
	}
	photos := api.callsOf("getUserProfilePhotos")
	if len(photos) != 1 || stringFromAny(photos[0].Params["user_id"]) != "12345" {
		t.Fatalf("bot avatar fetched for wrong user: %+v", photos)
	}
	parts := tool.ToolResultParts(output)
	if len(parts) != 1 || !strings.HasPrefix(parts[0].ImageURL, "data:image/") || strings.Contains(parts[0].ImageURL, "test-token") {
		t.Fatalf("avatar attachment = %+v", parts)
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatal(err)
	}
	if _, err := tool.Run(ctx, map[string]any{"action": "send", "image_id": result["image_id"]}); err != nil {
		t.Fatal(err)
	}
	if len(api.callsOf("sendPhoto")) != 1 {
		t.Fatal("viewed avatar was not sent")
	}
}

func TestRemoteImageViewAvatarRejectsUnavailableSource(t *testing.T) {
	api := newFakeTelegramAPI(t, map[string]any{"getUserProfilePhotos": map[string]any{"photos": []any{}}})
	event := MessageEvent{Platform: PlatformTelegram, Kind: EventKindPrivate, UserID: "22222"}
	r := NewRuntime(BotConfig{Platform: PlatformTelegram, BotAccount: "12345"}, api.channel(), NewPluginManager(), nil, nil, nil, nil)
	tool := newDianaRemoteImageTool(r, event)
	for _, input := range []map[string]any{
		{"action": "view_avatar"},
		{"action": "view_avatar", "avatar_source": avatarSourceSender},
		{"action": "view_avatar", "avatar_source": "https://example.com/a.png"},
	} {
		output, err := tool.Run(context.Background(), input)
		if err == nil {
			t.Fatalf("input %v accepted: %s", input, output)
		}
		if len(tool.ToolResultParts(output)) != 0 {
			t.Fatalf("input %v attached an image", input)
		}
	}
}

// 回放里模型会把相册的 ID 列表填进 image_id，照样按相册发。
func TestRemoteImageSendAlbumAcceptsIDListInImageID(t *testing.T) {
	t.Setenv("DIANA_ALLOW_PRIVATE_HTTP_FETCHES", "true")
	var data bytes.Buffer
	if err := png.Encode(&data, image.NewRGBA(image.Rect(0, 0, 20, 20))); err != nil {
		t.Fatal(err)
	}
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(data.Bytes())
	}))
	defer source.Close()
	api := newFakeTelegramAPI(t, map[string]any{"sendMediaGroup": []any{map[string]any{"message_id": 7}, map[string]any{"message_id": 8}}})
	event := MessageEvent{Platform: PlatformTelegram, Kind: EventKindGroup, GroupID: "-100123", UserID: "22222"}
	r := NewRuntime(BotConfig{Platform: PlatformTelegram}, api.channel(), NewPluginManager(), nil, nil, nil, nil)
	tool := newDianaRemoteImageTool(r, event)
	ctx := context.Background()
	for range 2 {
		if _, err := tool.Run(ctx, map[string]any{"action": "view", "url": source.URL}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := tool.Run(ctx, map[string]any{"action": "send_album", "image_id": []any{"remote_image_1", "remote_image_2"}}); err != nil {
		t.Fatal(err)
	}
	if len(api.callsOf("sendMediaGroup")) != 1 {
		t.Fatal("album was not sent")
	}
}

// 群里两轮回复同时在跑，两个人前后脚要同一张图：第二轮不再发一遍，别的群照发。
func TestRemoteImageSendSkipsSameImageAlreadySentByAnotherTurn(t *testing.T) {
	t.Setenv("DIANA_ALLOW_PRIVATE_HTTP_FETCHES", "true")
	var data bytes.Buffer
	if err := png.Encode(&data, image.NewRGBA(image.Rect(0, 0, 20, 20))); err != nil {
		t.Fatal(err)
	}
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(data.Bytes())
	}))
	defer source.Close()
	api := newFakeTelegramAPI(t, map[string]any{"sendPhoto": map[string]any{"message_id": 7}})
	r := NewRuntime(BotConfig{Platform: PlatformTelegram}, api.channel(), NewPluginManager(), nil, nil, nil, nil)
	ctx := context.Background()
	send := func(event MessageEvent, path string) map[string]any {
		t.Helper()
		tool := newDianaRemoteImageTool(r, event)
		// 两轮用的直链不一样（头像页 .png 和 avatars 直链），内容是同一张。
		if _, err := tool.Run(ctx, map[string]any{"action": "view", "url": source.URL + path}); err != nil {
			t.Fatal(err)
		}
		output, err := tool.Run(ctx, map[string]any{"action": "send", "image_id": "remote_image_1"})
		if err != nil {
			t.Fatal(err)
		}
		var result map[string]any
		if err := json.Unmarshal([]byte(output), &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	group := MessageEvent{Platform: PlatformTelegram, Kind: EventKindGroup, GroupID: "-100123", UserID: "22222"}
	if result := send(group, "/u/1?v=4"); result["status"] != "sent" {
		t.Fatalf("first send = %v", result)
	}
	other := group
	other.UserID = "33333"
	result := send(other, "/user.png")
	if result["status"] != "already_sent" || !strings.Contains(result["message"].(string), "没有重发") {
		t.Fatalf("second send = %v", result)
	}
	if got := len(api.callsOf("sendPhoto")); got != 1 {
		t.Fatalf("sendPhoto calls = %d, want 1", got)
	}
	elsewhere := group
	elsewhere.GroupID = "-100456"
	if result := send(elsewhere, "/u/1?v=4"); result["status"] != "sent" {
		t.Fatalf("other group send = %v", result)
	}
	if got := len(api.callsOf("sendPhoto")); got != 2 {
		t.Fatalf("sendPhoto calls = %d, want 2", got)
	}
}

// 发送失败要撤掉占位，下一轮还能发。
func TestReserveImageSendReleasesOnFailure(t *testing.T) {
	r := NewRuntime(BotConfig{}, nil, NewPluginManager(), nil, nil, nil, nil)
	event := MessageEvent{Kind: EventKindGroup, GroupID: "10001"}
	images := []string{"data:image/png;base64,aGVsbG8="}
	finish, _, ok := r.reserveImageSend(event, images)
	if !ok {
		t.Fatal("first reserve refused")
	}
	if _, _, ok := r.reserveImageSend(event, images); ok {
		t.Fatal("in-flight image reserved twice")
	}
	finish(false)
	finish, _, ok = r.reserveImageSend(event, images)
	if !ok {
		t.Fatal("failed send still blocks")
	}
	finish(true)
	// 同一份字节换个 MIME 写法也算同一张。
	if _, _, ok := r.reserveImageSend(event, []string{"data:image/jpeg;base64,aGVsbG8="}); ok {
		t.Fatal("same bytes with another MIME sent again")
	}
}
