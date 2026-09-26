// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SuInk/diana/model/agent"
	"github.com/SuInk/diana/model/llm"
)

func workspaceTestPNG(t *testing.T, width, height int) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, width, height))); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func workspaceTestJPEG(t *testing.T, width, height int) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, image.NewRGBA(image.Rect(0, 0, width, height)), nil); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func runSaveToWorkspace(t *testing.T, tool *dianaSaveToWorkspaceTool, input map[string]any) saveToWorkspaceResult {
	t.Helper()
	out, err := tool.Run(context.Background(), input)
	if err != nil {
		t.Fatalf("save_to_workspace %v: %v", input, err)
	}
	var result saveToWorkspaceResult
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("结果不是 JSON: %s", out)
	}
	return result
}

func TestSaveToWorkspaceFromChatMedia(t *testing.T) {
	t.Setenv("APP_DB_PATH", filepath.Join(t.TempDir(), "diana.db"))
	cache := t.TempDir()
	photo := workspaceTestJPEG(t, 40, 30)
	photoPath := filepath.Join(cache, "abcdef.image")
	if err := os.WriteFile(photoPath, photo, 0o600); err != nil {
		t.Fatal(err)
	}
	generated := workspaceTestPNG(t, 8, 6)
	generatedPath := filepath.Join(cache, "generated.png")
	if err := os.WriteFile(generatedPath, generated, 0o600); err != nil {
		t.Fatal(err)
	}
	pdf := []byte("%PDF-1.7\n1 0 obj\n")
	pdfPath := filepath.Join(cache, "download.tmp")
	if err := os.WriteFile(pdfPath, pdf, 0o600); err != nil {
		t.Fatal(err)
	}

	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.remember(MessageEvent{
		Kind: EventKindGroup, GroupID: "group-1", UserID: "user-1", MessageID: "user-photo",
		Segments: []MessageSegment{{Type: "image", Data: map[string]string{"file": "abcdef.image", "cached_file": photoPath}}},
	})
	runtime.remember(MessageEvent{
		Kind: EventKindGroup, GroupID: "group-1", UserID: "bot-1", SelfID: "bot-1", MessageID: "bot-image",
		Segments: []MessageSegment{{Type: "image", Data: map[string]string{"cached_file": generatedPath}}},
	})
	runtime.remember(MessageEvent{
		Kind: EventKindGroup, GroupID: "group-1", UserID: "user-1", MessageID: "mixed",
		Segments: []MessageSegment{
			{Type: "image", Data: map[string]string{"cached_file": photoPath}},
			{Type: "file", Data: map[string]string{"name": "报告/final.png", "path": pdfPath}},
		},
	})
	tool := newDianaSaveToWorkspaceTool(runtime, MessageEvent{Kind: EventKindGroup, GroupID: "group-1", UserID: "owner"})
	tool.now = func() time.Time { return time.Date(2026, 9, 26, 15, 30, 12, 0, time.UTC) }

	saved := runSaveToWorkspace(t, tool, map[string]any{"source": "chat", "message_id": "user-photo"})
	if saved.Path != "downloads/image-20260926-153012.jpg" || saved.MIME != "image/jpeg" || saved.Width != 40 || saved.Height != 30 || saved.Bytes != len(photo) {
		t.Fatalf("用户发的图存得不对: %+v", saved)
	}
	if data, _ := os.ReadFile(filepath.Join(AgentWorkspaceDir(), filepath.FromSlash(saved.Path))); !bytes.Equal(data, photo) {
		t.Fatal("存下来的不是原始字节")
	}

	own := runSaveToWorkspace(t, tool, map[string]any{"source": "chat", "message_id": "bot-image"})
	if own.Path != "outputs/image-20260926-153012.png" {
		t.Fatalf("机器人自己发的生成图应默认进 outputs/: %+v", own)
	}

	if _, err := tool.Run(context.Background(), map[string]any{"source": "chat", "message_id": "mixed"}); err == nil || !strings.Contains(err.Error(), "media_index") {
		t.Fatalf("多个媒体时应要求 media_index: %v", err)
	}
	file := runSaveToWorkspace(t, tool, map[string]any{"source": "chat", "message_id": "mixed", "media_index": 2, "path": "docs/"})
	// 平台给的名字带斜杠也只取末段；内容是 PDF，扩展名跟着纠正。
	if file.Path != "docs/final.pdf" || file.MIME != "application/pdf" {
		t.Fatalf("文件段存得不对: %+v", file)
	}
	renamed := runSaveToWorkspace(t, tool, map[string]any{"source": "chat", "message_id": "user-photo", "path": "keepsake/cat.png"})
	if renamed.Path != "keepsake/cat.jpg" || renamed.ExtensionCorrected != "cat.png → cat.jpg" {
		t.Fatalf("指定文件名时扩展名也要按内容纠正: %+v", renamed)
	}
	again := runSaveToWorkspace(t, tool, map[string]any{"source": "chat", "message_id": "user-photo", "path": "keepsake/cat.jpg"})
	if again.Path != "keepsake/cat-2.jpg" {
		t.Fatalf("同名默认不覆盖: %+v", again)
	}
	for _, input := range []map[string]any{
		{"source": "chat", "message_id": "missing"},
		{"source": "chat", "message_id": "mixed", "media_index": 5},
		{"source": "chat", "message_id": "user-photo", "path": "../escape.jpg"},
		{"source": "chat", "message_id": "user-photo", "path": agent.CodingRuntimeDirName + "/auth/token.jpg"},
		{"source": "chat", "message_id": "user-photo", "path": ".trash/x.jpg"},
		{"source": "nowhere"},
	} {
		if _, err := tool.Run(context.Background(), input); err == nil {
			t.Fatalf("%v 应被拒绝", input)
		}
	}
}

