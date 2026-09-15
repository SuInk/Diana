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

func TestAgentToolBudgetDefaultsToTwelve(t *testing.T) {
	if DefaultBotConfig().AgentMaxSteps != 12 || (agent.Config{}).WithDefaults().MaxSteps != 12 {
		t.Fatal("inconsistent default budget")
	}
	if (BotConfig{AgentMaxSteps: 8}).WithDefaults().AgentMaxSteps != 8 {
		t.Fatal("explicit budget overwritten")
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
