// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package llm

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

// 各家私有语音合成协议。请求和响应的形状对照 AstrBot 的 provider/sources 与
// SillyTavern 的 extensions/tts 写成；没有官方文档的字段以它们实测能用的为准。
//
// SpeechRequest.Options 里认的键：
//
//	gpt_sovits        ref_audio_path、prompt_text、text_lang、prompt_lang
//	gsvi              version、emotion、text_lang、prompt_lang
//	azure             region、style
//	edge              pitch、volume
//	dashscope         sample_rate
//	minimax           group_id、emotion
//	volcengine        app_id、cluster（v1 接口）、resource_id（v3 接口）
//	google_translate  language
//	custom_http       method、path、body_type、body、response
const (
	defaultGPTSoVITSBaseURL  = "http://127.0.0.1:9880"
	defaultGSVIBaseURL       = "http://127.0.0.1:8000"
	defaultGeminiBaseURL     = "https://generativelanguage.googleapis.com/v1beta"
	defaultDashScopeBaseURL  = "https://dashscope.aliyuncs.com"
	defaultFishAudioBaseURL  = "https://api.fish.audio/v1"
	defaultMiniMaxBaseURL    = "https://api.minimax.io/v1"
	defaultVolcengineV1URL   = "https://openspeech.bytedance.com/api/v1/tts"
	defaultVolcengineV3URL   = "https://openspeech.bytedance.com/api/v3/tts/unidirectional"
	defaultMiMoBaseURL       = "https://api.xiaomimimo.com/v1"
	defaultNovelAIBaseURL    = "https://api.novelai.net"
	defaultGoogleTranslate   = "https://translate.google.com"
	defaultAzureRegion       = "eastus"
	defaultEdgeVoice         = "zh-CN-XiaoxiaoNeural"
	defaultAzureVoice        = "zh-CN-XiaoxiaoNeural"
	defaultGeminiSpeechModel = "gemini-2.5-flash-preview-tts"
	defaultGeminiVoice       = "Kore"
	defaultMiMoSpeechModel   = "mimo-v2.5-tts"
	defaultMiMoVoice         = "mimo_default"
	defaultMiMoSTTModel      = "mimo-v2.5-asr"
	defaultMiniMaxModel      = "speech-02-hd"
	defaultFishAudioModel    = "s1"
	defaultQwenTTSVoice      = "Cherry"
	defaultVolcengineCluster = "volcano_tts"
	defaultVolcengineV3Res   = "seed-tts-1.0"
	defaultNovelAIVoice      = "Aini"
)

// speechAPIRequiresKey 列出没有 Key 就一定被拒的云服务，提前报缺 Key 比等一个
// 401 好懂。本机服务和免费接口不要求。
func speechAPIRequiresKey(api SpeechAPI) bool {
	switch api {
	case SpeechAPIGPTSoVITS, SpeechAPIGSVI, SpeechAPIEdge, SpeechAPIGoogleTranslate, SpeechAPICustomHTTP:
		return false
	}
	return true
}

func synthesizeVendorSpeech(ctx context.Context, cfg ProviderConfig, req SpeechRequest, opts []ClientOption) (*SpeechResponse, error) {
	req.Input = strings.TrimSpace(req.Input)
	req.Model = strings.TrimSpace(req.Model)
	req.Voice = strings.TrimSpace(req.Voice)
	req.Format = strings.ToLower(strings.TrimSpace(req.Format))
	if req.Input == "" {
		return nil, errors.New("llm: speech input is required")
	}
	if req.Speed < 0 || req.Speed > 4 {
		return nil, errors.New("llm: speech speed must be between 0.25 and 4")
	}
	if req.Timeout <= 0 {
		req.Timeout = defaultSpeechTimeout
	}
	var (
		audio  []byte
		format string
		err    error
	)
	switch req.API {
	case SpeechAPIEdge:
		audio, format, err = synthesizeEdge(ctx, cfg, req, opts)
	case SpeechAPIGoogleTranslate:
		audio, format, err = synthesizeGoogleTranslate(ctx, cfg, req, opts)
	default:
		var client *mediaHTTP
		client, err = newMediaHTTPWithKey(cfg, vendorDefaultBaseURL(req.API), opts, speechAPIRequiresKey(req.API))
		if err != nil {
			return nil, err
		}
		client.policy.AttemptTimeout = req.Timeout
		switch req.API {
		case SpeechAPIGPTSoVITS:
			audio, format, err = synthesizeGPTSoVITS(ctx, client, req)
		case SpeechAPIGSVI:
			audio, format, err = synthesizeGSVI(ctx, client, req)
		case SpeechAPIAzure:
			audio, format, err = synthesizeAzure(ctx, client, cfg, req)
		case SpeechAPIGemini:
			audio, format, err = synthesizeGemini(ctx, client, cfg, req)
		case SpeechAPIDashScope:
			audio, format, err = synthesizeDashScope(ctx, client, cfg, req, opts)
		case SpeechAPIFishAudio:
			audio, format, err = synthesizeFishAudio(ctx, client, cfg, req)
		case SpeechAPIMiniMax:
			audio, format, err = synthesizeMiniMax(ctx, client, cfg, req)
		case SpeechAPIVolcengine:
			audio, format, err = synthesizeVolcengine(ctx, client, cfg, req)
		case SpeechAPIMiMo:
			audio, format, err = synthesizeMiMo(ctx, client, cfg, req)
		case SpeechAPINovelAI:
			audio, format, err = synthesizeNovelAI(ctx, client, cfg, req)
		case SpeechAPICustomHTTP:
			audio, format, err = synthesizeCustomHTTP(ctx, client, req)
		default:
			return nil, fmt.Errorf("llm: speech api %q cannot synthesize speech", req.API)
		}
	}
	if err != nil {
		return nil, err
	}
	if len(audio) == 0 {
		return nil, &MediaAPIError{Op: "speech", Kind: MediaErrorBadResponse, Detail: "empty audio"}
	}
	mediaType := speechMediaType("", format)
	if sniffed := http.DetectContentType(audio); strings.HasPrefix(sniffed, "audio/") {
		mediaType = sniffed
	}
	return &SpeechResponse{API: req.API, Model: req.Model, Audio: audio, MediaType: mediaType, Format: format}, nil
}

