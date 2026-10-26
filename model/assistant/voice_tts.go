// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/SuInk/diana/model/agent"
	"github.com/SuInk/diana/model/llm"
)

// 语音合成以前是一个插件，GPT-SoVITS 的地址、参考音频都在插件设置里；现在服务
// 本身归模型分配里的「语音合成」插槽管，插件拆掉了。插槽配了，tts 工具就挂上；
// 发送模式和单次字数这两项不是服务的属性，作为插槽参数和音色、格式放在一起。
const (
	voiceTTSToolName = "tts"

	// 插槽参数里给发送层用的两项。
	mediaParamSendMode = "send_mode"
	mediaParamMaxChars = "max_chars"

	// voiceTTSSendModeOnDemand 是默认：模型判断用户要语音时才调 tts 工具。
	voiceTTSSendModeOnDemand = "on_demand"
	// voiceTTSSendModeAlways 是常驻语音：每条能念出来的回复都在发送前合成成语音。
	voiceTTSSendModeAlways = "always"

	defaultVoiceTTSMaxChars = 500
	defaultVoiceTTSMediaTTL = 10 * time.Minute
)

type dianaTTSTool struct {
	runtime *Runtime
	event   MessageEvent
}

type voiceTTSResult struct {
	OK       bool   `json:"ok"`
	Action   string `json:"action"`
	CQRecord string `json:"cq_record"`
	Text     string `json:"text"`
}

// voiceTTSSettings 是插槽参数里给发送层用的部分。
type voiceTTSSettings struct {
	SendMode string
	MaxChars int
}

// voiceTTSSettingsFromParams 读插槽参数；没配或写错时落到默认值。
func voiceTTSSettingsFromParams(params map[string]string) voiceTTSSettings {
	settings := voiceTTSSettings{SendMode: voiceTTSSendModeOnDemand, MaxChars: defaultVoiceTTSMaxChars}
	if strings.TrimSpace(params[mediaParamSendMode]) == voiceTTSSendModeAlways {
		settings.SendMode = voiceTTSSendModeAlways
	}
	if value, err := strconv.Atoi(strings.TrimSpace(params[mediaParamMaxChars])); err == nil && value > 0 {
		settings.MaxChars = value
	}
	return settings
}

// voiceTTSToolForEvent 取这条消息能用的语音合成工具。只在 OneBot 上发得出 QQ 语音，
// 其他平台和插槽没配时都没有。
func (r *Runtime) voiceTTSToolForEvent(ctx context.Context, event MessageEvent) (*dianaTTSTool, voiceTTSSettings, bool) {
	// 通话里回复本来就会被念出来，再挂 tts 工具只会多发一条语音文件。
	if event.voiceCall || !IsOneBotPlatform(r.currentPlatform(event)) {
		return nil, voiceTTSSettings{}, false
	}
	routes := r.mediaSlotRoutes(withModelConfigEvent(ctx, event), mediaSlotTTS)
	if len(routes) == 0 {
		return nil, voiceTTSSettings{}, false
	}
	return &dianaTTSTool{runtime: r, event: event}, voiceTTSSettingsFromParams(routes[0].Params), true
}

func (t *dianaTTSTool) Name() string {
	return voiceTTSToolName
}

func (t *dianaTTSTool) Description() string {
	// 这个工具没有 InputSchema，参数只能写在说明里。普通聊天、讨论 TTS 功能时
	// 模型曾误调，所以「只在明确要语音时用」要留。
	return fmt.Sprintf(`把最终回复合成为%s音色的语音直接发送。只在用户明确要语音回复或朗读时用，普通聊天、讨论语音功能时不用。工具会完成本次回复，不再重复发文字。input: {"text":"要说的纯文本，不含 Markdown、CQ 码"}`, voiceTTSVoiceName())
}

func (t *dianaTTSTool) Run(ctx context.Context, input map[string]any) (string, error) {
	if t == nil || t.runtime == nil {
		return "", fmt.Errorf("语音合成未配置")
	}
	ctx = withModelConfigEvent(ctx, t.event)
	routes := t.runtime.mediaSlotRoutes(ctx, mediaSlotTTS)
	if len(routes) == 0 {
		return "", fmt.Errorf("模型分配里没有配置语音合成")
	}
	settings := voiceTTSSettingsFromParams(routes[0].Params)
	text := sanitizeVoiceTTSText(configToolString(input, "text"), settings.MaxChars)
	if text == "" {
		return "", fmt.Errorf("text 不能为空")
	}
	resp, err := t.runtime.synthesizeSpeech(ctx, text, "")
	if err != nil {
		return "", err
	}
	path, err := storeVoiceAudio(resp.Audio, slotSpeechExtension(resp))
	if err != nil {
		return "", err
	}
	t.runtime.mu.RLock()
	sharer := t.runtime.localMedia
	t.runtime.mu.RUnlock()
	if sharer == nil {
		_ = os.Remove(path)
		return "", fmt.Errorf("本地媒体共享未配置，无法把语音交给 OneBot v11 客户端")
	}
	sharedURL, ok := sharer.Share(path, defaultVoiceTTSMediaTTL)
	if !ok {
		_ = os.Remove(path)
		return "", fmt.Errorf("本地媒体共享未配置，无法把语音交给 OneBot v11 客户端")
	}
	cleanupLocalMediaFilesLater([]string{path}, defaultVoiceTTSMediaTTL)
	body, err := json.Marshal(voiceTTSResult{
		OK:       true,
		Action:   "voice_ready",
		CQRecord: "[CQ:record,file=" + escapeCQParameter(sharedURL) + "]",
		Text:     text,
	})
	if err != nil {
		return "", err
	}
	return string(body), nil
}

