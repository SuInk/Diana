// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"
)

// SpeechAPI 是语音接口的协议形态。provider 类型说的是对话协议，一个 OpenAI 兼容
// 的配置档背后可能是 ElevenLabs，所以语音另外按接口形态分派。
type SpeechAPI string

const (
	// SpeechAPIOpenAI 覆盖 /audio/speech 与 /audio/transcriptions：OpenAI 本身，
	// 以及 CosyVoice、ChatTTS、FunASR、faster-whisper 这类自建服务的兼容层。
	SpeechAPIOpenAI     SpeechAPI = "openai"
	SpeechAPIElevenLabs SpeechAPI = "elevenlabs"
)

const (
	defaultOpenAIMediaBaseURL     = "https://api.openai.com/v1"
	defaultElevenLabsBaseURL      = "https://api.elevenlabs.io/v1"
	defaultSpeechTimeout          = 60 * time.Second
	defaultTranscriptionTimeout   = 120 * time.Second
	maxSpeechAudioBytes           = 32 << 20
	maxTranscriptionResponseBytes = 4 << 20
)

// ResolveSpeechAPI 选语音接口形态：显式指定的优先，否则按地址认出 ElevenLabs，
// 其余一律按 OpenAI 兼容处理。
func ResolveSpeechAPI(explicit string, cfg ProviderConfig) SpeechAPI {
	switch SpeechAPI(strings.ToLower(strings.TrimSpace(explicit))) {
	case SpeechAPIElevenLabs:
		return SpeechAPIElevenLabs
	case SpeechAPIOpenAI:
		return SpeechAPIOpenAI
	}
	if parsed, err := url.Parse(strings.TrimSpace(cfg.BaseURL)); err == nil && strings.HasSuffix(strings.ToLower(parsed.Hostname()), "elevenlabs.io") {
		return SpeechAPIElevenLabs
	}
	return SpeechAPIOpenAI
}

// SpeechRequest 是一次文本转语音请求。
type SpeechRequest struct {
	// API 为空时按配置档地址推断，见 ResolveSpeechAPI。
	API   SpeechAPI
	Model string
	Input string
	// Voice 在 OpenAI 兼容接口里是音色名（alloy、自建服务的说话人），在 ElevenLabs 里是 voice_id。
	Voice string
	// Format 是 mp3、wav、opus、aac、flac、pcm 之一；ElevenLabs 也接受它原生的
	// mp3_44100_128 这类写法。为空时用 mp3。
	Format string
	// Speed 为 0 时不下发，由服务端用默认语速。
	Speed float64
	// Instructions 是 gpt-4o-mini-tts 这类模型的语气说明，其他服务会忽略。
	Instructions string
	// Timeout 是每次尝试的上限，为 0 时用 60 秒。
	Timeout time.Duration
}

// SpeechResponse 是合成好的完整音频。
type SpeechResponse struct {
	API       SpeechAPI
	Model     string
	Audio     []byte
	MediaType string
	// Format 是文件扩展名意义上的格式（mp3、wav……），用来给落盘的文件命名。
	Format string
}

// SpeechStream 是边合成边返回的音频流，读完后由调用方 Close。
type SpeechStream struct {
	io.ReadCloser
	API       SpeechAPI
	Model     string
	MediaType string
	Format    string
}

// SynthesizeSpeech 调用配置档上的语音合成接口，返回完整音频。
func SynthesizeSpeech(ctx context.Context, cfg ProviderConfig, req SpeechRequest, opts ...ClientOption) (*SpeechResponse, error) {
	req.API = ResolveSpeechAPI(string(req.API), cfg)
	client, spec, format, err := newSpeechCall(cfg, req, false, opts)
	if err != nil {
		return nil, err
	}
	result, err := client.do(ctx, spec, maxSpeechAudioBytes)
	if err != nil {
		return nil, err
	}
	if err := jsonErrorOn200(spec.op, result); err != nil {
		return nil, err
	}
	if len(result.body) == 0 {
		return nil, &MediaAPIError{Op: spec.op, Kind: MediaErrorBadResponse, StatusCode: result.status, Detail: "empty audio"}
	}
	return &SpeechResponse{
		API:       req.API,
		Model:     req.Model,
		Audio:     result.body,
		MediaType: speechMediaType(result.header.Get("Content-Type"), format),
		Format:    format,
	}, nil
}

