package assistant

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/gif"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTelegramImageRouting(t *testing.T) {
	var animated bytes.Buffer
	frame := image.NewPaletted(image.Rect(0, 0, 2, 2), color.Palette{color.Black, color.White})
	if err := gif.EncodeAll(&animated, &gif.GIF{Image: []*image.Paletted{frame, frame}, Delay: []int{10, 10}}); err != nil {
		t.Fatal(err)
	}
	var tall bytes.Buffer
	if err := png.Encode(&tall, image.NewGray(image.Rect(0, 0, 1, 30))); err != nil {
		t.Fatal(err)
	}
	webp, err := base64.StdEncoding.DecodeString("UklGRiIAAABXRUJQVlA4IBYAAAAwAQCdASoBAAEADsD+JaQAA3AAAAAA")
	if err != nil {
		t.Fatal(err)
	}
	// A 512x2 static WebP, encoded once for this fixture; no external encoder is
	// needed to run the test.
	sticker, err := base64.StdEncoding.DecodeString("UklGRkAAAABXRUJQVlA4IDQAAABwBACdASoAAgIAPpFIpE0lpCOiIQgAsBIJaQAACrHTp06dOnTp06dOnTpvgADkQAAAAAAA")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, method, field string
		body                []byte
	}{
		{"cached-hash", "sendAnimation", "animation", animated.Bytes()},
		{"mislabelled.jpg", "sendAnimation", "animation", animated.Bytes()},
		{"photo", "sendPhoto", "photo", tinyJPEGBytes(t)},
		{"tall.png", "sendDocument", "document", tall.Bytes()},
		{"general.webp", "sendDocument", "document", webp},
		{"sticker-hash", "sendSticker", "sticker", sticker},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), tc.name)
			if err := os.WriteFile(path, tc.body, 0600); err != nil {
				t.Fatal(err)
			}
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if !strings.HasSuffix(r.URL.Path, "/"+tc.method) {
					t.Errorf("method = %s", r.URL.Path)
				}
				if err := r.ParseMultipartForm(1 << 20); err != nil {
					t.Error(err)
					return
				}
				defer r.MultipartForm.RemoveAll()
				for key, want := range map[string]string{"chat_id": "-100", "message_thread_id": "7", "reply_to_message_id": "9", "allow_sending_without_reply": "true"} {
					if got := r.FormValue(key); got != want {
						t.Errorf("%s = %q, want %q", key, got, want)
					}
				}
				file, _, err := r.FormFile(tc.field)
				if err != nil {
					t.Error(err)
					return
				}
				defer file.Close()
				var got bytes.Buffer
				if _, err := got.ReadFrom(file); err != nil {
					t.Error(err)
				}
				if !bytes.Equal(got.Bytes(), tc.body) {
					t.Error("media content changed")
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": map[string]any{"message_id": 12}})
			}))
			defer server.Close()
			ch := NewTelegramChannel(TelegramConfig{BotToken: "test", APIBaseURL: server.URL})
			result, err := ch.SendWithResult(context.Background(), OutgoingMessage{GroupID: "-100", MessageThreadID: "7", ReplyMessageID: "9", ImageURLs: []string{path}})
			if err != nil {
				t.Fatal(err)
			}
			if calls != 1 || result["message_id"] != float64(12) {
				t.Fatalf("calls=%d result=%v", calls, result)
			}
		})
	}
}

func TestTelegramRemoteImageRouting(t *testing.T) {
	for _, tc := range []struct{ source, method, field string }{
		{"https://example.com/a.GIF?token=1", "sendAnimation", "animation"},
		{"https://example.com/a.webp?token=1", "sendSticker", "sticker"},
		{"https://example.com/a.jpg", "sendPhoto", "photo"},
		{"opaque-photo-file-id", "sendPhoto", "photo"},
	} {
		t.Run(tc.method+tc.source, func(t *testing.T) {
			api := newFakeTelegramAPI(t, nil)
			err := api.channel().Send(context.Background(), OutgoingMessage{UserID: "1", ReplyMessageID: "2", ImageURLs: []string{tc.source}})
			if err != nil {
				t.Fatal(err)
			}
			calls := api.callsOf(tc.method)
			if len(calls) != 1 || calls[0].Params[tc.field] != tc.source || calls[0].Params["reply_to_message_id"] != "2" {
				t.Fatalf("calls = %#v", calls)
			}
		})
	}
}

func TestTelegramImageRejectsNonFiles(t *testing.T) {
	for _, path := range []string{t.TempDir(), filepath.Join(t.TempDir(), "missing")} {
		api := newFakeTelegramAPI(t, nil)
		if err := api.channel().Send(context.Background(), OutgoingMessage{UserID: "1", ImageURLs: []string{path}}); err == nil {
			t.Fatal("expected local file error")
		}
		if len(api.calls) != 0 {
			t.Fatalf("unexpected calls: %v", api.calls)
		}
	}
}
