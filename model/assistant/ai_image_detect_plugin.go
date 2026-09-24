// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/SuInk/diana/internal/secretmask"
)

// AI 图片检测插件：群里丢来一张图问「这是不是 AI 画的」，看图猜是猜不准的——
// 现在的生成图肉眼越来越难分。能拿来当证据的只有生成方主动留下的标识：
//
//   - C2PA 内容凭证与 IPTC 数字来源类型（OpenAI、Google、Adobe、微软等都会写）；
//   - Google 的「Made with Google AI」标注，这类图同时嵌有 SynthID 隐形水印；
//   - 国内《人工智能生成合成内容标识办法》要求的 AIGC 隐式标识；
//   - Stable Diffusion WebUI、ComfyUI、NovelAI 这类本地工具写进 PNG 的生成参数。
//
// 这些都在本地解析，图片不出网。SynthID 的像素水印没有公开的离线检测方法，
// 只能交给可选的外部检测服务；没配就如实说没查，不拿元数据的结论冒充它。
const (
	aiImageDetectPluginID = "official.ai-image-detect"

	aiImageDetectSettingPrivateEnabled = "private_enabled"
	aiImageDetectSettingSynthIDURL     = "synthid_endpoint"
	aiImageDetectSettingSynthIDKey     = "synthid_api_key"
	aiImageDetectSettingTimeout        = "timeout_seconds"
	aiImageDetectSettingMaxUploadMB    = "max_upload_mb"

	aiImageVerdictMarked = "ai_marked"
	aiImageVerdictHint   = "ai_hint"
	aiImageVerdictNone   = "no_marker"

	// 元数据块解压后的上限：正常的生成参数、XMP、C2PA 清单远小于这个值，
	// 超过的多半是构造出来的压缩炸弹。
	aiImageMaxMetadataBytes = 4 << 20
	// 认不出结构的格式（GIF、AVIF、HEIC）整文件扫关键词，只扫这么长。
	aiImageMaxRawScanBytes = 8 << 20
)

// AIImageFinding 是一条检测线索。Strong 表示生成方明确声明了 AI 生成，
// 否则只是相关迹象，不能单凭它下结论。
type AIImageFinding struct {
	Kind     string `json:"kind"`
	Source   string `json:"source"`
	Evidence string `json:"evidence"`
	Strong   bool   `json:"strong"`
}

// AIImageSynthIDResult 是外部 SynthID 检测服务的结论。
type AIImageSynthIDResult struct {
	Checked    bool     `json:"checked"`
	Detected   *bool    `json:"detected,omitempty"`
	Confidence *float64 `json:"confidence,omitempty"`
	Message    string   `json:"message,omitempty"`
	Error      string   `json:"error,omitempty"`
}

// AIImageReport 是一张图的完整检测结果。
type AIImageReport struct {
	Format   string               `json:"format"`
	Verdict  string               `json:"verdict"`
	Findings []AIImageFinding     `json:"findings,omitempty"`
	SynthID  AIImageSynthIDResult `json:"synthid"`
	// NoMetadata 表示图片里一点元数据都没有，通常是被平台压缩或截图重存过。
	NoMetadata bool `json:"no_metadata,omitempty"`
}

type AIImageDetectPlugin struct {
	client *http.Client
}

func NewAIImageDetectPlugin(client *http.Client) *AIImageDetectPlugin {
	if client == nil {
		client = &http.Client{}
	}
	return &AIImageDetectPlugin{client: client}
}

