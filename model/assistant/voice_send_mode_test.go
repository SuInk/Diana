// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestPersistentVoiceSpeakable(t *testing.T) {
	cases := []struct {
		name  string
		reply string
		max   int
		want  bool
	}{
		{"plain text", "晚安，做个好梦。", 500, true},
		{"face is fine", "晚安[CQ:face,id=75]", 500, true},
		{"empty", "  ", 500, false},
		{"only face", "[CQ:face,id=75]", 500, false},
		{"image", "看这个[CQ:image,file=https://example.com/a.png]", 500, false},
		{"mention", "[CQ:at,qq=10001] 晚安", 500, false},
		{"link", "文档在 https://example.com/docs", 500, false},
		{"code block", "```go\nfmt.Println(1)\n```", 500, false},
		{"too long", strings.Repeat("好", 21), 20, false},
		{"at limit", strings.Repeat("好", 20), 20, true},
	}
	for _, tc := range cases {
		if got := persistentVoiceSpeakable(tc.reply, tc.max); got != tc.want {
			t.Errorf("%s: persistentVoiceSpeakable(%q, %d) = %v, want %v", tc.name, tc.reply, tc.max, got, tc.want)
		}
	}
}

type persistentVoiceFixture struct {
	runtime  *Runtime
	channel  *recordingChannel
	provider *sequenceLLMProvider
	ttsCalls *atomic.Int32
	ttsText  *atomic.Value
}

