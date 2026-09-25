package assistant

import (
	"context"
	"encoding/base64"
	"fmt"
	"mime"
	"strings"

	"github.com/SuInk/diana/model/agent"
)

// mcp_media 把 MCP 工具返回的图片、音频和文件发到当前会话。MCP 的结果里只留
// media_id，真正的字节在 agent 的暂存区里（见 model/agent/mcp_media.go）。
//
// 它不碰本机文件系统：能发的只有 MCP 这一轮交出来、已经暂存下的东西，所以群成员
// 用得了自己能调的那些 MCP，也就用得了它，和 send_attachment 不是一回事。
const dianaMCPMediaToolName = "mcp_media"

type dianaMCPMediaTool struct {
	runtime *Runtime
	event   MessageEvent
}

func newDianaMCPMediaTool(r *Runtime, event MessageEvent) *dianaMCPMediaTool {
	return &dianaMCPMediaTool{runtime: r, event: event}
}

func (t *dianaMCPMediaTool) Name() string { return dianaMCPMediaToolName }

func (t *dianaMCPMediaTool) Description() string {
	return "把 MCP 工具返回的图片、音频或文件发到当前会话。MCP 结果里出现 media_id=mcpm_… 时用它，media_id 原样填进来。" +
		"as 省略时图片按图片发、音频和其他内容按文件发；as=file 强制按原文件发。只认 MCP 返回的 media_id（暂存 30 分钟），不能发本机任意文件。" +
		"描述画面前先看结果里附上的图，没附图的别编内容。"
}

func (t *dianaMCPMediaTool) InputSchema() map[string]any {
	return toolObjectSchema([]string{"media_id"}, map[string]any{
		"media_id": toolStringParam("MCP 结果里给出的 media_id，形如 mcpm_ 加 24 位十六进制。"),
		"as":       toolEnumParam("auto 按类型决定；image 按图片发；file 按原文件发。省略时为 auto。", "auto", "image", "file"),
	})
}

func (t *dianaMCPMediaTool) Run(ctx context.Context, input map[string]any) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	media, ok := agent.LookupMCPMedia(configToolString(input, "media_id"))
	if !ok {
		return "", fmt.Errorf("media_id 无效或已过期（只暂存 30 分钟）；重新调用对应的 MCP 工具拿新的 media_id")
	}
	as := strings.TrimSpace(configToolString(input, "as"))
	switch as {
	case "", "auto":
		as = "file"
		if media.Kind == agent.MCPMediaImage {
			as = "image"
		}
	case "image", "file":
	default:
		return "", fmt.Errorf("as 只能是 auto、image 或 file")
	}
	if as == "image" {
		if media.Kind != agent.MCPMediaImage {
			return "", fmt.Errorf("这个 media_id 是 %s（%s），不是图片；用 as=file 按文件发", media.Kind, media.MIMEType)
		}
		return t.sendImage(ctx, media)
	}
	if err := t.runtime.sendFileAttachment(ctx, t.event, mcpMediaFileName(media), media.Data); err != nil {
		return "", err
	}
	return `{"status":"sent","message":"文件已发送到当前会话，不要重复发送。"}`, nil
}

func (t *dianaMCPMediaTool) sendImage(ctx context.Context, media agent.MCPMedia) (string, error) {
	mimeType := strings.ToLower(media.MIMEType)
	if mimeType == "image/svg+xml" {
		// 和 send_attachment 一样：SVG 不能直接当图片发，栅格化归「网页渲染」插件管。
		if !t.runtime.sandboxedBrowserEnabled(t.event) {
			return "", fmt.Errorf("svg 作为图片发送需要先栅格化成 PNG，但「网页渲染」插件没有启用；可以改用 as=file 发原始 SVG")
		}
		png, err := t.runtime.renderContentPNG(ctx, t.event, renderFormatSVG, string(media.Data), "")
		if err != nil {
			return "", err
		}
		if err := t.runtime.sendPNGImage(ctx, t.event, png); err != nil {
			return "", err
		}
		return `{"status":"sent","message":"SVG 已栅格化为 PNG 并发送到当前会话，不要重复发送。"}`, nil
	}
	switch mimeType {
	case "image/png", "image/jpeg", "image/gif", "image/webp", "image/bmp":
	default:
		return "", fmt.Errorf("%s 不能直接当图片发；用 as=file 按文件发", media.MIMEType)
	}
	dataURL := "data:" + mimeType + ";base64," + base64.StdEncoding.EncodeToString(media.Data)
	shared, paths, err := t.runtime.shareAgentImages(ctx, t.event.Platform, []string{dataURL})
	if err != nil {
		return "", err
	}
	defer func() {
		for _, path := range paths {
			cleanupLocalMediaFile(path)
		}
	}()
	if err := t.runtime.sendOutgoing(ctx, t.event, routeOutgoingToEvent(t.event, OutgoingMessage{ImageURLs: shared})); err != nil {
		return "", fmt.Errorf("发送图片失败：%w", err)
	}
	return `{"status":"sent","message":"图片已发送到当前会话，不要重复发送。"}`, nil
}

// mcpMediaFileName 取服务给的文件名；没有时按类型起一个，平台至少要知道扩展名。
func mcpMediaFileName(media agent.MCPMedia) string {
	if name := strings.TrimSpace(media.Name); name != "" {
		return name
	}
	ext := ""
	if exts, err := mime.ExtensionsByType(media.MIMEType); err == nil && len(exts) > 0 {
		ext = exts[0]
	}
	return "mcp-" + string(media.Kind) + ext
}
