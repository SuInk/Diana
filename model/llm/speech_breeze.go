// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package llm

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// Breeze 的 HTTP 流式接口只接受裸格式名，不接受 ElevenLabs 的采样率后缀。
// 普通语音消息也读取这条流的完整响应，和通话共用协议及 24 kHz 输出。
func breezeSpeechSpec(req SpeechRequest, format string) (mediaRequestSpec, error) {
	if req.Voice == "" {
		return mediaRequestSpec{}, errors.New("llm: breeze speech requires a voice id")
	}
	switch format {
	case "mp3", "wav", "pcm":
	default:
		return mediaRequestSpec{}, fmt.Errorf("llm: breeze speech does not support format %q (use mp3, wav or pcm)", format)
	}
	if req.Speed != 0 && (req.Speed < 0.5 || req.Speed > 2) {
		return mediaRequestSpec{}, errors.New("llm: breeze speech speed must be between 0.5 and 2")
	}
	payload := map[string]any{"text": req.Input}
	if req.Model != "" {
		payload["model_id"] = req.Model
	}
	if req.Speed > 0 {
		payload["voice_settings"] = map[string]any{"speed": req.Speed}
	}
	if instructions := strings.TrimSpace(req.Instructions); instructions != "" {
		payload["instructions"] = instructions
	}
	if language := strings.TrimSpace(req.Options["language"]); language != "" && !strings.EqualFold(language, "auto") {
		payload["language_code"] = language
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return mediaRequestSpec{}, err
	}
	return mediaRequestSpec{
		op: "speech", method: http.MethodPost,
		endpoint: "text-to-speech/" + url.PathEscape(req.Voice) + "/stream",
		query:    url.Values{"output_format": {format}}, body: body,
		contentType: "application/json", accept: "audio/*, application/octet-stream",
	}, nil
}
