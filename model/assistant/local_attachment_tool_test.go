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

func TestLocalAttachmentViewAndDocumentUpload(t *testing.T) {
	t.Setenv("APP_DB_PATH", filepath.Join(t.TempDir(), "diana.db"))
	root := AgentWorkspaceDir()
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	var img bytes.Buffer
	if err := png.Encode(&img, image.NewRGBA(image.Rect(0, 0, 16, 16))); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "photo.png"), img.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	view := &dianaLocalAttachmentTool{view: true}
	ctx := context.Background()
	if _, err := view.Run(ctx, map[string]any{"path": "photo.png"}); err != nil {
		t.Fatal(err)
	}
	if len(view.ToolResultParts("")) != 1 {
		t.Fatal("missing visual attachment")
	}
	if _, err := view.Run(ctx, map[string]any{"path": "missing.png"}); err == nil {
		t.Fatal("missing file accepted")
	}
	if len(view.ToolResultParts("")) != 0 {
		t.Fatal("stale image after failure")
	}
	var received []byte
	var filename, chat, topic, method string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
		chat, topic, method = r.FormValue("chat_id"), r.FormValue("message_thread_id"), filepath.Base(r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":99}}`))
	}))
	defer api.Close()
	r := NewRuntime(BotConfig{Platform: PlatformTelegram}, NewTelegramChannel(TelegramConfig{BotToken: "test", APIBaseURL: api.URL}), NewPluginManager(), nil, nil, nil, nil)
	tool := &dianaLocalAttachmentTool{runtime: r, event: MessageEvent{Platform: PlatformTelegram, Kind: EventKindGroup, GroupID: "-100123", MessageThreadID: "77"}}
	if _, err := tool.Run(ctx, map[string]any{"path": "photo.png", "mode": "file"}); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(received, img.Bytes()) || filename != "photo.png" || chat != "-100123" || topic != "77" || method != "sendDocument" {
		t.Fatalf("unexpected upload: %s %s %s %s bytes=%d", method, filename, chat, topic, len(received))
	}
}

func TestLocalAttachmentRejectsUnsupportedTypes(t *testing.T) {
	t.Setenv("APP_DB_PATH", filepath.Join(t.TempDir(), "diana.db"))
	root := AgentWorkspaceDir()
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	write := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("evil.exe", "MZ")
	write("notes.txt", "hello")
	write("chart.svg", `<svg xmlns="http://www.w3.org/2000/svg" width="10" height="10"></svg>`)

	tool := &dianaLocalAttachmentTool{}
	ctx := context.Background()
	if _, err := tool.Run(ctx, map[string]any{"path": "evil.exe", "mode": "file"}); err == nil {
		t.Fatal("executable accepted as file attachment")
	}
	if _, err := tool.Run(ctx, map[string]any{"path": "missing.exe", "mode": "file"}); err == nil {
		t.Fatal("missing executable accepted")
	}
	if _, err := tool.Run(ctx, map[string]any{"path": "notes.txt", "mode": "image"}); err == nil {
		t.Fatal("text file accepted as image")
	}
	// 无 runtime 的 image 分支在读完位图后才用到 runtime；svg 在发送前就需要 runtime，
	// 走到这里说明 svg 通过了类型白名单。
	if _, err := tool.Run(ctx, map[string]any{"path": "chart.svg", "mode": "image"}); err == nil {
		t.Fatal("svg without runtime accepted")
	}
}

func TestLocalAttachmentSVGImageModeNeedsBrowserPlugin(t *testing.T) {
	t.Setenv("APP_DB_PATH", filepath.Join(t.TempDir(), "diana.db"))
	root := AgentWorkspaceDir()
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "chart.svg"), []byte(`<svg xmlns="http://www.w3.org/2000/svg" width="10" height="10"></svg>`), 0600); err != nil {
		t.Fatal(err)
	}
	apiCalls := 0
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		apiCalls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":99}}`))
	}))
	defer api.Close()
	r := NewRuntime(BotConfig{Platform: PlatformTelegram}, NewTelegramChannel(TelegramConfig{BotToken: "test", APIBaseURL: api.URL}), NewPluginManager(), nil, nil, nil, nil)
	tool := &dianaLocalAttachmentTool{runtime: r, event: MessageEvent{Platform: PlatformTelegram, Kind: EventKindGroup, GroupID: "-100123"}}
	_, err := tool.Run(context.Background(), map[string]any{"path": "chart.svg", "mode": "image"})
	if err == nil {
		t.Fatal("svg image send without browser plugin accepted")
	}
	if !strings.Contains(err.Error(), "mode=file") {
		t.Fatalf("error should point to mode=file, got: %v", err)
	}
	if apiCalls != 0 {
		t.Fatalf("unexpected api calls: %d", apiCalls)
	}
}

func TestLocalAttachmentTextFileUploadPassesWhitelist(t *testing.T) {
	t.Setenv("APP_DB_PATH", filepath.Join(t.TempDir(), "diana.db"))
	root := AgentWorkspaceDir()
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	content := []byte("hello diana")
	if err := os.WriteFile(filepath.Join(root, "notes.txt"), content, 0600); err != nil {
		t.Fatal(err)
	}
	var received []byte
	var filename, method string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
		method = filepath.Base(r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":99}}`))
	}))
	defer api.Close()
	r := NewRuntime(BotConfig{Platform: PlatformTelegram}, NewTelegramChannel(TelegramConfig{BotToken: "test", APIBaseURL: api.URL}), NewPluginManager(), nil, nil, nil, nil)
	tool := &dianaLocalAttachmentTool{runtime: r, event: MessageEvent{Platform: PlatformTelegram, Kind: EventKindGroup, GroupID: "-100123"}}
	if _, err := tool.Run(context.Background(), map[string]any{"path": "notes.txt", "mode": "file"}); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(received, content) || filename != "notes.txt" || method != "sendDocument" {
		t.Fatalf("unexpected upload: %s %s bytes=%d", method, filename, len(received))
	}
}

func TestLocalAttachmentToolsRemainOwnerOnly(t *testing.T) {
	allowed := (RelationshipPolicy{}).allowedAgentToolNames()
	if allowed["diana.view_image"] || allowed["diana.send_attachment"] {
		t.Fatal("local tools exposed to group members")
	}
	if !allowed[dianaRemoteImageToolName] {
		t.Fatal("remote image tool missing")
	}
}
