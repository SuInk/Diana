package assistant

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SuInk/diana/model/llm"
)

type voiceTestStore struct {
	mu          sync.Mutex
	transcripts map[string]VoiceTranscriptRecord
	blobLoads   int
	deleted     []string
}

func (s *voiceTestStore) AppendMessageEvent(context.Context, string, MessageEvent) error { return nil }
func (s *voiceTestStore) ListRecentMessageEvents(context.Context, string, int) ([]MessageEvent, error) {
	return nil, nil
}
func (s *voiceTestStore) LoadVoiceBlob(context.Context, string) ([]byte, bool, error) {
	s.mu.Lock()
	s.blobLoads++
	s.mu.Unlock()
	return nil, false, nil
}
func (s *voiceTestStore) DeleteVoiceBlob(_ context.Context, hash string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deleted = append(s.deleted, hash)
	return nil
}
func (s *voiceTestStore) LoadVoiceTranscript(_ context.Context, key string) (VoiceTranscriptRecord, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.transcripts[key]
	return record, ok, nil
}
func (s *voiceTestStore) SaveVoiceTranscript(_ context.Context, record VoiceTranscriptRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.transcripts[record.CacheKey] = record
	return nil
}

func TestVoiceSTTManifestDefaultsToDisabledAndProtectsAPIKey(t *testing.T) {
	manifest := NewVoiceSTTPlugin(nil).Manifest()
	settings := map[string]PluginSettingSpec{}
	for _, spec := range manifest.Settings {
		settings[spec.Key] = spec
	}
	if settings["backend"].Default != voiceSTTBackendDisabled {
		t.Fatalf("backend default=%v", settings["backend"].Default)
	}
	if !settings["api_key"].Secret {
		t.Fatal("API key setting is not secret")
	}
}

func TestVoiceSTTTranscribesOnceAndReusesCache(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg is unavailable")
	}
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		calls.Add(1)
		if req.Header.Get("Authorization") != "Bearer secret-token" {
			t.Errorf("authorization=%q", req.Header.Get("Authorization"))
		}
		if err := req.ParseMultipartForm(2 << 20); err != nil {
			t.Errorf("parse multipart: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"text": "这是缓存后的语音内容"})
	}))
	defer server.Close()

	wav := filepath.Join(t.TempDir(), "sample.wav")
	writeSilentWAV(t, wav, 1600)
	plugin := NewVoiceSTTPlugin(server.Client())
	manager := NewPluginManager(plugin)
	_, err := manager.UpdateSettings(voiceSTTPluginID, map[string]any{"backend": voiceSTTBackendOpenAI, "endpoint": server.URL, "api_key": "secret-token", "model": "whisper-test"})
	if err != nil {
		t.Fatal(err)
	}
	runtime := NewRuntime(BotConfig{}, nil, manager, nil, nil, nil, nil)
	store := &voiceTestStore{transcripts: map[string]VoiceTranscriptRecord{}}
	runtime.SetMessageHistoryStore(store)
	event := MessageEvent{Kind: EventKindGroup, GroupID: "1", MessageID: "voice-1", Segments: []MessageSegment{{Type: "record", Data: map[string]string{"file": wav}}}}

	first := runtime.prepareIncomingVoice(context.Background(), event)
	second := runtime.prepareIncomingVoice(context.Background(), event)
	for index, got := range []MessageEvent{first, second} {
		if transcript := got.Segments[0].Data[voiceSTTTranscriptKey]; transcript != "这是缓存后的语音内容" {
			t.Fatalf("result %d transcript=%q", index, transcript)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("provider calls=%d, want 1", calls.Load())
	}
	if !strings.Contains(PlainText(first.Segments), "这是缓存后的语音内容") {
		t.Fatalf("plain text=%q", PlainText(first.Segments))
	}
	quoted := runtime.prepareIncomingVoice(context.Background(), MessageEvent{Kind: EventKindGroup, GroupID: "1", Quoted: &QuotedMessage{MessageID: "voice-1", Segments: event.Segments}})
	if quoted.Quoted == nil || quoted.Quoted.Segments[0].Data[voiceSTTTranscriptKey] != "这是缓存后的语音内容" || calls.Load() != 1 {
		t.Fatalf("quoted cache result=%#v provider calls=%d", quoted.Quoted, calls.Load())
	}
}