// 聊天媒体的本地路径来自消息记录，不能借它把凭据文件「存」进工作目录。
func TestSaveToWorkspaceRefusesRuntimeSecretPaths(t *testing.T) {
	t.Setenv("APP_DB_PATH", filepath.Join(t.TempDir(), "diana.db"))
	secret := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(secret, []byte("admin_password: hunter2"), 0o600); err != nil {
		t.Fatal(err)
	}
	agent.ProtectRuntimeFiles(secret)
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.remember(MessageEvent{
		Kind: EventKindGroup, GroupID: "group-1", UserID: "user-1", MessageID: "crafted",
		Segments: []MessageSegment{{Type: "file", Data: map[string]string{"name": "config.yaml", "path": secret}}},
	})
	tool := newDianaSaveToWorkspaceTool(runtime, MessageEvent{Kind: EventKindGroup, GroupID: "group-1", UserID: "owner"})
	if out, err := tool.Run(context.Background(), map[string]any{"source": "chat", "message_id": "crafted"}); err == nil {
		t.Fatalf("凭据文件被存进了工作目录: %s", out)
	}
}

func TestSaveToWorkspaceFromMCPMedia(t *testing.T) {
	t.Setenv("APP_DB_PATH", filepath.Join(t.TempDir(), "diana.db"))
	data := workspaceTestJPEG(t, 12, 9)
	id, err := agent.StoreMCPMedia(agent.MCPMedia{Kind: agent.MCPMediaImage, MIMEType: "image/jpeg", Data: data})
	if err != nil {
		t.Fatal(err)
	}
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	tool := newDianaSaveToWorkspaceTool(runtime, MessageEvent{Kind: EventKindPrivate, UserID: "owner"})
	tool.now = func() time.Time { return time.Date(2026, 9, 26, 15, 30, 12, 0, time.UTC) }
	saved := runSaveToWorkspace(t, tool, map[string]any{"source": "mcp", "media_id": id})
	if saved.Path != "outputs/image-20260926-153012.jpg" || saved.Width != 12 || saved.Height != 9 {
		t.Fatalf("MCP 媒体存得不对: %+v", saved)
	}
	if _, err := tool.Run(context.Background(), map[string]any{"source": "mcp", "media_id": "mcpm_000000000000000000000000"}); err == nil {
		t.Fatal("过期的 media_id 应被拒绝")
	}
}

// 网址下载复用挡内网的下载缓存：本机地址、非 http 地址都不能下。
func TestSaveToWorkspaceURLUsesPublicFetcher(t *testing.T) {
	t.Setenv("APP_DB_PATH", filepath.Join(t.TempDir(), "diana.db"))
	t.Setenv("DIANA_ALLOW_PRIVATE_HTTP_FETCHES", "false")
	hits := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		_, _ = w.Write([]byte("secret intranet file"))
	}))
	defer server.Close()
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	tool := newDianaSaveToWorkspaceTool(runtime, MessageEvent{Kind: EventKindPrivate, UserID: "owner"})
	for _, raw := range []string{server.URL + "/file.txt", "file:///etc/passwd", "ftp://example.com/a"} {
		if out, err := tool.Run(context.Background(), map[string]any{"source": "url", "url": raw}); err == nil {
			t.Fatalf("%s 应被拒绝，却得到 %s", raw, out)
		}
	}
	if hits != 0 {
		t.Fatalf("内网地址被实际请求了 %d 次", hits)
	}
}

