// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"html"
	"regexp"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
)

// 聊天平台不渲染 Markdown：表格发出去是一坨竖线，流程图只能用文字硬描。
// 这里把内容渲染成一张自包含的 HTML，交给无头浏览器截图，再当图片发出去。
//
// 「自包含」是硬要求：截图沙箱屏蔽了 localhost 和内网域名，也不该让模型写的
// 内容去外面取任何东西。样式内联、脚本内嵌，页面里不能有一个外链。

//go:embed render_assets/mermaid.min.js
var mermaidBundle string

// 渲染格式。模型只能从这三种里选，不能直接给 HTML——那等于把一个任意 HTML
// 注入点交给模型，即使沙箱兜着也没必要开这个口子。
const (
	renderFormatMarkdown = "markdown"
	renderFormatMermaid  = "mermaid"
	renderFormatSVG      = "svg"
)

func renderFormatValid(format string) bool {
	switch format {
	case renderFormatMarkdown, renderFormatMermaid, renderFormatSVG:
		return true
	}
	return false
}

// buildRenderPage 按格式生成整页 HTML。
func buildRenderPage(format, content, title string) (string, error) {
	body, err := renderPageBody(format, content)
	if err != nil {
		return "", err
	}
	heading := ""
	if trimmed := strings.TrimSpace(title); trimmed != "" {
		heading = `<h1 class="render-title">` + html.EscapeString(trimmed) + `</h1>`
	}
	script := ""
	rootClass := "render-root"
	if format == renderFormatMermaid {
		script = "<script>" + mermaidBundle + "</script>\n<script>" + mermaidBootstrap + "</script>"
		// mermaid 是先读容器宽度再排版的。.render-root 是 inline-block，
		// 排版之前它只有 min-width 那么宽，甘特图这类横向铺开的图会被压成
		// 一团重叠的标签。给 mermaid 页面一个固定宽度的画布，让它有地方铺。
		rootClass += " render-root-mermaid"
	}
	return fmt.Sprintf(renderPageTemplate, renderPageCSS, rootClass, heading, body, script), nil
}

func renderPageBody(format, content string) (string, error) {
	content = strings.TrimSpace(content)
	if content == "" {
		return "", fmt.Errorf("render: 内容是空的")
	}
	switch format {
	case renderFormatMarkdown:
		return markdownToRenderHTML(content)
	case renderFormatMermaid:
		// mermaid 源码原样进 <pre>，由脚本自己解析。转义是必须的：源码里的
		// 尖括号（比如 A-->|是|B 里的箭头）不转义会被当成标签。
		return `<pre class="mermaid">` + html.EscapeString(content) + `</pre>`, nil
	case renderFormatSVG:
		return sanitizeRenderSVG(content)
	}
	return "", fmt.Errorf("render: 不支持的格式 %q", format)
}

// markdownToRenderHTML 把 Markdown 转成 HTML。
//
// 刻意不开 goldmark 的 WithUnsafe：不开的时候原始 HTML 会被当成文本转义掉，
// 模型想借 Markdown 里夹一段 <script> 也只会被原样显示出来。
func markdownToRenderHTML(source string) (string, error) {
	converter := goldmark.New(goldmark.WithExtensions(
		extension.GFM,
		extension.Typographer,
	))
	var buffer bytes.Buffer
	if err := converter.Convert([]byte(source), &buffer); err != nil {
		return "", fmt.Errorf("render: Markdown 解析失败：%w", err)
	}
	return buffer.String(), nil
}

var (
	svgRootPattern     = regexp.MustCompile(`(?is)^\s*<svg[\s>]`)
	svgCommentPattern  = regexp.MustCompile(`(?s)<!--.*?-->`)
	svgScriptPattern   = regexp.MustCompile(`(?is)<\s*script\b.*?<\s*/\s*script\s*>|<\s*script\b[^>]*/?>`)
	svgForeignPattern  = regexp.MustCompile(`(?is)<\s*foreignObject\b.*?<\s*/\s*foreignObject\s*>|<\s*foreignObject\b[^>]*/?>`)
	svgHandlerPattern  = regexp.MustCompile(`(?is)\son[a-z]+\s*=\s*("[^"]*"|'[^']*'|[^\s>]+)`)
	svgRemoteRefPatter = regexp.MustCompile(`(?is)\s(?:xlink:)?href\s*=\s*("(?:\s*(?:https?:|//|data:text/html))[^"]*"|'(?:\s*(?:https?:|//|data:text/html))[^']*')`)
)

// sanitizeRenderSVG 只接受 <svg> 开头的片段，并砍掉能执行脚本或往外取东西的部分。
//
// 这段内容是模型写的，最终会进一个浏览器。沙箱已经屏蔽了内网和 localhost，
// 但「沙箱兜着」不是不净化的理由：脚本能读到页面里别的内容，外链能把内容
// 带出去，两件事都不该发生在一张只是用来画图的页面里。
//
// 用正则而不是解析器是有意的：这里不需要理解 SVG，只需要在几类危险构造上
// 一律拒绝或删除。宁可把合法但少见的写法也删掉，也不要漏掉一种绕过写法。
func sanitizeRenderSVG(source string) (string, error) {
	if !svgRootPattern.MatchString(source) {
		return "", fmt.Errorf("render: svg 必须以 <svg> 开头")
	}
	cleaned := svgCommentPattern.ReplaceAllString(source, "")
	cleaned = svgScriptPattern.ReplaceAllString(cleaned, "")
	// foreignObject 里可以塞任意 HTML，等于绕开上面所有针对 SVG 的限制。
	cleaned = svgForeignPattern.ReplaceAllString(cleaned, "")
	cleaned = svgHandlerPattern.ReplaceAllString(cleaned, " ")
	cleaned = svgRemoteRefPatter.ReplaceAllString(cleaned, " ")
	if strings.Contains(strings.ToLower(cleaned), "javascript:") {
		return "", fmt.Errorf("render: svg 里不允许 javascript: 链接")
	}
	return cleaned, nil
}

