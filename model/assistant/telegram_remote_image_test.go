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
	"sync"
	"testing"
)

// Telegram 拉不到图片链接（推特图床挡了它的抓取）时，由本机下载后按文件上传，
// 文字和图片各只发一次。
func TestTelegramUploadsImageWhenURLFetchRejected(t *testing.T) {
	t.Setenv("DIANA_HISTORY_MEDIA_DIR", t.TempDir())
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 4, 4))); err != nil {
		t.Fatal(err)
	}
	const imageURL = "https://pbs.twimg.com/media/A.jpg?name=orig"
	previous := telegramRemoteImageClient
	telegramRemoteImageClient = func() *http.Client {
		return &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if r.URL.String() != imageURL {
				t.Errorf("downloaded %s", r.URL)
			}
			return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"image/png"}}, Body: io.NopCloser(bytes.NewReader(buf.Bytes())), Request: r}, nil
		})}
	}
	t.Cleanup(func() { telegramRemoteImageClient = previous })

	var mu sync.Mutex
	var calls []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		kind := "json"
		if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
			kind = "upload"
		}
		mu.Lock()
		calls = append(calls, method+":"+kind)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if method == "sendPhoto" && kind == "json" {
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error_code": 400, "description": "Bad Request: failed to get HTTP URL content"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": map[string]any{"message_id": len(calls)}})
	}))
	t.Cleanup(server.Close)
	channel := NewTelegramChannel(TelegramConfig{BotToken: "test-token", APIBaseURL: server.URL})

	if _, err := channel.SendWithResult(context.Background(), OutgoingMessage{GroupID: "-10012345", Text: "New visual", ImageURLs: []string{imageURL}}); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if got := strings.Join(calls, ","); got != "sendMessage:json,sendPhoto:json,sendPhoto:upload" {
		t.Fatalf("calls = %s", got)
	}
}

// 别的 400（不是拉不到链接）换成上传也一样会被拒，照原样报错，不去下载。
func TestTelegramURLFetchRejectedOnlyMatchesFetchFailures(t *testing.T) {
	for description, want := range map[string]bool{
		"Bad Request: failed to get HTTP URL content":           true,
		"Bad Request: wrong type of the web page content":       true,
		"Bad Request: wrong file identifier/HTTP URL specified": true,
		"Bad Request: chat not found":                           false,
		"Bad Request: IMAGE_PROCESS_FAILED":                     false,
	} {
		if got := telegramURLFetchRejected(&telegramAPIError{Code: 400, Description: description}); got != want {
			t.Errorf("%q = %v, want %v", description, got, want)
		}
	}
	if telegramURLFetchRejected(&telegramAPIError{Code: 429, Description: "failed to get HTTP URL content"}) {
		t.Error("non-400 matched")
	}
}
