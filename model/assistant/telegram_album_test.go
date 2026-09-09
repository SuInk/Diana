package assistant

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestTelegramAlbumMixedSources(t *testing.T) {
	path := filepath.Join(t.TempDir(), "photo.jpg")
	if err := os.WriteFile(path, []byte("test-image-bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	var media []map[string]string
	var upload, chat, topic, method string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Error(err)
			return
		}
		defer r.MultipartForm.RemoveAll()
		if err := json.Unmarshal([]byte(r.FormValue("media")), &media); err != nil {
			t.Error(err)
		}
		file, _, err := r.FormFile("photo0")
		if err != nil {
			t.Error(err)
			return
		}
		defer file.Close()
		body, _ := io.ReadAll(file)
		upload = string(body)
		chat, topic, method = r.FormValue("chat_id"), r.FormValue("message_thread_id"), filepath.Base(r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":[{"message_id":11},{"message_id":12},{"message_id":13}]}`))
	}))
	defer api.Close()
	channel := NewTelegramChannel(TelegramConfig{BotToken: "test", APIBaseURL: api.URL})
	msg := OutgoingMessage{Platform: PlatformTelegram, GroupID: "-100123", MessageThreadID: "77", ImageAlbum: true, ImageURLs: []string{path, "https://example.com/photo.jpg", "telegram-file-id"}}
	if telegramMessageNeedsSteps(msg) {
		t.Fatal("album would be split into single images")
	}
	result, err := channel.SendWithResult(context.Background(), msg)
	if err != nil {
		t.Fatal(err)
	}
	if method != "sendMediaGroup" || chat != "-100123" || topic != "77" || upload != "test-image-bytes" {
		t.Fatalf("wrong upload %s %s %s %s", method, chat, topic, upload)
	}
	if len(media) != 3 || media[0]["media"] != "attach://photo0" || media[1]["media"] != msg.ImageURLs[1] || media[2]["media"] != "telegram-file-id" {
		t.Fatalf("media=%+v", media)
	}
	if apiMessageID(result) != "11" {
		t.Fatalf("result=%+v", result)
	}
	if _, err := json.Marshal(result); err != nil {
		t.Fatal(err)
	}
}

func TestTelegramAlbumRejectsInvalidSize(t *testing.T) {
	channel := NewTelegramChannel(TelegramConfig{})
	for _, n := range []int{0, 1, 11} {
		if _, err := channel.SendWithResult(context.Background(), OutgoingMessage{GroupID: "1", ImageAlbum: true, ImageURLs: make([]string, n)}); err == nil {
			t.Fatalf("accepted %d images", n)
		}
	}
}

func TestTelegramImagesReusesKnownFileID(t *testing.T) {
	api := newFakeTelegramAPI(t, nil)
	r := NewRuntime(BotConfig{Platform: PlatformTelegram}, api.channel(), NewPluginManager(), nil, nil, nil, nil)
	event := MessageEvent{Platform: PlatformTelegram, Kind: EventKindGroup, GroupID: "-100123", UserID: "5", MessageID: "request", Quoted: &QuotedMessage{MessageID: "photo", GroupID: "-100123", Segments: []MessageSegment{{Type: "image", Data: map[string]string{"file_id": "known-file-id"}}}}}
	tool := &dianaTelegramImagesTool{runtime: r, event: event}
	if _, err := tool.Run(context.Background(), map[string]any{"message_ids": []any{"unknown"}}); err == nil {
		t.Fatal("unknown message accepted")
	}
	if _, err := tool.Run(context.Background(), map[string]any{"message_ids": []any{"photo"}}); err != nil {
		t.Fatal(err)
	}
	calls := api.callsOf("sendPhoto")
	if len(calls) != 1 || calls[0].Params["photo"] != "known-file-id" {
		t.Fatalf("calls=%+v", calls)
	}
	if len(api.callsOf("getFile")) != 0 {
		t.Fatal("reuse downloaded the image")
	}
}

func TestTelegramAlbumRuntimePreservesIndividualFileIDs(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if filepath.Base(r.URL.Path) != "sendMediaGroup" {
			t.Errorf("unexpected request: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":[{"message_id":21,"photo":[{"file_id":"saved-one"}]},{"message_id":22,"photo":[{"file_id":"saved-two"}]}]}`))
	}))
	defer api.Close()
	r := NewRuntime(BotConfig{Platform: PlatformTelegram, BotAccount: "bot"}, NewTelegramChannel(TelegramConfig{BotToken: "test", APIBaseURL: api.URL}), NewPluginManager(), nil, nil, nil, nil)
	event := MessageEvent{Platform: PlatformTelegram, Kind: EventKindGroup, GroupID: "-123", UserID: "1", SelfID: "bot", MessageID: "request"}
	if err := r.sendOutgoing(context.Background(), event, OutgoingMessage{ImageAlbum: true, ImageURLs: []string{"existing-one", "existing-two"}}); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]string{"21": "saved-one", "22": "saved-two"} {
		source, found := r.findSemanticReferenceEvent(context.Background(), event, id)
		if !found || len(source.Segments) != 1 || source.Segments[0].Data["file_id"] != want {
			t.Fatalf("missing per-photo history: %s %+v", id, source)
		}
	}
}

func TestTelegramAlbumAPIErrorIsNotSuccess(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":false,"error_code":400,"description":"invalid media"}`))
	}))
	defer api.Close()
	channel := NewTelegramChannel(TelegramConfig{BotToken: "test", APIBaseURL: api.URL})
	if _, err := channel.SendWithResult(context.Background(), OutgoingMessage{GroupID: "1", ImageAlbum: true, ImageURLs: []string{"one", "two"}}); err == nil {
		t.Fatal("send failure was reported as success")
	}
}
