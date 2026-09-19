package assistant

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/SuInk/diana/model/agent"
	"github.com/SuInk/diana/model/llm"
)

const localAttachmentMaxBytes = 32 << 20

// 发送前的类型白名单：不在表内的扩展名直接拒绝，避免把可执行文件或
// 平台接口不认识的格式丢给 OneBot / Telegram 报错。mode=image 额外放行
// SVG——SVG 不能直接当图片发，走 sendSVGImage 栅格化成 PNG。
var localAttachmentImageExts = map[string]struct{}{
	"png": {}, "jpg": {}, "jpeg": {}, "gif": {}, "webp": {}, "bmp": {}, "svg": {},
}

var localAttachmentFileExts = map[string]struct{}{
	// 文本、数据与办公文档
	"txt": {}, "md": {}, "markdown": {}, "json": {}, "csv": {}, "tsv": {}, "xml": {},
	"yaml": {}, "yml": {}, "toml": {}, "ini": {}, "log": {}, "pdf": {},
	"doc": {}, "docx": {}, "xls": {}, "xlsx": {}, "ppt": {}, "pptx": {},
	// 图片
	"png": {}, "jpg": {}, "jpeg": {}, "gif": {}, "webp": {}, "bmp": {}, "svg": {},
	"tif": {}, "tiff": {},
	// 压缩包与音视频
	"zip": {}, "gz": {}, "tar": {}, "7z": {}, "rar": {},
	"mp3": {}, "wav": {}, "ogg": {}, "mp4": {}, "webm": {}, "mov": {},
}

type dianaLocalAttachmentTool struct {
	runtime *Runtime
	event   MessageEvent
	view    bool
	mu      sync.Mutex
	parts   []llm.ContentPart
}

func (t *dianaLocalAttachmentTool) Name() string {
	if t.view {
		return "view_image"
	}
	return "send_attachment"
}

func (t *dianaLocalAttachmentTool) Description() string {
	if t.view {
		return "读取 Agent 工作目录内的真实图片，把画面作为附件交给下一轮模型。用于查看 run_command 下载或生成的图片；不能把文件名当作画面证据。path 必须是工作目录相对路径。读取成功不代表已发送到聊天。"
	}
	return "把 Agent 工作目录内的文件发送到当前会话，支持 Telegram 和 OneBot。mode=image 作为图片发送（位图直发；svg 会经「网页渲染」插件栅格化成 PNG 再发，插件没启用时请改用 mode=file），mode=file 作为原文件附件发送（常见文档、压缩包、音视频，可执行文件一律拒绝）。命令下载成功不等于用户收到文件；需要交付时调用本工具。描述图片内容前先用 view_image 查看。不会执行文件；仅主人可用。"
}

func (t *dianaLocalAttachmentTool) InputSchema() map[string]any {
	properties := map[string]any{"path": toolStringParam("Agent 工作目录相对路径，例如 downloads/photo.jpg。发送前会做类型与大小（32MB）校验。")}
	required := []string{"path"}
	if !t.view {
		properties["mode"] = toolEnumParam("图片或原始文件附件。", "image", "file")
		required = append(required, "mode")
	}
	return toolObjectSchema(required, properties)
}