func TestVoiceSTTUsesPersistedTranscriptBeforeLoadingRawAudio(t *testing.T) {
	cfg := voiceSTTConfig{Backend: voiceSTTBackendOpenAI, Model: "whisper-test", Language: "zh", Concurrency: 1}
	hash := strings.Repeat("a", 64)
	key := voiceSTTCacheKey(hash, cfg)
	store := &voiceTestStore{transcripts: map[string]VoiceTranscriptRecord{key: {CacheKey: key, AudioSHA256: hash, Transcript: "只读取持久化文字", DurationMS: 500}}}
	runtime := NewRuntime(BotConfig{}, nil, nil, nil, nil, nil, nil)
	runtime.SetMessageHistoryStore(store)
	transcript, gotHash, _, cacheHit, code, err := NewVoiceSTTPlugin(nil).transcribeSegment(context.Background(), runtime, MessageEvent{}, MessageSegment{Type: "record", Data: map[string]string{voiceSTTBlobHashKey: hash}}, cfg)
	if err != nil || code != "" || !cacheHit || transcript != "只读取持久化文字" || gotHash != hash {
		t.Fatalf("transcript=%q hash=%q cacheHit=%v code=%q err=%v", transcript, gotHash, cacheHit, code, err)
	}
	if store.blobLoads != 0 || len(store.deleted) != 1 || store.deleted[0] != hash {
		t.Fatalf("blob loads=%d deleted=%v", store.blobLoads, store.deleted)
	}
}

func TestVoiceSourceIsLoadedOnlyForVoiceCharacteristicQuestions(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg is unavailable")
	}
	wav := filepath.Join(t.TempDir(), "source.wav")
	writeSilentWAV(t, wav, 1600)
	runtime := NewRuntime(BotConfig{}, nil, nil, nil, nil, nil, nil)
	event := MessageEvent{Quoted: &QuotedMessage{MessageID: "voice-1", Segments: []MessageSegment{{Type: "record", Data: map[string]string{"file": wav, voiceSTTTranscriptKey: "你好"}}}}}
	cfg := voiceSTTConfig{MaxBytes: 1 << 20, MaxDuration: time.Minute}
	if parts, notice := runtime.voiceSourceAnalysisParts(context.Background(), event, "这段语音说了什么", cfg); len(parts) != 0 || notice != "" {
		t.Fatalf("ordinary transcript lookup loaded source audio: parts=%d notice=%q", len(parts), notice)
	}
	parts, notice := runtime.voiceSourceAnalysisParts(context.Background(), event, "他的语气和音色怎么样", cfg)
	if len(parts) != 1 || parts[0].Type != llm.ContentPartInputAudio || parts[0].AudioFormat != "wav" || parts[0].AudioData == "" {
		t.Fatalf("voice analysis parts=%#v", parts)
	}
	if !strings.Contains(notice, "按需重新读取") {
		t.Fatalf("notice=%q", notice)
	}
}

func TestVoiceHistoryPersistsTranscriptAndOpaqueSourceReferenceOnly(t *testing.T) {
	event := MessageEvent{
		Segments: []MessageSegment{{Type: "record", Data: map[string]string{
			voiceSTTTranscriptKey: "持久化的转写", voiceSTTAudioHashKey: strings.Repeat("b", 64),
			"file": "/tmp/ephemeral.wav", "url": "https://media.example/voice.wav", "cached_file": "/tmp/cache.wav",
		}}},
		Quoted: &QuotedMessage{Segments: []MessageSegment{{Type: "record", Data: map[string]string{
			voiceSTTTranscriptKey: "引用转写", "file": "onebot-record-token",
		}}}},
	}
	persisted := voiceTranscriptOnlyHistory(event)
	data := persisted.Segments[0].Data
	if data[voiceSTTTranscriptKey] != "持久化的转写" || data[voiceSTTAudioHashKey] == "" || data["file"] != "" || data["url"] != "" || data["cached_file"] != "" {
		t.Fatalf("persisted voice data=%#v", data)
	}
	if persisted.Quoted == nil || persisted.Quoted.Segments[0].Data["file"] != "onebot-record-token" {
		t.Fatalf("opaque OneBot source reference was not retained: %#v", persisted.Quoted)
	}
	if event.Segments[0].Data["file"] != "/tmp/ephemeral.wav" {
		t.Fatal("history sanitization mutated the live event")
	}
}

func TestVoiceSTTDisabledAndPrivateControls(t *testing.T) {
	plugin := NewVoiceSTTPlugin(nil)
	manager := NewPluginManager(plugin)
	runtime := NewRuntime(BotConfig{}, nil, manager, nil, nil, nil, nil)
	event := MessageEvent{Kind: EventKindPrivate, Segments: []MessageSegment{{Type: "record", Data: map[string]string{"file": "/not/read"}}}}
	if got := runtime.prepareIncomingVoice(context.Background(), event); got.Segments[0].Data["stt_error_code"] != "" {
		t.Fatal("disabled backend attempted transcription")
	}
	if _, err := manager.UpdateSettings(voiceSTTPluginID, map[string]any{"backend": voiceSTTBackendLocal, "private_enabled": false}); err != nil {
		t.Fatal(err)
	}
	if got := runtime.prepareIncomingVoice(context.Background(), event); got.Segments[0].Data["stt_error_code"] != "" {
		t.Fatal("private-disabled backend attempted transcription")
	}
}

