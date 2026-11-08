package assistant

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// 表情包库里动态/视频贴纸只存了静态缩略图，尺寸不够 sendSticker，以前会被当文件发出去。
// 知道原贴纸的 file_id 时按原贴纸转发；ID 被拒（400）才退回发文件。
func TestTelegramSendsLibraryStickerByOriginalFileID(t *testing.T) {
	webp, err := base64.StdEncoding.DecodeString("UklGRiIAAABXRUJQVlA4IBYAAAAwAQCdASoBAAEADsD+JaQAA3AAAAAA")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "media.webp")
	if err := os.WriteFile(path, webp, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name       string
		rejectID   bool
		wantMethod []string
	}{
		{"forward by file id", false, []string{"sendSticker"}},
		{"rejected id falls back to upload", true, []string{"sendSticker", "sendDocument"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			var methods []string
			var stickerParam any
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
				method := parts[len(parts)-1]
				mu.Lock()
				methods = append(methods, method)
				mu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				if method == "sendSticker" {
					params := map[string]any{}
					_ = json.NewDecoder(r.Body).Decode(&params)
					stickerParam = params["sticker"]
					if tc.rejectID {
						_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error_code": 400, "description": "Bad Request: wrong file identifier/HTTP URL specified"})
						return
					}
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": map[string]any{"message_id": 7}})
			}))
			defer server.Close()
			channel := NewTelegramChannel(TelegramConfig{BotToken: "test-token", APIBaseURL: server.URL})
			msg := stickerOutgoingMessage(PlatformTelegram, "bot-a", stickerCandidate{Path: path, StickerFileID: "CAACAgUAAx0", StickerProfileID: "bot-a"})
			msg.GroupID = "-10012345"
			if _, err := channel.SendWithResult(context.Background(), msg); err != nil {
				t.Fatal(err)
			}
			if strings.Join(methods, ",") != strings.Join(tc.wantMethod, ",") {
				t.Fatalf("methods = %v, want %v", methods, tc.wantMethod)
			}
			if stickerParam != "CAACAgUAAx0" {
				t.Fatalf("sticker param = %#v", stickerParam)
			}
		})
	}
}

// file_id 只对收到它的那台机器人有效：别的机器人库里的贴纸照旧发文件，其他平台不受影响。
func TestStickerOutgoingMessageFileIDNeedsSameTelegramBot(t *testing.T) {
	candidate := stickerCandidate{Path: "/cache/a.webp", StickerFileID: "CAACAgUAAx0", StickerProfileID: "bot-a"}
	if msg := stickerOutgoingMessage(PlatformTelegram, "bot-a", candidate); msg.StickerFileIDs["/cache/a.webp"] != "CAACAgUAAx0" {
		t.Fatalf("same bot = %#v", msg)
	}
	if msg := stickerOutgoingMessage(PlatformTelegram, "bot-b", candidate); len(msg.StickerFileIDs) != 0 {
		t.Fatalf("other bot = %#v", msg.StickerFileIDs)
	}
	if msg := stickerOutgoingMessage(PlatformOneBotV11, "bot-a", candidate); len(msg.StickerFileIDs) != 0 {
		t.Fatalf("onebot = %#v", msg.StickerFileIDs)
	}
}
