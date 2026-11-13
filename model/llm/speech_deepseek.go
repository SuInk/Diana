// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DeepSeek 的语音放在「工具 API」下面：POST /v1/tools/tts 回音频，POST
// /v1/tools/asr 收 multipart 上传、回 JSON。两个接口都没有 model 字段，也不收
// 语速和语言（语言按内容自动识别）。文档：api-docs.deepseek.com/guides/tts、
// /guides/asr。
const (
	defaultDeepSeekBaseURL = "https://api.deepseek.com"
	defaultDeepSeekVoice   = "stella"
	// 接口本身不认 model，请求里的模型名不往下传。语音槽位没选模型时，记录里用
	// deepSeekTTSModel、deepSeekASRModel 标出是谁合成、转写的；选了就照用，
	// 转写缓存键按槽位的模型名算，回报别的名字会让缓存永远对不上。
	deepSeekTTSModel = "deepseek-tts"
	deepSeekASRModel = "deepseek-asr"
	// DeepSeek 只认 16 位 PCM WAV 或 Ogg Opus，单个文件不超过 8 MiB。
	maxDeepSeekASRBytes = 8 << 20
)

// DeepSeekASRMaxDuration 是 DeepSeek 语音识别单段音频的时长上限，超了服务端回 400。
const DeepSeekASRMaxDuration = 120 * time.Second

// deepSeekToolEndpoint 让地址填 https://api.deepseek.com、带 /v1 或直接填到
// 接口本身都能用：对话配置档的地址两种写法都有，语音可以直接复用。
func deepSeekToolEndpoint(base, tool string) string {
	parsed, err := url.Parse(strings.TrimSpace(base))
	if err != nil {
		return "v1/tools/" + tool
	}
	path := strings.TrimRight(parsed.Path, "/")
	switch {
	case strings.HasSuffix(path, "/tools/"+tool):
		return ""
	case strings.HasSuffix(path, "/v1"):
		return "tools/" + tool
	}
	return "v1/tools/" + tool
}

// synthesizeDeepSeek 调 /v1/tools/tts。音色只有 stella（女）和 tide（男），格式
// 只有 wav 和 24kHz 裸 PCM；裸 PCM 补成 WAV，QQ 认不了没有头的音频。
func synthesizeDeepSeek(ctx context.Context, client *mediaHTTP, cfg ProviderConfig, req SpeechRequest) ([]byte, string, error) {
	client.auth = bearerAuth(cfg.APIKey)
	format := formatOr(req.Format, "wav", "wav", "pcm")
	spec, err := marshalJSONSpec("speech", http.MethodPost, deepSeekToolEndpoint(client.baseURL, "tts"), map[string]any{
		"text": req.Input, "voice_id": firstNonEmptyString(req.Voice, defaultDeepSeekVoice), "format": format,
	})
	if err != nil {
		return nil, "", err
	}
	spec.accept = "audio/*, application/octet-stream"
	result, err := client.do(ctx, spec, maxSpeechAudioBytes*4)
	if err != nil {
		return nil, "", err
	}
	audio, err := audioBody(result)
	if err != nil {
		return nil, "", err
	}
	if format == "pcm" {
		return pcmToWAV(audio, 24000), "wav", nil
	}
	return audio, "wav", nil
}

// transcribeDeepSeek 调 /v1/tools/asr。转写流程送来的是 ffmpeg 规整过的 16kHz
// 单声道 WAV，正好是它要的格式；音频上限 120 秒，超了由服务端报 400。
func transcribeDeepSeek(ctx context.Context, cfg ProviderConfig, req TranscriptionRequest, filename string, opts []ClientOption, timeout time.Duration) (*TranscriptionResponse, error) {
	if len(req.Audio) > maxDeepSeekASRBytes {
		return nil, &MediaAPIError{Op: "transcription", Kind: MediaErrorInvalid, Detail: "DeepSeek 语音识别单个文件不能超过 8 MiB"}
	}
	client, err := newMediaHTTP(cfg, defaultDeepSeekBaseURL, opts)
	if err != nil {
		return nil, err
	}
	client.auth = bearerAuth(cfg.APIKey)
	client.policy.AttemptTimeout = timeout
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
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
	result, err := client.do(ctx, mediaRequestSpec{
		op: "transcription", method: http.MethodPost, endpoint: deepSeekToolEndpoint(client.baseURL, "asr"),
		body: body.Bytes(), contentType: writer.FormDataContentType(), accept: "application/json",
	}, maxTranscriptionResponseBytes)
	if err != nil {
		return nil, err
	}
	var parsed struct {
		Text       string  `json:"text"`
		DurationMS float64 `json:"duration_ms"`
	}
	if err := json.Unmarshal(result.body, &parsed); err != nil {
		return nil, &MediaAPIError{Op: "transcription", Kind: MediaErrorBadResponse, StatusCode: result.status, Detail: strings.TrimSpace(string(result.body))}
	}
	text := strings.TrimSpace(parsed.Text)
	if text == "" {
		return nil, &MediaAPIError{Op: "transcription", Kind: MediaErrorBadResponse, StatusCode: result.status, Detail: "empty transcript"}
	}
	return &TranscriptionResponse{
		API:      SpeechAPIDeepSeek,
		Model:    firstNonEmptyString(req.Model, deepSeekASRModel),
		Text:     text,
		Duration: time.Duration(parsed.DurationMS * float64(time.Millisecond)),
	}, nil
}
