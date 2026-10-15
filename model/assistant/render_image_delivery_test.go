package assistant

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRenderedPNGUploadsToTelegram(t *testing.T) {
	var body bytes.Buffer
	if err := png.Encode(&body, image.NewRGBA(image.Rect(0, 0, 16, 16))); err != nil {
		t.Fatal(err)
	}
	calls := 0
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		calls++
		if req.URL.Path != "/bottest/sendPhoto" {
			t.Errorf("unexpected method: %s", req.URL.Path)
		}
		if err := req.ParseMultipartForm(1 << 20); err != nil {
			t.Errorf("expected multipart, not a remote file ID: %v", err)
			http.Error(w, "wrong remote file identifier", 400)
			return
		}
		defer req.MultipartForm.RemoveAll()
		file, header, err := req.FormFile("photo")
		if err != nil {
			t.Error(err)
			return
		}
		defer file.Close()
		got, err := io.ReadAll(file)
		if err != nil || !bytes.Equal(got, body.Bytes()) || header.Filename != "image.png" {
			t.Errorf("wrong PNG upload: filename=%q err=%v", header.Filename, err)
		}
		if req.FormValue("chat_id") != "-100123" || req.FormValue("message_thread_id") != "77" {
			t.Errorf("wrong destination: %v", req.Form)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":99}}`))
	}))
	defer api.Close()
	r := NewRuntime(BotConfig{Platform: PlatformTelegram}, NewTelegramChannel(TelegramConfig{BotToken: "test", APIBaseURL: api.URL}), NewPluginManager(), nil, nil, nil, nil)
	if err := r.sendPNGImage(context.Background(), MessageEvent{Kind: EventKindGroup, GroupID: "-100123", MessageThreadID: "77"}, body.Bytes()); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("expected one upload, got %d", calls)
	}
}