func TestSaveToWorkspaceURLDownloadsAndCorrectsExtension(t *testing.T) {
	t.Setenv("APP_DB_PATH", filepath.Join(t.TempDir(), "diana.db"))
	t.Setenv("DIANA_ALLOW_PRIVATE_HTTP_FETCHES", "true")
	photo := workspaceTestJPEG(t, 7, 5)
	hits := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		_, _ = w.Write(photo)
	}))
	defer server.Close()
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	tool := newDianaSaveToWorkspaceTool(runtime, MessageEvent{Kind: EventKindPrivate, UserID: "owner"})
	saved := runSaveToWorkspace(t, tool, map[string]any{"source": "url", "url": server.URL + "/pics/cover.webp?x=1"})
	if saved.Path != "downloads/cover.jpg" || saved.MIME != "image/jpeg" || saved.Width != 7 {
		t.Fatalf("网址文件存得不对: %+v", saved)
	}
	again := runSaveToWorkspace(t, tool, map[string]any{"source": "url", "url": server.URL + "/pics/cover.webp?x=1"})
	if again.Path != "downloads/cover-2.jpg" || hits != 1 {
		t.Fatalf("同一地址应命中下载缓存、不重复下载: %+v hits=%d", again, hits)
	}
}

func TestSaveToWorkspaceToolIsOwnerOnly(t *testing.T) {
	if (RelationshipPolicy{}).allowedAgentToolNames()[dianaSaveToWorkspaceToolName] {
		t.Fatal("save_to_workspace 不该给群成员")
	}
	if (RelationshipPolicy{}).allowedAgentToolNames()[agent.ManageFilesToolName] {
		t.Fatal("manage_files 不该给群成员")
	}
}

func TestGeneratedImageExtensionNeverJFIF(t *testing.T) {
	jpegData := workspaceTestJPEG(t, 2, 2)
	if ext := generatedImageExtension("image/jpeg", jpegData); ext != ".jpg" {
		t.Fatalf("JPEG 生成图的扩展名应是 .jpg，得到 %s", ext)
	}
	if ext := generatedImageExtension("image/jpeg", []byte("not decodable")); ext != ".jpg" {
		t.Fatalf("按接口类型兜底也不该是 .jfif，得到 %s", ext)
	}
	if ext := generatedImageExtension("", workspaceTestPNG(t, 2, 2)); ext != ".png" {
		t.Fatalf("PNG: %s", ext)
	}
	name := mcpMediaFileName(agent.MCPMedia{Kind: agent.MCPMediaImage, MIMEType: "image/jpeg"})
	if name != "mcp-image.jpg" {
		t.Fatalf("MCP 媒体文件名 = %s", name)
	}
}