func (p *AIImageDetectPlugin) Manifest() PluginManifest {
	return PluginManifest{
		ID: aiImageDetectPluginID, Name: "AI 图片检测", Version: "0.1.1",
		Description: "回答「这张图是不是 AI 生成的」：在本地解析图片里的 AI 生成标识，包括 C2PA 内容凭证、IPTC 数字来源类型、Google「Made with Google AI」/SynthID 标注、国内 AIGC 隐式标识，以及 Stable Diffusion WebUI、ComfyUI、NovelAI、Midjourney 等工具写入的生成参数。本地检测不上传图片。SynthID 像素水印没有公开的离线检测方法，需要另外配置检测服务地址才会检查。注意：聊天平台转发、截图和重新保存都会抹掉元数据，查不到标识不代表一定是真图。",
		Official:    true, BuiltIn: true,
		Permissions: []string{"message:read", "network:https", "agent:tool"},
		Settings: []PluginSettingSpec{
			{Key: aiImageDetectSettingPrivateEnabled, Label: "私聊也允许检测", Type: PluginSettingTypeBool, Default: true},
			{Key: aiImageDetectSettingSynthIDURL, Label: "SynthID 检测服务地址（可选）", Description: "留空只做本地元数据检测，图片不出网。填写后会把图片上传到这个地址检测 SynthID 像素水印：POST multipart 表单，图片放在 file 字段；返回 JSON {\"detected\": true/false, \"confidence\": 0~1, \"message\": \"说明\"}。只接受 HTTPS，本机调试可用 localhost 的 HTTP。", Type: PluginSettingTypeString, Default: ""},
			{Key: aiImageDetectSettingSynthIDKey, Label: "SynthID 检测服务密钥", Description: "填写后以 Authorization: Bearer 请求头发送。", Type: PluginSettingTypeString, Default: "", Secret: true},
			{Key: aiImageDetectSettingTimeout, Label: "检测服务超时", Type: PluginSettingTypeNumber, Default: 20, Min: settingRange(5), Max: settingRange(60), Step: 5, Unit: "秒"},
			{Key: aiImageDetectSettingMaxUploadMB, Label: "上传大小上限", Description: "超过这个大小的图片不上传检测服务，本地检测不受影响。", Type: PluginSettingTypeNumber, Default: 8, Min: settingRange(1), Max: settingRange(32), Step: 1, Unit: "MB"},
		},
	}
}

func (p *AIImageDetectPlugin) Handle(context.Context, PluginRequest) (*PluginResponse, error) {
	return nil, nil
}

type aiImageDetectConfig struct {
	PrivateEnabled bool
	SynthIDURL     string
	SynthIDKey     string
	Timeout        time.Duration
	MaxUploadMB    int
}

func aiImageDetectConfigFromSettings(v SettingValues) aiImageDetectConfig {
	return aiImageDetectConfig{
		PrivateEnabled: v.Bool(aiImageDetectSettingPrivateEnabled, true),
		// 配歪的地址（明文 HTTP、带账号密码）当作没配，不拿它把图片发出去。
		SynthIDURL:  imageSourceEndpoint(v.String(aiImageDetectSettingSynthIDURL, ""), ""),
		SynthIDKey:  strings.TrimSpace(v.String(aiImageDetectSettingSynthIDKey, "")),
		Timeout:     time.Duration(v.Int(aiImageDetectSettingTimeout, 20)) * time.Second,
		MaxUploadMB: v.Int(aiImageDetectSettingMaxUploadMB, 8),
	}
}

func (cfg aiImageDetectConfig) maxUploadBytes() int64 {
	mb := cfg.MaxUploadMB
	if mb <= 0 {
		mb = 8
	}
	return int64(mb) * 1024 * 1024
}

func (cfg aiImageDetectConfig) timeout() time.Duration {
	if cfg.Timeout <= 0 {
		return 20 * time.Second
	}
	return cfg.Timeout
}

// detect 先做本地检测，再按配置问 SynthID 服务。服务挂了只记一笔，本地结论照给。
func (p *AIImageDetectPlugin) detect(ctx context.Context, cfg aiImageDetectConfig, image []byte) AIImageReport {
	report := detectAIImageMarkers(image)
	if cfg.SynthIDURL == "" {
		return report
	}
	report.SynthID.Checked = true
	if limit := cfg.maxUploadBytes(); int64(len(image)) > limit {
		report.SynthID.Checked = false
		report.SynthID.Error = fmt.Sprintf("图片 %.1f MB，超过 %d MB 上传上限，没有送检", float64(len(image))/(1024*1024), limit>>20)
		return report
	}
	callCtx, cancel := context.WithTimeout(ctx, cfg.timeout())
	defer cancel()
	result, err := p.checkSynthID(callCtx, cfg, image)
	if err != nil {
		// 报告整份交给模型。检测服务地址是主人填的，令牌可能写在查询参数里，而
		// HTTP 客户端的报错带着整条地址。
		report.SynthID.Error = secretmask.Text(err.Error())
		return report
	}
	report.SynthID = result
	if result.Detected != nil && *result.Detected {
		evidence := "检测服务在像素里发现了 SynthID 水印"
		if result.Confidence != nil {
			evidence += fmt.Sprintf("（置信度 %.0f%%）", *result.Confidence*100)
		}
		report.Findings = append(report.Findings, AIImageFinding{Kind: "synthid", Source: "SynthID 检测服务", Evidence: evidence, Strong: true})
		report.Verdict = aiImageVerdict(report.Findings)
	}
	return report
}

