// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/SuInk/diana/model/agent"
	"github.com/SuInk/diana/model/applog"
)

const (
	fileDeliveryPluginID       = "official.file-delivery"
	dianaFileDeliveryToolName  = "send_file"
	defaultFileDeliveryMaxSize = 1 << 20
	fileDeliveryMaxNameRunes   = 100

	fileDeliverySettingMaxFileBytes = "max_file_bytes"
	fileDeliverySettingPreview      = "preview"
	fileDeliverySettingOwnerOnly    = "owner_only"
	// 下面两项管同插件里的 render_media 工具。
	fileDeliverySettingRenderMedia     = "render_media"
	fileDeliverySettingMaxVideoSeconds = "max_video_seconds"
)

// 内容来自模型输出的文本，这些扩展名在接收端双击就会执行或安装，一律不发。
var fileDeliveryBlockedExts = map[string]struct{}{
	"exe": {}, "dll": {}, "so": {}, "dylib": {}, "com": {}, "scr": {}, "msi": {}, "pif": {}, "cpl": {},
	"apk": {}, "ipa": {}, "jar": {}, "lnk": {}, "hta": {}, "vbs": {}, "vbe": {}, "wsf": {}, "wsh": {}, "reg": {},
}

var fileDeliveryCodeLanguages = map[string]string{
	"py": "python", "js": "javascript", "mjs": "javascript", "ts": "typescript", "tsx": "tsx", "jsx": "jsx",
	"go": "go", "rs": "rust", "java": "java", "kt": "kotlin", "c": "c", "h": "c", "cpp": "cpp", "cc": "cpp",
	"hpp": "cpp", "cs": "csharp", "rb": "ruby", "php": "php", "swift": "swift", "lua": "lua", "sh": "bash",
	"bash": "bash", "zsh": "bash", "ps1": "powershell", "bat": "bat", "cmd": "bat", "sql": "sql", "html": "html",
	"htm": "html", "css": "css", "scss": "scss", "vue": "vue", "json": "json", "yaml": "yaml", "yml": "yaml",
	"toml": "toml", "xml": "xml", "ini": "ini", "dockerfile": "dockerfile", "r": "r", "dart": "dart", "tex": "latex",
}

// FileDeliveryPlugin 让模型把自己写好的代码、SVG、文档作为可下载文件发到会话，
// 可选附带一张渲染预览图；也能把 HTML/SVG 直接渲染成图片、视频或 GIF 发出去。
type FileDeliveryPlugin struct{}

func NewFileDeliveryPlugin() *FileDeliveryPlugin { return &FileDeliveryPlugin{} }

func (p *FileDeliveryPlugin) Manifest() PluginManifest {
	return PluginManifest{
		ID:          fileDeliveryPluginID,
		Name:        "文件交付",
		Version:     "0.1.1",
		Description: "启用内置 Agent 后，模型可以把写好的代码、SVG、Markdown、配置等文本内容直接打包成文件发到会话供下载，不需要命令工具或工作目录。SVG、Mermaid、Markdown、HTML 和代码可附带一张渲染预览图；HTML 页面和 SVG 还能直接渲染成图片、MP4 视频或 GIF 发出（渲染需启用「网页渲染」插件，视频和 GIF 需要 ffmpeg）。",
		Official:    true,
		BuiltIn:     true,
		Permissions: []string{"message:send", "file:send", "browser:render"},
		Settings: []PluginSettingSpec{
			{
				Key:         fileDeliverySettingMaxFileBytes,
				Label:       "单个文件大小上限",
				Description: "模型生成的单个文件超过该大小时拒绝发送。",
				Type:        PluginSettingTypeSize,
				Default:     defaultFileDeliveryMaxSize,
				Min:         settingRange(4 * 1024),
				Max:         settingRange(16 * 1024 * 1024),
			},
			{
				Key:         fileDeliverySettingPreview,
				Label:       "默认附带预览图",
				Description: "发送 SVG、Mermaid、Markdown、HTML 或代码文件时，先发一张渲染后的预览图。模型也可以按次关闭。",
				Type:        PluginSettingTypeBool,
				Default:     true,
			},
			{
				Key:         fileDeliverySettingOwnerOnly,
				Label:       "仅主人可用",
				Description: "开启后只有主人触发的对话能让机器人发文件或渲染。",
				Type:        PluginSettingTypeBool,
				Default:     false,
			},
			{
				Key:         fileDeliverySettingRenderMedia,
				Label:       "HTML/动画渲染",
				Description: "允许模型把 HTML 页面或 SVG 渲染成图片、视频或 GIF 发出。页面可以运行脚本，但在断网的无头浏览器里执行，读不到本机文件。",
				Type:        PluginSettingTypeBool,
				Default:     true,
			},
			{
				Key:         fileDeliverySettingMaxVideoSeconds,
				Label:       "视频/GIF 最长时长",
				Description: "单次渲染的视频或 GIF 不能超过这个时长。录制按帧截图，越长越慢。",
				Type:        PluginSettingTypeNumber,
				Default:     defaultRenderMediaMaxSeconds,
				Min:         settingRange(1),
				Max:         settingRange(maxRenderMediaMaxSeconds),
				Step:        1,
				Unit:        "秒",
			},
		},
	}
}

