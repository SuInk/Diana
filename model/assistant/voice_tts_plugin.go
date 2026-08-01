package assistant

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	voiceTTSPluginID = "official.voice-tts"

	voiceTTSPresetLocal  = "local"
	voiceTTSPresetDocker = "docker"
	voiceTTSPresetCustom = "custom"

	voiceTTSSettingPreset       = "preset"
	voiceTTSSettingEndpoint     = "endpoint"
	voiceTTSSettingRefAudioPath = "ref_audio_path"
	voiceTTSSettingPromptText   = "prompt_text"
	voiceTTSSettingTextLang     = "text_lang"
	voiceTTSSettingPromptLang   = "prompt_lang"
	voiceTTSSettingSpeed        = "speed_factor"
	voiceTTSSettingMaxChars     = "max_chars"
	voiceTTSSettingTimeout      = "timeout_seconds"

	defaultVoiceTTSMaxBytes = 16 << 20
)

type VoiceTTSPlugin struct {
	client *http.Client
}

func NewVoiceTTSPlugin(client *http.Client) *VoiceTTSPlugin {
	if client == nil {
		client = &http.Client{}
	}
	return &VoiceTTSPlugin{client: client}
}

func (p *VoiceTTSPlugin) Manifest() PluginManifest {
	return PluginManifest{
		ID:          voiceTTSPluginID,
		Name:        "语音合成",
		Version:     "0.2.0",
		Description: "调用 GPT-SoVITS API 合成 QQ 语音。明确发送“语音说 …”或“朗读 …”时触发。",
		Official:    true,
		BuiltIn:     true,
		Permissions: []string{"network:http", "message:read", "message:send"},
		Settings: []PluginSettingSpec{
			{
				Key:     voiceTTSSettingPreset,
				Label:   "服务预设",
				Type:    PluginSettingTypeSelect,
				Default: voiceTTSPresetLocal,
				Options: []PluginSettingOption{
					{Value: voiceTTSPresetLocal, Label: "本机 GPT-SoVITS"},
					{Value: voiceTTSPresetDocker, Label: "Docker · gpt-sovits"},
					{Value: voiceTTSPresetCustom, Label: "自定义 GPT-SoVITS"},
				},
				Description: "预设决定默认 API 地址；下方填写地址可覆盖预设。",
			},
			{
				Key:         voiceTTSSettingEndpoint,
				Label:       "API 地址（可选）",
				Type:        PluginSettingTypeString,
				Default:     "",
				Description: "留空使用预设地址；GPT-SoVITS v2 通常为 http://主机:9880/tts。",
			},
			{
				Key:         voiceTTSSettingRefAudioPath,
				Label:       "参考音频路径",
				Type:        PluginSettingTypeString,
				Default:     "",
				Description: "GPT-SoVITS 服务能够读取的参考音频路径。",
			},
			{
				Key:         voiceTTSSettingPromptText,
				Label:       "参考音频文本",
				Type:        PluginSettingTypeString,
				Default:     "",
				Description: "参考音频对应的原文；部分模型允许留空。",
			},
			{
				Key:     voiceTTSSettingTextLang,
				Label:   "合成文本语言",
				Type:    PluginSettingTypeSelect,
				Default: "zh",
				Options: voiceTTSLanguageOptions(),
			},
			{
				Key:     voiceTTSSettingPromptLang,
				Label:   "参考音频语言",
				Type:    PluginSettingTypeSelect,
				Default: "zh",
				Options: voiceTTSLanguageOptions(),
			},
			{
				Key:     voiceTTSSettingSpeed,
				Label:   "语速",
				Type:    PluginSettingTypeNumber,
				Default: 1.0,
				Min:     settingRange(0.5),
				Max:     settingRange(2),
				Step:    0.05,
				Unit:    "x",
			},
			{
				Key:     voiceTTSSettingMaxChars,
				Label:   "单次合成字数",
				Type:    PluginSettingTypeNumber,
				Default: 500,
				Min:     settingRange(20),
				Max:     settingRange(2000),
				Step:    10,
				Unit:    "字",
			},
			{
				Key:     voiceTTSSettingTimeout,
				Label:   "请求超时",
				Type:    PluginSettingTypeNumber,
				Default: 40,
				Min:     settingRange(5),
				Max:     settingRange(120),
				Step:    5,
				Unit:    "秒",
			},
		},
	}
}

