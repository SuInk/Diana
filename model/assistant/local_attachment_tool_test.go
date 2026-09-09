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

func TestLocalAttachmentToolsRemainOwnerOnly(t *testing.T) {
	allowed := (RelationshipPolicy{}).allowedAgentToolNames()
	if allowed["diana.view_image"] || allowed["diana.send_attachment"] {
		t.Fatal("local tools exposed to group members")
	}
	if !allowed[dianaRemoteImageToolName] {
		t.Fatal("remote image tool missing")
	}
}