// newPersistentVoiceFixture 起一台只在群 123456 打开常驻语音的机器人，正文由 reply 给出。
func newPersistentVoiceFixture(t *testing.T, reply string, ttsStatus int, sendMode string) persistentVoiceFixture {
	t.Helper()
	var calls atomic.Int32
	var spoken atomic.Value
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var body struct {
			Text string `json:"text"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		spoken.Store(body.Text)
		if ttsStatus != http.StatusOK {
			http.Error(w, "model not loaded", ttsStatus)
			return
		}
		w.Header().Set("Content-Type", "audio/wav")
		_, _ = w.Write(testWAVBytes())
	}))
	t.Cleanup(server.Close)
	t.Setenv("DIANA_TTS_OUTPUT_DIR", t.TempDir())
	t.Setenv("DIANA_TTS_SILK_ENCODER_PATH", "")

	provider := &sequenceLLMProvider{replies: []string{`{"action":"none","prompt":""}`, reply}}
	channel := &recordingChannel{}
	runtime := NewRuntime(BotConfig{AgentEnabled: false}, channel, NewDefaultPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
		return provider, nil
	})
	overrides := map[string]any{
		voiceTTSSettingPreset:   voiceTTSPresetCustom,
		voiceTTSSettingEndpoint: server.URL,
	}
	if sendMode != "" {
		overrides[voiceTTSSettingSendMode] = sendMode
	}
	runtime.SetGroupConfigStore(&stubGroupConfigStore{configs: map[string]GroupConfig{
		"123456": {GroupID: "123456", PluginSettingOverrides: PluginSettingOverrides{voiceTTSPluginID: overrides}},
	}})
	runtime.SetLocalMediaSharer(&recordingLocalMediaSharer{url: "http://127.0.0.1:18080/api/assistant/media/persistent-voice"})
	return persistentVoiceFixture{runtime: runtime, channel: channel, provider: provider, ttsCalls: &calls, ttsText: &spoken}
}

func (f persistentVoiceFixture) reply(t *testing.T) OutgoingMessage {
	t.Helper()
	event := MessageEvent{
		Kind:           EventKindGroup,
		GroupID:        "123456",
		UserID:         "10001",
		MessageID:      "persistent-voice",
		Segments:       []MessageSegment{{Type: "text", Data: map[string]string{"text": "Diana 晚安"}}},
		proactiveReply: true,
	}
	if _, err := f.runtime.replyTo(context.Background(), event, "Diana 晚安"); err != nil {
		t.Fatal(err)
	}
	if len(f.channel.sent) != 1 {
		t.Fatalf("sent=%#v", f.channel.sent)
	}
	return f.channel.sent[0]
}

func (f persistentVoiceFixture) promptMentionsVoice() bool {
	f.provider.mu.Lock()
	defer f.provider.mu.Unlock()
	for _, req := range f.provider.requests {
		for _, message := range req.Messages {
			if strings.Contains(message.Content, "合成为语音发出去") {
				return true
			}
		}
	}
	return false
}

func TestPersistentVoiceSendsPlainReplyAsRecord(t *testing.T) {
	fixture := newPersistentVoiceFixture(t, "晚安，做个好梦。", http.StatusOK, voiceTTSSendModeAlways)
	message := fixture.reply(t)
	segments := buildOutgoingSegments(message)
	if len(segments) != 1 || segments[0]["type"] != "record" {
		t.Fatalf("segments=%#v", segments)
	}
	if !fixture.promptMentionsVoice() {
		t.Fatal("常驻语音模式应在提示词里告诉模型回复会被念出来")
	}
}

func TestPersistentVoiceConsumesLayoutMarkers(t *testing.T) {
	fixture := newPersistentVoiceFixture(t, "好呀"+notificationSplitMarker+"那我们明天见"+notificationLineMarker+"记得早点睡", http.StatusOK, voiceTTSSendModeAlways)
	message := fixture.reply(t)
	if segments := buildOutgoingSegments(message); len(segments) != 1 || segments[0]["type"] != "record" {
		t.Fatalf("segments=%#v", segments)
	}
	spoken, _ := fixture.ttsText.Load().(string)
	if strings.Contains(spoken, "diana") || !strings.Contains(spoken, "好呀") || !strings.Contains(spoken, "记得早点睡") {
		t.Fatalf("送进 TTS 的文字不该带排版标记：%q", spoken)
	}
}

func TestPersistentVoiceKeepsQuotedReplyAsText(t *testing.T) {
	fixture := newPersistentVoiceFixture(t, replyMarkerPrefix+"30003]就是这条", http.StatusOK, voiceTTSSendModeAlways)
	message := fixture.reply(t)
	if fixture.ttsCalls.Load() != 0 || !strings.Contains(message.Text, "就是这条") {
		t.Fatalf("带引用的回复应发文字：%#v calls=%d", message, fixture.ttsCalls.Load())
	}
}

func TestPersistentVoiceKeepsLinkReplyAsText(t *testing.T) {
	fixture := newPersistentVoiceFixture(t, "文档在 https://example.com/docs", http.StatusOK, voiceTTSSendModeAlways)
	message := fixture.reply(t)
	if !strings.Contains(message.Text, "https://example.com/docs") {
		t.Fatalf("message=%#v", message)
	}
	if fixture.ttsCalls.Load() != 0 {
		t.Fatalf("带链接的回复不该去合成，tts calls=%d", fixture.ttsCalls.Load())
	}
}

func TestPersistentVoiceFallsBackToTextWhenSynthesisFails(t *testing.T) {
	fixture := newPersistentVoiceFixture(t, "晚安，做个好梦。", http.StatusInternalServerError, voiceTTSSendModeAlways)
	message := fixture.reply(t)
	if !strings.Contains(message.Text, "晚安，做个好梦") {
		t.Fatalf("合成失败应回退文字：%#v", message)
	}
	if fixture.ttsCalls.Load() != 1 {
		t.Fatalf("tts calls=%d", fixture.ttsCalls.Load())
	}
}

func TestPersistentVoiceOffByDefault(t *testing.T) {
	fixture := newPersistentVoiceFixture(t, "晚安，做个好梦。", http.StatusOK, "")
	message := fixture.reply(t)
	if !strings.Contains(message.Text, "晚安，做个好梦") || fixture.ttsCalls.Load() != 0 {
		t.Fatalf("按需模式不该自动转语音：%#v calls=%d", message, fixture.ttsCalls.Load())
	}
	if fixture.promptMentionsVoice() {
		t.Fatal("按需模式不该注入常驻语音提示")
	}
}
