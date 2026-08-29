// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
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
	imageOCRBackendLocal    = "local"
	imageOCRBackendHTTP     = "http"
	// 交付方式：默认图片和识别文字一起给对话模型；「仅文字」把图片从消息里
	// 摘掉、换成识别文本，让不支持看图的对话模型也能处理图片消息（消息里没
	// 有图片后也不会再切到 vision 分组）。
	imageOCRDeliveryAttach = "attach"
	imageOCRDeliveryText   = "text"
	imageOCRNoTextMarker   = "[无可辨文字]"
	imageOCRCacheCap       = 128
	imageOCRPerImageMax    = 2000
	// 识别结果按图片内容哈希落库，同一张表情包在不同人、不同群、重启之后
	// 都只识别一次。kind 区分「文字转写」和「画面描述」。
	imageRecognitionKindOCR      = "ocr"
	imageRecognitionKindDescribe = "describe"
)

// ImageRecognitionRecord 是一张图在某套识别配置下的结果。
type ImageRecognitionRecord struct {
	CacheKey      string `json:"cache_key"`
	ContentSHA256 string `json:"content_sha256"`
	Kind          string `json:"kind"`
	Backend       string `json:"backend"`
	Model         string `json:"model,omitempty"`
	Text          string `json:"text"`
	CreatedAt     int64  `json:"created_at"`
}

type ImageRecognitionStore interface {
	LoadImageRecognition(context.Context, string) (ImageRecognitionRecord, bool, error)
	SaveImageRecognition(context.Context, ImageRecognitionRecord) error
}

type ImageOCRPlugin struct {
	client *http.Client
	mu     sync.Mutex
	cache  map[string]string
	order  []string
}

func NewImageOCRPlugin(client *http.Client) *ImageOCRPlugin {
	if client == nil {
		client = &http.Client{}
	}
	return &ImageOCRPlugin{client: client, cache: make(map[string]string)}
}

