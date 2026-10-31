// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SuInk/diana/model/llm"
)

func newVoiceCallTestRuntime(t *testing.T, reply string) (*Runtime, *recordingChannel, *capturingLLMProvider) {
	t.Helper()
	channel := &recordingChannel{}
	provider := &capturingLLMProvider{reply: reply}
	runtime := NewRuntime(BotConfig{ID: "bot", OwnerID: "10001"}, channel, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
		return provider, nil
	})
	return runtime, channel, provider
}

func TestVoiceCallReplyGoesToCallNotPlatform(t *testing.T) {
	runtime, channel, provider := newVoiceCallTestRuntime(t, "听得到哦。今天想聊点什么？")
	_, base, err := runtime.ownerChatEvent("bot")
	if err != nil {
		t.Fatalf("ownerChatEvent() error = %v", err)
	}
	base.voiceCall = true
	call := &VoiceCall{runtime: runtime, callID: "t1", base: base}
	var mu sync.Mutex
	var emitted []string
	if err := call.Reply(context.Background(), "喂，听得到吗", func(text string) {
		mu.Lock()
		emitted = append(emitted, text)
		mu.Unlock()
	}); err != nil {
		t.Fatalf("Reply() error = %v", err)
	}
	// 句号要留着：通话按它断句，聊天里去句号的规矩在这里不适用。
	if got := strings.Join(emitted, ""); !strings.Contains(got, "听得到哦。") || !strings.Contains(got, "今天想聊点什么") {
		t.Fatalf("emitted = %q", emitted)
	}
	channel.mu.Lock()
	sent := len(channel.sent)
	channel.mu.Unlock()
	if sent != 0 {
		t.Fatalf("通话里的回复不该发到平台上，sent = %#v", channel.sent)
	}
	req := provider.requestSnapshot()
	var system strings.Builder
	for _, message := range req.Messages {
		if message.Role == llm.RoleSystem {
			system.WriteString(message.Content)
		}
	}
	if !strings.Contains(system.String(), "实时语音通话") {
		t.Fatal("通话轮次没有注入语音通话提示")
	}
	// 通话按主人的私聊算：说的话和回的话都进了这段私聊的历史。
	history := runtime.history[sessionKey(base)]
	var sawUser, sawBot bool
	for _, event := range history {
		text := PlainText(event.Segments)
		if strings.Contains(text, "喂，听得到吗") {
			sawUser = true
		}
		if event.Outbound && strings.Contains(text, "听得到哦") {
			sawBot = true
		}
	}
	if !sawUser || !sawBot {
		t.Fatalf("私聊历史里缺通话内容：user=%v bot=%v history=%#v", sawUser, sawBot, history)
	}
}

func TestVoiceCallInfoRequiresSlots(t *testing.T) {
	runtime, _, _ := newVoiceCallTestRuntime(t, "")
	info := runtime.VoiceCallInfo(context.Background(), "bot")
	if info.STT || info.TTS || !strings.Contains(info.Problem, "语音识别和语音合成") {
		t.Fatalf("没配语音插槽时应拒绝，info = %#v", info)
	}
	if _, err := runtime.NewVoiceCall(context.Background(), "bot", "x"); err == nil {
		t.Fatal("NewVoiceCall() 应在插槽没配时报错")
	}
}

func TestVoiceCallSpeechFormatAvoidsUnplayableFormats(t *testing.T) {
	for in, want := range map[string]string{"": "mp3", "pcm": "mp3", "silk": "mp3", "wav": "wav", "opus": "opus"} {
		if got := voiceCallSpeechFormat(in); got != want {
			t.Fatalf("voiceCallSpeechFormat(%q) = %q, want %q", in, got, want)
		}
	}
}

// Breeze 必须在上游结束之前交出音频，不能退回整段合成。
func TestVoiceCallBreezeStreamsBeforeCompletion(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/text-to-speech/voc_test/stream" || r.URL.Query().Get("output_format") != "mp3" {
			t.Errorf("request = %s", r.URL)
		}
		w.Header().Set("Content-Type", "audio/mpeg")
		_, _ = w.Write([]byte("first audio"))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	runtime, _, _ := newVoiceCallTestRuntime(t, "")
	stopped := errors.New("playback stopped")
	err := runtime.synthesizeCallSpeech(ctx, mediaSlotRoute{
		Config: llm.ProviderConfig{Provider: llm.ProviderOpenAICompatible, BaseURL: server.URL, APIKey: "test"},
		Model:  "breeze-tts-2",
		Params: map[string]string{"api": "breeze", "voice": "voc_test", "format": "pcm"},
	}, "你好", func(mime string, data []byte) error {
		if mime != "audio/mpeg" || len(data) == 0 {
			t.Errorf("mime=%s data=%q", mime, data)
		}
		return stopped
	})
	if !errors.Is(err, stopped) {
		t.Fatalf("expected early playback callback, got %v", err)
	}
}