// StreamSpeech 和 SynthesizeSpeech 一样，但拿到响应头就把音频流交给调用方。
// 只在拿到响应头之前重试；之后断流由调用方处理。
func StreamSpeech(ctx context.Context, cfg ProviderConfig, req SpeechRequest, opts ...ClientOption) (*SpeechStream, error) {
	req.API = ResolveSpeechAPI(string(req.API), cfg)
	client, spec, format, err := newSpeechCall(cfg, req, true, opts)
	if err != nil {
		return nil, err
	}
	var resp *http.Response
	for attempt := 0; ; attempt++ {
		resp, err = client.send(ctx, spec)
		if err == nil {
			break
		}
		if !IsRetryableMediaError(err) || attempt >= client.policy.MaxRetries {
			return nil, err
		}
		var apiErr *MediaAPIError
		errors.As(err, &apiErr)
		if sleepErr := client.sleep(ctx, client.policy.delay(attempt, apiErr.RetryAfter)); sleepErr != nil {
			return nil, err
		}
	}
	return &SpeechStream{
		ReadCloser: resp.Body,
		API:        req.API,
		Model:      req.Model,
		MediaType:  speechMediaType(resp.Header.Get("Content-Type"), format),
		Format:     format,
	}, nil
}

func newSpeechCall(cfg ProviderConfig, req SpeechRequest, stream bool, opts []ClientOption) (*mediaHTTP, mediaRequestSpec, string, error) {
	req.Input = strings.TrimSpace(req.Input)
	req.Model = strings.TrimSpace(req.Model)
	req.Voice = strings.TrimSpace(req.Voice)
	if req.Input == "" {
		return nil, mediaRequestSpec{}, "", errors.New("llm: speech input is required")
	}
	if req.Speed < 0 || req.Speed > 4 {
		return nil, mediaRequestSpec{}, "", errors.New("llm: speech speed must be between 0.25 and 4")
	}
	format := strings.ToLower(strings.TrimSpace(req.Format))
	if format == "" {
		format = "mp3"
	}
	timeout := req.Timeout
	if timeout <= 0 {
		timeout = defaultSpeechTimeout
	}
	switch req.API {
	case SpeechAPIElevenLabs:
		client, err := newElevenLabsHTTP(cfg, opts)
		if err != nil {
			return nil, mediaRequestSpec{}, "", err
		}
		client.policy.AttemptTimeout = timeout
		spec, fileFormat, err := elevenLabsSpeechSpec(req, format, stream)
		return client, spec, fileFormat, err
	default:
		if req.Model == "" {
			return nil, mediaRequestSpec{}, "", ErrMissingModel
		}
		client, err := newMediaHTTP(cfg, defaultOpenAIMediaBaseURL, opts)
		if err != nil {
			return nil, mediaRequestSpec{}, "", err
		}
		client.policy.AttemptTimeout = timeout
		payload := map[string]any{"model": req.Model, "input": req.Input, "response_format": format}
		// 自建兼容层多半要求 voice 必填，但各家名字不一，没配就不替它瞎填一个。
		if req.Voice != "" {
			payload["voice"] = req.Voice
		}
		if req.Speed > 0 {
			payload["speed"] = req.Speed
		}
		if instructions := strings.TrimSpace(req.Instructions); instructions != "" {
			payload["instructions"] = instructions
		}
		if stream {
			payload["stream"] = true
		}
		body, err := json.Marshal(payload)
		if err != nil {
			return nil, mediaRequestSpec{}, "", err
		}
		return client, mediaRequestSpec{op: "speech", method: http.MethodPost, endpoint: "audio/speech", body: body, contentType: "application/json", accept: "audio/*, application/octet-stream"}, format, nil
	}
}

