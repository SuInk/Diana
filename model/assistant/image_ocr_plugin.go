// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/SuInk/diana/model/llm"
)

// 图片文字转写插件：聊天图片进上下文前，先用 vision 组模型跑一次严格转写，
// 把识别出的文字作为文本注入当前消息。对话主模型即使读图上文字的能力很差
// （不少便宜模型都这样），也能从转写文本里拿到内容——两者互补。默认关闭，
// 因为每张图多一次视觉调用。
const (
	imageOCRPluginID        = "official.image-ocr"
	imageOCRBackendDisabled = "disabled"
	imageOCRBackendLLM      = "llm"
	imageOCRNoTextMarker    = "[无可辨文字]"
	imageOCRCacheCap        = 128
	imageOCRPerImageMax     = 2000
)

type ImageOCRPlugin struct {
	mu    sync.Mutex
	cache map[string]string
	order []string
}

func NewImageOCRPlugin() *ImageOCRPlugin {
	return &ImageOCRPlugin{cache: make(map[string]string)}
}

func (p *ImageOCRPlugin) Manifest() PluginManifest {
	return PluginManifest{
		ID: imageOCRPluginID, Name: "图片文字识别", Version: "0.1.0",
		Description: "聊天图片进入上下文前先做一次文字转写（OCR），识别结果随图片一起交给对话模型。对话模型读图上文字能力弱时可作补充；转写调用走 LLM 配置里的 vision 分组，可在下方指定专用模型。",
		Official:    true, BuiltIn: true,
		Permissions: []string{"message:read", "llm:generate"},
		Settings: []PluginSettingSpec{
			{Key: "backend", Label: "转写方式", Type: PluginSettingTypeSelect, Default: imageOCRBackendDisabled, Options: []PluginSettingOption{
				{Value: imageOCRBackendDisabled, Label: "关闭"},
				{Value: imageOCRBackendLLM, Label: "LLM 视觉转写（vision 分组）"},
			}},
			{Key: "model", Label: "指定模型", Type: PluginSettingTypeString, Default: ""},
			{Key: "max_images", Label: "单条消息转写图片数上限", Type: PluginSettingTypeNumber, Default: 3, Min: settingRange(1), Max: settingRange(6), Step: 1},
			{Key: "timeout_seconds", Label: "单图转写超时", Type: PluginSettingTypeNumber, Default: 45, Min: settingRange(5), Max: settingRange(180), Step: 5, Unit: "秒"},
			{Key: "private_enabled", Label: "私聊图片也转写", Type: PluginSettingTypeBool, Default: true},
		},
	}
}

func (p *ImageOCRPlugin) Handle(context.Context, PluginRequest) (*PluginResponse, error) {
	return nil, nil
}

type imageOCRConfig struct {
	Backend        string
	Model          string
	MaxImages      int
	Timeout        time.Duration
	PrivateEnabled bool
}

func imageOCRConfigFromSettings(v SettingValues) imageOCRConfig {
	return imageOCRConfig{
		Backend:        v.String("backend", imageOCRBackendDisabled),
		Model:          strings.TrimSpace(v.String("model", "")),
		MaxImages:      v.Int("max_images", 3),
		Timeout:        time.Duration(v.Int("timeout_seconds", 45)) * time.Second,
		PrivateEnabled: v.Bool("private_enabled", true),
	}
}

// cachedTranscript 按图片内容（URL 或 data URL 的哈希）缓存转写结果：同一张图
// 在引用、追问里会反复进上下文，不该每次都烧一次视觉调用。
func (p *ImageOCRPlugin) cachedTranscript(key string) (string, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	text, ok := p.cache[key]
	return text, ok
}

func (p *ImageOCRPlugin) storeTranscript(key, text string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, exists := p.cache[key]; !exists {
		p.order = append(p.order, key)
		for len(p.order) > imageOCRCacheCap {
			delete(p.cache, p.order[0])
			p.order = p.order[1:]
		}
	}
	p.cache[key] = text
}