func (p *AIImageDetectPlugin) checkSynthID(ctx context.Context, cfg aiImageDetectConfig, image []byte) (AIImageSynthIDResult, error) {
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	part, err := writer.CreateFormFile("file", "image"+aiImageFormatExtension(sniffAIImageFormat(image)))
	if err != nil {
		return AIImageSynthIDResult{}, err
	}
	if _, err := part.Write(image); err != nil {
		return AIImageSynthIDResult{}, err
	}
	if err := writer.Close(); err != nil {
		return AIImageSynthIDResult{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.SynthIDURL, body)
	if err != nil {
		return AIImageSynthIDResult{}, err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("Accept", "application/json")
	if cfg.SynthIDKey != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.SynthIDKey)
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return AIImageSynthIDResult{}, err
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(resp.Body, imageSourceMaxResponseBytes))
	if err != nil {
		return AIImageSynthIDResult{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return AIImageSynthIDResult{}, fmt.Errorf("HTTP %d%s", resp.StatusCode, briefResponseDetail(payload))
	}
	var parsed struct {
		Detected   *bool    `json:"detected"`
		Confidence *float64 `json:"confidence"`
		Message    string   `json:"message"`
		Error      string   `json:"error"`
	}
	if err := json.Unmarshal(payload, &parsed); err != nil {
		return AIImageSynthIDResult{}, fmt.Errorf("返回内容不是预期的 JSON：%w", err)
	}
	if strings.TrimSpace(parsed.Error) != "" {
		return AIImageSynthIDResult{}, errors.New(strings.TrimSpace(parsed.Error))
	}
	if parsed.Detected == nil {
		// 没有 detected 字段就不知道结论，不能当成「没有水印」。
		return AIImageSynthIDResult{}, errors.New("返回里缺少 detected 字段")
	}
	if parsed.Confidence != nil && *parsed.Confidence > 1 {
		// 有的服务给百分比，统一成 0~1。
		normalized := *parsed.Confidence / 100
		parsed.Confidence = &normalized
	}
	return AIImageSynthIDResult{
		Checked:    true,
		Detected:   parsed.Detected,
		Confidence: parsed.Confidence,
		Message:    strings.TrimSpace(parsed.Message),
	}, nil
}

// aiImageMetadataBlock 是从图片里抠出来的一段元数据。Key 只有 PNG 文本块有。
type aiImageMetadataBlock struct {
	Source string
	Key    string
	Data   []byte
}

// detectAIImageMarkers 在本地解析图片元数据，找 AI 生成标识。
//
// 按格式只取元数据段而不是整文件扫：像素数据里碰巧凑出一个关键词的概率虽小，
// 但结论是要给人看的，能不冒这个险就不冒。
func detectAIImageMarkers(image []byte) AIImageReport {
	format := sniffAIImageFormat(image)
	var blocks []aiImageMetadataBlock
	switch format {
	case "png":
		blocks = pngAIImageMetadata(image)
	case "jpeg":
		blocks = jpegAIImageMetadata(image)
	case "webp":
		blocks = webpAIImageMetadata(image)
	default:
		raw := image
		if len(raw) > aiImageMaxRawScanBytes {
			raw = raw[:aiImageMaxRawScanBytes]
		}
		blocks = []aiImageMetadataBlock{{Source: "文件内容", Data: raw}}
	}

	report := AIImageReport{Format: format}
	seen := map[string]bool{}
	for _, block := range blocks {
		for _, finding := range aiImageFindingsInBlock(block) {
			key := finding.Kind + "|" + finding.Evidence
			if seen[key] {
				continue
			}
			seen[key] = true
			report.Findings = append(report.Findings, finding)
		}
	}
	sort.SliceStable(report.Findings, func(i, j int) bool {
		return report.Findings[i].Strong && !report.Findings[j].Strong
	})
	report.Verdict = aiImageVerdict(report.Findings)
	report.NoMetadata = format != "unknown" && len(blocks) == 0
	return report
}

func aiImageVerdict(findings []AIImageFinding) string {
	verdict := aiImageVerdictNone
	for _, finding := range findings {
		if finding.Strong {
			return aiImageVerdictMarked
		}
		verdict = aiImageVerdictHint
	}
	return verdict
}

func sniffAIImageFormat(data []byte) string {
	switch {
	case bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")):
		return "png"
	case bytes.HasPrefix(data, []byte{0xFF, 0xD8}):
		return "jpeg"
	case len(data) >= 12 && bytes.Equal(data[:4], []byte("RIFF")) && bytes.Equal(data[8:12], []byte("WEBP")):
		return "webp"
	case bytes.HasPrefix(data, []byte("GIF8")):
		return "gif"
	case len(data) >= 12 && bytes.Equal(data[4:8], []byte("ftyp")):
		brand := string(data[8:12])
		if strings.HasPrefix(brand, "avi") {
			return "avif"
		}
		return "heif"
	}
	return "unknown"
}

func aiImageFormatExtension(format string) string {
	switch format {
	case "jpeg":
		return ".jpg"
	case "unknown":
		return ".bin"
	}
	return "." + format
}

// pngAIImageMetadata 取 PNG 的文本块、EXIF 块和 C2PA（caBX）块。
func pngAIImageMetadata(data []byte) []aiImageMetadataBlock {
	var blocks []aiImageMetadataBlock
	offset := 8
	for offset+12 <= len(data) {
		length := int(binary.BigEndian.Uint32(data[offset : offset+4]))
		chunkType := string(data[offset+4 : offset+8])
		start := offset + 8
		if length < 0 || start+length+4 > len(data) {
			break
		}
		chunk := data[start : start+length]
		offset = start + length + 4
		switch chunkType {
		case "tEXt":
			key, text, ok := bytes.Cut(chunk, []byte{0})
			if ok {
				blocks = append(blocks, aiImageMetadataBlock{Source: "PNG 文本 " + string(key), Key: string(key), Data: text})
			}
		case "zTXt":
			key, rest, ok := bytes.Cut(chunk, []byte{0})
			if ok && len(rest) > 1 {
				if text, err := inflateAIImageMetadata(rest[1:]); err == nil {
					blocks = append(blocks, aiImageMetadataBlock{Source: "PNG 文本 " + string(key), Key: string(key), Data: text})
				}
			}
		case "iTXt":
			if block, ok := pngInternationalText(chunk); ok {
				blocks = append(blocks, block)
			}
		case "eXIf":
			blocks = append(blocks, aiImageMetadataBlock{Source: "EXIF", Data: chunk})
		case "caBX":
			blocks = append(blocks, aiImageMetadataBlock{Source: "C2PA", Data: chunk})
		case "IEND":
			return blocks
		}
	}
	return blocks
}

// pngInternationalText 解 iTXt：key\0 压缩标志 压缩方法 语言\0 译名\0 正文。
func pngInternationalText(chunk []byte) (aiImageMetadataBlock, bool) {
	key, rest, ok := bytes.Cut(chunk, []byte{0})
	if !ok || len(rest) < 2 {
		return aiImageMetadataBlock{}, false
	}
	compressed := rest[0] == 1
	rest = rest[2:]
	_, rest, ok = bytes.Cut(rest, []byte{0})
	if !ok {
		return aiImageMetadataBlock{}, false
	}
	_, text, ok := bytes.Cut(rest, []byte{0})
	if !ok {
		return aiImageMetadataBlock{}, false
	}
	if compressed {
		inflated, err := inflateAIImageMetadata(text)
		if err != nil {
			return aiImageMetadataBlock{}, false
		}
		text = inflated
	}
	source := "PNG 文本 " + string(key)
	if string(key) == "XML:com.adobe.xmp" {
		source = "XMP"
	}
	return aiImageMetadataBlock{Source: source, Key: string(key), Data: text}, true
}

func inflateAIImageMetadata(compressed []byte) ([]byte, error) {
	reader, err := zlib.NewReader(bytes.NewReader(compressed))
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	return io.ReadAll(io.LimitReader(reader, aiImageMaxMetadataBytes))
}

// jpegAIImageMetadata 取 SOS 之前的 APPn 和 COM 段：EXIF、XMP 在 APP1，
// C2PA 的 JUMBF 在 APP11。
func jpegAIImageMetadata(data []byte) []aiImageMetadataBlock {
	var blocks []aiImageMetadataBlock
	offset := 2
	for offset+4 <= len(data) {
		if data[offset] != 0xFF {
			break
		}
		marker := data[offset+1]
		if marker == 0xFF {
			offset++
			continue
		}
		if marker == 0xD9 || marker == 0xDA {
			break
		}
		if marker == 0x01 || (marker >= 0xD0 && marker <= 0xD7) {
			offset += 2
			continue
		}
		length := int(binary.BigEndian.Uint16(data[offset+2 : offset+4]))
		if length < 2 || offset+2+length > len(data) {
			break
		}
		segment := data[offset+4 : offset+2+length]
		offset += 2 + length
		switch {
		case marker == 0xE0:
			// APP0 是 JFIF/JFXX 头，没有可检测的内容；算作元数据会让「完全没有
			// 元数据」这条提示永远不出现。
			continue
		case marker == 0xE1 && bytes.HasPrefix(segment, []byte("Exif\x00")):
			blocks = append(blocks, aiImageMetadataBlock{Source: "EXIF", Data: segment})
		case marker == 0xE1 && bytes.Contains(segment[:min(len(segment), 64)], []byte("ns.adobe.com/xap")):
			blocks = append(blocks, aiImageMetadataBlock{Source: "XMP", Data: segment})
		case marker == 0xEB:
			// APP11 装的是 JUMBF，C2PA 清单是其中一种。
			blocks = append(blocks, aiImageMetadataBlock{Source: "C2PA", Data: segment})
		case marker == 0xED:
			blocks = append(blocks, aiImageMetadataBlock{Source: "IPTC", Data: segment})
		case marker == 0xFE:
			blocks = append(blocks, aiImageMetadataBlock{Source: "JPEG 注释", Data: segment})
		case marker >= 0xE1 && marker <= 0xEF:
			blocks = append(blocks, aiImageMetadataBlock{Source: fmt.Sprintf("APP%d", marker-0xE0), Data: segment})
		}
	}
	return blocks
}

// webpAIImageMetadata 取 RIFF 里除图像数据以外的块。
func webpAIImageMetadata(data []byte) []aiImageMetadataBlock {
	var blocks []aiImageMetadataBlock
	offset := 12
	for offset+8 <= len(data) {
		fourCC := string(data[offset : offset+4])
		length := int(binary.LittleEndian.Uint32(data[offset+4 : offset+8]))
		start := offset + 8
		if length < 0 || start+length > len(data) {
			break
		}
		chunk := data[start : start+length]
		offset = start + length + length%2
		switch fourCC {
		case "VP8 ", "VP8L", "VP8X", "ALPH", "ANIM", "ANMF", "ICCP":
			continue
		case "XMP ":
			blocks = append(blocks, aiImageMetadataBlock{Source: "XMP", Data: chunk})
		case "EXIF":
			blocks = append(blocks, aiImageMetadataBlock{Source: "EXIF", Data: chunk})
		case "C2PA":
			blocks = append(blocks, aiImageMetadataBlock{Source: "C2PA", Data: chunk})
		default:
			blocks = append(blocks, aiImageMetadataBlock{Source: "WebP " + strings.TrimSpace(fourCC), Data: chunk})
		}
	}
	return blocks
}

// aiImageGenerators 是只做 AI 生成的工具名：元数据里出现就足以说明来源。
// OpenAI、Gemini 这类名字另算弱线索——它们也出现在普通编辑记录里。
var aiImageGenerators = []struct {
	needle string
	name   string
	strong bool
}{
	{"midjourney", "Midjourney", true},
	{"dall-e", "DALL·E", true},
	{"dall·e", "DALL·E", true},
	{"adobe firefly", "Adobe Firefly", true},
	{"novelai", "NovelAI", true},
	{"invokeai", "InvokeAI", true},
	{"stable diffusion", "Stable Diffusion", true},
	{"stablediffusion", "Stable Diffusion", true},
	{"comfyui", "ComfyUI", true},
	{"leonardo.ai", "Leonardo.Ai", true},
	{"ideogram", "Ideogram", true},
	{"bing image creator", "Bing Image Creator", true},
	{"microsoft designer", "Microsoft Designer", true},
	{"dreamina", "即梦", true},
	{"即梦", "即梦", true},
	{"通义万相", "通义万相", true},
	{"文心一格", "文心一格", true},
	{"gpt-image", "OpenAI GPT Image", true},
	{"openai", "OpenAI", false},
	{"chatgpt", "ChatGPT", false},
	{"google imagen", "Google Imagen", false},
	{"gemini", "Google Gemini", false},
	{"豆包", "豆包", false},
}

// aiImageFindingsInBlock 对一段元数据跑全部规则。
func aiImageFindingsInBlock(block aiImageMetadataBlock) []AIImageFinding {
	lower := strings.ToLower(string(block.Data))
	source := block.Source
	var findings []AIImageFinding
	add := func(kind, evidence string, strong bool) {
		findings = append(findings, AIImageFinding{Kind: kind, Source: source, Evidence: evidence, Strong: strong})
	}

	if strings.Contains(lower, "synthid") {
		add("synthid", "元数据声明含 SynthID 水印", true)
	}
	if strings.Contains(lower, "made with google ai") || strings.Contains(lower, "edited with google ai") {
		add("google_ai", "Google「Made with Google AI」标注（这类图同时嵌有 SynthID 水印）", true)
	}

	// IPTC 数字来源类型。compositeWithTrainedAlgorithmicMedia 里也含
	// trainedAlgorithmicMedia，得看前缀区分「整张生成」和「AI 参与编辑」。
	for index := 0; ; {
		found := strings.Index(lower[index:], "trainedalgorithmicmedia")
		if found < 0 {
			break
		}
		at := index + found
		if strings.HasSuffix(lower[:at], "compositewith") {
			add("iptc", "IPTC 数字来源类型：AI 参与合成或编辑（compositeWithTrainedAlgorithmicMedia）", true)
		} else {
			add("iptc", "IPTC 数字来源类型：AI 生成（trainedAlgorithmicMedia）", true)
		}
		index = at + len("trainedalgorithmicmedia")
	}
	if aiImageContainsStandalone(lower, "algorithmicmedia", "trained") {
		add("iptc", "IPTC 数字来源类型：算法生成（algorithmicMedia，不一定是 AI 模型）", false)
	}
	if strings.Contains(lower, "compositesynthetic") {
		add("iptc", "IPTC 数字来源类型：含合成元素（compositeSynthetic）", false)
	}

	if strings.Contains(lower, "c2pa") && (block.Source == "C2PA" || strings.Contains(lower, "jumb") || strings.Contains(lower, "c2pa.claim") || strings.Contains(lower, "c2pa.actions")) {
		add("c2pa", "带 C2PA 内容凭证（Content Credentials），生成或编辑工具签过名", false)
	}

	// 《人工智能生成合成内容标识办法》的隐式标识：AIGC 字段里写 Label、ContentProducer 等。
	if at := strings.Index(lower, "aigc"); at >= 0 {
		window := lower[at:min(len(lower), at+512)]
		if strings.Contains(window, "label") || strings.Contains(window, "contentproducer") {
			add("aigc_label", "AIGC 隐式标识（《人工智能生成合成内容标识办法》）", true)
		}
	}

	// 本地出图工具写进去的生成参数。
	if strings.Contains(lower, "negative prompt:") || (strings.Contains(lower, "steps:") && strings.Contains(lower, "sampler:") && strings.Contains(lower, "cfg scale:")) {
		add("generator", "Stable Diffusion WebUI 生成参数", true)
	}
	if strings.Contains(lower, "class_type") && (strings.Contains(lower, "ksampler") || strings.Contains(lower, "checkpointloader")) {
		add("generator", "ComfyUI 工作流", true)
	}
	for _, generator := range aiImageGenerators {
		if !strings.Contains(lower, generator.needle) {
			continue
		}
		if generator.strong {
			add("generator", "生成工具："+generator.name, true)
		} else {
			add("generator", "出现 "+generator.name+" 相关字样", false)
		}
	}
	return findings
}

// aiImageContainsStandalone 判断 needle 出现过、且至少有一次前面不是 prefix。
func aiImageContainsStandalone(text, needle, prefix string) bool {
	for index := 0; ; {
		found := strings.Index(text[index:], needle)
		if found < 0 {
			return false
		}
		at := index + found
		if !strings.HasSuffix(text[:at], prefix) {
			return true
		}
		index = at + len(needle)
	}
}