func newElevenLabsHTTP(cfg ProviderConfig, opts []ClientOption) (*mediaHTTP, error) {
	client, err := newMediaHTTP(cfg, defaultElevenLabsBaseURL, opts)
	if err != nil {
		return nil, err
	}
	// 地址填成 https://api.elevenlabs.io 也要能用：官方路径都在 /v1 下面。
	if parsed, err := url.Parse(client.baseURL); err == nil && !strings.HasSuffix(strings.TrimRight(parsed.Path, "/"), "/v1") {
		parsed.Path = strings.TrimRight(parsed.Path, "/") + "/v1"
		client.baseURL = parsed.String()
	}
	key := strings.TrimSpace(cfg.APIKey)
	client.auth = func(req *http.Request) {
		if key != "" {
			req.Header.Set("xi-api-key", key)
		}
	}
	return client, nil
}

func elevenLabsSpeechSpec(req SpeechRequest, format string, stream bool) (mediaRequestSpec, string, error) {
	if req.Voice == "" {
		return mediaRequestSpec{}, "", errors.New("llm: elevenlabs speech requires a voice id")
	}
	outputFormat, fileFormat := elevenLabsOutputFormat(format)
	payload := map[string]any{"text": req.Input}
	if req.Model != "" {
		payload["model_id"] = req.Model
	}
	if req.Speed > 0 {
		payload["voice_settings"] = map[string]any{"speed": req.Speed}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return mediaRequestSpec{}, "", err
	}
	endpoint := "text-to-speech/" + url.PathEscape(req.Voice)
	if stream {
		endpoint += "/stream"
	}
	return mediaRequestSpec{
		op:          "speech",
		method:      http.MethodPost,
		endpoint:    endpoint,
		query:       url.Values{"output_format": {outputFormat}},
		body:        body,
		contentType: "application/json",
		accept:      "audio/*, application/octet-stream",
	}, fileFormat, nil
}

// elevenLabsOutputFormat 把通用格式名翻成 ElevenLabs 的 output_format，同时给出
// 落盘用的扩展名。原生写法（mp3_44100_128）原样放行。
func elevenLabsOutputFormat(format string) (string, string) {
	if prefix, _, ok := strings.Cut(format, "_"); ok {
		switch prefix {
		case "ulaw", "alaw":
			return format, "wav"
		}
		return format, prefix
	}
	switch format {
	case "wav":
		return "wav_44100", "wav"
	case "pcm":
		return "pcm_24000", "pcm"
	case "opus":
		return "opus_48000_64", "opus"
	default:
		return "mp3_44100_128", "mp3"
	}
}

func speechMediaType(contentType, format string) string {
	if mediaType := strings.TrimSpace(strings.Split(contentType, ";")[0]); strings.HasPrefix(strings.ToLower(mediaType), "audio/") {
		return mediaType
	}
	switch format {
	case "wav":
		return "audio/wav"
	case "opus":
		return "audio/ogg"
	case "aac":
		return "audio/aac"
	case "flac":
		return "audio/flac"
	case "pcm":
		return "audio/pcm"
	default:
		return "audio/mpeg"
	}
}

// TranscriptionRequest 是一次语音转文字请求。
type TranscriptionRequest struct {
	API   SpeechAPI
	Model string
	Audio []byte
	// Filename 决定上传时的扩展名，服务端多半靠它判断格式；为空时按 audio.wav。
	Filename string
	// Language 是 ISO-639-1 代码；空或 auto 时让服务端自己识别。
	Language string
	Prompt   string
	// Timeout 是每次尝试的上限，为 0 时用 120 秒。
	Timeout time.Duration
}

