package assistant

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/agent"
)

// 三个文件入口都经过真实 Runtime 和 QQ 适配器，仅开放平台请求由测试服务接收。
func TestQQOfficialFileDeliveryTools(t *testing.T) {
	for _, group := range []bool{true, false} {
		for _, entry := range []string{"send_attachment", "send_file", "mcp_media", "near_limit"} {
			t.Run(fmt.Sprintf("group=%t/%s", group, entry), func(t *testing.T) {
				t.Setenv("APP_DB_PATH", filepath.Join(t.TempDir(), "diana.db"))
				api := newQQFakeAPI(t)
				r := NewRuntime(BotConfig{Platform: PlatformQQOfficial}, api.channel(), NewPluginManager(), nil, nil, nil, nil)
				// 不填 event.Platform 也要能按实际通道能力发送。
				event := MessageEvent{Kind: EventKindPrivate, UserID: "U", MessageID: "trigger"}
				prefix := "/v2/users/U"
				if group {
					event.Kind, event.GroupID = EventKindGroup, "G"
					prefix = "/v2/groups/G"
				}
				name, data := "notes.md", []byte("# 文件\n原始内容")
				var result string
				var err error
				switch entry {
				case "send_attachment", "near_limit":
					if entry == "near_limit" {
						data = bytes.Repeat([]byte("x"), localAttachmentMaxBytes-1)
					}
					root := AgentWorkspaceDir()
					if err := os.MkdirAll(root, 0700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(root, name), data, 0600); err != nil {
						t.Fatal(err)
					}
					tool := &dianaLocalAttachmentTool{runtime: r, event: event}
					result, err = tool.Run(context.Background(), map[string]any{"path": name, "mode": "file"})
				case "send_file":
					name = "main.go"
					tool := newDianaFileDeliveryTool(r, event, SettingValues{}, RelationshipPolicy{Owner: true})
					result, err = tool.Run(context.Background(), map[string]any{"filename": name, "content": string(data), "preview": false})
				case "mcp_media":
					media := agent.MCPMedia{Kind: agent.MCPMediaAudio, MIMEType: "audio/wav", Data: data}
					id, storeErr := agent.StoreMCPMedia(media)
					if storeErr != nil {
						t.Fatal(storeErr)
					}
					media, _ = agent.LookupMCPMedia(id)
					name = mcpMediaFileName(media)
					result, err = newDianaMCPMediaTool(r, event).Run(context.Background(), map[string]any{"media_id": id})
				}
				if err != nil {
					t.Fatal(err)
				}
				if entry == "send_file" && !decodeFileDeliveryResult(t, result).OK {
					t.Fatalf("send_file failed: %s", result)
				}
				if !bytes.Equal(api.put, data) || len(api.prepares) != 1 || len(api.messages) != 1 {
					t.Fatalf("uploaded %d/%d bytes, prepares=%d, messages=%d", len(api.put), len(data), len(api.prepares), len(api.messages))
				}
				if prepare := api.prepares[0]; prepare["file_type"] != float64(4) || prepare["file_name"] != name {
					t.Fatalf("prepare = %#v", prepare)
				}
				for _, path := range api.paths {
					if path != "PUT /put/1" && !strings.HasPrefix(path, "POST "+prefix+"/") {
						t.Fatalf("wrong transport: %v", api.paths)
					}
				}
				if sent := api.messages[0]; sent["msg_type"] != float64(7) || sent["msg_id"] != "trigger" {
					t.Fatalf("message = %#v", sent)
				}
			})
		}
	}
}

func TestFileAttachmentOneBotTransportUnchanged(t *testing.T) {
	for _, group := range []bool{true, false} {
		t.Run(fmt.Sprintf("group=%t", group), func(t *testing.T) {
			channel := &scriptedChannel{}
			r := NewRuntime(BotConfig{Platform: PlatformOneBotV11}, channel, NewPluginManager(), nil, nil, nil, nil)
			event := MessageEvent{Platform: PlatformOneBotV11, Kind: EventKindPrivate, UserID: "10001"}
			action, targetKey := "upload_private_file", "user_id"
			if group {
				event.Kind, event.GroupID = EventKindGroup, "10001"
				action, targetKey = "upload_group_file", "group_id"
			}
			if err := r.sendFileAttachment(context.Background(), event, "notes.md", []byte("hello")); err != nil {
				t.Fatal(err)
			}
			if len(channel.sent) != 0 || len(channel.apiCalls) != 1 || channel.apiCalls[0] != action {
				t.Fatalf("sent=%v, apiCalls=%v", channel.sent, channel.apiCalls)
			}
			params := channel.apiParams[0]
			if params["name"] != "notes.md" || params[targetKey] != int64(10001) {
				t.Fatalf("params=%#v", params)
			}
			if _, err := os.Stat(params["file"].(string)); !os.IsNotExist(err) {
				t.Fatalf("attachment snapshot was not cleaned up: %v", err)
			}
		})
	}
}

func TestFileAttachmentRejectsUnsupportedPlatform(t *testing.T) {
	channel := &scriptedChannel{}
	r := NewRuntime(BotConfig{Platform: PlatformFeishu}, channel, NewPluginManager(), nil, nil, nil, nil)
	err := r.sendFileAttachment(context.Background(), MessageEvent{Platform: PlatformFeishu}, "notes.md", []byte("hello"))
	if err == nil || !strings.Contains(err.Error(), "file attachment sending is unavailable") {
		t.Fatalf("error = %v", err)
	}
	if len(channel.sent) != 0 || len(channel.apiCalls) != 0 {
		t.Fatal("unsupported platform tried to send the file")
	}
}

func TestQQOfficialLocalAttachmentSizeLimit(t *testing.T) {
	t.Setenv("APP_DB_PATH", filepath.Join(t.TempDir(), "diana.db"))
	root := AgentWorkspaceDir()
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "large.zip")
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(path, localAttachmentMaxBytes+1); err != nil {
		t.Fatal(err)
	}
	api := newQQFakeAPI(t)
	r := NewRuntime(BotConfig{Platform: PlatformQQOfficial}, api.channel(), NewPluginManager(), nil, nil, nil, nil)
	tool := &dianaLocalAttachmentTool{runtime: r, event: MessageEvent{Platform: PlatformQQOfficial, Kind: EventKindPrivate, UserID: "U"}}
	if _, err := tool.Run(context.Background(), map[string]any{"path": "large.zip", "mode": "file"}); err == nil {
		t.Fatal("oversized local attachment accepted")
	}
	if len(api.paths) != 0 {
		t.Fatalf("oversized attachment reached QQ API: %v", api.paths)
	}
}

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

func TestLocalAttachmentSVGImageModeNeedsCoreBrowser(t *testing.T) {
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
		t.Fatal("svg image send without core browser accepted")
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
	if allowed["view_image"] || allowed["send_attachment"] {
		t.Fatal("local tools exposed to group members")
	}
	if !allowed[dianaRemoteImageToolName] {
		t.Fatal("remote image tool missing")
	}
}