func (p *ImageOCRPlugin) Manifest() PluginManifest {
	return PluginManifest{
		ID: imageOCRPluginID, Name: "图片文字识别", Version: "0.1.0",
		Description: "聊天图片进入上下文前先做一次文字转写（OCR），识别结果随图片一起交给对话模型；对话模型读图上文字能力弱时可作补充。转写可走提供商配置里的 vision 分组、自托管的 PaddleOCR/RapidOCR 等传统 OCR 服务，或本地 tesseract 命令，后两者完全离线。对话模型不支持看图时，可把交付方式改为「仅识别文字」：图片不再交给对话模型，改为 vision 分组的画面描述加 OCR 文字（两者可各自开关，组合或单用）。识别结果按图片内容哈希缓存并落库，同一张图或表情包在不同人、不同群、重启之后都只识别一次。",
		Official:    true, BuiltIn: true,
		Permissions: []string{"message:read", "llm:generate"},
		Settings: []PluginSettingSpec{
			{Key: "backend", Label: "文字转写方式", Type: PluginSettingTypeSelect, Default: imageOCRBackendDisabled, Options: []PluginSettingOption{
				{Value: imageOCRBackendDisabled, Label: "关闭"},
				{Value: imageOCRBackendLLM, Label: "LLM 视觉转写（vision 分组）"},
				{Value: imageOCRBackendHTTP, Label: "OCR 服务接口（PaddleOCR/RapidOCR 等，离线）"},
				{Value: imageOCRBackendLocal, Label: "本地命令（tesseract 等，离线）"},
			}},
			{Key: "delivery", Label: "图片交付方式", Type: PluginSettingTypeSelect, Default: imageOCRDeliveryAttach, Options: []PluginSettingOption{
				{Value: imageOCRDeliveryAttach, Label: "图片和识别文字一起给对话模型（需支持看图）"},
				{Value: imageOCRDeliveryText, Label: "仅识别文字：不把图片给对话模型（对话模型不支持看图）"},
			}},
			{Key: "describe_enabled", Label: "仅文字模式下补充画面描述（vision 分组模型）", Type: PluginSettingTypeBool, Default: true},
			{Key: "model", Label: "指定模型", Type: PluginSettingTypeString, Default: ""},
			{Key: "http_endpoint", Label: "OCR 服务地址", Type: PluginSettingTypeString, Default: ""},
			{Key: "http_api_key", Label: "OCR 服务 API Key", Type: PluginSettingTypeString, Default: "", Secret: true},
			{Key: "local_command", Label: "本地命令", Type: PluginSettingTypeString, Default: "tesseract"},
			{Key: "local_languages", Label: "本地识别语言", Type: PluginSettingTypeString, Default: "chi_sim+eng"},
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
	Backend         string
	Delivery        string
	DescribeEnabled bool
	Model           string
	HTTPEndpoint    string
	HTTPAPIKey      string
	LocalCommand    string
	LocalLanguages  string
	MaxImages       int
	Timeout         time.Duration
	PrivateEnabled  bool
}

// ocrEnabled 表示配置了可用的文字转写后端。
func (cfg imageOCRConfig) ocrEnabled() bool {
	return cfg.Backend == imageOCRBackendLLM || cfg.Backend == imageOCRBackendLocal || cfg.Backend == imageOCRBackendHTTP
}

func (cfg imageOCRConfig) textOnly() bool {
	return cfg.Delivery == imageOCRDeliveryText
}

// active 表示插件对图片有事可做：有 OCR 后端，或仅文字模式下开了画面描述。
func (cfg imageOCRConfig) active() bool {
	return cfg.ocrEnabled() || (cfg.textOnly() && cfg.DescribeEnabled)
}

func imageOCRConfigFromSettings(v SettingValues) imageOCRConfig {
	return imageOCRConfig{
		Backend:         v.String("backend", imageOCRBackendDisabled),
		Delivery:        v.String("delivery", imageOCRDeliveryAttach),
		DescribeEnabled: v.Bool("describe_enabled", true),
		Model:           strings.TrimSpace(v.String("model", "")),
		HTTPEndpoint:    strings.TrimSpace(v.String("http_endpoint", "")),
		HTTPAPIKey:      strings.TrimSpace(v.String("http_api_key", "")),
		LocalCommand:    strings.TrimSpace(v.String("local_command", "tesseract")),
		LocalLanguages:  strings.TrimSpace(v.String("local_languages", "chi_sim+eng")),
		MaxImages:       v.Int("max_images", 3),
		Timeout:         time.Duration(v.Int("timeout_seconds", 45)) * time.Second,
		PrivateEnabled:  v.Bool("private_enabled", true),
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

// imageOCRContentDigest 取图片的内容哈希。上下文里的图片在 loadLLMImageURLs
// 之后都是 data URL，解出字节再哈希，得到的值和入库时的 content_sha256 一致；
// 拿不到字节的（远程 URL）退回按地址哈希，只是复用范围窄一些。
func imageOCRContentDigest(imageURL string) string {
	if data, _, err := imageOCRDataURLBytes(imageURL); err == nil {
		sum := sha256.Sum256(data)
		return hex.EncodeToString(sum[:])
	}
	sum := sha256.Sum256([]byte("url:" + strings.TrimSpace(imageURL)))
	return hex.EncodeToString(sum[:])
}

// imageOCRCacheKey 把识别配置也算进键里：换了后端、模型或识别语言，结果就
// 该重算，不能拿上一套引擎的输出顶上。
func imageOCRCacheKey(kind, contentDigest string, cfg imageOCRConfig) string {
	parts := []string{kind, contentDigest, cfg.Model}
	if kind == imageRecognitionKindOCR {
		parts = append(parts, cfg.Backend, cfg.LocalCommand, cfg.LocalLanguages, cfg.HTTPEndpoint)
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "|")))
	return hex.EncodeToString(sum[:])
}

func (r *Runtime) imageRecognitionStore() ImageRecognitionStore {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	store, _ := r.messageStore.(ImageRecognitionStore)
	return store
}

// cachedRecognition 先查进程内缓存，再查持久化记录；命中持久化时回填内存。
func (r *Runtime) cachedRecognition(ctx context.Context, plugin *ImageOCRPlugin, cacheKey string) (string, bool) {
	if text, ok := plugin.cachedTranscript(cacheKey); ok {
		return text, true
	}
	store := r.imageRecognitionStore()
	if store == nil {
		return "", false
	}
	record, found, err := store.LoadImageRecognition(ctx, cacheKey)
	if err != nil || !found {
		return "", false
	}
	plugin.storeTranscript(cacheKey, record.Text)
	return record.Text, true
}

func (r *Runtime) storeRecognition(ctx context.Context, plugin *ImageOCRPlugin, cacheKey, kind, digest, text string, cfg imageOCRConfig) {
	plugin.storeTranscript(cacheKey, text)
	store := r.imageRecognitionStore()
	if store == nil || text == "" {
		return
	}
	backend := cfg.Backend
	if kind == imageRecognitionKindDescribe {
		backend = imageOCRBackendLLM
	}
	// 缓存写失败不该影响这次回复，识别结果已经拿到了。
	_ = store.SaveImageRecognition(ctx, ImageRecognitionRecord{
		CacheKey:      cacheKey,
		ContentSHA256: digest,
		Kind:          kind,
		Backend:       backend,
		Model:         cfg.Model,
		Text:          text,
		CreatedAt:     time.Now().Unix(),
	})
}

// imageOCRActiveConfig 取插件实例和已生效的配置；未启用、无事可做或该场景被
// 关掉时 ok 为 false。
func (r *Runtime) imageOCRActiveConfig(event MessageEvent) (*ImageOCRPlugin, imageOCRConfig, bool) {
	if r.plugins == nil {
		return nil, imageOCRConfig{}, false
	}
	pluginValue, settings, enabled := r.plugins.PluginWithSettings(imageOCRPluginID, r.pluginOverridesForEvent(event))
	plugin, ok := pluginValue.(*ImageOCRPlugin)
	if !enabled || !ok {
		return nil, imageOCRConfig{}, false
	}
	cfg := imageOCRConfigFromSettings(settings)
	if !cfg.active() {
		return nil, imageOCRConfig{}, false
	}
	if event.Kind == EventKindPrivate && !cfg.PrivateEnabled {
		return nil, imageOCRConfig{}, false
	}
	return plugin, cfg, true
}

// imageOCRMessageImageURLs 收集消息里的图片，按配置截断到单条上限。
func imageOCRMessageImageURLs(cfg imageOCRConfig, message llm.Message) (urls []string, total int) {
	for _, part := range message.Parts {
		if part.Type == llm.ContentPartImageURL && strings.TrimSpace(part.ImageURL) != "" {
			urls = append(urls, part.ImageURL)
		}
	}
	total = len(urls)
	if cfg.MaxImages > 0 && len(urls) > cfg.MaxImages {
		urls = urls[:cfg.MaxImages]
	}
	return urls, total
}

// imageOCRAdjustMessage 按插件配置处理当前消息里的图片：默认在图片之外附加
// 文字转写；「仅文字」模式把图片从消息里摘掉，换成画面描述与转写文本，让不
// 支持看图的对话模型也能处理图片消息。识别失败只影响对应图片，绝不阻断回复。
func (r *Runtime) imageOCRAdjustMessage(ctx context.Context, event MessageEvent, message llm.Message) llm.Message {
	plugin, cfg, ok := r.imageOCRActiveConfig(event)
	if !ok {
		return message
	}
	if !cfg.textOnly() {
		if notice := r.imageOCRAttachNotice(ctx, event, plugin, cfg, message); notice != "" {
			message = appendLLMMessageText(message, notice)
		}
		return message
	}

	imageURLs, total := imageOCRMessageImageURLs(cfg, message)
	if total == 0 {
		return message
	}
	var lines []string
	for index, imageURL := range imageURLs {
		var segments []string
		if cfg.DescribeEnabled {
			if description := r.describeContextImage(ctx, event, plugin, cfg, imageURL); description != "" {
				segments = append(segments, "画面描述："+description)
			}
		}
		if cfg.ocrEnabled() {
			if text := r.transcribeContextImage(ctx, event, plugin, cfg, imageURL); text != "" && text != imageOCRNoTextMarker {
				segments = append(segments, "图中文字：\n"+text)
			}
		}
		entry := strings.Join(segments, "\n")
		if entry == "" {
			// 识别不出来也要占位：模型至少要知道这里有一张图，别凭空脑补。
			entry = "（未能识别出这张图的内容）"
		}
		if total == 1 {
			lines = append(lines, entry)
		} else {
			lines = append(lines, fmt.Sprintf("第 %d 张图：\n%s", index+1, entry))
		}
	}
	if total > len(imageURLs) {
		lines = append(lines, fmt.Sprintf("（另有 %d 张图超出单条识别上限，未识别）", total-len(imageURLs)))
	}
	// 结尾这句是必须的：识别文本本身长得就像一段现成的答复，模型很容易原样发出去，
	// 用户发张表情包只会收到一段图解。
	block := "【图片消息】对话模型未直接查看图片，以下为机器识别内容（可能有误），只供你理解这张图，不要复述、转述或改写给用户：\n" + strings.Join(lines, "\n\n")
	message = stripLLMImageParts(message)
	return appendLLMMessageText(message, block)
}

// stripLLMImageParts 把消息里的图片段全部去掉（含视频抽帧），文本等其他段保留。
func stripLLMImageParts(message llm.Message) llm.Message {
	parts := make([]llm.ContentPart, 0, len(message.Parts))
	for _, part := range message.Parts {
		if part.Type == llm.ContentPartImageURL {
			continue
		}
		parts = append(parts, part)
	}
	message.Parts = parts
	return message
}

// imageOCRContextText 为当前消息里的图片生成随图附带的转写文本；没有可用后端、
// 没有图片或全部无可辨文字时返回空串。
func (r *Runtime) imageOCRContextText(ctx context.Context, event MessageEvent, message llm.Message) string {
	plugin, cfg, ok := r.imageOCRActiveConfig(event)
	if !ok {
		return ""
	}
	return r.imageOCRAttachNotice(ctx, event, plugin, cfg, message)
}

func (r *Runtime) imageOCRAttachNotice(ctx context.Context, event MessageEvent, plugin *ImageOCRPlugin, cfg imageOCRConfig, message llm.Message) string {
	if !cfg.ocrEnabled() {
		return ""
	}
	imageURLs, total := imageOCRMessageImageURLs(cfg, message)
	if total == 0 {
		return ""
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
	digest := imageOCRContentDigest(imageURL)
	cacheKey := imageOCRCacheKey(imageRecognitionKindOCR, digest, cfg)
	if text, ok := r.cachedRecognition(ctx, plugin, cacheKey); ok {
		return text
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 45 * time.Second
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var text string
	var err error
	switch cfg.Backend {
	case imageOCRBackendLocal:
		text, err = localImageOCRTranscription(callCtx, cfg, imageURL)
	case imageOCRBackendHTTP:
		text, err = plugin.httpImageOCRTranscription(callCtx, cfg, imageURL)
	default:
		text, err = r.llmImageOCRTranscription(callCtx, event, cfg, imageURL)
	}
	if err != nil {
		// 转写只是辅助信息，失败不值得打断回复，也不缓存失败结果。
		return ""
	}
	text = sanitizeFileTextString(text, imageOCRPerImageMax)
	if text == "" {
		text = imageOCRNoTextMarker
	}
	r.storeRecognition(ctx, plugin, cacheKey, imageRecognitionKindOCR, digest, text, cfg)
	return text
}

// describeContextImage 在仅文字模式下用 vision 分组模型生成画面描述。与转写
// 共用一套缓存（前缀区分），失败同样不缓存、不阻断回复。
func (r *Runtime) describeContextImage(ctx context.Context, event MessageEvent, plugin *ImageOCRPlugin, cfg imageOCRConfig, imageURL string) string {
	digest := imageOCRContentDigest(imageURL)
	cacheKey := imageOCRCacheKey(imageRecognitionKindDescribe, digest, cfg)
	if text, ok := r.cachedRecognition(ctx, plugin, cacheKey); ok {
		return text
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 45 * time.Second
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	text, err := r.llmImageDescription(callCtx, event, cfg, imageURL)
	if err != nil {
		return ""
	}
	text = sanitizeFileTextString(text, imageOCRPerImageMax)
	r.storeRecognition(ctx, plugin, cacheKey, imageRecognitionKindDescribe, digest, text, cfg)
	return text
}

func (r *Runtime) llmImageDescription(callCtx context.Context, event MessageEvent, cfg imageOCRConfig, imageURL string) (string, error) {
	callCtx = withLLMUsagePurpose(withLLMUsageContext(callCtx, event), "image_describe")
	systemPrompt := `你是图片理解子代理。对话主模型无法直接查看图片，请客观描述这张图片，让只读文字的人能明白图里是什么。

要求：
- 描述画面主体、场景、动作与显著细节，聊天截图说明谁在说什么，表情包说明表达的情绪。
- 不要臆测图片外的信息，不确定就说不确定。
- 用中文陈述，控制在 200 字以内，不使用 Markdown 代码块。`
	if cfg.ocrEnabled() {
		systemPrompt += "\n- 图中文字另有 OCR 转写，不必逐字抄录，点到关键文字即可。"
	} else {
		systemPrompt += "\n- 图中若有关键文字，请一并转述出来。"
	}
	prompt := "请描述这张图片的内容。"
	return r.runLLMProviderForGroup(callCtx, llm.GroupVision, func(client LLMProvider) (string, error) {
		resp, err := client.Generate(callCtx, llm.GenerateRequest{
			Model: cfg.Model,
			Messages: []llm.Message{
				{Role: llm.RoleSystem, Content: strings.TrimSpace(systemPrompt)},
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
		return resp.Text, nil
	})
}

// localImageOCRTranscription 用本地 OCR 命令完全离线转写。命令按 tesseract 的
// 约定调用：`<command> <图片文件> stdout -l <languages>`；其他 OCR 工具做一层
// 同参数的包装脚本即可接入。
func localImageOCRTranscription(ctx context.Context, cfg imageOCRConfig, imageURL string) (string, error) {
	if cfg.LocalCommand == "" {
		return "", errors.New("image ocr: local command is empty")
	}
	data, ext, err := imageOCRDataURLBytes(imageURL)
	if err != nil {
		return "", err
	}
	dir, err := os.MkdirTemp("", "diana-image-ocr-*")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	path := filepath.Join(dir, "image"+ext)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return "", err
	}
	args := []string{path, "stdout"}
	if cfg.LocalLanguages != "" {
		args = append(args, "-l", cfg.LocalLanguages)
	}
	// tesseract 会把警告写到 stderr，只取 stdout 免得混进转写文本。
	out, err := exec.CommandContext(ctx, cfg.LocalCommand, args...).Output()
	if err != nil {
		detail := ""
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			detail = ": " + strings.TrimSpace(string(exitErr.Stderr))
		}
		return "", fmt.Errorf("image ocr: local command: %w%s", err, detail)
	}
	return strings.TrimSpace(string(out)), nil
}

// httpImageOCRTranscription 调自托管的传统 OCR 模型服务（PaddleOCR hub
// serving、RapidOCR api 等）。请求统一为 JSON {"images": ["<base64>"]}；响应
// 结构各家不一，按通用方式递归收集 text / rec_txt / transcription / words
// 字段拼成行——足够覆盖 Paddle 系服务的返回格式。
func (p *ImageOCRPlugin) httpImageOCRTranscription(ctx context.Context, cfg imageOCRConfig, imageURL string) (string, error) {
	if cfg.HTTPEndpoint == "" {
		return "", errors.New("image ocr: http endpoint is empty")
	}
	data, _, err := imageOCRDataURLBytes(imageURL)
	if err != nil {
		return "", err
	}
	payload, err := json.Marshal(map[string]any{"images": []string{base64.StdEncoding.EncodeToString(data)}})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.HTTPEndpoint, bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if cfg.HTTPAPIKey != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.HTTPAPIKey)
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("image ocr: http service status %d", resp.StatusCode)
	}
	var parsed any
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", fmt.Errorf("image ocr: http service returned non-JSON: %w", err)
	}
	var texts []string
	collectOCRTexts(parsed, &texts)
	// 收不到任何文字字段按「无可辨文字」处理：不少服务对空图就是返回空结果。
	return strings.Join(texts, "\n"), nil
}

// collectOCRTexts 递归收集常见 OCR 响应里的文字字段。同一个对象里最多命中
// 一个字段；数组保持原顺序，对象键的遍历顺序不保证——实际服务的文字都挂在
// 同一个数组下，够用。
func collectOCRTexts(node any, out *[]string) {
	switch value := node.(type) {
	case map[string]any:
		for _, key := range []string{"text", "rec_txt", "transcription", "words"} {
			if s, ok := value[key].(string); ok && strings.TrimSpace(s) != "" {
				*out = append(*out, strings.TrimSpace(s))
				break
			}
		}
		for _, child := range value {
			if _, isString := child.(string); isString {
				continue
			}
			collectOCRTexts(child, out)
		}
	case []any:
		for _, child := range value {
			collectOCRTexts(child, out)
		}
	}
}

// imageOCRDataURLBytes 解出 base64 data URL 的原始图片字节和扩展名。上下文里的
// 图片在 loadLLMImageURLs 之后都是 data URL；不是的直接跳过。
func imageOCRDataURLBytes(imageURL string) ([]byte, string, error) {
	prefix, encoded, ok := strings.Cut(strings.TrimSpace(imageURL), ",")
	lower := strings.ToLower(prefix)
	if !ok || !strings.HasPrefix(lower, "data:image/") || !strings.Contains(lower, ";base64") {
		return nil, "", errors.New("image ocr: image is not a base64 data URL")
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, "", err
	}
	ext := ".png"
	switch {
	case strings.HasPrefix(lower, "data:image/jpeg"), strings.HasPrefix(lower, "data:image/jpg"):
		ext = ".jpg"
	case strings.HasPrefix(lower, "data:image/webp"):
		ext = ".webp"
	case strings.HasPrefix(lower, "data:image/gif"):
		ext = ".gif"
	}
	return data, ext, nil
}

func (r *Runtime) llmImageOCRTranscription(callCtx context.Context, event MessageEvent, cfg imageOCRConfig, imageURL string) (string, error) {
	callCtx = withLLMUsagePurpose(withLLMUsageContext(callCtx, event), "image_ocr")
	prompt := "请完整转写这张图片里的文字。"
	return r.runLLMProviderForGroup(callCtx, llm.GroupVision, func(client LLMProvider) (string, error) {
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
		return resp.Text, nil
	})
}
