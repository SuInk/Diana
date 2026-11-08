package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/applog"
)

func decodeRenderMediaResult(t *testing.T, raw string) dianaRenderMediaResult {
	t.Helper()
	var result dianaRenderMediaResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatalf("decode %q: %v", raw, err)
	}
	return result
}

// 参数、开关和身份都要在起浏览器之前拦下，并且把原因说成模型能照着改的话。
func TestRenderMediaRejectsBeforeRendering(t *testing.T) {
	runtime := NewRuntime(BotConfig{BotAccount: "42"}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	event := MessageEvent{Kind: EventKindGroup, GroupID: "555", UserID: "10001"}
	html := "<p>hi</p>"
	cases := []struct {
		name     string
		settings SettingValues
		input    map[string]any
		want     string
	}{
		{"unknown format", nil, map[string]any{"format": "markdown", "content": html}, "format"},
		{"unknown output", nil, map[string]any{"format": "html", "content": html, "output": "webm"}, "output"},
		{"empty", nil, map[string]any{"format": "html", "content": "  "}, "空"},
		{"placeholder", nil, map[string]any{"format": "html", "content": "file-read-needed"}, "HTML 正文"},
		{"file URL", nil, map[string]any{"format": "html", "content": "file:///workspace/page.html"}, "path"},
		{"path denied", nil, map[string]any{"format": "html", "path": "page.html"}, "本群"},
		{"too large", SettingValues{fileDeliverySettingMaxFileBytes: 4}, map[string]any{"format": "html", "content": html}, "上限"},
		{"too long", nil, map[string]any{"format": "html", "content": html, "output": "video", "duration": 11}, "duration"},
		{"setting caps duration", SettingValues{fileDeliverySettingMaxVideoSeconds: 2}, map[string]any{"format": "html", "content": html, "output": "gif", "duration": "3"}, "duration"},
		{"bad fps", nil, map[string]any{"format": "html", "content": html, "output": "video", "fps": 60}, "fps"},
		{"bad width", nil, map[string]any{"format": "html", "content": html, "width": 10}, "width"},
		{"owner only", SettingValues{fileDeliverySettingOwnerOnly: true}, map[string]any{"format": "html", "content": html}, "主人"},
		{"render disabled", SettingValues{fileDeliverySettingRenderMedia: false}, map[string]any{"format": "html", "content": html}, "没有开启"},
		{"browser disabled", nil, map[string]any{"format": "html", "content": html}, "网页渲染"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tool := newDianaRenderMediaTool(runtime, event, tc.settings, RelationshipPolicy{})
			raw, err := tool.Run(context.Background(), tc.input)
			if err != nil {
				t.Fatalf("工具不该抛错：%v", err)
			}
			if result := decodeRenderMediaResult(t, raw); result.OK || !strings.Contains(result.Message, tc.want) {
				t.Fatalf("got %+v, want message containing %q", result, tc.want)
			}
		})
	}
}

func TestRenderMediaFailureIsLogged(t *testing.T) {
	runtime := NewRuntime(BotConfig{BotAccount: "42"}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	logs := &captureRelationLogs{}
	runtime.SetAppLogWriter(logs)
	tool := newDianaRenderMediaTool(runtime, MessageEvent{Kind: EventKindGroup, GroupID: "555", UserID: "1"}, nil, RelationshipPolicy{})
	if _, err := tool.Run(context.Background(), map[string]any{"format": "html", "content": "<p>x</p>"}); err != nil {
		t.Fatal(err)
	}
	entry, ok := logs.find(dianaRenderMediaToolName)
	if !ok || entry.Level != applog.LevelError {
		t.Fatalf("失败应当记成 error：%#v", logs.entries)
	}
}

func TestRenderMediaDefaults(t *testing.T) {
	tool := newDianaRenderMediaTool(nil, MessageEvent{}, nil, RelationshipPolicy{})
	spec, err := tool.parse(map[string]any{"format": "HTML", "content": "<p>x</p>", "output": "gif"})
	if err != nil {
		t.Fatal(err)
	}
	if spec.width != 640 || spec.height != 360 || spec.fps != defaultRenderMediaGIFFPS || spec.frames != defaultRenderMediaSeconds*defaultRenderMediaGIFFPS || spec.sized {
		t.Fatalf("unexpected gif defaults: %+v", spec)
	}
	spec, err = tool.parse(map[string]any{"format": "svg", "content": "<svg/>", "output": "video", "duration": 1.5, "fps": 20, "width": 400})
	if err != nil {
		t.Fatal(err)
	}
	if spec.frames != 30 || spec.width != 400 || spec.height != 720 || !spec.sized {
		t.Fatalf("unexpected video spec: %+v", spec)
	}
	spec, err = tool.parse(map[string]any{"format": "html", "content": "<p>x</p>"})
	if err != nil || spec.output != renderMediaOutputImage || spec.height != 0 || spec.frames != 0 {
		t.Fatalf("image should default to full-page height: %+v %v", spec, err)
	}
}

// SVG 要走 render 同一套净化：脚本和事件属性进不了带脚本执行能力的页面。
func TestRenderMediaSVGIsSanitizedAndFitted(t *testing.T) {
	request, err := buildRenderMediaRequest(context.Background(), renderMediaSpec{
		format: renderMediaFormatSVG, output: renderMediaOutputVideo, width: 1280, height: 720,
		content: `<svg xmlns="http://www.w3.org/2000/svg" onload="alert(1)"><script>alert(2)</script><circle r="5"><animate attributeName="r" to="9" dur="1s"/></circle></svg>`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(request.HTML, "alert") {
		t.Fatalf("svg script survived: %s", request.HTML)
	}
	if !strings.Contains(request.HTML, "<animate") {
		t.Fatalf("SMIL animation should be kept: %s", request.HTML)
	}
	if request.FitSelector != "#render-root" || request.Height != 0 {
		t.Fatalf("svg without explicit size should fit its box: %+v", request)
	}
	if _, err := buildRenderMediaRequest(context.Background(), renderMediaSpec{format: renderMediaFormatSVG, content: "<div/>"}); err == nil {
		t.Fatal("non-svg content accepted")
	}
}

func TestRenderMediaFFmpegArgs(t *testing.T) {
	mp4 := strings.Join(renderMediaMP4Args(24, "in/%05d.jpg", "out.mp4"), " ")
	for _, want := range []string{"-framerate 24", "libx264", "yuv420p", "+faststart", "pad=ceil(iw/2)*2:ceil(ih/2)*2"} {
		if !strings.Contains(mp4, want) {
			t.Fatalf("mp4 args missing %q: %s", want, mp4)
		}
	}
	gif := strings.Join(renderMediaGIFArgs(12, "in/%05d.png", "out.gif"), " ")
	for _, want := range []string{"-framerate 12", "palettegen", "paletteuse", "-loop 0"} {
		if !strings.Contains(gif, want) {
			t.Fatalf("gif args missing %q: %s", want, gif)
		}
	}
}

func TestRenderMediaToolAllowedForNonOwners(t *testing.T) {
	if !(RelationshipPolicy{}).allowedAgentToolNames()[dianaRenderMediaToolName] {
		t.Fatal("render_media should be available to non-owners; owner_only is enforced by the plugin setting")
	}
}
