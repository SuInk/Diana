// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/SuInk/diana/internal/procgroup"
	"github.com/SuInk/diana/model/agent"
	"github.com/SuInk/diana/model/applog"
)

// render_media 把模型写的 HTML 页面或 SVG 渲染成图片、视频或 GIF 发到会话。
//
// 和 render 工具分开：render 只收 Markdown/Mermaid/静态 SVG，页面里不许有
// 脚本；这里的 HTML 要能跑脚本（canvas、动画都靠它），隔离改由
// agent.CaptureHTMLFrames 的假源 + 全量拦截 + CSP 承担。归在「文件交付」插件
// 下，开关、仅主人可用和大小上限都跟发文件共用一套。
const dianaRenderMediaToolName = "render_media"

const (
	renderMediaFormatHTML = "html"
	renderMediaFormatSVG  = "svg"

	renderMediaOutputImage = "image"
	renderMediaOutputVideo = "video"
	renderMediaOutputGIF   = "gif"

	defaultRenderMediaMaxSeconds = 10
	maxRenderMediaMaxSeconds     = 20
	defaultRenderMediaSeconds    = 4
	defaultRenderMediaVideoFPS   = 24
	defaultRenderMediaGIFFPS     = 12
	maxRenderMediaFPS            = 30
	// 静帧前先把虚拟时间推进这么久，入场动画走完再截。
	renderMediaStillAt = time.Second
	// 发出去的成品上限。GIF 没有帧间压缩，几秒大画面就能到几十 MB。
	renderMediaMaxOutputBytes = 32 << 20
	renderMediaCaptureTimeout = 2 * time.Minute
)

type renderMediaSize struct{ width, height int }

// 各输出的默认视口。HTML 图片只定宽度，高度按页面实际长度截整页。
var renderMediaDefaultSizes = map[string]renderMediaSize{
	renderMediaOutputImage: {1000, 0},
	renderMediaOutputVideo: {1280, 720},
	renderMediaOutputGIF:   {640, 360},
}

type dianaRenderMediaTool struct {
	runtime  *Runtime
	event    MessageEvent
	settings SettingValues
	owner    bool
}

func newDianaRenderMediaTool(runtime *Runtime, event MessageEvent, settings SettingValues, relationship RelationshipPolicy) *dianaRenderMediaTool {
	return &dianaRenderMediaTool{runtime: runtime, event: event, settings: settings, owner: relationship.Owner}
}

func (t *dianaRenderMediaTool) Name() string { return dianaRenderMediaToolName }

func (t *dianaRenderMediaTool) Description() string {
	return `把你写的 HTML 页面或 SVG 渲染成图片、视频或 GIF 直接发到当前会话。` +
		`适合排好版的卡片、海报、数据看板、canvas 绘图、CSS/JS 动画、SVG 动画演示——对方要看的是效果而不是源码时用它；要源码文件用 send_file。` +
		`format 选 html（完整页面，可以写 <style> 和 <script>）或 svg（以 <svg> 开头，可带 SMIL/CSS 动画，不能有脚本）。` +
		`output 选 image（PNG 静帧，HTML 按整页高度截）、video（MP4）或 gif（短循环动图，画面宜小）。` +
		`页面完全离线：外链的脚本、样式、字体、图片一律加载不到，所有资源都要内联（CDN 上的库也用不了）。` +
		`动画从 0 秒开始按 duration 录制，时间是虚拟的，setTimeout、requestAnimationFrame、CSS 动画都按帧推进。` +
		`成品由运行时发送，调用后用一句话交代即可。`
}

