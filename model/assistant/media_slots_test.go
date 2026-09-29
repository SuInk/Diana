// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/SuInk/diana/model/applog"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/SuInk/diana/model/llm"
)

func mediaSlotTestRuntime(t *testing.T, roles map[string]ModelRole, profiles ...llm.Profile) *Runtime {
	t.Helper()
	store := &stubLLMProfileStore{set: llm.ProfileSet{Profiles: profiles}}
	return NewRuntime(BotConfig{ModelRoles: roles}, nilChannel{}, NewDefaultPluginManager(), store, nil, nil, nil)
}

func mediaSlotProfile(id, baseURL string) llm.Profile {
	return llm.Profile{ID: id, Name: id, Config: llm.ProviderConfig{Provider: llm.ProviderOpenAICompatible, APIKey: "sk-" + id, BaseURL: baseURL + "/v1", Model: "placeholder"}}
}

func TestMediaSlotKeysAreBindableAndKeepParams(t *testing.T) {
	roles := normalizeModelRoles(map[string]ModelRole{
		"tts": {ProfileID: "p1", Model: "tts-1", Params: map[string]string{" Voice ": " alloy ", "format": " "}, Fallbacks: []ModelRole{
			{ProfileID: "p2", Model: "tts-2", Params: map[string]string{"voice": "ignored"}},
		}},
		"stt":   {ProviderID: "p1", ModelID: "p1:whisper-1"},
		"video": {Group: "video", Model: "sora-2"},
	})
	for _, key := range []string{"tts", "stt", "video"} {
		if _, ok := roles[key]; !ok {
			t.Fatalf("%s slot dropped by normalizeModelRoles: %#v", key, roles)
		}
	}
	if got := roles["tts"].Params; len(got) != 1 || got["voice"] != "alloy" {
		t.Fatalf("params = %#v", got)
	}
	if roles["tts"].Fallbacks[0].Params != nil {
		t.Fatalf("fallback params must not be stored separately")
	}
}

func TestMediaSlotRoutesResolveBindingsWithoutChatFallback(t *testing.T) {
	r := mediaSlotTestRuntime(t, map[string]ModelRole{
		"chat":  {ProfileID: "p1", Model: "gpt"},
		"tts":   {ProfileID: "p1", Model: "tts-1", Params: map[string]string{"voice": "alloy"}, Fallbacks: []ModelRole{{ProviderID: "p2", ModelID: "p2:cosyvoice"}}},
		"video": {FollowChat: true},
	}, mediaSlotProfile("p1", "http://one"), mediaSlotProfile("p2", "http://two"))

	routes := r.mediaSlotRoutes(context.Background(), mediaSlotTTS)
	if len(routes) != 2 || routes[0].Model != "tts-1" || routes[1].Model != "cosyvoice" || routes[1].Config.APIKey != "sk-p2" {
		t.Fatalf("routes = %#v", routes)
	}
	if routes[1].param(mediaParamVoice) != "alloy" {
		t.Fatalf("fallback must reuse the primary params: %#v", routes[1].Params)
	}
	// 没配的插槽和「跟随对话」都不能落到对话模型上：对话模型接不了这些接口。
	if len(r.mediaSlotRoutes(context.Background(), mediaSlotSTT)) != 0 || r.mediaSlotConfigured(context.Background(), mediaSlotVideo) {
		t.Fatal("unconfigured media slot fell back to chat")
	}
}

func TestSynthesizeSpeechFailsOverToNextRoute(t *testing.T) {
	var primaryCalls atomic.Int32
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		primaryCalls.Add(1)
		http.Error(w, `{"error":"voice not found"}`, http.StatusBadRequest)
	}))
	defer primary.Close()
	backup := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["model"] != "cosyvoice" || body["voice"] != "中文女" || body["response_format"] != "wav" {
			t.Errorf("backup body = %#v", body)
		}
		_, _ = w.Write([]byte("RIFFxxxxWAVE"))
	}))
	defer backup.Close()
	r := mediaSlotTestRuntime(t, map[string]ModelRole{
		"tts": {ProfileID: "p1", Model: "tts-1", Params: map[string]string{"voice": "中文女", "format": "wav"}, Fallbacks: []ModelRole{{ProfileID: "p2", Model: "cosyvoice"}}},
	}, mediaSlotProfile("p1", primary.URL), mediaSlotProfile("p2", backup.URL))

	resp, err := r.synthesizeSpeech(context.Background(), "你好", "")
	if err != nil {
		t.Fatal(err)
	}
	if string(resp.Audio) != "RIFFxxxxWAVE" || primaryCalls.Load() != 1 {
		t.Fatalf("resp=%#v primaryCalls=%d", resp, primaryCalls.Load())
	}
}