// TranscriptionResponse 是转写结果。
type TranscriptionResponse struct {
	API      SpeechAPI
	Model    string
	Text     string
	Language string
	Duration time.Duration
}

// TranscribeAudio 调用配置档上的语音识别接口。
func TranscribeAudio(ctx context.Context, cfg ProviderConfig, req TranscriptionRequest, opts ...ClientOption) (*TranscriptionResponse, error) {
	req.API = ResolveSpeechAPI(string(req.API), cfg)
	req.Model = strings.TrimSpace(req.Model)
	if len(req.Audio) == 0 {
		return nil, errors.New("llm: transcription audio is required")
	}
	filename := filepath.Base(strings.TrimSpace(req.Filename))
	if filename == "" || filename == "." || filename == "/" {
		filename = "audio.wav"
	}
	language := strings.TrimSpace(req.Language)
	if strings.EqualFold(language, "auto") {
		language = ""
	}
	timeout := req.Timeout
	if timeout <= 0 {
		timeout = defaultTranscriptionTimeout
	}
	var (
		client *mediaHTTP
		err    error
		fields [][2]string
		spec   = mediaRequestSpec{op: "transcription", method: http.MethodPost, accept: "application/json"}
	)
	switch req.API {
	case SpeechAPIElevenLabs:
		client, err = newElevenLabsHTTP(cfg, opts)
		spec.endpoint = "speech-to-text"
		fields = append(fields, [2]string{"model_id", firstNonEmptyString(req.Model, "scribe_v1")})
		if language != "" {
			fields = append(fields, [2]string{"language_code", language})
		}
	default:
		if req.Model == "" {
			return nil, ErrMissingModel
		}
		client, err = newMediaHTTP(cfg, defaultOpenAIMediaBaseURL, opts)
		spec.endpoint = "audio/transcriptions"
		fields = append(fields, [2]string{"model", req.Model}, [2]string{"response_format", "json"})
		if language != "" {
			fields = append(fields, [2]string{"language", language})
		}
		if prompt := strings.TrimSpace(req.Prompt); prompt != "" {
			fields = append(fields, [2]string{"prompt", prompt})
		}
	}
	if err != nil {
		return nil, err
	}
	client.policy.AttemptTimeout = timeout
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for _, field := range fields {
		if err := writer.WriteField(field[0], field[1]); err != nil {
			return nil, err
		}
	}
	part, err := writer.CreateFormFile("file", filename)
	if err != nil {
		return nil, err
	}
	if _, err := part.Write(req.Audio); err != nil {
		return nil, err
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	spec.body = body.Bytes()
	spec.contentType = writer.FormDataContentType()
	result, err := client.do(ctx, spec, maxTranscriptionResponseBytes)
	if err != nil {
		return nil, err
	}
	var parsed struct {
		Text         string  `json:"text"`
		Language     string  `json:"language"`
		LanguageCode string  `json:"language_code"`
		Duration     float64 `json:"duration"`
		Error        any     `json:"error"`
	}
	text := ""
	if json.Unmarshal(result.body, &parsed) == nil {
		if parsed.Error != nil && strings.TrimSpace(parsed.Text) == "" {
			return nil, &MediaAPIError{Op: spec.op, Kind: MediaErrorBadResponse, StatusCode: result.status, Detail: strings.TrimSpace(string(result.body))}
		}
		text = parsed.Text
	} else {
		// 有的自建服务无视 response_format=json，直接回纯文本。
		text = string(result.body)
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, &MediaAPIError{Op: spec.op, Kind: MediaErrorBadResponse, StatusCode: result.status, Detail: "empty transcript"}
	}
	return &TranscriptionResponse{
		API:      req.API,
		Model:    req.Model,
		Text:     text,
		Language: firstNonEmptyString(parsed.Language, parsed.LanguageCode),
		Duration: time.Duration(parsed.Duration * float64(time.Second)),
	}, nil
}