func (t *dianaTTSTool) TerminalResult(output string) (string, bool) {
	var result voiceTTSResult
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		return "", false
	}
	if !result.OK || strings.TrimSpace(result.CQRecord) == "" {
		return "", false
	}
	return result.CQRecord, true
}

var _ agent.Tool = (*dianaTTSTool)(nil)

// slotSpeechExtension 给插槽合成的音频定扩展名。先看接口报的类型，再嗅探内容，
// 最后才用请求的格式：自建兼容层常常无视 response_format 照样回 wav，扩展名和
// 内容对不上时 QQ 发不出或放不了。都认不出按 mp3，那是插槽的默认格式。
func slotSpeechExtension(resp *llm.SpeechResponse) string {
	for _, mediaType := range []string{resp.MediaType, agent.SniffMediaType(resp.Audio)} {
		if strings.HasPrefix(strings.ToLower(mediaType), "audio/") {
			if ext := agent.CanonicalMediaExtension(mediaType); ext != "" {
				return strings.TrimPrefix(ext, ".")
			}
		}
	}
	switch format := strings.ToLower(strings.TrimSpace(resp.Format)); format {
	case "mp3", "wav", "ogg", "flac", "aac", "m4a", "amr", "silk":
		return format
	case "opus":
		return "ogg"
	}
	return "mp3"
}

// storeVoiceAudio 把合成好的音频原样落进缓存目录。不在这里转 Silk：OneBot 客户端
// 自己会转码，SnowLuma 收到现成的 Silk 量不出时长，QQ 里会显示成 1 秒（#920）。
func storeVoiceAudio(audio []byte, extension string) (string, error) {
	dir := voiceTTSOutputDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("创建语音缓存目录失败: %w", err)
	}
	file, err := os.CreateTemp(dir, "diana-tts-*."+extension)
	if err != nil {
		return "", fmt.Errorf("创建语音缓存文件失败: %w", err)
	}
	path := file.Name()
	if _, writeErr := file.Write(audio); writeErr != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return "", fmt.Errorf("写入语音缓存失败: %w", writeErr)
	}
	if closeErr := file.Close(); closeErr != nil {
		_ = os.Remove(path)
		return "", fmt.Errorf("保存语音缓存失败: %w", closeErr)
	}
	return path, nil
}

func voiceTTSVoiceName() string {
	return firstNonEmpty(strings.TrimSpace(os.Getenv("DIANA_TTS_VOICE_NAME")), "自定义")
}

func voiceTTSOutputDir() string {
	if configured := strings.TrimSpace(os.Getenv("DIANA_TTS_OUTPUT_DIR")); configured != "" {
		return configured
	}
	if dbPath := strings.TrimSpace(os.Getenv("APP_DB_PATH")); dbPath != "" {
		if absolute, err := filepath.Abs(dbPath); err == nil {
			return filepath.Join(filepath.Dir(absolute), "tts-cache")
		}
	}
	if cacheDir, err := os.UserCacheDir(); err == nil && cacheDir != "" {
		return filepath.Join(cacheDir, "diana", "tts-cache")
	}
	return filepath.Join(os.TempDir(), "diana-tts")
}

func sanitizeVoiceTTSText(text string, maxRunes int) string {
	text = strings.TrimSpace(text)
	if strings.Contains(text, "[CQ:") {
		text = PlainText(CQToSegments(text))
	}
	text = strings.Join(strings.Fields(text), " ")
	runes := []rune(text)
	if maxRunes > 0 && len(runes) > maxRunes {
		runes = runes[:maxRunes]
	}
	return strings.TrimSpace(string(runes))
}

func escapeCQParameter(value string) string {
	replacer := strings.NewReplacer("&", "&amp;", "[", "&#91;", "]", "&#93;", ",", "&#44;")
	return replacer.Replace(value)
}