func TestSynthesizeSpeechStopsOnContentPolicy(t *testing.T) {
	var backupCalls atomic.Int32
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"code":"content_policy_violation"}}`, http.StatusBadRequest)
	}))
	defer primary.Close()
	backup := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		backupCalls.Add(1)
		_, _ = w.Write([]byte("audio"))
	}))
	defer backup.Close()
	r := mediaSlotTestRuntime(t, map[string]ModelRole{
		"tts": {ProfileID: "p1", Model: "tts-1", Fallbacks: []ModelRole{{ProfileID: "p2", Model: "tts-1"}}},
	}, mediaSlotProfile("p1", primary.URL), mediaSlotProfile("p2", backup.URL))

	_, err := r.synthesizeSpeech(context.Background(), "x", "")
	if llm.MediaErrorKindOf(err) != llm.MediaErrorContentPolicy || backupCalls.Load() != 0 {
		t.Fatalf("err=%v backupCalls=%d", err, backupCalls.Load())
	}
	if !strings.Contains(err.Error(), "「p1」") {
		t.Fatalf("error must name the route: %v", err)
	}
}

func TestSynthesizeSpeechWithoutSlotSaysSo(t *testing.T) {
	r := mediaSlotTestRuntime(t, nil, mediaSlotProfile("p1", "http://unused"))
	_, err := r.synthesizeSpeech(context.Background(), "x", "")
	if err == nil || !strings.Contains(err.Error(), "没有配置") {
		t.Fatalf("err = %v", err)
	}
}

func TestTTSPluginModelSlotPresetUsesSpeechSlot(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/audio/speech" {
			t.Errorf("path = %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "audio/mpeg")
		_, _ = w.Write([]byte("ID3-mp3"))
	}))
	defer server.Close()
	outputDir := t.TempDir()
	t.Setenv("DIANA_TTS_OUTPUT_DIR", outputDir)
	r := mediaSlotTestRuntime(t, map[string]ModelRole{
		"tts": {ProfileID: "p1", Model: "gpt-4o-mini-tts", Params: map[string]string{"voice": "alloy"}},
	}, mediaSlotProfile("p1", server.URL))
	sharer := &recordingLocalMediaSharer{url: "http://127.0.0.1:18080/api/assistant/media/slot-voice"}
	r.SetLocalMediaSharer(sharer)
	pluginValue, _, _ := r.plugins.PluginWithSettings(voiceTTSPluginID, nil)
	tool := mustVoiceTTSTool(t, pluginValue.(*VoiceTTSPlugin), SettingValues{voiceTTSSettingPreset: voiceTTSPresetModelSlot})

	raw, err := tool.Run(context.Background(), map[string]any{"text": "晚上好"})
	if err != nil {
		t.Fatal(err)
	}
	paths := sharer.pathsSnapshot()
	if len(paths) != 1 || filepath.Ext(paths[0]) != ".mp3" || filepath.Dir(paths[0]) != outputDir {
		t.Fatalf("shared paths = %#v", paths)
	}
	if data, _ := os.ReadFile(paths[0]); string(data) != "ID3-mp3" {
		t.Fatalf("cached audio = %q", data)
	}
	reply, done := tool.(interface{ TerminalResult(string) (string, bool) }).TerminalResult(raw)
	if !done || reply != "[CQ:record,file=http://127.0.0.1:18080/api/assistant/media/slot-voice]" {
		t.Fatalf("reply=%q done=%v", reply, done)
	}
}

func TestTTSPluginModelSlotPresetIgnoresGPTSoVITSEndpoint(t *testing.T) {
	cfg, err := voiceTTSConfigFromSettings(SettingValues{voiceTTSSettingPreset: voiceTTSPresetModelSlot, voiceTTSSettingEndpoint: "http://gpt-sovits:9880/tts"})
	if err != nil || !cfg.UseModelSlot || cfg.Endpoint != "" {
		t.Fatalf("cfg=%#v err=%v", cfg, err)
	}
}

func TestSTTPluginModelSlotTranscribesThroughSlot(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Fatal(err)
		}
		// 插件语言是 auto 时让插槽上的语言生效。
		if r.FormValue("model") != "whisper-1" || r.FormValue("language") != "zh" {
			t.Errorf("form = %#v", r.MultipartForm.Value)
		}
		file, _, _ := r.FormFile("file")
		data, _ := io.ReadAll(file)
		if string(data) != "wav-bytes" {
			t.Errorf("audio = %q", data)
		}
		_, _ = w.Write([]byte(`{"text":"今天吃什么"}`))
	}))
	defer server.Close()
	r := mediaSlotTestRuntime(t, map[string]ModelRole{
		"stt": {ProfileID: "p1", Model: "whisper-1", Params: map[string]string{"language": "zh"}},
	}, mediaSlotProfile("p1", server.URL))
	wav := filepath.Join(t.TempDir(), "audio.wav")
	if err := os.WriteFile(wav, []byte("wav-bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	text, model, err := r.slotVoiceTranscription(context.Background(), wav, voiceSTTConfig{Backend: voiceSTTBackendModelSlot, Language: "auto"})
	if err != nil || text != "今天吃什么" || model != "whisper-1" {
		t.Fatalf("text=%q model=%q err=%v", text, model, err)
	}
}

func TestSTTModelSlotBackendIsSelectable(t *testing.T) {
	manifest := NewVoiceSTTPlugin(nil).Manifest()
	for _, spec := range manifest.Settings {
		if spec.Key != "backend" {
			continue
		}
		for _, option := range spec.Options {
			if option.Value == voiceSTTBackendModelSlot {
				return
			}
		}
	}
	t.Fatal("model_slot backend missing from STT plugin settings")
}

func TestSTTModelSlotErrorsAreTransientWhenUpstreamIsDown(t *testing.T) {
	if !voiceSTTErrorIsTransient("provider_rejected", &llm.MediaAPIError{Kind: llm.MediaErrorUpstream}) {
		t.Fatal("upstream outage must be retried later")
	}
	if voiceSTTErrorIsTransient("provider_rejected", &llm.MediaAPIError{Kind: llm.MediaErrorInvalid}) {
		t.Fatal("invalid request must not be retried")
	}
}

func TestVideoToolGeneratesAndDeliversVideo(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/videos":
			_ = r.ParseMultipartForm(1 << 20)
			// 工具没给的时长和尺寸从插槽参数补。
			if r.FormValue("model") != "sora-2" || r.FormValue("seconds") != "4" || r.FormValue("size") != "720x1280" || r.FormValue("prompt") != "海边日落" {
				t.Errorf("form = %#v", r.MultipartForm.Value)
			}
			_, _ = w.Write([]byte(`{"id":"vid","status":"queued"}`))
		case r.URL.Path == "/v1/videos/vid":
			_, _ = w.Write([]byte(`{"id":"vid","status":"completed","progress":100}`))
		case r.URL.Path == "/v1/videos/vid/content":
			w.Header().Set("Content-Type", "video/mp4")
			_, _ = w.Write([]byte("mp4"))
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()
	r := mediaSlotTestRuntime(t, map[string]ModelRole{
		"video": {ProfileID: "p1", Model: "sora-2", Params: map[string]string{"seconds": "4", "size": "720x1280", "poll_interval_seconds": "0.001"}},
	}, mediaSlotProfile("p1", server.URL))
	sharer := &recordingLocalMediaSharer{url: "http://127.0.0.1:18080/api/assistant/media/video"}
	r.SetLocalMediaSharer(sharer)
	event := MessageEvent{Platform: "onebot", Kind: EventKindGroup, GroupID: "g1", UserID: "u1", MessageID: "m1"}
	tool := newDianaVideoTool(r, event, RelationshipPolicyFor(UserMemoryProfile{}, "owner", "u1")).(*dianaVideoTool)

	request, err := tool.prepareRequest(context.Background(), map[string]any{"prompt": "海边日落", "caption": "好了"})
	if err != nil {
		t.Fatal(err)
	}
	var progress []PluginTaskProgress
	message, err := tool.execute(context.Background(), request, PluginTaskServices{Report: func(p PluginTaskProgress) { progress = append(progress, p) }})
	if err != nil {
		t.Fatal(err)
	}
	if len(message.VideoURLs) != 1 || message.VideoURLs[0] != sharer.url || message.Text != "好了" || message.ReplyMessageID != "m1" {
		t.Fatalf("message = %#v", message)
	}
	paths := sharer.pathsSnapshot()
	if len(paths) != 1 || filepath.Ext(paths[0]) != ".mp4" {
		t.Fatalf("shared = %#v", paths)
	}
	if len(progress) == 0 || progress[len(progress)-1].Completed != 100 {
		t.Fatalf("progress = %#v", progress)
	}
}

func TestVideoToolRefusesWithoutSlot(t *testing.T) {
	r := mediaSlotTestRuntime(t, nil, mediaSlotProfile("p1", "http://unused"))
	_, err := newDianaVideoTool(r, MessageEvent{Kind: EventKindPrivate, UserID: "u"}, RelationshipPolicyFor(UserMemoryProfile{}, "owner", "u")).Run(context.Background(), map[string]any{"prompt": "cat"})
	if err == nil || !strings.Contains(err.Error(), "视频生成") {
		t.Fatalf("err = %v", err)
	}
}

func TestVideoToolImageModeNeedsAnImage(t *testing.T) {
	r := mediaSlotTestRuntime(t, map[string]ModelRole{"video": {ProfileID: "p1", Model: "sora-2"}}, mediaSlotProfile("p1", "http://unused"))
	tool := newDianaVideoTool(r, MessageEvent{Kind: EventKindPrivate, UserID: "u", MessageID: "m"}, RelationshipPolicyFor(UserMemoryProfile{}, "owner", "u")).(*dianaVideoTool)
	if _, err := tool.prepareRequest(context.Background(), map[string]any{"prompt": "动起来", "use_image": true}); err != errVideoSourceNotFound {
		t.Fatalf("err = %v", err)
	}
	if _, err := tool.prepareRequest(context.Background(), map[string]any{"prompt": "x", "seconds": 600}); err == nil {
		t.Fatal("overlong video must be rejected")
	}
}

// 视频对群成员开放，和生图同一档。
func TestVideoToolIsAvailableToGroupMembers(t *testing.T) {
	member := RelationshipPolicyFor(UserMemoryProfile{}, "owner", "member")
	if !member.allowedAgentToolNames()[dianaVideoToolName] {
		t.Fatal("video must be usable by group members")
	}
}

// 没有生图权限的人拿不到视频：权限位跟着生图走。
func TestVideoToolFollowsImageGenerationPermission(t *testing.T) {
	r := mediaSlotTestRuntime(t, map[string]ModelRole{"video": {ProfileID: "p1", Model: "sora-2"}}, mediaSlotProfile("p1", "http://unused"))
	policy := RelationshipPolicyFor(UserMemoryProfile{}, "owner", "member")
	policy.AllowImageGeneration = false
	_, err := newDianaVideoTool(r, MessageEvent{Kind: EventKindGroup, GroupID: "g", UserID: "member"}, policy).Run(context.Background(), map[string]any{"prompt": "cat"})
	if err == nil || !strings.Contains(err.Error(), "权限") {
		t.Fatalf("err = %v", err)
	}
}

// 后备路由顶上时要报出后备的模型名，缓存键和记录才不会记到主模型名下。
func TestSTTSlotReportsTheRouteThatAnswered(t *testing.T) {
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"model not loaded"}`, http.StatusBadRequest)
	}))
	defer primary.Close()
	backup := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"text":"后备转写"}`))
	}))
	defer backup.Close()
	r := mediaSlotTestRuntime(t, map[string]ModelRole{
		"stt": {ProfileID: "p1", Model: "whisper-large", Fallbacks: []ModelRole{{ProfileID: "p2", Model: "funasr"}}},
	}, mediaSlotProfile("p1", primary.URL), mediaSlotProfile("p2", backup.URL))
	wav := filepath.Join(t.TempDir(), "audio.wav")
	_ = os.WriteFile(wav, []byte("wav"), 0o600)
	text, model, err := r.slotVoiceTranscription(context.Background(), wav, voiceSTTConfig{Language: "auto"})
	if err != nil || text != "后备转写" || model != "funasr" {
		t.Fatalf("text=%q model=%q err=%v", text, model, err)
	}
	primaryKey := voiceSTTCacheKey("hash", voiceSTTConfig{Backend: voiceSTTBackendModelSlot, Model: "whisper-large"})
	backupKey := voiceSTTCacheKey("hash", voiceSTTConfig{Backend: voiceSTTBackendModelSlot, Model: model})
	if primaryKey == backupKey {
		t.Fatal("fallback transcript must not share the primary model's cache key")
	}
}

func TestSlotSpeechExtensionPrefersReportedMediaType(t *testing.T) {
	cases := []struct {
		resp llm.SpeechResponse
		want string
	}{
		// 请求的是 mp3，服务端实际回了 wav：按报的类型走。
		{llm.SpeechResponse{MediaType: "audio/wav", Format: "mp3", Audio: []byte("RIFF....WAVE")}, "wav"},
		{llm.SpeechResponse{MediaType: "audio/mpeg"}, "mp3"},
		// 类型不是音频时嗅探内容。
		{llm.SpeechResponse{MediaType: "application/octet-stream", Audio: []byte("fLaC....")}, "flac"},
		{llm.SpeechResponse{Format: "opus"}, "ogg"},
		// 什么都认不出就按插槽默认的 mp3，不再落成 .audio。
		{llm.SpeechResponse{}, "mp3"},
	}
	for _, tc := range cases {
		if got := slotSpeechExtension(&tc.resp); got != tc.want {
			t.Fatalf("%#v => %q, want %q", tc.resp, got, tc.want)
		}
	}
}

type usageCaptureLog struct {
	mu      sync.Mutex
	entries []applog.Entry
}

func (l *usageCaptureLog) AppendLog(_ context.Context, entry applog.Entry) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.entries = append(l.entries, entry)
	return nil
}

func (l *usageCaptureLog) purposes() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []string
	for _, entry := range l.entries {
		if entry.Action == "llm_usage" {
			out = append(out, entry.Metadata["purpose"].(string))
		}
	}
	return out
}

// 视频任务受理之后失败，不能换后备再提交一次（双重计费），但要照样计入用量。
func TestVideoSlotDoesNotResubmitAfterAcceptance(t *testing.T) {
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			_, _ = w.Write([]byte(`{"id":"v1","status":"queued"}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"v1","status":"failed","error":{"message":"render crashed"}}`))
	}))
	defer primary.Close()
	var backupCalls atomic.Int32
	backup := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		backupCalls.Add(1)
		http.Error(w, "should not be called", http.StatusTeapot)
	}))
	defer backup.Close()
	r := mediaSlotTestRuntime(t, map[string]ModelRole{
		"video": {ProfileID: "p1", Model: "sora-2", Params: map[string]string{"poll_interval_seconds": "0.001"}, Fallbacks: []ModelRole{{ProfileID: "p2", Model: "sora-2"}}},
	}, mediaSlotProfile("p1", primary.URL), mediaSlotProfile("p2", backup.URL))
	usage := &usageCaptureLog{}
	r.SetAppLogWriter(usage)

	_, _, err := r.generateVideo(context.Background(), llm.VideoGenerateRequest{Prompt: "x"}, nil)
	if err == nil || !llm.VideoJobMayBeBilled(err) || !strings.Contains(err.Error(), "render crashed") {
		t.Fatalf("err = %v", err)
	}
	if backupCalls.Load() != 0 {
		t.Fatalf("accepted job was resubmitted to the fallback %d times", backupCalls.Load())
	}
	if got := usage.purposes(); len(got) != 1 || got[0] != "video_generate" {
		t.Fatalf("failed accepted job must still count once, got %v", got)
	}
}

// 提交阶段就被明确拒掉（没建任务）时照常换后备，被拒的那次不计用量。
func TestVideoSlotFailsOverWhenSubmitIsRejected(t *testing.T) {
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"model not found"}`, http.StatusBadRequest)
	}))
	defer primary.Close()
	backup := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost:
			_, _ = w.Write([]byte(`{"id":"v2","status":"completed"}`))
		case strings.HasSuffix(r.URL.Path, "/content"):
			_, _ = w.Write([]byte("mp4"))
		}
	}))
	defer backup.Close()
	r := mediaSlotTestRuntime(t, map[string]ModelRole{
		"video": {ProfileID: "p1", Model: "sora-2", Fallbacks: []ModelRole{{ProfileID: "p2", Model: "sora-2"}}},
	}, mediaSlotProfile("p1", primary.URL), mediaSlotProfile("p2", backup.URL))
	usage := &usageCaptureLog{}
	r.SetAppLogWriter(usage)

	result, route, err := r.generateVideo(context.Background(), llm.VideoGenerateRequest{Prompt: "x"}, nil)
	if err != nil || string(result.Video) != "mp4" || route.Name != "p2" {
		t.Fatalf("result=%#v route=%#v err=%v", result, route, err)
	}
	if got := usage.purposes(); len(got) != 1 {
		t.Fatalf("usage = %v", got)
	}
}
