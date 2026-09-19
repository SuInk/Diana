package assistant

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

type fileDeliveryUpload struct {
	method, filename, chat, topic, body string
}

func newFileDeliveryTestRuntime(t *testing.T) (*Runtime, *[]fileDeliveryUpload) {
	t.Helper()
	uploads := &[]fileDeliveryUpload{}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upload := fileDeliveryUpload{method: filepath.Base(r.URL.Path)}
		if err := r.ParseMultipartForm(1 << 20); err == nil {
			defer r.MultipartForm.RemoveAll()
			if file, header, err := r.FormFile("document"); err == nil {
				data, _ := io.ReadAll(file)
				file.Close()
				upload.filename, upload.body = header.Filename, string(data)
			}
			upload.chat, upload.topic = r.FormValue("chat_id"), r.FormValue("message_thread_id")
		}
		*uploads = append(*uploads, upload)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":99}}`))
	}))
	t.Cleanup(api.Close)
	r := NewRuntime(BotConfig{Platform: PlatformTelegram}, NewTelegramChannel(TelegramConfig{BotToken: "test", APIBaseURL: api.URL}), NewPluginManager(), nil, nil, nil, nil)
	return r, uploads
}

func decodeFileDeliveryResult(t *testing.T, raw string) dianaFileDeliveryResult {
	t.Helper()
	var result dianaFileDeliveryResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatalf("decode %q: %v", raw, err)
	}
	return result
}

func TestFileDeliverySendsGeneratedFile(t *testing.T) {
	r, uploads := newFileDeliveryTestRuntime(t)
	event := MessageEvent{Platform: PlatformTelegram, Kind: EventKindGroup, GroupID: "-100123", MessageThreadID: "77"}
	tool := newDianaFileDeliveryTool(r, event, SettingValues{}, RelationshipPolicy{})
	source := "package main\n\nfunc main() {}\n"
	raw, err := tool.Run(context.Background(), map[string]any{"filename": "main.go", "content": source})
	if err != nil {
		t.Fatal(err)
	}
	result := decodeFileDeliveryResult(t, raw)
	if !result.OK || result.Filename != "main.go" || result.Bytes != len(source) {
		t.Fatalf("unexpected result: %+v", result)
	}
	// 测试环境没启用「网页渲染」，预览应跳过但文件照发。
	if !strings.HasPrefix(result.Preview, "skipped") {
		t.Fatalf("preview should be skipped without browser plugin, got %q", result.Preview)
	}
	if len(*uploads) != 1 {
		t.Fatalf("expected one upload, got %d", len(*uploads))
	}
	got := (*uploads)[0]
	if got.method != "sendDocument" || got.filename != "main.go" || got.body != source || got.chat != "-100123" || got.topic != "77" {
		t.Fatalf("unexpected upload: %+v", got)
	}
}

func TestFileDeliveryPreviewCanBeDisabled(t *testing.T) {
	r, _ := newFileDeliveryTestRuntime(t)
	tool := newDianaFileDeliveryTool(r, MessageEvent{Platform: PlatformTelegram, Kind: EventKindPrivate, UserID: "1"}, SettingValues{}, RelationshipPolicy{})
	raw, err := tool.Run(context.Background(), map[string]any{"filename": "logo.svg", "content": `<svg xmlns="http://www.w3.org/2000/svg"/>`, "preview": false})
	if err != nil {
		t.Fatal(err)
	}
	if result := decodeFileDeliveryResult(t, raw); !result.OK || result.Preview != "" {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestFileDeliveryRejectsBadInput(t *testing.T) {
	r, uploads := newFileDeliveryTestRuntime(t)
	event := MessageEvent{Platform: PlatformTelegram, Kind: EventKindPrivate, UserID: "1"}
	cases := []struct {
		name     string
		settings SettingValues
		input    map[string]any
	}{
		{"path traversal", nil, map[string]any{"filename": "../x.py", "content": "x"}},
		{"directory", nil, map[string]any{"filename": "src/x.py", "content": "x"}},
		{"hidden", nil, map[string]any{"filename": ".bashrc", "content": "x"}},
		{"no extension", nil, map[string]any{"filename": "notes", "content": "x"}},
		{"executable", nil, map[string]any{"filename": "setup.exe", "content": "x"}},
		{"empty content", nil, map[string]any{"filename": "a.txt", "content": "  "}},
		{"too large", SettingValues{fileDeliverySettingMaxFileBytes: 4}, map[string]any{"filename": "a.txt", "content": "hello"}},
		{"owner only", SettingValues{fileDeliverySettingOwnerOnly: true}, map[string]any{"filename": "a.txt", "content": "hello"}},
	}
	for _, tc := range cases {
		tool := newDianaFileDeliveryTool(r, event, tc.settings, RelationshipPolicy{})
		raw, err := tool.Run(context.Background(), tc.input)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if result := decodeFileDeliveryResult(t, raw); result.OK {
			t.Fatalf("%s: accepted: %+v", tc.name, result)
		}
	}
	if len(*uploads) != 0 {
		t.Fatalf("rejected input reached the platform: %+v", *uploads)
	}
	owner := newDianaFileDeliveryTool(r, event, SettingValues{fileDeliverySettingOwnerOnly: true}, RelationshipPolicy{Owner: true})
	raw, _ := owner.Run(context.Background(), map[string]any{"filename": "a.txt", "content": "hello"})
	if result := decodeFileDeliveryResult(t, raw); !result.OK {
		t.Fatalf("owner rejected: %+v", result)
	}
}

func TestFileDeliveryPreviewSource(t *testing.T) {
	cases := []struct {
		name, content, format, contains string
	}{
		{"a.svg", "<svg/>", renderFormatSVG, "<svg/>"},
		{"flow.mmd", "graph TD; A-->B", renderFormatMermaid, "graph TD"},
		{"README.md", "# hi", renderFormatMarkdown, "# hi"},
		{"snake.py", "print(1)", renderFormatMarkdown, "```python\nprint(1)\n```"},
		{"Dockerfile", "FROM scratch", renderFormatMarkdown, "```dockerfile"},
		{"doc.md.txt", "x", renderFormatMarkdown, "```\nx\n```"},
		{"a.bin", "x", "", ""},
	}
	for _, tc := range cases {
		format, source := fileDeliveryPreviewSource(tc.name, tc.content)
		if format != tc.format || !strings.Contains(source, tc.contains) {
			t.Fatalf("%s: got %q %q", tc.name, format, source)
		}
	}
	// 内容自带 ``` 时围栏要更长，不能被提前闭合。
	if fence := fileDeliveryCodeFence("md", "a\n```\nb"); !strings.HasPrefix(fence, "````md\n") || !strings.HasSuffix(fence, "\n````") {
		t.Fatalf("fence not escaped: %q", fence)
	}
}

func TestFileDeliveryToolAllowedForNonOwners(t *testing.T) {
	if !(RelationshipPolicy{}).allowedAgentToolNames()[dianaFileDeliveryToolName] {
		t.Fatal("send_file should be available to non-owners; owner_only is enforced by the plugin setting")
	}
	state, ok := NewDefaultPluginManager().Get(fileDeliveryPluginID)
	if !ok || !state.Enabled {
		t.Fatalf("file delivery plugin should be registered and enabled by default: %+v", state)
	}
}