func imageOCRCacheKey(imageURL string) string {
	sum := sha256.Sum256([]byte(imageURL))
	return hex.EncodeToString(sum[:])
}

// imageOCRContextText 为当前消息里的图片生成转写文本；未启用、没有图片或全部
// 无可辨文字时返回空串。转写失败只跳过对应图片，绝不阻断回复。
func (r *Runtime) imageOCRContextText(ctx context.Context, event MessageEvent, message llm.Message) string {
	if r.plugins == nil {
		return ""
	}
	pluginValue, settings, enabled := r.plugins.PluginWithSettings(imageOCRPluginID, r.pluginOverridesForEvent(event))
	plugin, ok := pluginValue.(*ImageOCRPlugin)
	if !enabled || !ok {
		return ""
	}
	cfg := imageOCRConfigFromSettings(settings)
	if cfg.Backend != imageOCRBackendLLM {
		return ""
	}
	if event.Kind == EventKindPrivate && !cfg.PrivateEnabled {
		return ""
	}

	var imageURLs []string
	for _, part := range message.Parts {
		if part.Type == llm.ContentPartImageURL && strings.TrimSpace(part.ImageURL) != "" {
			imageURLs = append(imageURLs, part.ImageURL)
		}
	}
	if len(imageURLs) == 0 {
		return ""
	}
	if cfg.MaxImages > 0 && len(imageURLs) > cfg.MaxImages {
		imageURLs = imageURLs[:cfg.MaxImages]
	}

	var lines []string
	for index, imageURL := range imageURLs {
		text := r.transcribeContextImage(ctx, event, plugin, cfg, imageURL)
		if text == "" || text == imageOCRNoTextMarker {
			continue
		}
		if len(imageURLs) == 1 {
			lines = append(lines, text)
		} else {
			lines = append(lines, fmt.Sprintf("第 %d 张图：\n%s", index+1, text))
		}
	}
	if len(lines) == 0 {
		return ""
	}
	return "【图片文字转写（OCR 辅助，机器识别可能有误，请结合图片本身判断）】\n" + strings.Join(lines, "\n\n")
}

func (r *Runtime) transcribeContextImage(ctx context.Context, event MessageEvent, plugin *ImageOCRPlugin, cfg imageOCRConfig, imageURL string) string {
	cacheKey := imageOCRCacheKey(imageURL)
	if text, ok := plugin.cachedTranscript(cacheKey); ok {
		return text
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 45 * time.Second
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	prompt := "请完整转写这张图片里的文字。"
	text, err := r.runLLMProviderForGroup(callCtx, llm.GroupVision, func(client LLMProvider) (string, error) {
		resp, err := client.Generate(callCtx, llm.GenerateRequest{
			Model: cfg.Model,
			Messages: []llm.Message{
				{
					Role: llm.RoleSystem,
					Content: strings.TrimSpace(`你是高精度图片文字转写（OCR）子代理。严格转写图片中的可辨文字，保持自然阅读顺序。

要求：
- 只转写文字，不要描述画面、总结或回答图片内容，也不要补写图片上没有的信息。
- 聊天截图保留发言人与内容的对应；表格按行转成清晰纯文本。
- 不使用 Markdown 代码块。图片没有可辨文字时只返回“[无可辨文字]”。`),
				},
				{
					Role:    llm.RoleUser,
					Content: prompt,
					Parts: []llm.ContentPart{
						{Type: llm.ContentPartText, Text: prompt},
						{Type: llm.ContentPartImageURL, ImageURL: imageURL, Detail: "high"},
					},
				},
			},
		})
		if err != nil {
			return "", err
		}
		r.recordLLMUsage(ctx, event, resp.Provider, resp.Model, resp.Usage, "image_ocr")
		return resp.Text, nil
	})
	if err != nil {
		// 转写只是辅助信息，失败不值得打断回复，也不缓存失败结果。
		return ""
	}
	text = sanitizeFileTextString(text, imageOCRPerImageMax)
	if text == "" {
		text = imageOCRNoTextMarker
	}
	plugin.storeTranscript(cacheKey, text)
	return text
}