func vendorDefaultBaseURL(api SpeechAPI) string {
	switch api {
	case SpeechAPIGPTSoVITS:
		return defaultGPTSoVITSBaseURL
	case SpeechAPIGSVI:
		return defaultGSVIBaseURL
	case SpeechAPIGemini:
		return defaultGeminiBaseURL
	case SpeechAPIDashScope:
		return defaultDashScopeBaseURL
	case SpeechAPIFishAudio:
		return defaultFishAudioBaseURL
	case SpeechAPIMiniMax:
		return defaultMiniMaxBaseURL
	case SpeechAPIVolcengine:
		return defaultVolcengineV1URL
	case SpeechAPIMiMo:
		return defaultMiMoBaseURL
	case SpeechAPINovelAI:
		return defaultNovelAIBaseURL
	case SpeechAPIGoogleTranslate:
		return defaultGoogleTranslate
	}
	return defaultOpenAIMediaBaseURL
}

// endpointUnlessPresent 让配置档地址既能填到服务根，也能直接填到接口本身：
// 地址已经以 endpoint 结尾时返回空，不再重复拼。
func endpointUnlessPresent(base, endpoint string) string {
	parsed, err := url.Parse(strings.TrimSpace(base))
	if err != nil {
		return endpoint
	}
	if strings.HasSuffix(strings.TrimRight(parsed.Path, "/"), "/"+strings.Trim(endpoint, "/")) {
		return ""
	}
	return endpoint
}

func bearerAuth(key string) func(*http.Request) {
	key = strings.TrimSpace(key)
	return func(req *http.Request) {
		if key != "" {
			req.Header.Set("Authorization", "Bearer "+key)
		}
	}
}

func headerAuth(name, key string) func(*http.Request) {
	key = strings.TrimSpace(key)
	return func(req *http.Request) {
		if key != "" {
			req.Header.Set(name, key)
		}
	}
}

func marshalJSONSpec(op, method, endpoint string, payload any) (mediaRequestSpec, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return mediaRequestSpec{}, err
	}
	return mediaRequestSpec{op: op, method: method, endpoint: endpoint, body: body, contentType: "application/json"}, nil
}

func badSpeechResponse(status int, detail string) error {
	detail = strings.TrimSpace(detail)
	if len(detail) > 2000 {
		detail = detail[:2000]
	}
	kind := MediaErrorBadResponse
	if mediaBodyMentionsPolicy(detail) {
		kind = MediaErrorContentPolicy
	}
	return &MediaAPIError{Op: "speech", Kind: kind, StatusCode: status, Detail: detail}
}

// audioBody 收下「直接回音频」的响应：200 里塞着 JSON 错误的照样当失败。
func audioBody(result *mediaResult) ([]byte, error) {
	if err := jsonErrorOn200("speech", result); err != nil {
		return nil, err
	}
	return result.body, nil
}

func (m *mediaHTTP) download(ctx context.Context, link string) ([]byte, error) {
	if strings.HasPrefix(link, "data:") {
		_, payload, ok := strings.Cut(link, ",")
		if !ok {
			return nil, badSpeechResponse(0, "invalid data url")
		}
		return base64.StdEncoding.DecodeString(payload)
	}
	// 音频链接多半在另一个域名上（OSS 签名链接、服务的静态目录），不能把配置档的
	// 凭据和自定义头带过去：签名链接见到多余的 Authorization 会直接拒。
	plain := *m
	plain.auth = func(*http.Request) {}
	plain.cfg.Headers = nil
	plain.client = http.DefaultClient
	result, err := plain.do(ctx, mediaRequestSpec{op: "speech", method: http.MethodGet, endpoint: link, accept: "audio/*, application/octet-stream"}, maxSpeechAudioBytes)
	if err != nil {
		return nil, err
	}
	return audioBody(result)
}

// formatOr 把请求的格式限制在服务支持的那几种里，不认得的落到 fallback。
func formatOr(format, fallback string, allowed ...string) string {
	for _, candidate := range allowed {
		if format == candidate {
			return format
		}
	}
	return fallback
}

// pcmToWAV 给裸 16 位单声道 PCM 补上 WAV 头：Gemini 回的是 audio/L16，QQ 认不了裸 PCM。
func pcmToWAV(pcm []byte, sampleRate int) []byte {
	if sampleRate <= 0 {
		sampleRate = 24000
	}
	var out bytes.Buffer
	out.Grow(44 + len(pcm))
	out.WriteString("RIFF")
	_ = binary.Write(&out, binary.LittleEndian, uint32(36+len(pcm)))
	out.WriteString("WAVEfmt ")
	_ = binary.Write(&out, binary.LittleEndian, uint32(16))
	_ = binary.Write(&out, binary.LittleEndian, uint16(1))
	_ = binary.Write(&out, binary.LittleEndian, uint16(1))
	_ = binary.Write(&out, binary.LittleEndian, uint32(sampleRate))
	_ = binary.Write(&out, binary.LittleEndian, uint32(sampleRate*2))
	_ = binary.Write(&out, binary.LittleEndian, uint16(2))
	_ = binary.Write(&out, binary.LittleEndian, uint16(16))
	out.WriteString("data")
	_ = binary.Write(&out, binary.LittleEndian, uint32(len(pcm)))
	out.Write(pcm)
	return out.Bytes()
}

func isWAV(audio []byte) bool {
	return len(audio) >= 12 && string(audio[:4]) == "RIFF" && string(audio[8:12]) == "WAVE"
}

