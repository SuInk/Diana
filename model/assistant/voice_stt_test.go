// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
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
	profile := llm.Profile{ID: "whisper", Name: "whisper", Group: "stt", Config: llm.ProviderConfig{Provider: llm.ProviderOpenAICompatible, APIKey: "secret-token", BaseURL: server.URL}}
	runtime := mediaSlotTestRuntime(t, map[string]ModelRole{"stt": {ProfileID: "whisper", Model: "whisper-test"}}, profile)
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
	cfg := voiceSTTConfig{Backend: "openai", Model: "whisper-test", Language: "zh"}
	hash := strings.Repeat("a", 64)
	key := voiceSTTCacheKey(hash, cfg)
	store := &voiceTestStore{transcripts: map[string]VoiceTranscriptRecord{key: {CacheKey: key, AudioSHA256: hash, Transcript: "只读取持久化文字", DurationMS: 500}}}
	runtime := NewRuntime(BotConfig{}, nil, nil, nil, nil, nil, nil)
	runtime.SetMessageHistoryStore(store)
	transcript, gotHash, _, cacheHit, code, err := runtime.transcribeVoiceSegment(context.Background(), MessageEvent{}, MessageSegment{Type: "record", Data: map[string]string{voiceSTTBlobHashKey: hash}}, cfg)
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
	event := MessageEvent{Kind: EventKindPrivate, Segments: []MessageSegment{{Type: "record", Data: map[string]string{"file": "/not/read"}}}}
	unconfigured := mediaSlotTestRuntime(t, nil)
	if got := unconfigured.prepareIncomingVoice(context.Background(), event); got.Segments[0].Data["stt_error_code"] != "" {
		t.Fatal("transcription ran without a speech recognition slot")
	}
	privateOff := mediaSlotTestRuntime(t, map[string]ModelRole{
		"stt": {ProfileID: "builtin:whisper-cli", Model: "whisper-cli", Params: map[string]string{mediaParamPrivateEnabled: "false"}},
	})
	if got := privateOff.prepareIncomingVoice(context.Background(), event); got.Segments[0].Data["stt_error_code"] != "" {
		t.Fatal("private-disabled slot attempted transcription")
	}
}

func TestVoiceSegmentsParticipateInMediaTurnAssembly(t *testing.T) {
	voice := MessageEvent{Segments: []MessageSegment{{Type: "record", Data: map[string]string{"file": "voice.amr"}}}}
	if !eventHasDirectReferenceContent(voice) {
		t.Fatal("record segment is not treated as reference media")
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
		kind      llm.MediaErrorKind
		transient bool
	}{{llm.MediaErrorInvalid, false}, {llm.MediaErrorRateLimit, true}, {llm.MediaErrorUpstream, true}} {
		if got := voiceSTTErrorIsTransient("provider_rejected", &llm.MediaAPIError{Kind: test.kind}); got != test.transient {
			t.Fatalf("kind=%s transient=%v, want %v", test.kind, got, test.transient)
		}
	}
	if !voiceSTTErrorIsTransient("timeout", context.DeadlineExceeded) {
		t.Fatal("timeout was not classified as transient")
	}
}