func (p *FileDeliveryPlugin) Handle(context.Context, PluginRequest) (*PluginResponse, error) {
	return nil, nil
}

type dianaFileDeliveryTool struct {
	runtime  *Runtime
	event    MessageEvent
	settings SettingValues
	owner    bool
}

func newDianaFileDeliveryTool(runtime *Runtime, event MessageEvent, settings SettingValues, relationship RelationshipPolicy) *dianaFileDeliveryTool {
	return &dianaFileDeliveryTool{runtime: runtime, event: event, settings: settings, owner: relationship.Owner}
}

func (t *dianaFileDeliveryTool) Name() string { return dianaFileDeliveryToolName }

func (t *dianaFileDeliveryTool) Description() string {
	return `把你写好的文本内容保存成文件发到当前会话，对方可以直接下载。` +
		`适合完整的代码文件、脚本、SVG 图、Markdown 文档、配置文件、CSV 数据等——内容长、要保存或要拿去运行时用它，别把几百行代码塞进聊天正文。` +
		`filename 带扩展名（如 snake.py、logo.svg、README.md），content 是完整文件内容。` +
		`preview 为 true 时先发一张渲染预览图：svg 画成图，mmd 按 mermaid 画，md 按 Markdown 排版，html 按网页效果截图，代码按等宽代码块排版。` +
		`文件由运行时发送，调用后用一句话交代即可，不要在正文里再贴一遍内容。`
}

func (t *dianaFileDeliveryTool) InputSchema() map[string]any {
	return toolObjectSchema([]string{"filename", "content"}, map[string]any{
		"filename": toolStringParam("文件名，只能是名字不能带目录，必须有扩展名，例如 main.go、chart.svg、notes.md。"),
		"content":  toolStringParam("完整的文件内容（UTF-8 文本）。"),
		"preview":  toolBoolParam("是否先发渲染预览图；省略时按插件设置。"),
	})
}

type dianaFileDeliveryResult struct {
	OK       bool   `json:"ok"`
	Message  string `json:"message"`
	Filename string `json:"filename,omitempty"`
	Bytes    int    `json:"bytes,omitempty"`
	Preview  string `json:"preview,omitempty"`
}

func (t *dianaFileDeliveryTool) Run(ctx context.Context, input map[string]any) (string, error) {
	if t.settings.Bool(fileDeliverySettingOwnerOnly, false) && !t.owner {
		return t.fail(ctx, "", "发文件目前只对主人开放，这次不能发。")
	}
	name, err := fileDeliveryName(configToolString(input, "filename"))
	if err != nil {
		return t.fail(ctx, "", err.Error())
	}
	content := configToolString(input, "content")
	if strings.TrimSpace(content) == "" {
		return t.fail(ctx, name, "content 是空的，没有东西可以发。")
	}
	if !utf8.ValidString(content) {
		return t.fail(ctx, name, "content 不是合法的 UTF-8 文本。")
	}
	maxBytes := t.settings.Bytes(fileDeliverySettingMaxFileBytes, defaultFileDeliveryMaxSize)
	if int64(len(content)) > maxBytes {
		return t.fail(ctx, name, fmt.Sprintf("文件有 %d 字节，超过上限 %d 字节；拆成几个文件或精简一下。", len(content), maxBytes))
	}

	preview := t.settings.Bool(fileDeliverySettingPreview, true)
	if _, ok := input["preview"]; ok {
		preview = toolInputBool(input, "preview")
	}
	previewStatus := ""
	if preview {
		previewStatus = t.sendPreview(ctx, name, content)
	}
	if err := t.runtime.sendFileAttachment(ctx, t.event, name, []byte(content)); err != nil {
		return t.fail(ctx, name, "文件发送失败："+firstLineOf(err.Error()))
	}
	result := dianaFileDeliveryResult{OK: true, Message: "文件已经发到会话里了。", Filename: name, Bytes: len(content), Preview: previewStatus}
	t.record(ctx, result, "")
	return marshalFileDeliveryResult(result), nil
}

// sendPreview 预览失败不拦文件发送，只把原因带回给模型。
func (t *dianaFileDeliveryTool) sendPreview(ctx context.Context, name, content string) string {
	// HTML 预览要跑页面脚本，归 render_media 开关管；关着就退回源码预览。
	if ext := fileDeliveryExt(name); (ext == "html" || ext == "htm") && t.settings.Bool(fileDeliverySettingRenderMedia, true) {
		return t.sendHTMLPreview(ctx, content)
	}
	format, source := fileDeliveryPreviewSource(name, content)
	if format == "" {
		return "skipped: 该类型没有预览"
	}
	if len([]rune(source)) > renderImageMaxContent {
		return fmt.Sprintf("skipped: 内容超过 %d 字，只发文件", renderImageMaxContent)
	}
	if !t.runtime.sandboxedBrowserEnabled(t.event) {
		return "skipped: 「网页渲染」插件没有启用"
	}
	png, err := t.runtime.renderContentPNG(ctx, t.event, format, source, "")
	if err != nil {
		return "failed: " + firstLineOf(err.Error())
	}
	if err := t.runtime.sendPNGImage(ctx, t.event, png); err != nil {
		return "failed: " + firstLineOf(err.Error())
	}
	return "sent"
}