func (t *dianaRenderMediaTool) InputSchema() map[string]any {
	return toolObjectSchema([]string{"format", "content"}, map[string]any{
		"format":   map[string]any{"type": "string", "enum": []string{renderMediaFormatHTML, renderMediaFormatSVG}, "description": "内容格式"},
		"content":  toolStringParam("完整的 HTML 文档，或以 <svg> 开头的 SVG。"),
		"output":   map[string]any{"type": "string", "enum": []string{renderMediaOutputImage, renderMediaOutputVideo, renderMediaOutputGIF}, "description": "输出类型，默认 image。"},
		"duration": map[string]any{"type": "number", "description": "video/gif 的时长（秒），默认 4。"},
		"fps":      map[string]any{"type": "integer", "description": "video/gif 的帧率，默认 video 24、gif 12，最高 30。"},
		"width":    map[string]any{"type": "integer", "description": "画面宽度（像素）。默认 image 1000、video 1280、gif 640；svg 按图形自身大小。"},
		"height":   map[string]any{"type": "integer", "description": "画面高度（像素）。默认 video 720、gif 360；image 省略时截整页。"},
	})
}

type dianaRenderMediaResult struct {
	OK       bool    `json:"ok"`
	Message  string  `json:"message"`
	Format   string  `json:"format,omitempty"`
	Output   string  `json:"output,omitempty"`
	Width    int     `json:"width,omitempty"`
	Height   int     `json:"height,omitempty"`
	Frames   int     `json:"frames,omitempty"`
	Duration float64 `json:"duration,omitempty"`
	Bytes    int64   `json:"bytes,omitempty"`
}

type renderMediaSpec struct {
	format, output, content string
	width, height           int
	// sized 表示调用方显式给了宽或高，SVG 就不再按图形外框自动定尺寸。
	sized       bool
	fps, frames int
	duration    float64
}

func (t *dianaRenderMediaTool) Run(ctx context.Context, input map[string]any) (string, error) {
	if t.settings.Bool(fileDeliverySettingOwnerOnly, false) && !t.owner {
		return t.fail(ctx, dianaRenderMediaResult{Message: "发文件和渲染目前只对主人开放，这次不能用。"}, "")
	}
	if !t.settings.Bool(fileDeliverySettingRenderMedia, true) {
		return t.fail(ctx, dianaRenderMediaResult{Message: "「文件交付」插件没有开启 HTML/动画渲染。"}, "")
	}
	spec, err := t.parse(input)
	if err != nil {
		return t.fail(ctx, dianaRenderMediaResult{Message: err.Error(), Format: spec.format, Output: spec.output}, "")
	}
	result := dianaRenderMediaResult{Format: spec.format, Output: spec.output}
	if !t.runtime.sandboxedBrowserEnabled(t.event) {
		result.Message = "「网页渲染」插件没有启用，渲染不了。"
		return t.fail(ctx, result, "")
	}
	ffmpeg := ""
	if spec.output != renderMediaOutputImage {
		if ffmpeg, err = lookResolverCommand("ffmpeg"); err != nil {
			result.Message = "这台机器没有安装 ffmpeg，出不了视频或 GIF；可以改用 output=image 出静帧。"
			return t.fail(ctx, result, "")
		}
	}
	request, err := buildRenderMediaRequest(ctx, spec)
	if err != nil {
		result.Message = err.Error()
		return t.fail(ctx, result, "")
	}

	if spec.output == renderMediaOutputImage {
		png, size, err := agent.CaptureHTMLStill(ctx, request, renderMediaStillAt)
		if err != nil {
			result.Message = "渲染失败：" + firstLineOf(err.Error())
			return t.fail(ctx, result, err.Error())
		}
		if err := t.runtime.sendPNGImage(ctx, t.event, png); err != nil {
			result.Message = firstLineOf(err.Error())
			return t.fail(ctx, result, err.Error())
		}
		result.OK, result.Message = true, "图片已经发到会话里了。"
		result.Width, result.Height, result.Bytes = size.Width, size.Height, int64(len(png))
		t.record(ctx, result, "")
		return marshalRenderMediaResult(result), nil
	}

	outputDir, err := os.MkdirTemp("", renderMediaTempPrefix+"*")
	if err != nil {
		return "", err
	}
	outputPath, size, err := encodeRenderMedia(ctx, ffmpeg, request, spec, outputDir)
	if err != nil {
		_ = os.RemoveAll(outputDir)
		result.Message = firstLineOf(err.Error())
		return t.fail(ctx, result, err.Error())
	}
	info, err := os.Stat(outputPath)
	if err != nil {
		_ = os.RemoveAll(outputDir)
		return "", err
	}
	if info.Size() > renderMediaMaxOutputBytes {
		_ = os.RemoveAll(outputDir)
		result.Message = fmt.Sprintf("成品有 %.1f MB，超过 %d MB 上限；缩小画面、降低帧率或缩短时长再试。", float64(info.Size())/(1<<20), renderMediaMaxOutputBytes>>20)
		return t.fail(ctx, result, "")
	}
	if spec.output == renderMediaOutputGIF {
		// GIF 按图片发，平台会原地循环播放；发完即删，和 PNG 一样。
		err = t.runtime.sendOutgoing(ctx, t.event, routeOutgoingToEvent(t.event, OutgoingMessage{ImageURLs: []string{outputPath}}))
		_ = os.RemoveAll(outputDir)
	} else {
		// 视频走和解析视频同一条投递链：OneBot 先让接入端回源拉，拉不到退回
		// 上传文件；本地文件由它按共享有效期延后清理。
		err = t.runtime.sendDirectPluginResponse(ctx, t.event, "", nil, []string{outputPath})
	}
	if err != nil {
		_ = os.RemoveAll(outputDir)
		result.Message = "发送失败：" + firstLineOf(err.Error())
		return t.fail(ctx, result, err.Error())
	}
	result.OK = true
	result.Message = map[string]string{renderMediaOutputVideo: "视频已经发到会话里了。", renderMediaOutputGIF: "GIF 已经发到会话里了。"}[spec.output]
	result.Width, result.Height, result.Bytes = size.Width, size.Height, info.Size()
	result.Frames, result.Duration = spec.frames, spec.duration
	t.record(ctx, result, "")
	return marshalRenderMediaResult(result), nil
}