// mermaidBootstrap 用 strict 安全级别初始化：禁掉 click 指令，标签里的 HTML
// 也会被转义，模型写进图里的文字不会变成可执行的东西。
//
// 甘特图在没有显式 tickInterval 时会按天画横轴。四周左右的排期会因此塞进
// 近 30 个完整日期，哪怕画布够宽也一定重叠。渲染完成后按实际文字宽度隐藏
// 相撞的标签；只隐藏文字、不删网格线，也不改模型提供的日期和任务数据。
var mermaidBootstrap = `mermaid.initialize(` + mustJSON(map[string]any{
	"startOnLoad":   false,
	"securityLevel": "strict",
	"theme":         "neutral",
	"fontFamily":    renderFontStack,
}) + `);
function thinOverlappingGanttTicks() {
  document.querySelectorAll('.mermaid svg .grid').forEach(function (grid) {
    var labels = Array.from(grid.querySelectorAll('.tick text')).filter(function (label) {
      return label.getClientRects().length > 0;
    });
    if (labels.length < 2) return;

    var gap = 8;
    var lastRight = -Infinity;
    labels.forEach(function (label, index) {
      var box = label.getBoundingClientRect();
      var isLast = index === labels.length - 1;
      if (box.left < lastRight + gap && !isLast) {
        label.style.display = 'none';
        return;
      }
      if (isLast && box.left < lastRight + gap) {
        for (var previous = index - 1; previous >= 0; previous--) {
          if (labels[previous].style.display !== 'none') {
            labels[previous].style.display = 'none';
            break;
          }
        }
      }
      lastRight = box.right;
    });
  });
}
mermaid.run().then(thinOverlappingGanttTicks);`

func mustJSON(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		// 输入是本文件里的常量字面量，编不出 JSON 只可能是改坏了。
		panic("render: mermaid 配置无法编码：" + err.Error())
	}
	return string(encoded)
}

// renderFontStack 里挨个列出常见的中文字体：截图沙箱里没有网络，网页字体取不到，
// 只能指望这台机器本地装了其中一个。列表和关系图找字体时的偏好一致。
const renderFontStack = `"PingFang SC","Hiragino Sans GB","Microsoft YaHei","Source Han Sans SC","Noto Sans CJK SC","WenQuanYi Micro Hei",-apple-system,"Segoe UI",Roboto,sans-serif`

const renderPageTemplate = `<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8"><style>%s</style></head>
<body><div class="%s" id="render-root">%s%s</div>%s</body></html>`

const renderPageCSS = `
:root { color-scheme: light; }
* { box-sizing: border-box; }
body {
  margin: 0;
  background: #ffffff;
  color: #1f2328;
  font-family: ` + renderFontStack + `;
  font-size: 15px;
  line-height: 1.65;
}
.render-root { display: inline-block; min-width: 320px; padding: 28px 32px; }
/* 见 buildRenderPage：mermaid 要先有宽度才能排版，不能等 inline-block 收缩。 */
.render-root-mermaid { display: block; width: 1000px; }
.render-root-mermaid .mermaid { width: 100%; }
.render-root-mermaid svg { max-width: 100%; height: auto; }
.render-title { margin: 0 0 18px; font-size: 20px; font-weight: 600; }
h1, h2, h3, h4 { margin: 20px 0 10px; font-weight: 600; line-height: 1.3; }
h1 { font-size: 22px; } h2 { font-size: 19px; } h3 { font-size: 17px; } h4 { font-size: 15px; }
p { margin: 0 0 10px; }
ul, ol { margin: 0 0 10px; padding-left: 22px; }
li { margin: 3px 0; }
table { border-collapse: collapse; margin: 6px 0 14px; }
th, td { border: 1px solid #d8dde3; padding: 7px 13px; text-align: left; vertical-align: top; }
th { background: #f3f5f7; font-weight: 600; white-space: nowrap; }
tbody tr:nth-child(even) { background: #fafbfc; }
code { background: #f3f5f7; padding: 1px 5px; border-radius: 4px; font-size: 13px;
       font-family: "SFMono-Regular",Menlo,Consolas,"Liberation Mono",monospace; }
pre { background: #f6f8fa; padding: 13px 15px; border-radius: 7px; overflow: visible;
      margin: 6px 0 14px; border: 1px solid #e6e9ed; }
pre code { background: none; padding: 0; }
pre.mermaid { background: none; border: none; padding: 0; }
blockquote { margin: 6px 0 14px; padding: 2px 0 2px 14px; border-left: 3px solid #d8dde3; color: #5b6570; }
hr { border: none; border-top: 1px solid #e6e9ed; margin: 16px 0; }
a { color: #1f2328; text-decoration: underline; }
img, svg { max-width: 1400px; height: auto; }
`
