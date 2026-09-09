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