func (t *dianaRenderMediaTool) parse(input map[string]any) (renderMediaSpec, error) {
	spec := renderMediaSpec{
		format:  strings.ToLower(strings.TrimSpace(configToolString(input, "format"))),
		output:  strings.ToLower(strings.TrimSpace(configToolString(input, "output"))),
		content: configToolString(input, "content"),
	}
	if spec.output == "" {
		spec.output = renderMediaOutputImage
	}
	if spec.format != renderMediaFormatHTML && spec.format != renderMediaFormatSVG {
		return spec, errors.New("format 只支持 html 或 svg。")
	}
	defaults, ok := renderMediaDefaultSizes[spec.output]
	if !ok {
		return spec, errors.New("output 只支持 image、video 或 gif。")
	}
	if strings.TrimSpace(spec.content) == "" {
		return spec, errors.New("content 是空的，没有东西可以渲染。")
	}
	if !utf8.ValidString(spec.content) {
		return spec, errors.New("content 不是合法的 UTF-8 文本。")
	}
	if maxBytes := t.settings.Bytes(fileDeliverySettingMaxFileBytes, defaultFileDeliveryMaxSize); int64(len(spec.content)) > maxBytes {
		return spec, fmt.Errorf("内容有 %d 字节，超过上限 %d 字节；精简一下再试。", len(spec.content), maxBytes)
	}
	_, hasWidth := input["width"]
	_, hasHeight := input["height"]
	spec.sized = hasWidth || hasHeight
	var err error
	if spec.width, err = renderMediaDimension(input, "width", defaults.width, 64, 1920); err != nil {
		return spec, err
	}
	if spec.height, err = renderMediaDimension(input, "height", defaults.height, 64, 1920); err != nil {
		return spec, err
	}
	if spec.output == renderMediaOutputImage {
		return spec, nil
	}
	maxSeconds := min(max(t.settings.Int(fileDeliverySettingMaxVideoSeconds, defaultRenderMediaMaxSeconds), 1), maxRenderMediaMaxSeconds)
	spec.duration = defaultRenderMediaSeconds
	if value, ok := toolInputNumber(input, "duration"); ok {
		spec.duration = value
	}
	if spec.duration <= 0 || spec.duration > float64(maxSeconds) {
		return spec, fmt.Errorf("duration 要在 0 到 %d 秒之间。", maxSeconds)
	}
	spec.fps = defaultRenderMediaVideoFPS
	if spec.output == renderMediaOutputGIF {
		spec.fps = defaultRenderMediaGIFFPS
	}
	if value, ok := toolInputNumber(input, "fps"); ok {
		spec.fps = int(value)
	}
	if spec.fps < 1 || spec.fps > maxRenderMediaFPS {
		return spec, fmt.Errorf("fps 要在 1 到 %d 之间。", maxRenderMediaFPS)
	}
	spec.frames = max(1, int(spec.duration*float64(spec.fps)+0.5))
	if spec.frames > agent.MaxHTMLCaptureFrames {
		return spec, fmt.Errorf("总帧数 %d 超过上限 %d；降低帧率或缩短时长。", spec.frames, agent.MaxHTMLCaptureFrames)
	}
	return spec, nil
}