func TestVoiceSegmentsParticipateInMediaTurnAssembly(t *testing.T) {
	voice := MessageEvent{Segments: []MessageSegment{{Type: "record", Data: map[string]string{"file": "voice.amr"}}}}
	if !EventIsMergeableMediaOnly(voice) || !eventHasDirectReferenceContent(voice) {
		t.Fatal("record segment is not treated as mergeable reference media")
	}
	question := MessageEvent{Segments: []MessageSegment{{Type: "text", Data: map[string]string{"text": "这段语音说了什么"}}}}
	merged := attachInboundTurnMedia(question, []MessageEvent{voice})
	if len(merged.Segments) != 2 || merged.Segments[1].Type != "record" {
		t.Fatalf("merged segments=%#v", merged.Segments)
	}
}

func TestVoiceSourceDecodingAndLimits(t *testing.T) {
	want := []byte("small-audio")
	encoded := "c21hbGwtYXVkaW8="
	for _, source := range []string{"base64://" + encoded, "data:audio/wav;base64," + encoded} {
		got, _, err := materializeVoiceBytes(context.Background(), NewRuntime(BotConfig{}, nil, nil, nil, nil, nil, nil), MessageEvent{}, MessageSegment{Type: "record", Data: map[string]string{"file": source}}, 1024)
		if err != nil || string(got) != string(want) {
			t.Fatalf("source=%q body=%q err=%v", source, got, err)
		}
	}
	if _, _, err := materializeVoiceBytes(context.Background(), NewRuntime(BotConfig{}, nil, nil, nil, nil, nil, nil), MessageEvent{}, MessageSegment{Type: "record", Data: map[string]string{"file": "base64://" + encoded}}, 2); err == nil {
		t.Fatal("oversized inline audio was accepted")
	}
}

func TestVoiceSTTRetriesOnlyTransientProviderFailures(t *testing.T) {
	for _, test := range []struct {
		status    int
		transient bool
	}{{http.StatusBadRequest, false}, {http.StatusTooManyRequests, true}, {http.StatusBadGateway, true}} {
		err := voiceSTTProviderError{StatusCode: test.status}
		if got := voiceSTTErrorIsTransient("provider_rejected", err); got != test.transient {
			t.Fatalf("status=%d transient=%v, want %v", test.status, got, test.transient)
		}
	}
	if !voiceSTTErrorIsTransient("timeout", context.DeadlineExceeded) {
		t.Fatal("timeout was not classified as transient")
	}
}

func TestVoiceSTTAcquireHonorsCancellation(t *testing.T) {
	plugin := NewVoiceSTTPlugin(nil)
	sem, err := plugin.acquire(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := plugin.acquire(ctx, 1); err == nil {
		t.Fatal("cancelled concurrency wait succeeded")
	}
	<-sem
}

func TestOneBotRecordSourceResolution(t *testing.T) {
	target := MessageSegment{Type: "record", Data: map[string]string{"file": "voice-token.amr"}}
	response := map[string]any{"message": []any{map[string]any{"type": "record", "data": map[string]any{"file": "voice-token.amr", "url": "https://media.example/voice.amr"}}}}
	got, key := mediaSourceFromOneBotData(response, target)
	if got != "https://media.example/voice.amr" || key != "url" {
		t.Fatalf("source=%q key=%q", got, key)
	}
}

func writeSilentWAV(t *testing.T, path string, samples int) {
	t.Helper()
	dataSize := samples * 2
	body := make([]byte, 44+dataSize)
	copy(body[0:4], "RIFF")
	binary.LittleEndian.PutUint32(body[4:8], uint32(36+dataSize))
	copy(body[8:12], "WAVE")
	copy(body[12:16], "fmt ")
	binary.LittleEndian.PutUint32(body[16:20], 16)
	binary.LittleEndian.PutUint16(body[20:22], 1)
	binary.LittleEndian.PutUint16(body[22:24], 1)
	binary.LittleEndian.PutUint32(body[24:28], 16000)
	binary.LittleEndian.PutUint32(body[28:32], 32000)
	binary.LittleEndian.PutUint16(body[32:34], 2)
	binary.LittleEndian.PutUint16(body[34:36], 16)
	copy(body[36:40], "data")
	binary.LittleEndian.PutUint32(body[40:44], uint32(dataSize))
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
}