// languageOfVoice 从 zh-CN-XiaoxiaoNeural 这类音色名里取出 zh-CN。
func languageOfVoice(voice, fallback string) string {
	parts := strings.Split(voice, "-")
	if len(parts) >= 2 && len(parts[0]) == 2 {
		return parts[0] + "-" + parts[1]
	}
	return fallback
}

// ratePercent 把倍速写成 SSML 的 +20% 这种写法；0 表示默认语速。
func ratePercent(speed float64) string {
	if speed <= 0 {
		return "+0%"
	}
	return fmt.Sprintf("%+d%%", int(math.Round((speed-1)*100)))
}

func xmlEscape(text string) string {
	var out strings.Builder
	for _, r := range text {
		switch r {
		case '&':
			out.WriteString("&amp;")
		case '<':
			out.WriteString("&lt;")
		case '>':
			out.WriteString("&gt;")
		case '"':
			out.WriteString("&quot;")
		case '\'':
			out.WriteString("&apos;")
		default:
			out.WriteRune(r)
		}
	}
	return out.String()
}

// synthesizeGPTSoVITS 调 GPT-SoVITS v2 的 /tts（api_v2.py），参数与原来的语音合成
// 插件一致。参考音频是 v2 的必填项，没配时服务端会直接 400。
func synthesizeGPTSoVITS(ctx context.Context, client *mediaHTTP, req SpeechRequest) ([]byte, string, error) {
	speed := req.Speed
	if speed <= 0 {
		speed = 1
	}
	payload := map[string]any{
		"text":               req.Input,
		"text_lang":          firstNonEmptyString(req.option("text_lang"), "zh"),
		"ref_audio_path":     req.option("ref_audio_path"),
		"prompt_text":        req.option("prompt_text"),
		"prompt_lang":        firstNonEmptyString(req.option("prompt_lang"), "zh"),
		"text_split_method":  "cut5",
		"batch_size":         1,
		"split_bucket":       true,
		"speed_factor":       speed,
		"fragment_interval":  0.3,
		"media_type":         "wav",
		"streaming_mode":     false,
		"parallel_infer":     true,
		"repetition_penalty": 1.35,
		"seed":               -1,
	}
	spec, err := marshalJSONSpec("speech", http.MethodPost, endpointUnlessPresent(client.baseURL, "tts"), payload)
	if err != nil {
		return nil, "", err
	}
	spec.accept = "audio/*, application/octet-stream"
	result, err := client.do(ctx, spec, maxSpeechAudioBytes)
	if err != nil {
		return nil, "", err
	}
	audio, err := audioBody(result)
	if err != nil {
		return nil, "", err
	}
	if !isWAV(audio) {
		return nil, "", badSpeechResponse(result.status, "GPT-SoVITS 未返回有效 WAV 音频")
	}
	return audio, "wav", nil
}

// synthesizeGSVI 调 GPT-SoVITS-Inference 整合包的 /infer_single：先合成，再按回包里的
// audio_url 下载。音色就是整合包里的角色名。
func synthesizeGSVI(ctx context.Context, client *mediaHTTP, req SpeechRequest) ([]byte, string, error) {
	client.auth = bearerAuth(client.cfg.APIKey)
	payload := map[string]any{
		"dl_url":           strings.TrimRight(client.baseURL, "/"),
		"version":          firstNonEmptyString(req.option("version"), "v4"),
		"model_name":       firstNonEmptyString(req.Voice, req.Model),
		"prompt_text_lang": firstNonEmptyString(req.option("prompt_lang"), "中文"),
		"emotion":          firstNonEmptyString(req.option("emotion"), "默认"),
		"text":             req.Input,
		"text_lang":        firstNonEmptyString(req.option("text_lang"), "中文"),
	}
	spec, err := marshalJSONSpec("speech", http.MethodPost, endpointUnlessPresent(client.baseURL, "infer_single"), payload)
	if err != nil {
		return nil, "", err
	}
	result, err := client.do(ctx, spec, maxTranscriptionResponseBytes)
	if err != nil {
		return nil, "", err
	}
	var parsed struct {
		Msg      string `json:"msg"`
		AudioURL string `json:"audio_url"`
	}
	if json.Unmarshal(result.body, &parsed) != nil || parsed.AudioURL == "" {
		return nil, "", badSpeechResponse(result.status, firstNonEmptyString(parsed.Msg, string(result.body)))
	}
	audio, err := client.download(ctx, parsed.AudioURL)
	return audio, "wav", err
}

// synthesizeAzure 走 Azure 语音服务的 REST 接口。地址留空时按 region 拼官方地址。
func synthesizeAzure(ctx context.Context, client *mediaHTTP, cfg ProviderConfig, req SpeechRequest) ([]byte, string, error) {
	if strings.TrimSpace(cfg.BaseURL) == "" {
		region := firstNonEmptyString(req.option("region"), defaultAzureRegion)
		client.baseURL = "https://" + region + ".tts.speech.microsoft.com"
	}
	client.auth = headerAuth("Ocp-Apim-Subscription-Key", cfg.APIKey)
	voice := firstNonEmptyString(req.Voice, defaultAzureVoice)
	lang := languageOfVoice(voice, "zh-CN")
	content := fmt.Sprintf("<prosody rate='%s'>%s</prosody>", ratePercent(req.Speed), xmlEscape(req.Input))
	if style := firstNonEmptyString(req.option("style"), req.Instructions); style != "" {
		content = fmt.Sprintf("<mstts:express-as style='%s'>%s</mstts:express-as>", xmlEscape(style), content)
	}
	ssml := fmt.Sprintf("<speak version='1.0' xmlns='http://www.w3.org/2001/10/synthesis' xmlns:mstts='http://www.w3.org/2001/mstts' xml:lang='%s'><voice name='%s'>%s</voice></speak>", lang, xmlEscape(voice), content)
	format := formatOr(req.Format, "mp3", "mp3", "wav", "opus")
	outputFormat := map[string]string{
		"mp3":  "audio-24khz-48kbitrate-mono-mp3",
		"wav":  "riff-24khz-16bit-mono-pcm",
		"opus": "ogg-24khz-16bit-mono-opus",
	}[format]
	result, err := client.do(ctx, mediaRequestSpec{
		op: "speech", method: http.MethodPost, endpoint: endpointUnlessPresent(client.baseURL, "cognitiveservices/v1"),
		body: []byte(ssml), contentType: "application/ssml+xml", accept: "audio/*",
		headers: map[string]string{"X-Microsoft-OutputFormat": outputFormat},
	}, maxSpeechAudioBytes)
	if err != nil {
		return nil, "", err
	}
	audio, err := audioBody(result)
	if format == "opus" {
		format = "ogg"
	}
	return audio, format, err
}