func renderMediaDimension(input map[string]any, key string, fallback, low, high int) (int, error) {
	value, ok := toolInputNumber(input, key)
	if !ok {
		return fallback, nil
	}
	if value < float64(low) || value > float64(high) {
		return 0, fmt.Errorf("%s 要在 %d 到 %d 像素之间。", key, low, high)
	}
	return int(value), nil
}

// toolInputNumber 兼容模型把数字写成字符串的情况。
func toolInputNumber(input map[string]any, key string) (float64, bool) {
	switch value := input[key].(type) {
	case float64:
		return value, true
	case int:
		return float64(value), true
	case int64:
		return float64(value), true
	case json.Number:
		parsed, err := value.Float64()
		return parsed, err == nil
	case string:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
		return parsed, err == nil
	}
	return 0, false
}

// buildRenderMediaRequest 组页面并备好字体。SVG 走 render 同一套净化，只量
// 图形本身的外框；HTML 原样交给浏览器。
func buildRenderMediaRequest(ctx context.Context, spec renderMediaSpec) (agent.HTMLCaptureRequest, error) {
	request := agent.HTMLCaptureRequest{Width: spec.width, Height: spec.height, MaxHeight: 4000, Timeout: renderMediaCaptureTimeout}
	page := spec.content
	if spec.format == renderMediaFormatSVG {
		built, err := buildRenderPage(renderFormatSVG, spec.content, "")
		if err != nil {
			return request, errors.New(renderContentErrorMessage(renderFormatSVG, err))
		}
		// render 页面给画布留了最小宽度，小图会偏在左边、右侧空一大块；这里按
		// 图形本身收边。
		page = insertHeadHTML(built, `<style>.render-root{min-width:0}</style>`)
		// 没给尺寸时视口先给足，量完图形外框再收；给了就按给的来。
		if !spec.sized {
			request.FitSelector = "#render-root"
			request.Width, request.Height, request.MaxHeight = 1920, 0, 1920
		}
	}
	prepareFonts := prepareAuthoredHTMLFonts
	if spec.format == renderMediaFormatSVG {
		prepareFonts = prepareRenderFontHTML
	}
	page, fontFiles, err := prepareFonts(ctx, page)
	if err != nil {
		return request, fmt.Errorf("字体准备失败：%s", firstLineOf(err.Error()))
	}
	request.HTML, request.FontFiles = page, fontFiles
	return request, nil
}

