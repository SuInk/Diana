// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/SuInk/diana/model/agent"
	"github.com/SuInk/diana/model/applog"
)

// 把表格、流程图这类纯文本讲不清的内容渲染成图片发出去。
//
// 做成工具而不是「回复里出现表格就自动转图」：短表格用文字说反而更快，
// 值不值得出图是看语境的判断，那是模型的事，不是一条格式规则能定的。
const dianaRenderToolName = "render"

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

// 「五子棋盘用 svg」来自线上：mermaid/Markdown 画棋盘坐标会错位。出图要起浏览器，
// 一两句话的内容别用它。
func (t *dianaRenderTool) Description() string {
	return `把表格、流程图、结构图或 SVG 渲染成一张图片发到当前会话。` +
		`适合多行多列表格、流程/时序/状态图、树形结构、坐标棋盘等要精确可复现的画面，五子棋盘用 svg；一两句话说得清的别用。` +
		`图由运行时发送，调用后一句话交代，不复述图里的内容。`
}

func (t *dianaRenderTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"format": map[string]any{
				"type": "string",
				"enum": []string{renderFormatMarkdown, renderFormatMermaid, renderFormatSVG},
			},
			"content": map[string]any{
				"type":        "string",
				"description": "GitHub 风格 Markdown、mermaid 源码或以 <svg> 开头的 SVG",
			},
			"title": map[string]any{
				"type":        "string",
				"description": "图片顶部标题",
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

	page, fontFiles, err := prepareRenderFontHTML(ctx, page)
	if err != nil {
		return t.fail(ctx, format, "字体准备失败："+firstLineOf(err.Error()), err.Error())
	}
	cfg := t.runtime.effectiveConfigForEvent(t.event)
	request := agent.ScreenshotRequest{
		HTML:         page,
		WaitForFonts: len(fontFiles) > 0,
		FontFiles:    fontFiles,
		Width:        renderImageWidth,
		Height:       renderImageMaxHeight,
		Timeout:      time.Duration(cfg.AgentBrowserTimeoutMS) * time.Millisecond,
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

// renderContentPNG 用 render 工具同一套页面构造、净化和截图链把内容画成 PNG。
// 调用方负责先确认「网页渲染」插件已启用。
func (r *Runtime) renderContentPNG(ctx context.Context, event MessageEvent, format, content, title string) ([]byte, error) {
	page, err := buildRenderPage(format, content, title)
	if err != nil {
		return nil, fmt.Errorf("%s", renderContentErrorMessage(format, err))
	}
	page, fontFiles, err := prepareRenderFontHTML(ctx, page)
	if err != nil {
		return nil, fmt.Errorf("字体准备失败：%s", firstLineOf(err.Error()))
	}
	cfg := r.effectiveConfigForEvent(event)
	request := agent.ScreenshotRequest{
		HTML:         page,
		WaitForFonts: len(fontFiles) > 0,
		FontFiles:    fontFiles,
		Width:        renderImageWidth,
		Height:       renderImageMaxHeight,
		Timeout:      time.Duration(cfg.AgentBrowserTimeoutMS) * time.Millisecond,
	}
	if format == renderFormatMermaid {
		request.VirtualTimeBudget = renderMermaidTimeBudget
	}
	shot, err := agent.CaptureHTMLScreenshot(ctx, request)
	if err != nil {
		return nil, fmt.Errorf("渲染失败：%s", firstLineOf(err.Error()))
	}
	return trimRenderScreenshot(shot), nil
}

// sendPNGImage 先落盘成临时文件再投递：Telegram 侧 data URL 只会被当成普通
// 字符串塞进 JSON，真实 Bot API 不收，必须走 multipart 本地文件上传。
func (r *Runtime) sendPNGImage(ctx context.Context, event MessageEvent, png []byte) error {
	dir, err := os.MkdirTemp("", "diana-render-image-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	pngPath := filepath.Join(dir, "image.png")
	if err := os.WriteFile(pngPath, png, 0600); err != nil {
		return err
	}
	if err := r.sendOutgoing(ctx, event, routeOutgoingToEvent(event, OutgoingMessage{ImageURLs: []string{pngPath}})); err != nil {
		return fmt.Errorf("发送图片失败：%w", err)
	}
	return nil
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
		Action:   "render",
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