// synthesizeGemini 用 generateContent 的 AUDIO 输出合成；回来的是 24kHz 裸 PCM。
func synthesizeGemini(ctx context.Context, client *mediaHTTP, cfg ProviderConfig, req SpeechRequest) ([]byte, string, error) {
	if parsed, err := url.Parse(client.baseURL); err == nil && !strings.Contains(parsed.Path, "/v1") {
		parsed.Path = strings.TrimRight(parsed.Path, "/") + "/v1beta"
		client.baseURL = parsed.String()
	}
	client.auth = headerAuth("x-goog-api-key", cfg.APIKey)
	model := firstNonEmptyString(req.Model, defaultGeminiSpeechModel)
	prompt := req.Input
	if instructions := strings.TrimSpace(req.Instructions); instructions != "" {
		prompt = instructions + ": " + prompt
	}
	payload := map[string]any{
		"contents": []any{map[string]any{"role": "user", "parts": []any{map[string]any{"text": prompt}}}},
		"generationConfig": map[string]any{
			"responseModalities": []string{"AUDIO"},
			"speechConfig": map[string]any{
				"voiceConfig": map[string]any{"prebuiltVoiceConfig": map[string]any{"voiceName": firstNonEmptyString(req.Voice, defaultGeminiVoice)}},
			},
		},
	}
	spec, err := marshalJSONSpec("speech", http.MethodPost, "models/"+url.PathEscape(model)+":generateContent", payload)
	if err != nil {
		return nil, "", err
	}
	result, err := client.do(ctx, spec, maxSpeechAudioBytes*2)
	if err != nil {
		return nil, "", err
	}
	var parsed struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					InlineData struct {
						MimeType string `json:"mimeType"`
						Data     string `json:"data"`
					} `json:"inlineData"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
	}
	if err := json.Unmarshal(result.body, &parsed); err != nil || len(parsed.Candidates) == 0 || len(parsed.Candidates[0].Content.Parts) == 0 {
		return nil, "", badSpeechResponse(result.status, string(result.body))
	}
	inline := parsed.Candidates[0].Content.Parts[0].InlineData
	audio, err := base64.StdEncoding.DecodeString(inline.Data)
	if err != nil || len(audio) == 0 {
		return nil, "", badSpeechResponse(result.status, "Gemini 未返回音频")
	}
	mimeType := strings.ToLower(inline.MimeType)
	if strings.Contains(mimeType, "l16") || strings.Contains(mimeType, "pcm") {
		rate := 24000
		if match := regexp.MustCompile(`rate=(\d+)`).FindStringSubmatch(mimeType); len(match) == 2 {
			rate, _ = strconv.Atoi(match[1])
		}
		return pcmToWAV(audio, rate), "wav", nil
	}
	return audio, formatFromMediaType(mimeType), nil
}

func formatFromMediaType(mediaType string) string {
	switch {
	case strings.Contains(mediaType, "wav"):
		return "wav"
	case strings.Contains(mediaType, "ogg"), strings.Contains(mediaType, "opus"):
		return "ogg"
	case strings.Contains(mediaType, "flac"):
		return "flac"
	case strings.Contains(mediaType, "aac"):
		return "aac"
	}
	return "mp3"
}

// synthesizeDashScope 调阿里云百炼：qwen-tts 系列走多模态生成 HTTP 接口，回一个
// 音频链接；CosyVoice、Sambert 只有 WebSocket 接口。
func synthesizeDashScope(ctx context.Context, client *mediaHTTP, cfg ProviderConfig, req SpeechRequest, opts []ClientOption) ([]byte, string, error) {
	model := strings.ToLower(req.Model)
	if model == "" {
		return nil, "", ErrMissingModel
	}
	// 对话配置档常填 .../compatible-mode/v1，语音接口在服务根下面。
	if parsed, err := url.Parse(client.baseURL); err == nil {
		parsed.Path, parsed.RawQuery = "", ""
		client.baseURL = parsed.String()
	}
	if !(strings.HasPrefix(model, "qwen") && strings.Contains(model, "tts")) {
		return synthesizeDashScopeStream(ctx, client, cfg, req)
	}
	payload := map[string]any{
		"model": req.Model,
		"input": map[string]any{"text": req.Input, "voice": firstNonEmptyString(req.Voice, defaultQwenTTSVoice)},
	}
	spec, err := marshalJSONSpec("speech", http.MethodPost, "api/v1/services/aigc/multimodal-generation/generation", payload)
	if err != nil {
		return nil, "", err
	}
	result, err := client.do(ctx, spec, maxSpeechAudioBytes)
	if err != nil {
		return nil, "", err
	}
	var parsed struct {
		Output struct {
			Audio struct {
				URL  string `json:"url"`
				Data string `json:"data"`
			} `json:"audio"`
		} `json:"output"`
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(result.body, &parsed); err != nil {
		return nil, "", badSpeechResponse(result.status, string(result.body))
	}
	if parsed.Output.Audio.Data != "" {
		audio, err := base64.StdEncoding.DecodeString(parsed.Output.Audio.Data)
		return audio, "wav", err
	}
	if parsed.Output.Audio.URL == "" {
		return nil, "", badSpeechResponse(result.status, firstNonEmptyString(parsed.Message, parsed.Code, string(result.body)))
	}
	audio, err := client.download(ctx, parsed.Output.Audio.URL)
	return audio, "wav", err
}

// synthesizeFishAudio 调 Fish Audio 的 /v1/tts。音色是声音模型的 reference_id，
// 模型名放在 model 请求头里而不是请求体。
func synthesizeFishAudio(ctx context.Context, client *mediaHTTP, cfg ProviderConfig, req SpeechRequest) ([]byte, string, error) {
	client.auth = bearerAuth(cfg.APIKey)
	format := formatOr(req.Format, "mp3", "mp3", "wav", "opus")
	payload := map[string]any{"text": req.Input, "format": format, "normalize": true, "latency": "normal"}
	if req.Voice != "" {
		payload["reference_id"] = req.Voice
	}
	if req.Speed > 0 {
		payload["prosody"] = map[string]any{"speed": req.Speed}
	}
	spec, err := marshalJSONSpec("speech", http.MethodPost, endpointUnlessPresent(client.baseURL, "tts"), payload)
	if err != nil {
		return nil, "", err
	}
	spec.accept = "audio/*"
	spec.headers = map[string]string{"model": firstNonEmptyString(req.Model, defaultFishAudioModel)}
	result, err := client.do(ctx, spec, maxSpeechAudioBytes)
	if err != nil {
		return nil, "", err
	}
	audio, err := audioBody(result)
	if format == "opus" {
		format = "ogg"
	}
	return audio, format, err
}

// synthesizeMiniMax 调 MiniMax 的 t2a_v2，音频以十六进制字符串放在 data.audio 里。
func synthesizeMiniMax(ctx context.Context, client *mediaHTTP, cfg ProviderConfig, req SpeechRequest) ([]byte, string, error) {
	client.auth = bearerAuth(cfg.APIKey)
	if req.Voice == "" {
		return nil, "", errors.New("llm: minimax speech requires a voice id")
	}
	format := formatOr(req.Format, "mp3", "mp3", "wav", "flac")
	speed := req.Speed
	if speed <= 0 {
		speed = 1
	}
	voice := map[string]any{"voice_id": req.Voice, "speed": speed, "vol": 1, "pitch": 0}
	if emotion := req.option("emotion"); emotion != "" {
		voice["emotion"] = emotion
	}
	payload := map[string]any{
		"model":          firstNonEmptyString(req.Model, defaultMiniMaxModel),
		"text":           req.Input,
		"stream":         false,
		"voice_setting":  voice,
		"audio_setting":  map[string]any{"sample_rate": 32000, "bitrate": 128000, "format": format, "channel": 1},
		"language_boost": "auto",
	}
	spec, err := marshalJSONSpec("speech", http.MethodPost, endpointUnlessPresent(client.baseURL, "t2a_v2"), payload)
	if err != nil {
		return nil, "", err
	}
	if group := req.option("group_id"); group != "" {
		spec.query = url.Values{"GroupId": {group}}
	}
	result, err := client.do(ctx, spec, maxSpeechAudioBytes*2+4096)
	if err != nil {
		return nil, "", err
	}
	var parsed struct {
		Data struct {
			Audio string `json:"audio"`
		} `json:"data"`
		BaseResp struct {
			StatusCode int    `json:"status_code"`
			StatusMsg  string `json:"status_msg"`
		} `json:"base_resp"`
	}
	if err := json.Unmarshal(result.body, &parsed); err != nil {
		return nil, "", badSpeechResponse(result.status, string(result.body))
	}
	if parsed.BaseResp.StatusCode != 0 {
		kind := MediaErrorInvalid
		if parsed.BaseResp.StatusCode == 1004 {
			kind = MediaErrorAuth
		} else if parsed.BaseResp.StatusCode == 1002 {
			kind = MediaErrorRateLimit
		}
		return nil, "", &MediaAPIError{Op: "speech", Kind: kind, StatusCode: result.status, Detail: fmt.Sprintf("MiniMax %d: %s", parsed.BaseResp.StatusCode, parsed.BaseResp.StatusMsg)}
	}
	audio, err := hex.DecodeString(strings.TrimPrefix(strings.TrimSpace(parsed.Data.Audio), "0x"))
	if err != nil {
		return nil, "", badSpeechResponse(result.status, "MiniMax 返回的音频不是十六进制")
	}
	return audio, format, nil
}

// synthesizeVolcengine 调火山引擎（豆包）语音合成。v1 接口用 appid + token + cluster，
// 一次回一整段 base64；v3 单向流式接口用请求头鉴权，按行回 JSON 分片。填了
// resource_id 或地址里带 /v3/ 时走 v3。
func synthesizeVolcengine(ctx context.Context, client *mediaHTTP, cfg ProviderConfig, req SpeechRequest) ([]byte, string, error) {
	if req.Voice == "" {
		return nil, "", errors.New("llm: volcengine speech requires a voice type")
	}
	appID := req.option("app_id")
	if appID == "" {
		return nil, "", errors.New("llm: volcengine speech requires app_id")
	}
	format := formatOr(req.Format, "mp3", "mp3", "wav", "ogg_opus", "pcm")
	resource := req.option("resource_id")
	if resource != "" || strings.Contains(client.baseURL, "/v3/") {
		if !strings.Contains(client.baseURL, "/v3/") {
			client.baseURL = defaultVolcengineV3URL
		}
		return synthesizeVolcengineV3(ctx, client, cfg, req, appID, firstNonEmptyString(resource, defaultVolcengineV3Res), format)
	}
	token := strings.TrimSpace(cfg.APIKey)
	client.auth = func(r *http.Request) { r.Header.Set("Authorization", "Bearer; "+token) }
	speed := req.Speed
	if speed <= 0 {
		speed = 1
	}
	payload := map[string]any{
		"app":  map[string]any{"appid": appID, "token": token, "cluster": firstNonEmptyString(req.option("cluster"), defaultVolcengineCluster)},
		"user": map[string]any{"uid": uuid.NewString()},
		"audio": map[string]any{
			"voice_type": req.Voice, "encoding": format, "speed_ratio": speed, "volume_ratio": 1.0, "pitch_ratio": 1.0,
		},
		"request": map[string]any{
			"reqid": uuid.NewString(), "text": req.Input, "text_type": "plain", "operation": "query",
		},
	}
	spec, err := marshalJSONSpec("speech", http.MethodPost, "", payload)
	if err != nil {
		return nil, "", err
	}
	result, err := client.do(ctx, spec, maxSpeechAudioBytes*2)
	if err != nil {
		return nil, "", err
	}
	var parsed struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Data    string `json:"data"`
	}
	if err := json.Unmarshal(result.body, &parsed); err != nil {
		return nil, "", badSpeechResponse(result.status, string(result.body))
	}
	if parsed.Code != 3000 || parsed.Data == "" {
		return nil, "", volcengineError(result.status, parsed.Code, parsed.Message)
	}
	audio, err := base64.StdEncoding.DecodeString(parsed.Data)
	return audio, volcengineFileFormat(format), err
}

func synthesizeVolcengineV3(ctx context.Context, client *mediaHTTP, cfg ProviderConfig, req SpeechRequest, appID, resource, format string) ([]byte, string, error) {
	key := strings.TrimSpace(cfg.APIKey)
	client.auth = func(r *http.Request) {
		r.Header.Set("X-Api-App-Id", appID)
		r.Header.Set("X-Api-Access-Key", key)
		r.Header.Set("X-Api-Resource-Id", resource)
	}
	audioParams := map[string]any{"format": format, "sample_rate": 24000}
	if req.Speed > 0 {
		// v3 的语速是 [-50, 100]，0 为正常，对应 0.5 到 2 倍。
		audioParams["speech_rate"] = int(math.Round((req.Speed - 1) * 100))
	}
	payload := map[string]any{
		"user":       map[string]any{"uid": uuid.NewString()},
		"req_params": map[string]any{"text": req.Input, "speaker": req.Voice, "audio_params": audioParams},
	}
	spec, err := marshalJSONSpec("speech", http.MethodPost, "", payload)
	if err != nil {
		return nil, "", err
	}
	result, err := client.do(ctx, spec, maxSpeechAudioBytes*2)
	if err != nil {
		return nil, "", err
	}
	var audio bytes.Buffer
	for _, line := range bytes.Split(result.body, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var chunk struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
			Data    string `json:"data"`
		}
		if err := json.Unmarshal(line, &chunk); err != nil {
			return nil, "", badSpeechResponse(result.status, string(line))
		}
		if chunk.Code != 0 && chunk.Code != 20000000 {
			return nil, "", volcengineError(result.status, chunk.Code, chunk.Message)
		}
		if chunk.Data == "" {
			continue
		}
		data, err := base64.StdEncoding.DecodeString(chunk.Data)
		if err != nil {
			return nil, "", badSpeechResponse(result.status, "火山引擎返回的音频分片不是 base64")
		}
		audio.Write(data)
	}
	return audio.Bytes(), volcengineFileFormat(format), nil
}

func volcengineFileFormat(format string) string {
	if format == "ogg_opus" {
		return "ogg"
	}
	return format
}

func volcengineError(status, code int, message string) error {
	kind := MediaErrorInvalid
	switch {
	case code == 3001 || code == 45000000 || strings.Contains(strings.ToLower(message), "auth"):
		kind = MediaErrorAuth
	case code == 3005 || code == 3006 || code == 45000292:
		kind = MediaErrorRateLimit
	case code == 3040 || code == 3050:
		kind = MediaErrorUpstream
	}
	return &MediaAPIError{Op: "speech", Kind: kind, StatusCode: status, Detail: fmt.Sprintf("火山引擎 %d: %s", code, message)}
}

// synthesizeMiMo 调小米 MiMo-TTS：借对话接口合成，要念的文字放在 assistant 消息里，
// 音频以 base64 放在 choices[0].message.audio.data。语气说明按 AstrBot 的做法写成
// 开头的 <style> 标签；音色设计模型没有内置音色，说明改放 user 消息。
func synthesizeMiMo(ctx context.Context, client *mediaHTTP, cfg ProviderConfig, req SpeechRequest) ([]byte, string, error) {
	client.auth = bearerAuth(cfg.APIKey)
	model := firstNonEmptyString(req.Model, defaultMiMoSpeechModel)
	format := formatOr(req.Format, "wav", "wav", "mp3")
	instructions := strings.TrimSpace(req.Instructions)
	audioParams := map[string]any{"format": format}
	var messages []map[string]any
	if strings.Contains(strings.ToLower(model), "voicedesign") {
		if instructions == "" {
			return nil, "", errors.New("llm: mimo voice design requires instructions describing the voice")
		}
		messages = append(messages, map[string]any{"role": "user", "content": instructions})
		messages = append(messages, map[string]any{"role": "assistant", "content": req.Input})
	} else {
		audioParams["voice"] = firstNonEmptyString(req.Voice, defaultMiMoVoice)
		content := req.Input
		if instructions != "" {
			content = "<style>" + instructions + "</style>" + content
		}
		messages = append(messages, map[string]any{"role": "assistant", "content": content})
	}
	spec, err := marshalJSONSpec("speech", http.MethodPost, endpointUnlessPresent(client.baseURL, "chat/completions"), map[string]any{
		"model": model, "messages": messages, "audio": audioParams,
	})
	if err != nil {
		return nil, "", err
	}
	result, err := client.do(ctx, spec, maxSpeechAudioBytes*2)
	if err != nil {
		return nil, "", err
	}
	var parsed struct {
		Choices []struct {
			FinishReason string `json:"finish_reason"`
			Message      struct {
				Audio struct {
					Data string `json:"data"`
				} `json:"audio"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(result.body, &parsed); err != nil || len(parsed.Choices) == 0 || parsed.Choices[0].Message.Audio.Data == "" {
		if len(parsed.Choices) > 0 && parsed.Choices[0].FinishReason == "content_filter" {
			return nil, "", &MediaAPIError{Op: "speech", Kind: MediaErrorContentPolicy, StatusCode: result.status, Detail: "content_filter"}
		}
		return nil, "", badSpeechResponse(result.status, string(result.body))
	}
	audio, err := base64.StdEncoding.DecodeString(parsed.Choices[0].Message.Audio.Data)
	return audio, format, err
}