func TestPersistGeneratedImagesWritesOutputs(t *testing.T) {
	t.Setenv("APP_DB_PATH", filepath.Join(t.TempDir(), "diana.db"))
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	first := workspaceTestPNG(t, 5, 5)
	second := workspaceTestJPEG(t, 6, 6)
	images := []string{
		"data:image/png;base64," + base64.StdEncoding.EncodeToString(first),
		// 接口报的类型说是 png，字节其实是 jpeg：扩展名按字节定。
		"data:image/png;base64," + base64.StdEncoding.EncodeToString(second),
	}
	stem := dianaImageWorkspaceStem("image:abcdef123456", time.Date(2026, 9, 26, 15, 30, 12, 0, time.UTC))
	if stem != "image-20260926-153012-abcdef" {
		t.Fatalf("stem = %s", stem)
	}
	runtime.persistGeneratedImages(context.Background(), MessageEvent{Kind: EventKindPrivate, UserID: "owner"}, stem, images)
	root := AgentWorkspaceDir()
	if data, err := os.ReadFile(filepath.Join(root, "outputs", stem+".png")); err != nil || !bytes.Equal(data, first) {
		t.Fatalf("第一张没存对: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(root, "outputs", stem+"-2.jpg")); err != nil || !bytes.Equal(data, second) {
		t.Fatalf("第二张没存对: %v", err)
	}
	// 没有落盘前缀（非主人或没开写入）时什么都不写。
	runtime.persistGeneratedImages(context.Background(), MessageEvent{}, "", images)
	entries, _ := os.ReadDir(filepath.Join(root, "outputs"))
	if len(entries) != 2 {
		t.Fatalf("outputs 里应该只有 2 个文件，得到 %d", len(entries))
	}
}

func TestImageToolPersistsOnlyForOwnerWithFileWrite(t *testing.T) {
	cfg := BotConfig{AgentEnabled: true, AgentMode: AgentModeStandard, AgentFileWriteEnabled: true}
	runtime := NewRuntime(cfg, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	owner := &dianaImageTool{runtime: runtime, relationship: RelationshipPolicy{Owner: true}}
	if !owner.persistsToWorkspace() {
		t.Fatal("主人开了写入时应该落盘")
	}
	member := &dianaImageTool{runtime: runtime, relationship: RelationshipPolicy{}}
	if member.persistsToWorkspace() {
		t.Fatal("群成员出的图不该往主人的工作目录里堆")
	}
	// 安全模式下即使写入开关开着也不落盘：成品自动存盘不经过工具，同样要受安全模式约束。
	safe := NewRuntime(BotConfig{AgentEnabled: true, AgentMode: AgentModeSafe, AgentFileWriteEnabled: true}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	if (&dianaImageTool{runtime: safe, relationship: RelationshipPolicy{Owner: true}}).persistsToWorkspace() {
		t.Fatal("安全模式下画图成品不该落进工作目录")
	}
	closed := NewRuntime(BotConfig{AgentEnabled: true, AgentMode: AgentModeStandard}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	if (&dianaImageTool{runtime: closed, relationship: RelationshipPolicy{Owner: true}}).persistsToWorkspace() {
		t.Fatal("没开文件写入时不该落盘")
	}
}

func TestLocalAttachmentValidatesSniffedImageType(t *testing.T) {
	t.Setenv("APP_DB_PATH", filepath.Join(t.TempDir(), "diana.db"))
	root := AgentWorkspaceDir()
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "fake.png"), []byte("这其实是一段文字"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "logo.svg"), []byte(`<svg xmlns="http://www.w3.org/2000/svg" width="4" height="4"></svg>`), 0o600); err != nil {
		t.Fatal(err)
	}
	view := &dianaLocalAttachmentTool{view: true}
	_, err := view.Run(context.Background(), map[string]any{"path": "fake.png"})
	if err == nil || !strings.Contains(err.Error(), "不是图片") {
		t.Fatalf("内容不是图片的 .png 应被拒绝: %v", err)
	}
	if len(view.ToolResultParts("")) != 0 {
		t.Fatal("被拒绝时不能附上任何画面")
	}
	send := &dianaLocalAttachmentTool{}
	if _, err := send.Run(context.Background(), map[string]any{"path": "fake.png", "mode": "image"}); err == nil || !strings.Contains(err.Error(), "mode=file") {
		t.Fatalf("send_attachment mode=image 发假图片应被拒绝: %v", err)
	}
	// SVG 看图走栅格化；「网页渲染」没开时给出清楚的报错，而不是把源码当图片塞给模型。
	if _, err := view.Run(context.Background(), map[string]any{"path": "logo.svg"}); err == nil || !strings.Contains(err.Error(), "网页渲染") {
		t.Fatalf("svg 看图: %v", err)
	}
	if _, err := view.Run(context.Background(), map[string]any{"path": "logo.png"}); err == nil || !strings.Contains(err.Error(), "logo.svg") {
		t.Fatalf("找不到时应提示同名文件: %v", err)
	}
}

func TestLocalAttachmentImageSendsOriginalBytes(t *testing.T) {
	t.Setenv("APP_DB_PATH", filepath.Join(t.TempDir(), "diana.db"))
	root := AgentWorkspaceDir()
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	// 宽 2100 超过给模型看的 2000px 上限：以前发出去的是缩放后重新编码的版本。
	original := workspaceTestPNG(t, 2100, 200)
	if err := os.WriteFile(filepath.Join(root, "wide.png"), original, 0o600); err != nil {
		t.Fatal(err)
	}
	var received []byte
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(32 << 20); err == nil {
			if file, _, err := r.FormFile("photo"); err == nil {
				received, _ = io.ReadAll(file)
				_ = file.Close()
			}
			r.MultipartForm.RemoveAll()
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":99}}`))
	}))
	defer api.Close()
	r := NewRuntime(BotConfig{Platform: PlatformTelegram}, NewTelegramChannel(TelegramConfig{BotToken: "test", APIBaseURL: api.URL}), NewPluginManager(), nil, nil, nil, nil)
	tool := &dianaLocalAttachmentTool{runtime: r, event: MessageEvent{Platform: PlatformTelegram, Kind: EventKindGroup, GroupID: "-100123"}}
	out, err := tool.Run(context.Background(), map[string]any{"path": "wide.png", "mode": "image"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"original":true`) || !bytes.Equal(received, original) {
		t.Fatalf("发出去的不是原图: out=%s received=%d bytes want=%d", out, len(received), len(original))
	}
}

func TestLocalImageFitsPlatformLimits(t *testing.T) {
	tall := workspaceTestPNG(t, 100, 2500)
	if localImageFitsPlatform(PlatformTelegram, tall) {
		t.Fatal("长宽比超过 20 的图 Telegram sendPhoto 不收，应退回缩放版")
	}
	if !localImageFitsPlatform(PlatformOneBotV11, tall) {
		t.Fatal("OneBot 没有这条限制，原图照发")
	}
	if !localImageFitsPlatform(PlatformTelegram, workspaceTestPNG(t, 800, 600)) {
		t.Fatal("普通尺寸的图 Telegram 应发原图")
	}
}

// 主人出的图除了照常发出去，还要把原图落进工作目录 outputs/，受理结果里告诉模型
// 会存到哪——之后「把那张图再发一次」「存到别处」才有东西可用。
func TestDianaImageToolPersistsOwnerResultIntoWorkspace(t *testing.T) {
	t.Setenv("APP_DB_PATH", filepath.Join(t.TempDir(), "diana.db"))
	generated := workspaceTestPNG(t, 9, 7)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/media" {
			writeTestPNG(w)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"b64_json":"` + base64.StdEncoding.EncodeToString(generated) + `"}]}`))
	}))
	defer server.Close()
	store := &stubLLMProfileStore{set: llm.NewProfileSet(llm.ProviderConfig{
		Provider:   llm.ProviderOpenAICompatible,
		APIKey:     "secret",
		BaseURL:    server.URL + "/v1",
		Model:      "gpt-test",
		ImageModel: "gpt-image-2",
	})}
	channel := &recordingChannel{}
	runtime := NewRuntime(BotConfig{AgentEnabled: true, AgentMode: AgentModeStandard, AgentFileWriteEnabled: true}, channel, NewPluginManager(), store, nil, nil, nil)
	runtime.SetMediaStore(mediaStore(t))
	runtime.SetLocalMediaSharer(&recordingLocalMediaSharer{url: server.URL + "/media"})
	event := MessageEvent{Kind: EventKindPrivate, UserID: "owner", MessageID: "owner-image"}
	tool := newDianaImageTool(runtime, event, RelationshipPolicyFor(UserMemoryProfile{}, "owner", "owner"))
	raw, err := tool.Run(context.Background(), map[string]any{"prompt": "一只坐在窗台上的橘猫"})
	if err != nil {
		t.Fatal(err)
	}
	var queued dianaImageToolResult
	if err := json.Unmarshal([]byte(raw), &queued); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(queued.WorkspacePathPrefix, agent.WorkspaceOutputsDir+"/image-") || queued.WorkspaceNote == "" {
		t.Fatalf("受理结果里缺少落盘位置: %s", raw)
	}
	waitForCondition(t, 2*time.Second, func() bool {
		return runtime.activeSubagentTaskCount() == 0
	})
	saved := filepath.Join(AgentWorkspaceDir(), filepath.FromSlash(queued.WorkspacePathPrefix)+".png")
	data, err := os.ReadFile(saved)
	if err != nil || !bytes.Equal(data, generated) {
		t.Fatalf("原图没有存进工作目录 %s: %v", saved, err)
	}
	if sent := channel.sentSnapshot(); len(sent) == 0 || len(sent[len(sent)-1].ImageURLs) != 1 {
		t.Fatalf("落盘不能影响原来的发图: %#v", sent)
	}
}
