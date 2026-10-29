// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/llm"
)

// gptSoVITSTestServer 是一个假的 GPT-SoVITS：记下请求体，回一段 WAV。
func gptSoVITSTestServer(t *testing.T, body []byte) (*httptest.Server, *map[string]any) {
	t.Helper()
	var request map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/tts" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		w.Header().Set("Content-Type", "audio/wav")
		_, _ = w.Write(body)
	}))
	t.Cleanup(server.Close)
	return server, &request
}

func gptSoVITSRole(params map[string]string) map[string]ModelRole {
	merged := map[string]string{"api": "gpt_sovits", "ref_audio_path": "/models/reference.wav"}
	for key, value := range params {
		merged[key] = value
	}
	return map[string]ModelRole{"tts": {ProfileID: "sovits", Model: "gpt-sovits", Params: merged}}
}

func gptSoVITSProfile(baseURL string) llm.Profile {
	return llm.Profile{ID: "sovits", Name: "GPT-SoVITS", Group: "tts", Config: llm.ProviderConfig{Provider: llm.ProviderOpenAICompatible, BaseURL: baseURL, Model: "gpt-sovits"}}
}

func TestTTSToolFollowsSpeechSlotAndPlatform(t *testing.T) {
	t.Setenv("DIANA_TTS_VOICE_NAME", "测试")
	unconfigured := mediaSlotTestRuntime(t, nil)
	if _, _, ok := unconfigured.voiceTTSToolForEvent(context.Background(), MessageEvent{Kind: EventKindPrivate}); ok {
		t.Fatal("tts tool must not exist without a speech slot")
	}
	r := mediaSlotTestRuntime(t, gptSoVITSRole(nil), gptSoVITSProfile("http://127.0.0.1:1"))
	tool, settings, ok := r.voiceTTSToolForEvent(context.Background(), MessageEvent{Kind: EventKindPrivate})
	if !ok || tool.Name() != voiceTTSToolName || !strings.Contains(tool.Description(), "测试音色") {
		t.Fatalf("tool=%#v ok=%v", tool, ok)
	}
	if settings.SendMode != voiceTTSSendModeOnDemand || settings.MaxChars != defaultVoiceTTSMaxChars {
		t.Fatalf("settings = %#v", settings)
	}
	// OneBot、Telegram、QQ 官方都发得出语音；其他平台没有发语音的通道。
	for _, platform := range []string{PlatformTelegram, PlatformQQOfficial} {
		if _, _, ok := r.voiceTTSToolForEvent(context.Background(), MessageEvent{Kind: EventKindPrivate, Platform: platform}); !ok {
			t.Fatalf("tts tool should be offered on %s", platform)
		}
	}
	if _, _, ok := r.voiceTTSToolForEvent(context.Background(), MessageEvent{Kind: EventKindPrivate, Platform: PlatformIMessage}); ok {
		t.Fatal("tts tool must not be offered where voice cannot be sent")
	}
}

func TestTTSToolSynthesizesThroughSlotAndReturnsTerminalRecord(t *testing.T) {
	server, request := gptSoVITSTestServer(t, testWAVBytes())
	outputDir := t.TempDir()
	t.Setenv("DIANA_TTS_OUTPUT_DIR", outputDir)
	r := mediaSlotTestRuntime(t, gptSoVITSRole(map[string]string{"speed": "1.25", "max_chars": "6"}), gptSoVITSProfile(server.URL))
	sharer := &recordingLocalMediaSharer{url: "http://127.0.0.1:18080/api/assistant/media/voice-token"}
	r.SetLocalMediaSharer(sharer)
	tool, _, _ := r.voiceTTSToolForEvent(context.Background(), MessageEvent{Kind: EventKindPrivate})

	raw, err := tool.Run(context.Background(), map[string]any{"text": "大家晚上好，我是语音助手。"})
	if err != nil {
		t.Fatal(err)
	}
	// 字数上限是插槽参数，超出部分截掉。
	if (*request)["text"] != "大家晚上好，" || (*request)["ref_audio_path"] != "/models/reference.wav" || (*request)["speed_factor"] != 1.25 {
		t.Fatalf("request=%#v", *request)
	}
	paths := sharer.pathsSnapshot()
	if len(paths) != 1 || filepath.Dir(paths[0]) != outputDir || filepath.Ext(paths[0]) != ".wav" {
		t.Fatalf("shared paths=%#v", paths)
	}
	if _, err := os.Stat(paths[0]); err != nil {
		t.Fatalf("audio cache missing: %v", err)
	}
	reply, done := tool.TerminalResult(raw)
	if !done || reply != "[CQ:record,file=http://127.0.0.1:18080/api/assistant/media/voice-token]" {
		t.Fatalf("terminal reply=%q done=%v raw=%s", reply, done, raw)
	}
	segments := buildOutgoingSegments(OutgoingMessage{Text: reply})
	if len(segments) != 1 || segments[0]["type"] != "record" {
		t.Fatalf("segments=%#v", segments)
	}
}

func TestTTSToolRejectsNonAudioResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"detail":"model unavailable"}`))
	}))
	defer server.Close()
	t.Setenv("DIANA_TTS_OUTPUT_DIR", t.TempDir())
	r := mediaSlotTestRuntime(t, gptSoVITSRole(nil), gptSoVITSProfile(server.URL))
	r.SetLocalMediaSharer(&recordingLocalMediaSharer{url: "http://127.0.0.1/audio"})
	tool, _, _ := r.voiceTTSToolForEvent(context.Background(), MessageEvent{Kind: EventKindPrivate})
	_, err := tool.Run(context.Background(), map[string]any{"text": "测试"})
	if err == nil || !strings.Contains(err.Error(), "model unavailable") {
		t.Fatalf("err=%v", err)
	}
}

func TestRuntimeAgentUsesTTSForModelSelectedVoiceRequest(t *testing.T) {
	server, _ := gptSoVITSTestServer(t, testWAVBytes())
	t.Setenv("DIANA_TTS_OUTPUT_DIR", t.TempDir())
	provider := &sequenceLLMProvider{replies: []string{
		`{"action":"none","prompt":""}`,
		`{"action":"tool","tool":"tools_load","input":{"names":["tts"]}}`,
		`{"action":"tool","tool":"tools_execute","input":{"name":"tts","input":{"text":"晚上好呀，今天也要开心。"}}}`,
	}}
	channel := &recordingChannel{}
	store := &stubLLMProfileStore{set: llm.ProfileSet{Profiles: []llm.Profile{gptSoVITSProfile(server.URL)}}}
	runtime := NewRuntime(BotConfig{OwnerID: "owner", AgentEnabled: true, ReplySafetyMasterEnabled: boolPointer(false), ModelRoles: gptSoVITSRole(nil)}, channel, NewDefaultPluginManager(), store, nil, nil, func() (LLMProvider, error) {
		return provider, nil
	})
	runtime.SetLocalMediaSharer(&recordingLocalMediaSharer{url: "http://127.0.0.1:18080/api/assistant/media/voice-token"})
	event := MessageEvent{
		Kind:      EventKindGroup,
		GroupID:   "123456",
		UserID:    "10001",
		SelfID:    "20002",
		MessageID: "voice-request",
		Segments:  []MessageSegment{{Type: "text", Data: map[string]string{"text": "Diana，用语音跟我说晚安"}}},
	}
	reply, err := runtime.replyTo(context.Background(), event, "Diana，用语音跟我说晚安")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(reply, "[CQ:record,file=") || len(channel.sent) != 1 {
		t.Fatalf("reply=%q sent=%#v", reply, channel.sent)
	}
	// 群里的语音是单独一条，不带引用和 @。
	message := channel.sent[0]
	if message.ReplyMessageID != "" || message.MentionUserID != "" {
		t.Fatalf("message=%#v", message)
	}
	segments := buildOutgoingSegments(message)
	if len(segments) != 1 || segments[0]["type"] != "record" {
		t.Fatalf("segments=%#v", segments)
	}
	// 群里多一轮发送前审核，所以请求数不定；关键是加载 tts 的那一轮带着语音提示。
	if len(provider.requests) < 3 || !requestMessagesContain(provider.requests[1].Messages, "只有用户明确要求用语音回复") {
		t.Fatalf("requests=%d", len(provider.requests))
	}
}

func testWAVBytes() []byte {
	return []byte{
		'R', 'I', 'F', 'F', 36, 0, 0, 0,
		'W', 'A', 'V', 'E', 'f', 'm', 't', ' ',
		16, 0, 0, 0, 1, 0, 1, 0,
		0x80, 0x3e, 0, 0, 0, 0x7d, 0, 0,
		2, 0, 16, 0, 'd', 'a', 't', 'a',
		0, 0, 0, 0,
	}
}