func voiceTTSLanguageOptions() []PluginSettingOption {
	return []PluginSettingOption{
		{Value: "zh", Label: "中文"},
		{Value: "en", Label: "英语"},
		{Value: "ja", Label: "日语"},
		{Value: "ko", Label: "韩语"},
		{Value: "yue", Label: "粤语"},
		{Value: "auto", Label: "自动识别"},
	}
}

func (p *VoiceTTSPlugin) Handle(ctx context.Context, req PluginRequest) (*PluginResponse, error) {
	text, ok := voiceTTSCommandText(req.Text)
	if !ok {
		return nil, nil
	}
	maxChars := req.Settings.Int(voiceTTSSettingMaxChars, 500)
	runes := []rune(strings.TrimSpace(text))
	if len(runes) == 0 {
		return &PluginResponse{Handled: true, Reply: "请在“语音说”后面填写要合成的内容。"}, nil
	}
	if len(runes) > maxChars {
		runes = runes[:maxChars]
	}
	audio, err := p.synthesize(ctx, req.Settings, string(runes))
	if err != nil {
		return nil, err
	}
	record := "[CQ:record,file=base64://" + base64.StdEncoding.EncodeToString(audio) + "]"
	return &PluginResponse{Handled: true, Reply: record}, nil
}

func voiceTTSCommandText(text string) (string, bool) {
	text = strings.TrimSpace(text)
	prefixes := []string{"用语音说", "语音说", "朗读", "念一下", "读一下"}
	for _, prefix := range prefixes {
		if strings.HasPrefix(text, prefix) {
			return strings.TrimSpace(strings.TrimLeft(strings.TrimPrefix(text, prefix), "：:，, ")), true
		}
	}
	return "", false
}

func (p *VoiceTTSPlugin) synthesize(ctx context.Context, settings SettingValues, text string) ([]byte, error) {
	endpoint := settings.String(voiceTTSSettingEndpoint, "")
	if endpoint == "" {
		switch settings.String(voiceTTSSettingPreset, voiceTTSPresetLocal) {
		case voiceTTSPresetDocker:
			endpoint = "http://gpt-sovits:9880/tts"
		case voiceTTSPresetCustom:
			return nil, fmt.Errorf("语音合成自定义预设需要填写 API 地址")
		default:
			endpoint = "http://127.0.0.1:9880/tts"
		}
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return nil, fmt.Errorf("语音合成 API 地址无效")
	}
	payload := map[string]any{
		"text":               text,
		"text_lang":          settings.String(voiceTTSSettingTextLang, "zh"),
		"ref_audio_path":     settings.String(voiceTTSSettingRefAudioPath, ""),
		"prompt_text":        settings.String(voiceTTSSettingPromptText, ""),
		"prompt_lang":        settings.String(voiceTTSSettingPromptLang, "zh"),
		"text_split_method":  "cut5",
		"batch_size":         1,
		"split_bucket":       true,
		"speed_factor":       settingFloat(settings, voiceTTSSettingSpeed, 1),
		"fragment_interval":  0.3,
		"media_type":         "wav",
		"streaming_mode":     false,
		"parallel_infer":     true,
		"repetition_penalty": 1.35,
		"seed":               -1,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	requestCtx, cancel := context.WithTimeout(ctx, time.Duration(settings.Int(voiceTTSSettingTimeout, 40))*time.Second)
	defer cancel()
	httpReq, err := http.NewRequestWithContext(requestCtx, http.MethodPost, parsed.String(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("语音合成请求失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("语音合成服务返回 %s: %s", resp.Status, strings.TrimSpace(string(detail)))
	}
	audio, err := io.ReadAll(io.LimitReader(resp.Body, defaultVoiceTTSMaxBytes+1))
	if err != nil {
		return nil, err
	}
	if len(audio) > defaultVoiceTTSMaxBytes {
		return nil, fmt.Errorf("语音合成结果超过 16 MB")
	}
	if len(audio) < 12 || string(audio[:4]) != "RIFF" || string(audio[8:12]) != "WAVE" {
		return nil, fmt.Errorf("语音合成服务未返回有效 WAV 音频")
	}
	return audio, nil
}

func settingFloat(settings SettingValues, key string, fallback float64) float64 {
	value, ok := numberValue(settings[key])
	if !ok {
		return fallback
	}
	return value
}
