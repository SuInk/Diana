package assistant

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/SuInk/diana/model/agent"
)

func TestMCPMediaToolSendsImageAndFile(t *testing.T) {
	t.Setenv("APP_DB_PATH", filepath.Join(t.TempDir(), "diana.db"))
	var img bytes.Buffer
	if err := png.Encode(&img, image.NewRGBA(image.Rect(0, 0, 8, 8))); err != nil {
		t.Fatal(err)
	}
	type upload struct {
		method, field, name string
		data                []byte
	}
	var mu sync.Mutex
	var uploads []upload
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method := filepath.Base(r.URL.Path)
		if err := r.ParseMultipartForm(8 << 20); err == nil {
			for field, headers := range r.MultipartForm.File {
				file, _ := headers[0].Open()
				data, _ := io.ReadAll(file)
				_ = file.Close()
				mu.Lock()
				uploads = append(uploads, upload{method: method, field: field, name: headers[0].Filename, data: data})
				mu.Unlock()
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":99}}`))
	}))
	defer api.Close()
	r := NewRuntime(BotConfig{Platform: PlatformTelegram}, NewTelegramChannel(TelegramConfig{BotToken: "test", APIBaseURL: api.URL}), NewPluginManager(), nil, nil, nil, nil)
	tool := newDianaMCPMediaTool(r, MessageEvent{Platform: PlatformTelegram, Kind: EventKindGroup, GroupID: "-100123"})

	imageID, _ := agent.StoreMCPMedia(agent.MCPMedia{Kind: agent.MCPMediaImage, MIMEType: "image/png", Data: img.Bytes()})
	audioID, _ := agent.StoreMCPMedia(agent.MCPMedia{Kind: agent.MCPMediaAudio, MIMEType: "audio/wav", Data: []byte("RIFFfake")})
	ctx := context.Background()
	if _, err := tool.Run(ctx, map[string]any{"media_id": imageID}); err != nil {
		t.Fatal(err)
	}
	if _, err := tool.Run(ctx, map[string]any{"media_id": audioID}); err != nil {
		t.Fatal(err)
	}
	// 不是图片的不能硬按图片发。
	if _, err := tool.Run(ctx, map[string]any{"media_id": audioID, "as": "image"}); err == nil {
		t.Fatal("音频不该能按图片发")
	}
	if _, err := tool.Run(ctx, map[string]any{"media_id": "mcpm_000000000000000000000000"}); err == nil || !strings.Contains(err.Error(), "过期") {
		t.Fatalf("无效 id 应报错：%v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(uploads) != 2 {
		t.Fatalf("应上传两次：%+v", uploads)
	}
	if uploads[0].method != "sendPhoto" || !bytes.Equal(uploads[0].data, img.Bytes()) {
		t.Fatalf("图片没按原图发：%s %d bytes", uploads[0].method, len(uploads[0].data))
	}
	if uploads[1].method != "sendDocument" || !strings.HasPrefix(uploads[1].name, "mcp-audio") || string(uploads[1].data) != "RIFFfake" {
		t.Fatalf("音频没按文件发：%s %s", uploads[1].method, uploads[1].name)
	}
}

func TestMCPMediaToolAvailableToGroupMembers(t *testing.T) {
	if !(RelationshipPolicy{}).allowedAgentToolNames()[dianaMCPMediaToolName] {
		t.Fatal("群成员用不了 mcp_media，他们能调的 MCP 出的图就发不出去")
	}
}
