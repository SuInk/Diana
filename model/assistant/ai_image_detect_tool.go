// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// AI 图片检测的聊天入口。和图片溯源一样做成工具：「是不是 AI 画的」「AI 图吧」
// 「有没有 SynthID」的问法太多，交给模型判断什么时候该查。
const dianaAIImageDetectToolName = "diana.ai_image_detect"

type dianaAIImageDetectTool struct {
	runtime  *Runtime
	event    MessageEvent
	plugin   *AIImageDetectPlugin
	settings SettingValues
}

func newDianaAIImageDetectTool(runtime *Runtime, event MessageEvent, plugin *AIImageDetectPlugin, settings SettingValues) *dianaAIImageDetectTool {
	return &dianaAIImageDetectTool{runtime: runtime, event: event, plugin: plugin, settings: settings}
}

func (t *dianaAIImageDetectTool) Name() string { return dianaAIImageDetectToolName }

func (t *dianaAIImageDetectTool) Description() string {
	return `检测一张聊天图片是不是 AI 生成的：解析图片里的 AI 生成标识（C2PA 内容凭证、IPTC 数字来源类型、Google SynthID/「Made with Google AI」标注、国内 AIGC 隐式标识、Stable Diffusion/ComfyUI/NovelAI/Midjourney 等生成参数），配置了检测服务时还会检查 SynthID 像素水印。` +
		`用户问「这是 AI 图吗」「是不是 AI 画的」「有没有 SynthID 水印」时用它——你自己看图判断不了。` +
		`默认检测当前消息（或引用消息）里的图片；要查更早的图就传那条消息的 message_id。` +
		`没查到标识不等于真图：聊天平台转发、截图、重新保存都会抹掉元数据，转述时要说清楚这一点。`
}

func (t *dianaAIImageDetectTool) InputSchema() map[string]any {
	return toolObjectSchema(nil, map[string]any{
		"message_id":  toolStringParam("要检测的图片所在的消息 ID；省略表示当前消息或它引用的那条。"),
		"image_index": toolIntParam("这条消息里的第几张图，从 1 开始，默认第 1 张。", 1, 8),
	})
}

type dianaAIImageDetectResult struct {
	OK        bool           `json:"ok"`
	Message   string         `json:"message"`
	MessageID string         `json:"message_id,omitempty"`
	Report    *AIImageReport `json:"report,omitempty"`
}

func (t *dianaAIImageDetectTool) Run(ctx context.Context, input map[string]any) (string, error) {
	if t == nil || t.runtime == nil || t.plugin == nil {
		return "", fmt.Errorf("diana ai image detect: runtime is not configured")
	}
	cfg := aiImageDetectConfigFromSettings(t.settings)
	if t.event.Kind == EventKindPrivate && !cfg.PrivateEnabled {
		return marshalAIImageDetectResult(dianaAIImageDetectResult{Message: "私聊里没有开启 AI 图片检测。"}), nil
	}
	messageID := strings.TrimSpace(configToolString(input, "message_id"))
	image, resolvedID, err := resolveToolChatImage(ctx, t.runtime, t.event, messageID, imageSourceIndex(input), rawImageFromSegments)
	if err != nil {
		return marshalAIImageDetectResult(dianaAIImageDetectResult{Message: err.Error()}), nil
	}
	report := t.plugin.detect(ctx, cfg, image)
	return marshalAIImageDetectResult(dianaAIImageDetectResult{
		OK:        true,
		MessageID: resolvedID,
		Report:    &report,
		Message:   aiImageDetectSummary(report),
	}), nil
}

// rawImageFromSegments 取第 index 张图的原始文件字节。
//
// 不能走给模型看图的那条加载路径：大图会被缩放重新编码，EXIF、XMP、C2PA
// 和 PNG 文本块全都没了，检测结果就成了「没有标识」的假阴性。
func rawImageFromSegments(ctx context.Context, segments []MessageSegment, index int) ([]byte, error) {
	sources := availableImageURLs(segments)
	if index < 1 || index > len(sources) {
		return nil, fmt.Errorf("ai image detect: no image at index %d", index)
	}
	source := strings.TrimSpace(sources[index-1])
	switch {
	case strings.HasPrefix(source, "data:"):
		prefix, encoded, ok := strings.Cut(source, ",")
		if !ok || !strings.Contains(strings.ToLower(prefix), ";base64") {
			return nil, fmt.Errorf("ai image detect: image is not a base64 data URL")
		}
		if base64.StdEncoding.DecodedLen(len(encoded)) > maxLLMImageSourceBytes {
			return nil, errLLMImageSourceTooLarge
		}
		return base64.StdEncoding.DecodeString(encoded)
	case strings.HasPrefix(source, "http://"), strings.HasPrefix(source, "https://"):
		body, _, err := downloadImageBytesWithLimit(ctx, source, maxLLMImageSourceBytes)
		return body, err
	default:
		path := strings.TrimPrefix(source, "file://")
		info, err := os.Stat(path)
		if err != nil {
			return nil, err
		}
		if info.IsDir() || info.Size() <= 0 {
			return nil, fmt.Errorf("ai image detect: invalid image file")
		}
		if info.Size() > maxLLMImageSourceBytes {
			return nil, errLLMImageSourceTooLarge
		}
		return os.ReadFile(path)
	}
}

// aiImageDetectSummary 给模型一句能直接转述的结论，并把检测的边界说在前面，
// 免得它把「没查到」说成「是真图」。
func aiImageDetectSummary(report AIImageReport) string {
	var parts []string
	switch report.Verdict {
	case aiImageVerdictMarked:
		parts = append(parts, "图片里有明确的 AI 生成标识："+aiImageEvidenceList(report.Findings, true)+"。这是生成方主动写入的标识，可以据此说是 AI 生成或经 AI 编辑。")
	case aiImageVerdictHint:
		parts = append(parts, "没有明确的 AI 生成声明，但有相关线索："+aiImageEvidenceList(report.Findings, false)+"。只能说可能与 AI 有关，不能下结论。")
	default:
		summary := "没有找到 AI 生成的水印或元数据标识。这不代表一定不是 AI 图：聊天平台转发、截图、重新保存都会抹掉元数据。"
		if report.NoMetadata {
			summary += "这张图完全没有元数据，很可能已经被压缩或重新编码过。"
		}
		parts = append(parts, summary)
	}
	switch {
	case report.SynthID.Checked && report.SynthID.Detected != nil && *report.SynthID.Detected:
		// 已经算进 findings 里了。
	case report.SynthID.Checked && report.SynthID.Detected != nil:
		parts = append(parts, "SynthID 检测服务没有发现像素水印（只能说明不是带 SynthID 的 Google AI 图，其他厂商的生成图不在检测范围）。")
	case report.SynthID.Error != "":
		parts = append(parts, "SynthID 像素水印没查成："+report.SynthID.Error+"。")
	default:
		parts = append(parts, "没有配置 SynthID 检测服务，Google 的像素级隐形水印没有检查；需要的话可以把原图发到 Gemini App 问它是不是 Google AI 生成的。")
	}
	return strings.Join(parts, "")
}

func aiImageEvidenceList(findings []AIImageFinding, strongOnly bool) string {
	items := make([]string, 0, len(findings))
	for _, finding := range findings {
		if strongOnly && !finding.Strong {
			continue
		}
		items = append(items, finding.Evidence)
	}
	return strings.Join(items, "；")
}

func marshalAIImageDetectResult(result dianaAIImageDetectResult) string {
	payload, err := json.Marshal(result)
	if err != nil {
		return `{"ok":false,"message":"AI 图片检测结果序列化失败"}`
	}
	return string(payload)
}