// synthesizeNovelAI 调 NovelAI 的 generate-voice；音色名作为 seed，v2 语音。
func synthesizeNovelAI(ctx context.Context, client *mediaHTTP, cfg ProviderConfig, req SpeechRequest) ([]byte, string, error) {
	client.auth = bearerAuth(cfg.APIKey)
	result, err := client.do(ctx, mediaRequestSpec{
		op: "speech", method: http.MethodGet, endpoint: endpointUnlessPresent(client.baseURL, "ai/generate-voice"), accept: "audio/mpeg",
		query: url.Values{"text": {req.Input}, "voice": {"-1"}, "seed": {firstNonEmptyString(req.Voice, defaultNovelAIVoice)}, "opus": {"false"}, "version": {"v2"}},
	}, maxSpeechAudioBytes)
	if err != nil {
		return nil, "", err
	}
	audio, err := audioBody(result)
	return audio, "mp3", err
}

// synthesizeGoogleTranslate 用谷歌翻译的朗读，免费、不要 Key。和 gTTS 一样走
// batchexecute；单次最多一百来字，长文按标点切段后把 MP3 首尾相接。
func synthesizeGoogleTranslate(ctx context.Context, cfg ProviderConfig, req SpeechRequest, opts []ClientOption) ([]byte, string, error) {
	client, err := newMediaHTTPWithKey(cfg, defaultGoogleTranslate, opts, false)
	if err != nil {
		return nil, "", err
	}
	client.policy.AttemptTimeout = req.Timeout
	lang := firstNonEmptyString(req.option("language"), req.Voice, "zh-CN")
	var audio bytes.Buffer
	for _, chunk := range splitSpeechText(req.Input, 100) {
		parameter, _ := json.Marshal([]any{chunk, lang, nil, "null"})
		rpc, _ := json.Marshal([]any{[]any{[]any{"jQ1olc", string(parameter), nil, "generic"}}})
		result, err := client.do(ctx, mediaRequestSpec{
			op: "speech", method: http.MethodPost, endpoint: "_/TranslateWebserverUi/data/batchexecute",
			body: []byte("f.req=" + url.QueryEscape(string(rpc)) + "&"), contentType: "application/x-www-form-urlencoded;charset=utf-8",
		}, maxSpeechAudioBytes)
		if err != nil {
			return nil, "", err
		}
		match := regexp.MustCompile(`jQ1olc","\[\\"(.*?)\\"]`).FindSubmatch(result.body)
		if len(match) != 2 {
			return nil, "", badSpeechResponse(result.status, "谷歌翻译未返回音频，语言代码可能不支持："+lang)
		}
		data, err := base64.StdEncoding.DecodeString(string(match[1]))
		if err != nil {
			return nil, "", badSpeechResponse(result.status, "谷歌翻译返回的音频不是 base64")
		}
		audio.Write(data)
	}
	return audio.Bytes(), "mp3", nil
}