// encodeRenderMedia 逐帧截图落盘，再交给 ffmpeg 编码成 MP4 或 GIF。
func encodeRenderMedia(ctx context.Context, ffmpeg string, request agent.HTMLCaptureRequest, spec renderMediaSpec, outputDir string) (string, agent.HTMLCaptureSize, error) {
	framesDir, err := os.MkdirTemp("", "diana-render-frames-*")
	if err != nil {
		return "", agent.HTMLCaptureSize{}, err
	}
	defer os.RemoveAll(framesDir)
	// MP4 用 JPEG 帧：截得快、落盘小，反正还要再有损编码一次。GIF 要调色板，
	// 用无损 PNG 帧免得 JPEG 噪点吃掉颜色。
	jpeg := spec.output == renderMediaOutputVideo
	ext := map[bool]string{true: "jpg", false: "png"}[jpeg]
	size, err := agent.CaptureHTMLFrames(ctx, request, agent.HTMLFrameOptions{
		FPS: spec.fps, Frames: spec.frames, JPEG: jpeg,
		OnFrame: func(index int, data []byte) error {
			return os.WriteFile(filepath.Join(framesDir, fmt.Sprintf("%05d.%s", index, ext)), data, 0o600)
		},
	})
	if err != nil {
		return "", size, fmt.Errorf("渲染失败：%w", err)
	}
	input := filepath.Join(framesDir, "%05d."+ext)
	var args []string
	outputPath := filepath.Join(outputDir, "render.mp4")
	if spec.output == renderMediaOutputGIF {
		outputPath = filepath.Join(outputDir, "render.gif")
		args = renderMediaGIFArgs(spec.fps, input, outputPath)
	} else {
		args = renderMediaMP4Args(spec.fps, input, outputPath)
	}
	stderr := &limitedCommandOutput{remaining: 4096}
	cmd := procgroup.CommandContext(ctx, ffmpeg, args...)
	cmd.Env = resolverCommandEnv()
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		return "", size, fmt.Errorf("ffmpeg 编码失败：%s", firstNonEmpty(strings.TrimSpace(stderr.String()), err.Error()))
	}
	return outputPath, size, nil
}

func renderMediaMP4Args(fps int, input, output string) []string {
	// H.264 + yuv420p 要求宽高都是偶数，奇数边补一像素白边。faststart 把索引
	// 挪到文件头，接入端边下边播。
	return []string{"-hide_banner", "-loglevel", "error", "-y",
		"-framerate", strconv.Itoa(fps), "-i", input,
		"-vf", "pad=ceil(iw/2)*2:ceil(ih/2)*2:color=white",
		"-c:v", "libx264", "-preset", "veryfast", "-crf", "23", "-pix_fmt", "yuv420p",
		"-movflags", "+faststart", output}
}

func renderMediaGIFArgs(fps int, input, output string) []string {
	// 先按整段画面生成调色板再上色，比默认的 256 色网格清楚得多。
	return []string{"-hide_banner", "-loglevel", "error", "-y",
		"-framerate", strconv.Itoa(fps), "-i", input,
		"-filter_complex", "split[a][b];[a]palettegen=stats_mode=diff[p];[b][p]paletteuse=dither=bayer:bayer_scale=5",
		"-loop", "0", output}
}

const renderMediaTempPrefix = "diana-render-media-"

func (t *dianaRenderMediaTool) fail(ctx context.Context, result dianaRenderMediaResult, detail string) (string, error) {
	result.OK = false
	t.record(ctx, result, detail)
	return marshalRenderMediaResult(result), nil
}

func (t *dianaRenderMediaTool) record(ctx context.Context, result dianaRenderMediaResult, detail string) {
	writer := t.runtime.appLogWriter()
	if writer == nil {
		return
	}
	kind, level := applog.KindOperation, applog.LevelInfo
	if !result.OK {
		kind, level = applog.KindError, applog.LevelError
	}
	logCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	_ = writer.AppendLog(logCtx, applog.Entry{
		Kind:    kind,
		Level:   level,
		Action:  dianaRenderMediaToolName,
		Message: result.Message,
		Detail:  detail,
		Actor:   oneBotEventActor(t.event),
		Target:  strings.TrimSpace(firstNonEmpty(t.event.GroupID, t.event.UserID)),
		Metadata: map[string]any{
			"format": result.Format, "output": result.Output, "width": result.Width, "height": result.Height,
			"frames": result.Frames, "duration": result.Duration, "bytes": result.Bytes,
		},
	})
}

func marshalRenderMediaResult(result dianaRenderMediaResult) string {
	encoded, err := json.Marshal(result)
	if err != nil {
		return `{"ok":false,"message":"结果无法编码"}`
	}
	return string(encoded)
}