func (t *dianaFileDeliveryTool) sendHTMLPreview(ctx context.Context, content string) string {
	if !t.runtime.sandboxedBrowserEnabled(t.event) {
		return "skipped: 「网页渲染」插件没有启用"
	}
	request, err := buildRenderMediaRequest(ctx, renderMediaSpec{format: renderMediaFormatHTML, output: renderMediaOutputImage, content: content, width: renderMediaDefaultSizes[renderMediaOutputImage].width})
	if err != nil {
		return "failed: " + firstLineOf(err.Error())
	}
	png, _, err := agent.CaptureHTMLStill(ctx, request, renderMediaStillAt)
	if err != nil {
		return "failed: " + firstLineOf(err.Error())
	}
	if err := t.runtime.sendPNGImage(ctx, t.event, png); err != nil {
		return "failed: " + firstLineOf(err.Error())
	}
	return "sent"
}

func fileDeliveryPreviewSource(name, content string) (string, string) {
	ext := fileDeliveryExt(name)
	switch ext {
	case "svg":
		return renderFormatSVG, content
	case "mmd", "mermaid":
		return renderFormatMermaid, content
	case "md", "markdown":
		return renderFormatMarkdown, content
	case "txt", "log", "csv", "tsv":
		return renderFormatMarkdown, fileDeliveryCodeFence("", content)
	}
	if lang, ok := fileDeliveryCodeLanguages[ext]; ok {
		return renderFormatMarkdown, fileDeliveryCodeFence(lang, content)
	}
	return "", ""
}

// fileDeliveryCodeFence 用比内容里最长反引号串更长的围栏，内容里自带 ``` 也不会提前闭合。
func fileDeliveryCodeFence(lang, content string) string {
	longest, run := 0, 0
	for _, r := range content {
		if r == '`' {
			run++
			longest = max(longest, run)
		} else {
			run = 0
		}
	}
	fence := strings.Repeat("`", max(3, longest+1))
	return fence + lang + "\n" + strings.TrimRight(content, "\n") + "\n" + fence
}

func fileDeliveryExt(name string) string {
	if strings.EqualFold(name, "Dockerfile") {
		return "dockerfile"
	}
	return strings.ToLower(strings.TrimPrefix(filepath.Ext(name), "."))
}

func fileDeliveryName(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	if name == "" {
		return "", fmt.Errorf("filename 不能为空。")
	}
	if strings.ContainsAny(name, `/\:*?"<>|`) || strings.HasPrefix(name, ".") {
		return "", fmt.Errorf("filename 只能是文件名，不能带目录、以点开头或包含 /\\:*?\"<>| 这些字符。")
	}
	if strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return "", fmt.Errorf("filename 不能包含控制字符。")
	}
	if utf8.RuneCountInString(name) > fileDeliveryMaxNameRunes {
		return "", fmt.Errorf("filename 太长，最多 %d 个字符。", fileDeliveryMaxNameRunes)
	}
	ext := fileDeliveryExt(name)
	if ext == "" {
		return "", fmt.Errorf("filename 需要带扩展名，例如 main.py、image.svg。")
	}
	if _, blocked := fileDeliveryBlockedExts[ext]; blocked {
		return "", fmt.Errorf("不发送 .%s 这类可执行或安装文件。", ext)
	}
	return name, nil
}

func (t *dianaFileDeliveryTool) fail(ctx context.Context, name, message string) (string, error) {
	result := dianaFileDeliveryResult{Message: message, Filename: name}
	t.record(ctx, result, "")
	return marshalFileDeliveryResult(result), nil
}

func (t *dianaFileDeliveryTool) record(ctx context.Context, result dianaFileDeliveryResult, detail string) {
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
		Kind:     kind,
		Level:    level,
		Action:   "send_file",
		Message:  result.Message,
		Detail:   detail,
		Actor:    oneBotEventActor(t.event),
		Target:   strings.TrimSpace(firstNonEmpty(t.event.GroupID, t.event.UserID)),
		Metadata: map[string]any{"filename": result.Filename, "bytes": result.Bytes, "preview": result.Preview},
	})
}

func marshalFileDeliveryResult(result dianaFileDeliveryResult) string {
	encoded, err := json.Marshal(result)
	if err != nil {
		return `{"ok":false,"message":"结果无法编码"}`
	}
	return string(encoded)
}
