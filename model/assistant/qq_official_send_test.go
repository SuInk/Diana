package assistant

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type qqFakeAPI struct {
	mu       sync.Mutex
	messages []map[string]any
	paths    []string
	put      []byte
	server   *httptest.Server
}

func newQQFakeAPI(t *testing.T) *qqFakeAPI {
	t.Helper()
	api := &qqFakeAPI{}
	mux := http.NewServeMux()
	mux.HandleFunc("/put/1", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		api.mu.Lock()
		api.put = body
		api.paths = append(api.paths, "PUT "+r.URL.Path)
		api.mu.Unlock()
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		api.mu.Lock()
		api.paths = append(api.paths, r.Method+" "+r.URL.Path)
		api.mu.Unlock()
		switch {
		case strings.HasSuffix(r.URL.Path, "/upload_prepare"):
			size := body["file_size"].(string)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"upload_id": "up1", "block_size": size,
				"parts": []map[string]any{{"index": 1, "presigned_url": api.server.URL + "/put/1"}},
			})
		case strings.HasSuffix(r.URL.Path, "/upload_part_finish"):
			_, _ = w.Write([]byte(`{}`))
		case strings.HasSuffix(r.URL.Path, "/files"):
			_ = json.NewEncoder(w).Encode(map[string]any{"file_info": "FILEINFO", "ttl": 0})
		case strings.HasSuffix(r.URL.Path, "/messages"):
			api.mu.Lock()
			api.messages = append(api.messages, body)
			n := len(api.messages)
			api.mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "m" + string(rune('0'+n))})
		default:
			http.NotFound(w, r)
		}
	})
	api.server = httptest.NewServer(mux)
	t.Cleanup(api.server.Close)
	return api
}

func (a *qqFakeAPI) channel() *QQOfficialChannel {
	channel := NewQQOfficialChannel(QQOfficialConfig{AppID: "app", AppSecret: "secret"})
	channel.apiBaseOverride = a.server.URL
	channel.tokens = &platformTokenCache{fetch: func(context.Context) (string, time.Duration, error) {
		return "token", time.Hour, nil
	}}
	return channel
}

func TestQQOfficialSendsTextThenImageWithConsecutiveSeq(t *testing.T) {
	api := newQQFakeAPI(t)
	channel := api.channel()
	png := "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg=="
	result, err := channel.SendWithResult(context.Background(), OutgoingMessage{
		GroupID: "G", Text: "看图", ImageURLs: []string{png}, ReplyMessageID: "in1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result["message_id"] != "m1" {
		t.Fatalf("result = %#v", result)
	}
	if len(api.messages) != 2 {
		t.Fatalf("messages = %#v", api.messages)
	}
	text, image := api.messages[0], api.messages[1]
	if text["content"] != "看图" || text["msg_seq"].(float64) != 1 {
		t.Fatalf("text message = %#v", text)
	}
	media, _ := image["media"].(map[string]any)
	if image["msg_type"].(float64) != 7 || media["file_info"] != "FILEINFO" || image["msg_seq"].(float64) != 2 || image["msg_id"] != "in1" {
		t.Fatalf("image message = %#v", image)
	}
	if len(api.put) == 0 {
		t.Fatal("image bytes were never uploaded to the presigned URL")
	}
	want := []string{"POST /v2/groups/G/messages", "POST /v2/groups/G/upload_prepare", "PUT /put/1", "POST /v2/groups/G/upload_part_finish", "POST /v2/groups/G/files", "POST /v2/groups/G/messages"}
	if strings.Join(api.paths, "|") != strings.Join(want, "|") {
		t.Fatalf("request order = %v", api.paths)
	}
}

func TestQQOfficialProactiveMessageHasNoMsgSeq(t *testing.T) {
	api := newQQFakeAPI(t)
	if _, err := api.channel().SendWithResult(context.Background(), OutgoingMessage{GroupID: "G", Text: "hi"}); err != nil {
		t.Fatal(err)
	}
	if _, has := api.messages[0]["msg_seq"]; has {
		t.Fatalf("proactive message carried msg_seq: %#v", api.messages[0])
	}
}