func TestVoiceSTTAcquireHonorsCancellation(t *testing.T) {
	var limiter voiceSTTLimiter
	var releases []func()
	for range defaultVoiceSTTConcurrency {
		release, err := limiter.acquire(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		releases = append(releases, release)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := limiter.acquire(ctx); err == nil {
		t.Fatal("cancelled concurrency wait succeeded")
	}
	for _, release := range releases {
		release()
	}
}

func TestOneBotRecordSourceResolution(t *testing.T) {
	target := MessageSegment{Type: "record", Data: map[string]string{"file": "voice-token.amr"}}
	response := map[string]any{"message": []any{map[string]any{"type": "record", "data": map[string]any{"file": "voice-token.amr", "url": "https://media.example/voice.amr"}}}}
	got, key := mediaSourceFromOneBotData(response, target)
	if got != "https://media.example/voice.amr" || key != "url" {
		t.Fatalf("source=%q key=%q", got, key)
	}
}

// silkRecordChannel 模拟 SnowLuma：get_record 带 out_format 时返回转码后的 base64。
type silkRecordChannel struct {
	nilChannel
	wav   []byte
	calls atomic.Int32
}

func (c *silkRecordChannel) CallAPI(_ context.Context, action string, params map[string]any) (map[string]any, error) {
	if action != "get_record" || params["out_format"] != "wav" {
		return nil, errors.New("unsupported")
	}
	c.calls.Add(1)
	return map[string]any{"file": "https://multimedia.example/voice.amr", "out_format": "wav", "base64": base64.StdEncoding.EncodeToString(c.wav)}, nil
}

func TestSilkVoiceBytes(t *testing.T) {
	for _, tc := range []struct {
		body []byte
		want bool
	}{
		{append([]byte{0x02}, "#!SILK_V3\x0c\x00"...), true},
		{[]byte("#!SILK_V3\x0c\x00"), true},
		{[]byte("#!AMR\n"), false},
		{[]byte("RIFF....WAVE"), false},
		{[]byte{0x02}, false},
	} {
		if got := silkVoiceBytes(tc.body); got != tc.want {
			t.Fatalf("silkVoiceBytes(%q)=%v, want %v", tc.body, got, tc.want)
		}
	}
}

// QQ 语音是 SILK，扩展名却是 .amr，ffmpeg 解不了：要先让 OneBot 客户端转成 wav
// 再转写。生产上曾经每条 QQ 语音都报 unsupported_or_corrupt。
func TestVoiceSTTTranscodesQQSilkThroughOneBot(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg is unavailable")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"text": "晚上好"})
	}))
	defer server.Close()

	dir := t.TempDir()
	wavPath := filepath.Join(dir, "converted.wav")
	writeSilentWAV(t, wavPath, 1600)
	wav, err := os.ReadFile(wavPath)
	if err != nil {
		t.Fatal(err)
	}
	silkPath := filepath.Join(dir, "7c08e80e.amr")
	if err := os.WriteFile(silkPath, append([]byte{0x02}, "#!SILK_V3\x0c\x00not-really-audio"...), 0o600); err != nil {
		t.Fatal(err)
	}
	profile := llm.Profile{ID: "whisper", Name: "whisper", Group: "stt", Config: llm.ProviderConfig{Provider: llm.ProviderOpenAICompatible, APIKey: "k", BaseURL: server.URL}}
	runtime := mediaSlotTestRuntime(t, map[string]ModelRole{"stt": {ProfileID: "whisper", Model: "whisper-test"}}, profile)
	channel := &silkRecordChannel{wav: wav}
	runtime.channel = channel
	event := MessageEvent{Kind: EventKindPrivate, Platform: PlatformOneBotV11, UserID: "10001", MessageID: "voice-silk", Segments: []MessageSegment{{Type: "record", Data: map[string]string{"file": silkPath}}}}

	got := runtime.prepareIncomingVoice(context.Background(), event)
	if transcript := got.Segments[0].Data[voiceSTTTranscriptKey]; transcript != "晚上好" {
		t.Fatalf("transcript=%q error_code=%q", transcript, got.Segments[0].Data["stt_error_code"])
	}
	if channel.calls.Load() != 1 {
		t.Fatalf("get_record calls=%d, want 1", channel.calls.Load())
	}
}

func TestFFmpegReadableVoiceLeavesOtherFormatsAlone(t *testing.T) {
	runtime := NewRuntime(BotConfig{}, nil, nil, nil, nil, nil, nil)
	body := []byte("RIFF....WAVE")
	got, format, err := runtime.ffmpegReadableVoice(context.Background(), MessageEvent{}, MessageSegment{Type: "record"}, body, ".wav", 1<<20)
	if err != nil || format != ".wav" || string(got) != string(body) {
		t.Fatalf("body=%q format=%q err=%v", got, format, err)
	}
	if _, _, err := runtime.ffmpegReadableVoice(context.Background(), MessageEvent{}, MessageSegment{Type: "record"}, []byte("#!SILK_V3"), ".amr", 1<<20); err == nil {
		t.Fatal("silk without OneBot file token should fail instead of reaching ffmpeg")
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
