// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/SuInk/diana/model/agent"
	"github.com/SuInk/diana/model/applog"
)

// 把表格、流程图这类纯文本讲不清的内容渲染成图片发出去。
//
// 做成工具而不是「回复里出现表格就自动转图」：短表格用文字说反而更快，
// 值不值得出图是看语境的判断，那是模型的事，不是一条格式规则能定的。
const dianaRenderToolName = "diana.render"

const (
	renderImageWidth      = 1000
	renderImageMaxHeight  = 2600
	renderImageMaxContent = 12000
	// mermaid 是异步画的，拍照前要留给脚本一段虚拟时间。Markdown 和 SVG
	// 加载完就定型，用不着。
	renderMermaidTimeBudget = 6 * time.Second
)

type dianaRenderTool struct {
	runtime *Runtime
	event   MessageEvent
}

func newDianaRenderTool(runtime *Runtime, event MessageEvent) agent.Tool {
	return &dianaRenderTool{runtime: runtime, event: event}
}

func (t *dianaRenderTool) Name() string { return dianaRenderToolName }

func (t *dianaRenderTool) Description() string {
	return `把内容渲染成一张图片直接发到当前会话：聊天窗口不渲染 Markdown，表格会散成一堆竖线，流程和结构只能用文字硬描。` +
		`适合多行多列的表格、对比清单、流程图、时序图、状态机、树形结构、坐标棋盘，以及任何要求位置、文字、数量确定且可复现的画面；五子棋盘优先使用 svg 精确绘制。` +
		`format 选 markdown（表格、清单、代码，用 GitHub 风格 Markdown）、mermaid（流程图/时序图/状态图等，写 mermaid 源码）或 svg（自己画的矢量图，必须以 <svg> 开头）。` +
		`一两句话说得清的东西别用它——出图要起一次浏览器，比直接说话慢得多，读者也不方便复制。` +
		`图由运行时发送，你在调用后用一句话交代就行，不要复述图里的内容。`
}

func (t *dianaRenderTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"format": map[string]any{
				"type":        "string",
				"enum":        []string{renderFormatMarkdown, renderFormatMermaid, renderFormatSVG},
				"description": "内容格式",
			},
			"content": map[string]any{
				"type":        "string",
				"description": "要渲染的内容：Markdown 正文、mermaid 源码，或完整的 <svg> 片段",
			},
			"title": map[string]any{
				"type":        "string",
				"description": "可选的图片标题，画在最上面一行",
			},
		},
		"required": []string{"format", "content"},
	}
}

type dianaRenderResult struct {
	OK      bool   `json:"ok"`
	Message string `json:"message"`
	Format  string `json:"format,omitempty"`
}

func (t *dianaRenderTool) Run(ctx context.Context, input map[string]any) (string, error) {
	format := strings.ToLower(strings.TrimSpace(configToolString(input, "format")))
	content := configToolString(input, "content")
	title := configToolString(input, "title")

	if !renderFormatValid(format) {
		return t.fail(ctx, format, "format 只支持 markdown、mermaid 或 svg。", "")
	}
	if strings.TrimSpace(content) == "" {
		return t.fail(ctx, format, "content 是空的，没有东西可以画。", "")
	}
	if runes := []rune(content); len(runes) > renderImageMaxContent {
		// 截断了再画只会得到一张半截图，不如直接说清楚让模型自己拆。
		return t.fail(ctx, format, fmt.Sprintf("内容超过 %d 字，画不下；拆成几张或者精简一下。", renderImageMaxContent), "")
	}
	// 出图要起一次无头浏览器，浏览器归「网页渲染」插件管。那个插件停用就是不许
	// 起浏览器，这里不能绕过去自己起一个。
	if !t.runtime.sandboxedBrowserEnabled(t.event) {
		return t.fail(ctx, format, "「网页渲染」插件没有启用，画不了图。", "")
	}

	page, err := buildRenderPage(format, content, title)
	if err != nil {
		return t.fail(ctx, format, renderContentErrorMessage(format, err), err.Error())
	}

	cfg := t.runtime.effectiveConfigForEvent(t.event)
	request := agent.ScreenshotRequest{
		HTML:    page,
		Width:   renderImageWidth,
		Height:  renderImageMaxHeight,
		Timeout: time.Duration(cfg.AgentBrowserTimeoutMS) * time.Millisecond,
	}
	if format == renderFormatMermaid {
		request.VirtualTimeBudget = renderMermaidTimeBudget
	}
	shot, err := agent.CaptureHTMLScreenshot(ctx, request)
	if err != nil {
		return t.fail(ctx, format, "渲染失败："+firstLineOf(err.Error()), err.Error())
	}

	// 内联成 data URI 发出去：出站会把它转成 base64://，不用落盘，也就不用管清理。
	image := "data:image/png;base64," + base64.StdEncoding.EncodeToString(trimRenderScreenshot(shot))
	if err := t.runtime.sendOutgoing(ctx, t.event, routeOutgoingToEvent(t.event, OutgoingMessage{ImageURLs: []string{image}})); err != nil {
		return "", fmt.Errorf("发送图片失败：%w", err)
	}
	result := dianaRenderResult{OK: true, Message: "图片已经发到会话里了。", Format: format}
	t.record(ctx, result, "")
	return marshalRenderResult(result), nil
}

// renderContentErrorMessage 把内容层面的错误说成模型能照着改的话。
//
// buildRenderPage 的错误分两类：格式不对（模型写错了，说清楚哪里错）和
// 净化拒绝（写了不该写的东西）。两类都不该只回一句「渲染失败」——模型看到
// 那句话只会原样再试一次。
func renderContentErrorMessage(format string, err error) string {
	detail := firstLineOf(err.Error())
	detail = strings.TrimPrefix(detail, "render: ")
	if format == renderFormatSVG {
		return "SVG 不合要求：" + detail + "（只接受静态图形，不能带脚本、事件属性或外链）"
	}
	return "内容不合要求：" + detail
}

func (t *dianaRenderTool) fail(ctx context.Context, format, message, detail string) (string, error) {
	result := dianaRenderResult{Message: message, Format: format}
	t.record(ctx, result, detail)
	return marshalRenderResult(result), nil
}

func (t *dianaRenderTool) record(ctx context.Context, result dianaRenderResult, detail string) {
	writer := t.runtime.appLogWriter()
	if writer == nil {
		return
	}
	kind, level := applog.KindOperation, applog.LevelInfo
	if !result.OK {
		kind, level = applog.KindError, applog.LevelError
	}
	// 审计不该被上游取消卡住，用自己的短超时。
	logCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	_ = writer.AppendLog(logCtx, applog.Entry{
		Kind:     kind,
		Level:    level,
		Action:   "assistant.render",
		Message:  result.Message,
		Detail:   detail,
		Actor:    oneBotEventActor(t.event),
		Target:   strings.TrimSpace(firstNonEmpty(t.event.GroupID, t.event.UserID)),
		Metadata: map[string]any{"format": result.Format},
	})
}

func marshalRenderResult(result dianaRenderResult) string {
	encoded, err := json.Marshal(result)
	if err != nil {
		return `{"ok":false,"message":"渲染结果无法编码"}`
	}
	return string(encoded)
}
