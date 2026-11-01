package assistant

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SuInk/diana/model/llm"
)

func groupWorkspaceTestRuntime(t *testing.T) (*Runtime, MessageEvent) {
	t.Helper()
	t.Setenv("APP_DB_PATH", filepath.Join(t.TempDir(), "app.db"))
	cfg := BotConfig{ID: "bot-a", OwnerID: "owner", Platform: PlatformTelegram, AgentEnabled: true, AgentMode: AgentModeStandard, AgentFileWriteEnabled: true}
	r := NewRuntime(cfg, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	return r, MessageEvent{ProfileID: "bot-a", Platform: PlatformTelegram, Kind: EventKindGroup, GroupID: "group-a", UserID: "alice"}
}

func TestLiveWorkspaceGroupSaveThenRender(t *testing.T) {
	model := liveThreadStateModel("DIANA_TEST_LLM_MODEL", "gpt-6.1-terra")
	client := liveThreadStateClient(t, model, "diana-group-workspace-replay")
	r, event := groupWorkspaceTestRuntime(t)
	data := []byte("<!doctype html><html><body><h1>Group file</h1></body></html>")
	if path := os.Getenv("DIANA_RENDER_REPLAY_HTML"); path != "" {
		var err error
		data, err = os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
	}
	attachment := filepath.Join(t.TempDir(), "cijian-m3.html")
	if err := os.WriteFile(attachment, data, 0600); err != nil {
		t.Fatal(err)
	}
	message := event
	message.MessageID = "file-msg"
	message.Segments = []MessageSegment{{Type: "file", Data: map[string]string{"name": "cijian-m3.html", "path": attachment}}}
	r.remember(message)
	save := newDianaSaveToWorkspaceTool(r, event)
	render := newDianaRenderMediaTool(r, event, nil, RelationshipPolicy{})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	req := llm.GenerateRequest{Messages: []llm.Message{
		{Role: llm.RoleSystem, Content: "当前发言者是普通群成员，不是主人。本群消息 file-msg 上传了 cijian-m3.html，文件还没有保存到工作区。请用工具完成请求，不要改写文件或虚构页面内容。"},
		{Role: llm.RoleUser, Content: "把刚上传的 HTML 渲染成图片发出来。"},
	}, Tools: []llm.ToolDefinition{
		{Name: save.Name(), Description: save.Description(), Parameters: save.InputSchema()},
		{Name: render.Name(), Description: render.Description(), Parameters: render.InputSchema()},
	}}
	first, err := client.Generate(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.ToolCalls) != 1 || first.ToolCalls[0].Name != save.Name() {
		t.Fatalf("expected save call, got %+v", first.ToolCalls)
	}
	call := first.ToolCalls[0]
	result, err := save.Run(ctx, call.Arguments)
	if err != nil {
		t.Fatal(err)
	}
	var saved saveToWorkspaceResult
	if err := json.Unmarshal([]byte(result), &saved); err != nil {
		t.Fatal(err)
	}
	req.Messages = append(req.Messages, llm.Message{Role: llm.RoleAssistant, ToolCalls: first.ToolCalls}, llm.Message{Role: llm.RoleTool, ToolCallID: call.ID, ToolName: save.Name(), Content: result})
	second, err := client.Generate(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.ToolCalls) != 1 || second.ToolCalls[0].Name != render.Name() {
		t.Fatalf("expected render call, got %+v", second.ToolCalls)
	}
	call = second.ToolCalls[0]
	if call.Arguments["path"] != saved.Path || strings.TrimSpace(configToolString(call.Arguments, "content")) != "" {
		t.Fatalf("model didn't reuse saved path: %+v", call)
	}
	spec, err := render.parse(call.Arguments)
	if err != nil || spec.content != string(data) {
		t.Fatalf("real group file read failed: %v", err)
	}
	t.Logf("model=%s group member saved %d bytes and selected render_media with authorized path; no platform delivery", model, len(data))
}

func TestWorkspaceGroupSaveAndRenderSharedByMembers(t *testing.T) {
	r, event := groupWorkspaceTestRuntime(t)
	source := "<!doctype html><html><body>" + strings.Repeat("<p>hello</p>\n", 1268) + "</body></html>"
	attachment := filepath.Join(t.TempDir(), "page.html")
	if err := os.WriteFile(attachment, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	message := event
	message.MessageID = "attachment-1"
	message.Segments = []MessageSegment{{Type: "file", Data: map[string]string{"name": "page.html", "path": attachment}}}
	r.remember(message)
	saved := runSaveToWorkspace(t, newDianaSaveToWorkspaceTool(r, event), map[string]any{"source": "chat", "message_id": message.MessageID})
	if !strings.HasPrefix(saved.Path, "downloads/groups/"+r.workspaceGroupScope(event)+"/") {
		t.Fatalf("unscoped save: %+v", saved)
	}
	event.UserID = "bob"
	// Another member and a fresh runtime must retain access, without in-memory grants.
	restarted := NewRuntime(r.effectiveConfigForEvent(event), nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	tool := newDianaRenderMediaTool(restarted, event, nil, RelationshipPolicy{})
	spec, err := tool.parse(map[string]any{"format": "html", "path": saved.Path})
	if err != nil || spec.content != source {
		t.Fatalf("same-group render failed: %v", err)
	}
	for _, absolute := range []bool{false, true} {
		path := saved.Path
		if absolute {
			path = filepath.Join(AgentWorkspaceDir(), path)
		}
		got, err := toolTextSource(restarted, event, false, map[string]any{"path": path}, int64(len(source)))
		if err != nil || got != source {
			t.Fatalf("path read: %v", err)
		}
	}
	// All three consumers use the same authorization; the sender uses a local fake API.
	textResult, err := newDianaRenderTool(restarted, event).Run(context.Background(), map[string]any{"format": "markdown", "path": saved.Path})
	if err != nil || !strings.Contains(textResult, "12000") {
		t.Fatalf("render should reach the existing length limit: %s %v", textResult, err)
	}
	sender, uploads := newFileDeliveryTestRuntime(t)
	raw, err := newDianaFileDeliveryTool(sender, event, nil, RelationshipPolicy{}).Run(context.Background(), map[string]any{"filename": "page.html", "path": saved.Path, "preview": false})
	if err != nil || !decodeFileDeliveryResult(t, raw).OK || len(*uploads) != 1 || (*uploads)[0].body != source {
		t.Fatalf("shared file delivery: %s %v", raw, err)
	}
	raw, err = newDianaRenderMediaTool(restarted, event, SettingValues{fileDeliverySettingOwnerOnly: true}, RelationshipPolicy{}).Run(context.Background(), map[string]any{"format": "html", "path": saved.Path})
	if err != nil || !strings.Contains(raw, "主人") {
		t.Fatalf("owner-only setting bypassed: %s %v", raw, err)
	}
}

func TestWorkspaceGroupAccessBoundaries(t *testing.T) {
	r, event := groupWorkspaceTestRuntime(t)
	saved, err := r.saveWorkspacePayload(event, workspaceMediaPayload{data: []byte("<p>group</p>"), name: "page.html"}, "downloads/page.html", false, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		change func(*MessageEvent)
	}{
		{"other group", func(e *MessageEvent) { e.GroupID = "group-b" }},
		{"other bot", func(e *MessageEvent) { e.ProfileID = "bot-b" }},
		{"other platform", func(e *MessageEvent) { e.Platform = "discord" }},
		{"private", func(e *MessageEvent) { e.Kind = EventKindPrivate }},
		{"empty group", func(e *MessageEvent) { e.GroupID = "" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			other := event
			tc.change(&other)
			if _, err := toolTextSource(r, other, false, map[string]any{"path": saved.Path}, 100); err == nil {
				t.Fatal("cross-scope access allowed")
			}
		})
	}
	for _, name := range []string{"private.html", ".mcp.json"} {
		if err := os.WriteFile(filepath.Join(AgentWorkspaceDir(), name), []byte("<p>private</p>"), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := toolTextSource(r, event, false, map[string]any{"path": name}, 100); err == nil {
			t.Fatalf("unshared file %s allowed", name)
		}
	}
	if err := os.WriteFile(filepath.Join(AgentWorkspaceDir(), saved.Path), []byte("<p>overwritten by private session</p>"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := toolTextSource(r, event, false, map[string]any{"path": saved.Path}, 100); err == nil {
		t.Fatal("stale grant survived overwrite")
	}
	// A guessed symlink alias must not inherit a grant for a different path.
	if err := os.Symlink(filepath.Join(AgentWorkspaceDir(), saved.Path), filepath.Join(AgentWorkspaceDir(), "alias.html")); err != nil {
		t.Fatal(err)
	}
	if _, err := toolTextSource(r, event, false, map[string]any{"path": "alias.html"}, 100); err == nil {
		t.Fatal("unregistered alias accepted")
	}
	if _, err := toolTextSource(r, event, true, map[string]any{"path": "private.html"}, 100); err != nil {
		t.Fatalf("owner access regressed: %v", err)
	}
}

func TestWorkspaceGroupSaveRestrictionsAndRegistry(t *testing.T) {
	r, event := groupWorkspaceTestRuntime(t)
	tool := newDianaSaveToWorkspaceTool(r, event)
	for _, input := range []map[string]any{
		{"source": "mcp", "media_id": "guessed"},
		{"source": "url", "url": "https://example.com/file", "overwrite": true},
		{"source": "url", "url": "https://example.com/file", "keep": true},
		{"source": "url", "url": "https://example.com/file", "path": "../private.html"},
		{"source": "url", "url": "https://example.com/file", "path": "outputs/file.html"},
		{"source": "chat", "message_id": "other-group-message"},
	} {
		if _, err := tool.Run(context.Background(), input); err == nil {
			t.Fatalf("unsafe save allowed: %+v", input)
		}
	}
	if !r.allowedAgentToolNamesForEvent(event, RelationshipPolicy{})[dianaSaveToWorkspaceToolName] {
		t.Fatal("group save tool not allowed")
	}
	private := event
	private.Kind = EventKindPrivate
	if r.allowedAgentToolNamesForEvent(private, RelationshipPolicy{})[dianaSaveToWorkspaceToolName] {
		t.Fatal("private save tool exposed")
	}
	if _, err := newDianaSaveToWorkspaceTool(r, private).Run(context.Background(), map[string]any{"source": "chat"}); err == nil {
		t.Fatal("private member saved file")
	}
	for _, mode := range []string{AgentModeStandard, AgentModeSafe} {
		cfg := r.effectiveConfigForEvent(event)
		cfg.AgentMode = mode
		cfg.AgentFileWriteEnabled = mode == AgentModeSafe
		disabled := NewRuntime(cfg, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
		if _, err := newDianaSaveToWorkspaceTool(disabled, event).Run(context.Background(), map[string]any{"source": "chat"}); err == nil {
			t.Fatal("write settings bypassed")
		}
	}
	registry, err := r.newReplyAgentRegistry(context.Background(), r.effectiveConfigForEvent(event), event, RelationshipPolicy{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer registry.Close()
	if _, ok := registry.Get(dianaSaveToWorkspaceToolName); !ok {
		t.Fatal("group save tool missing from actual registry")
	}
}

func TestWorkspaceGroupSaveRejectsCrossGroupMessagesAndQuotes(t *testing.T) {
	r, event := groupWorkspaceTestRuntime(t)
	attachment := filepath.Join(t.TempDir(), "other.html")
	if err := os.WriteFile(attachment, []byte("<p>other group</p>"), 0600); err != nil {
		t.Fatal(err)
	}
	segments := []MessageSegment{{Type: "file", Data: map[string]string{"name": "other.html", "path": attachment}}}
	other := event
	other.GroupID, other.MessageID, other.Segments = "group-b", "other-group-message", segments
	r.remember(other)
	quoted := event
	quoted.MessageID = "quoted-other-group"
	quoted.Quoted = &QuotedMessage{GroupID: "group-b", MessageID: other.MessageID, Segments: segments}
	r.remember(quoted)
	for _, messageID := range []string{other.MessageID, quoted.MessageID} {
		if _, err := newDianaSaveToWorkspaceTool(r, event).Run(context.Background(), map[string]any{"source": "chat", "message_id": messageID}); err == nil {
			t.Fatalf("cross-group source %s accepted", messageID)
		}
	}
}