// splitSpeechText 把长文按标点切成不超过 limit 个字的段，找不到标点时硬切。
func splitSpeechText(text string, limit int) []string {
	var chunks []string
	for utf8.RuneCountInString(text) > limit {
		runes := []rune(text)
		cut := limit
		for i := limit; i > limit/2; i-- {
			if strings.ContainsRune("。！？!?；;，,、 \n", runes[i-1]) {
				cut = i
				break
			}
		}
		if chunk := strings.TrimSpace(string(runes[:cut])); chunk != "" {
			chunks = append(chunks, chunk)
		}
		text = string(runes[cut:])
	}
	if chunk := strings.TrimSpace(text); chunk != "" {
		chunks = append(chunks, chunk)
	}
	return chunks
}

// synthesizeCustomHTTP 按模板请求任意自建服务。模板里的 {{text}}、{{voice}}、
// {{model}}、{{speed}}、{{format}}、{{lang}} 会被替换：JSON 模板里按 JSON 字符串转义
// （不带引号，模板自己写引号），表单和查询串里按 URL 转义。response 说明音频在哪：
// audio（默认，响应体就是音频）、base64:路径、hex:路径、url:路径，路径用点号分隔，
// 如 data.audio、output.audio.url。
func synthesizeCustomHTTP(ctx context.Context, client *mediaHTTP, req SpeechRequest) ([]byte, string, error) {
	client.auth = bearerAuth(client.cfg.APIKey)
	method := strings.ToUpper(firstNonEmptyString(req.option("method"), http.MethodPost))
	bodyType := strings.ToLower(firstNonEmptyString(req.option("body_type"), "json"))
	values := map[string]string{
		"text": req.Input, "voice": req.Voice, "model": req.Model, "format": firstNonEmptyString(req.Format, "wav"),
		"speed": strconv.FormatFloat(firstNonZero(req.Speed, 1), 'f', -1, 64), "lang": firstNonEmptyString(req.option("language"), "zh"),
	}
	spec := mediaRequestSpec{op: "speech", method: method, accept: "audio/*, application/json, application/octet-stream"}
	path := fillSpeechTemplate(req.option("path"), values, url.QueryEscape)
	body := req.option("body")
	switch bodyType {
	case "form":
		spec.body = []byte(fillSpeechTemplate(body, values, url.QueryEscape))
		spec.contentType = "application/x-www-form-urlencoded"
	case "query":
		if filled := fillSpeechTemplate(body, values, url.QueryEscape); filled != "" {
			if strings.Contains(path, "?") {
				path += "&" + filled
			} else {
				path += "?" + filled
			}
		}
	default:
		filled := fillSpeechTemplate(body, values, jsonStringContent)
		if filled != "" && !json.Valid([]byte(filled)) {
			return nil, "", fmt.Errorf("llm: custom speech request body is not valid JSON after filling: %s", filled)
		}
		if filled != "" {
			spec.body = []byte(filled)
			spec.contentType = "application/json"
		}
	}
	// 查询串单独交给 spec.query：拼进 endpoint 的问号会被当成路径的一部分转义掉。
	path, rawQuery, _ := strings.Cut(path, "?")
	spec.endpoint = path
	if rawQuery != "" {
		query, err := url.ParseQuery(rawQuery)
		if err != nil {
			return nil, "", fmt.Errorf("llm: custom speech query is invalid: %w", err)
		}
		spec.query = query
	}
	if method == http.MethodGet && bodyType != "query" {
		spec.body = nil
	}
	result, err := client.do(ctx, spec, maxSpeechAudioBytes*2)
	if err != nil {
		return nil, "", err
	}
	format := values["format"]
	mode, jsonPathText, _ := strings.Cut(firstNonEmptyString(req.option("response"), "audio"), ":")
	if mode == "audio" {
		audio, err := audioBody(result)
		if contentType := result.header.Get("Content-Type"); strings.HasPrefix(contentType, "audio/") {
			format = formatFromMediaType(contentType)
		}
		return audio, format, err
	}
	var document any
	if err := json.Unmarshal(result.body, &document); err != nil {
		return nil, "", badSpeechResponse(result.status, string(result.body))
	}
	value, _ := jsonPathValue(document, jsonPathText).(string)
	if value == "" {
		return nil, "", badSpeechResponse(result.status, "响应里没有 "+jsonPathText+"："+string(result.body))
	}
	switch mode {
	case "base64":
		if _, payload, ok := strings.Cut(value, "base64,"); ok {
			value = payload
		}
		audio, err := base64.StdEncoding.DecodeString(value)
		return audio, format, err
	case "hex":
		audio, err := hex.DecodeString(value)
		return audio, format, err
	case "url":
		if !strings.HasPrefix(value, "http") && !strings.HasPrefix(value, "data:") {
			// 有的服务回的是相对路径，按服务根补全。
			value = strings.TrimRight(client.baseURL, "/") + "/" + strings.TrimLeft(value, "/")
		}
		audio, err := client.download(ctx, value)
		return audio, format, err
	}
	return nil, "", fmt.Errorf("llm: unknown custom speech response mode %q", mode)
}

