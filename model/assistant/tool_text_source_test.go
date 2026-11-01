package assistant

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SuInk/diana/model/agent"
	"github.com/SuInk/diana/model/llm"
)

func TestToolTextSourceWorkspaceBoundaries(t *testing.T) {
	t.Setenv("APP_DB_PATH", filepath.Join(t.TempDir(), "app.db"))
	root := AgentWorkspaceDir()
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string]string{"page.html": "<p>hello</p>", ".mcp.json": "secret", "binary": string([]byte{0xff})} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	outside := filepath.Join(t.TempDir(), "outside.html")
	if err := os.WriteFile(outside, []byte("<p>outside</p>"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape.html")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, ".mcp.json"), filepath.Join(root, "alias.html")); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name  string
		owner bool
		input map[string]any
		limit int64
		ok    bool
	}{
		{"inline member", false, map[string]any{"content": "<p>hello</p>"}, 100, true},
		{"owner file", true, map[string]any{"path": "page.html"}, 100, true},
		{"absolute workspace path", true, map[string]any{"path": filepath.Join(root, "page.html")}, 100, true},
		{"member denied", false, map[string]any{"path": "page.html"}, 100, false},
		{"ambiguous", true, map[string]any{"path": "page.html", "content": "<p>other</p>"}, 100, false},
		{"credential", true, map[string]any{"path": ".mcp.json"}, 100, false},
		{"credential alias", true, map[string]any{"path": "alias.html"}, 100, false},
		{"outside", true, map[string]any{"path": outside}, 100, false},
		{"symlink escape", true, map[string]any{"path": "escape.html"}, 100, false},
		{"traversal", true, map[string]any{"path": "../app.db"}, 100, false},
		{"missing", true, map[string]any{"path": "missing.html"}, 100, false},
		{"directory", true, map[string]any{"path": "."}, 100, false},
		{"too large", true, map[string]any{"path": "page.html"}, 4, false},
		{"binary", true, map[string]any{"path": "binary"}, 100, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := toolTextSource(nil, MessageEvent{}, tc.owner, tc.input, tc.limit)
			if (err == nil) != tc.ok {
				t.Fatalf("got %q, %v; want success=%v", got, err, tc.ok)
			}
			if tc.ok && got != "<p>hello</p>" {
				t.Fatalf("content changed: %q", got)
			}
		})
	}
}

func TestFileDeliverySendsWorkspaceTextWithoutTruncation(t *testing.T) {
	t.Setenv("APP_DB_PATH", filepath.Join(t.TempDir(), "app.db"))
	if err := os.MkdirAll(AgentWorkspaceDir(), 0700); err != nil {
		t.Fatal(err)
	}
	source := "<!doctype html><html><body>" + strings.Repeat("<p>line</p>\n", 1268) + "</body></html>"
	if err := os.WriteFile(filepath.Join(AgentWorkspaceDir(), "page.html"), []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	r, uploads := newFileDeliveryTestRuntime(t)
	tool := newDianaFileDeliveryTool(r, MessageEvent{Platform: PlatformTelegram, Kind: EventKindPrivate, UserID: "1"}, nil, RelationshipPolicy{Owner: true})
	raw, err := tool.Run(context.Background(), map[string]any{"filename": "page.html", "path": "page.html", "preview": false})
	if err != nil || !decodeFileDeliveryResult(t, raw).OK {
		t.Fatalf("%s %v", raw, err)
	}
	if len(*uploads) != 1 || (*uploads)[0].body != source {
		t.Fatal("file was truncated or not sent")
	}
	media := newDianaRenderMediaTool(nil, MessageEvent{}, nil, RelationshipPolicy{Owner: true})
	spec, err := media.parse(map[string]any{"format": "html", "path": "page.html"})
	if err != nil || spec.content != source {
		t.Fatalf("render source mismatch: %v", err)
	}
}

// Optional replay of the actual production HTML; no platform delivery occurs.
func TestRenderMediaProductionFileReplay(t *testing.T) {
	path := os.Getenv("DIANA_RENDER_REPLAY_HTML")
	if path == "" {
		t.Skip("set DIANA_RENDER_REPLAY_HTML to replay a production HTML file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("APP_DB_PATH", filepath.Join(t.TempDir(), "app.db"))
	if err := os.MkdirAll(AgentWorkspaceDir(), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(AgentWorkspaceDir(), "page.html"), data, 0600); err != nil {
		t.Fatal(err)
	}
	tool := newDianaRenderMediaTool(nil, MessageEvent{}, nil, RelationshipPolicy{Owner: true})
	spec, err := tool.parse(map[string]any{"format": "html", "path": "page.html"})
	if err != nil {
		t.Fatal(err)
	}
	req, err := buildRenderMediaRequest(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	png, size, err := agent.CaptureHTMLStill(context.Background(), req, renderMediaStillAt)
	if err != nil {
		t.Fatal(err)
	}
	if output := os.Getenv("DIANA_RENDER_REPLAY_PNG"); output != "" {
		if err := os.WriteFile(output, png, 0600); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("rendered production HTML: input=%d output=%d size=%+v", len(data), len(png), size)
}

func TestLiveRenderMediaUsesWorkspacePath(t *testing.T) {
	model := liveThreadStateModel("DIANA_TEST_LLM_MODEL", "gpt-6.1-terra")
	client := liveThreadStateClient(t, model, "diana-render-file-replay")
	tool := newDianaRenderMediaTool(nil, MessageEvent{}, nil, RelationshipPolicy{Owner: true})
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	response, err := client.Generate(ctx, llm.GenerateRequest{Messages: []llm.Message{
		{Role: llm.RoleSystem, Content: "当前发言者是机器人主人。之前已把用户的 HTML 附件保存到 downloads/cijian-m3.html，共 437849 字节、1268 行。read_file 只读到前 200 行字体许可证，未拿到页面主体。上次把 file-read-needed 传给渲染工具发出了一张只有占位文字的白图。请使用当前工具处理用户请求。"},
		{Role: llm.RoleUser, Content: "重试，把刚才那个 HTML 渲染成图片发出来，不要改写我的文件。"},
	}, Tools: []llm.ToolDefinition{{Name: tool.Name(), Description: tool.Description(), Parameters: tool.InputSchema()}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.ToolCalls) != 1 {
		t.Fatalf("expected one render call, got %d", len(response.ToolCalls))
	}
	call := response.ToolCalls[0]
	if call.Name != tool.Name() || call.Arguments["path"] != "downloads/cijian-m3.html" || strings.TrimSpace(configToolString(call.Arguments, "content")) != "" {
		t.Fatalf("unexpected call: %+v", call)
	}
	t.Logf("model=%s selected render_media path without copying HTML; delivery not executed", model)
}
