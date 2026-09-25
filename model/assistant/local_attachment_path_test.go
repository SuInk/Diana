package assistant

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLocalAttachmentAcceptsModelPathForms 模型常把路径写成 /workspace/x、带 workspace/
// 前缀，或者直接抄宿主机上的绝对路径。能落进工作目录的都照常发送，附件名取整理后的
// 文件名；落不进去的给中文报错，不碰平台接口。
func TestLocalAttachmentAcceptsModelPathForms(t *testing.T) {
	t.Setenv("APP_DB_PATH", filepath.Join(t.TempDir(), "diana.db"))
	root := AgentWorkspaceDir()
	if err := os.MkdirAll(filepath.Join(root, "outputs"), 0700); err != nil {
		t.Fatal(err)
	}
	content := []byte("hello report")
	if err := os.WriteFile(filepath.Join(root, "outputs", "report.txt"), content, 0600); err != nil {
		t.Fatal(err)
	}
	var received []byte
	var filename string
	calls := 0
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Error(err)
			http.Error(w, "invalid", 400)
			return
		}
		defer r.MultipartForm.RemoveAll()
		file, header, err := r.FormFile("document")
		if err != nil {
			t.Error(err)
			http.Error(w, "no document", 400)
			return
		}
		defer file.Close()
		received, _ = io.ReadAll(file)
		filename = header.Filename
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":99}}`))
	}))
	defer api.Close()
	r := NewRuntime(BotConfig{Platform: PlatformTelegram}, NewTelegramChannel(TelegramConfig{BotToken: "test", APIBaseURL: api.URL}), NewPluginManager(), nil, nil, nil, nil)
	tool := &dianaLocalAttachmentTool{runtime: r, event: MessageEvent{Platform: PlatformTelegram, Kind: EventKindGroup, GroupID: "-100123"}}
	ctx := context.Background()

	for _, path := range []string{
		"outputs/report.txt",
		" ./outputs/report.txt ",
		"/workspace/outputs/report.txt",
		"workspace/outputs/report.txt",
		`outputs\report.txt`,
		filepath.Join(root, "outputs", "report.txt"),
	} {
		t.Run(path, func(t *testing.T) {
			received, filename = nil, ""
			if _, err := tool.Run(ctx, map[string]any{"path": path, "mode": "file"}); err != nil {
				t.Fatalf("send_attachment(%q): %v", path, err)
			}
			if !bytes.Equal(received, content) || filename != "report.txt" {
				t.Fatalf("send_attachment(%q) uploaded %q as %q", path, received, filename)
			}
		})
	}

	calls = 0
	rejects := []struct {
		path, want string
	}{
		{"/etc/hosts.txt", "相对路径"},
		{"../diana.txt", "跑出了工作目录"},
		{"outputs/missing.txt", "find_files"},
	}
	for _, tc := range rejects {
		t.Run("拒绝/"+tc.path, func(t *testing.T) {
			_, err := tool.Run(ctx, map[string]any{"path": tc.path, "mode": "file"})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("send_attachment(%q) error = %v, want %q", tc.path, err, tc.want)
			}
		})
	}
	if calls != 0 {
		t.Fatalf("rejected paths reached the platform API %d times", calls)
	}

	var img bytes.Buffer
	if err := png.Encode(&img, image.NewRGBA(image.Rect(0, 0, 8, 8))); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "outputs", "x.png"), img.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	view := &dianaLocalAttachmentTool{view: true}
	if _, err := view.Run(ctx, map[string]any{"path": "/workspace/outputs/x.png"}); err != nil || len(view.ToolResultParts("")) != 1 {
		t.Fatalf("view_image with /workspace prefix: %v", err)
	}
}