func firstNonZero(value, fallback float64) float64 {
	if value > 0 {
		return value
	}
	return fallback
}

func fillSpeechTemplate(template string, values map[string]string, escape func(string) string) string {
	for key, value := range values {
		template = strings.ReplaceAll(template, "{{"+key+"}}", escape(value))
	}
	return template
}

func jsonStringContent(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded[1 : len(encoded)-1])
}

func jsonPathValue(document any, path string) any {
	current := document
	for _, key := range strings.Split(strings.TrimSpace(path), ".") {
		if key == "" {
			continue
		}
		switch node := current.(type) {
		case map[string]any:
			current = node[key]
		case []any:
			index, err := strconv.Atoi(key)
			if err != nil || index < 0 || index >= len(node) {
				return nil
			}
			current = node[index]
		default:
			return nil
		}
	}
	return current
}

// transcribeMiMo 用 MiMo 的对话接口做语音识别：专用 ASR 模型只收音频，多模态模型
// 还要一句转写指令，否则会被拒。音频以 data URL 形式内联。
func transcribeMiMo(ctx context.Context, cfg ProviderConfig, req TranscriptionRequest, opts []ClientOption, timeout time.Duration) (*TranscriptionResponse, error) {
	client, err := newMediaHTTP(cfg, defaultMiMoBaseURL, opts)
	if err != nil {
		return nil, err
	}
	client.policy.AttemptTimeout = timeout
	model := firstNonEmptyString(req.Model, defaultMiMoSTTModel)
	audio := map[string]any{"type": "input_audio", "input_audio": map[string]any{"data": "data:audio/wav;base64," + base64.StdEncoding.EncodeToString(req.Audio)}}
	messages := []any{map[string]any{"role": "user", "content": []any{audio}}}
	if !strings.Contains(strings.ToLower(model), "asr") {
		instruction := "Please transcribe the content of the audio and return only the transcription text."
		messages = []any{
			map[string]any{"role": "system", "content": "You are a speech transcription assistant. Transcribe the spoken content from the audio exactly and return only the transcription text."},
			map[string]any{"role": "user", "content": []any{audio, map[string]any{"type": "text", "text": instruction}}},
		}
	}
	spec, err := marshalJSONSpec("transcription", http.MethodPost, endpointUnlessPresent(client.baseURL, "chat/completions"), map[string]any{
		"model": model, "messages": messages, "max_completion_tokens": 1024,
	})
	if err != nil {
		return nil, err
	}
	result, err := client.do(ctx, spec, maxTranscriptionResponseBytes)
	if err != nil {
		return nil, err
	}
	var parsed struct {
		Choices []struct {
			Message struct {
				Content          string `json:"content"`
				ReasoningContent string `json:"reasoning_content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(result.body, &parsed); err != nil || len(parsed.Choices) == 0 {
		return nil, &MediaAPIError{Op: "transcription", Kind: MediaErrorBadResponse, StatusCode: result.status, Detail: strings.TrimSpace(string(result.body))}
	}
	text := strings.TrimSpace(firstNonEmptyString(parsed.Choices[0].Message.Content, parsed.Choices[0].Message.ReasoningContent))
	if text == "" {
		return nil, &MediaAPIError{Op: "transcription", Kind: MediaErrorBadResponse, StatusCode: result.status, Detail: "empty transcript"}
	}
	return &TranscriptionResponse{API: SpeechAPIMiMo, Model: model, Text: text}, nil
}