func (t *dianaLocalAttachmentTool) Run(ctx context.Context, input map[string]any) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.parts = nil
	if err := ctx.Err(); err != nil {
		return "", err
	}
	path := configToolString(input, "path")
	mode := configToolString(input, "mode")
	if !t.view && mode != "image" && mode != "file" {
		return "", fmt.Errorf("mode must be image or file")
	}
	// 类型前置校验：先拦扩展名，再读文件、再走平台接口。
	if !t.view {
		ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(path), "."))
		if mode == "image" {
			if _, ok := localAttachmentImageExts[ext]; !ok {
				return "", fmt.Errorf("mode=image 只支持位图（png/jpg/gif/webp/bmp）和 svg，收到 .%s；文本或文档请用 mode=file", ext)
			}
		} else if _, ok := localAttachmentFileExts[ext]; !ok {
			return "", fmt.Errorf("不支持的附件类型 .%s；可执行文件不会发送，其他格式请先打成 zip 再试", ext)
		}
	}
	data, err := agent.ReadWorkspaceFile(AgentWorkspaceDir(), path, localAttachmentMaxBytes)
	if err != nil {
		return "", err
	}
	if !t.view && mode == "image" && strings.EqualFold(filepath.Ext(path), ".svg") {
		// SVG 不走通用图片链：Telegram sendPhoto 不收 SVG，OneBot 图片段
		// 对 SVG 的渲染取决于桥端实现。栅格化成 PNG 是各平台都稳的路径。
		return t.sendSVGImage(ctx, data)
	}
	if t.view || mode == "image" {
		images, err := normalizeLLMImageParts(data, http.DetectContentType(data))
		if err != nil {
			return "", err
		}
		if t.view {
			for _, image := range images {
				t.parts = append(t.parts, llm.ContentPart{Type: llm.ContentPartImageURL, ImageURL: image, Detail: "high"})
			}
			return `{"status":"loaded","message":"真实画面已附加到工具结果；尚未发送到聊天。"}`, nil
		}
		shared, paths, err := t.runtime.shareAgentImages(ctx, t.event.Platform, images[:1])
		if err != nil {
			return "", err
		}
		defer func() {
			for _, p := range paths {
				cleanupLocalMediaFile(p)
			}
		}()
		err = t.runtime.sendOutgoing(ctx, t.event, OutgoingMessage{ImageURLs: shared})
		if err != nil {
			return "", err
		}
	} else {
		platform := NormalizePlatformID(t.event.Platform)
		if platform != PlatformTelegram && !IsOneBotPlatform(platform) {
			return "", fmt.Errorf("file attachment sending is unavailable for this platform")
		}
		// Send a private snapshot, not a path the command process can change during upload.
		dir, err := os.MkdirTemp("", "diana-attachment-*")
		if err != nil {
			return "", err
		}
		defer os.RemoveAll(dir)
		name := filepath.Base(path)
		snapshot := filepath.Join(dir, name)
		if err = os.WriteFile(snapshot, data, 0600); err != nil {
			return "", err
		}
		if platform == PlatformTelegram {
			err = t.runtime.sendOutgoing(ctx, t.event, OutgoingMessage{Segments: []MessageSegment{{Type: "file", Data: map[string]string{"file": snapshot, "name": name}}}})
		} else {
			err = t.runtime.uploadResolverVideoFile(ctx, t.event, resolverVideoUpload{Path: snapshot, Name: name})
		}
		if err != nil {
			return "", err
		}
	}
	return `{"status":"sent","message":"附件已发送到当前会话。"}`, nil
}

// sendSVGImage 把 SVG 文件经无头浏览器栅格化成 PNG 发到当前会话。
// 复用 render 工具的页面构造与截图链，净化规则保持一致；截图前不跟随
// 文件系统状态——数据已经整体读进内存，命令进程改不了它。
//
// 产物先落盘成临时文件再投递：Telegram 侧 data URL 只会被当成普通
// 字符串塞进 JSON，真实 Bot API 不收，必须走 multipart 本地文件上传。
func (t *dianaLocalAttachmentTool) sendSVGImage(ctx context.Context, data []byte) (string, error) {
	if !t.runtime.sandboxedBrowserEnabled(t.event) {
		return "", fmt.Errorf("svg 作为图片发送需要先栅格化成 PNG，但「网页渲染」插件没有启用；可以改用 mode=file 直接发送原始 SVG 文件")
	}
	page, err := buildRenderPage(renderFormatSVG, string(data), "")
	if err != nil {
		return "", fmt.Errorf("%s", renderContentErrorMessage(renderFormatSVG, err))
	}
	page, fontFiles, err := prepareRenderFontHTML(ctx, page)
	if err != nil {
		return "", fmt.Errorf("字体准备失败：%s", firstLineOf(err.Error()))
	}
	cfg := t.runtime.effectiveConfigForEvent(t.event)
	shot, err := agent.CaptureHTMLScreenshot(ctx, agent.ScreenshotRequest{
		HTML:         page,
		WaitForFonts: len(fontFiles) > 0,
		FontFiles:    fontFiles,
		Width:        renderImageWidth,
		Height:       renderImageMaxHeight,
		Timeout:      time.Duration(cfg.AgentBrowserTimeoutMS) * time.Millisecond,
	})
	if err != nil {
		return "", fmt.Errorf("svg 渲染失败：%s", firstLineOf(err.Error()))
	}
	dir, err := os.MkdirTemp("", "diana-svg-image-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	pngPath := filepath.Join(dir, "image.png")
	if err := os.WriteFile(pngPath, trimRenderScreenshot(shot), 0600); err != nil {
		return "", err
	}
	if err := t.runtime.sendOutgoing(ctx, t.event, OutgoingMessage{ImageURLs: []string{pngPath}}); err != nil {
		return "", fmt.Errorf("发送图片失败：%w", err)
	}
	return `{"status":"sent","message":"SVG 已栅格化为 PNG 并发送到当前会话。"}`, nil
}

func (t *dianaLocalAttachmentTool) ToolResultParts(string) []llm.ContentPart {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]llm.ContentPart(nil), t.parts...)
}
