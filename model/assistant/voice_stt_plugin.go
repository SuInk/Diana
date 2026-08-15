package assistant

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/SuInk/diana/model/applog"
	"github.com/SuInk/diana/model/netguard"
)

const (
	voiceSTTPluginID        = "official.voice-stt"
	voiceSTTBackendDisabled = "disabled"
	voiceSTTBackendLocal    = "local"
	voiceSTTBackendOpenAI   = "openai_compatible"
	voiceSTTTranscriptKey   = "transcript"
	voiceSTTAudioHashKey    = "audio_sha256"
	voiceSTTBlobHashKey     = "cached_blob_sha256"
)

type VoiceTranscriptRecord struct {
	CacheKey    string `json:"cache_key"`
	AudioSHA256 string `json:"audio_sha256"`
	Backend     string `json:"backend"`
	Model       string `json:"model"`
	Language    string `json:"language,omitempty"`
	Transcript  string `json:"transcript"`
	DurationMS  int64  `json:"duration_ms,omitempty"`
	CreatedAt   int64  `json:"created_at"`
}

type VoiceTranscriptStore interface {
	LoadVoiceBlob(context.Context, string) ([]byte, bool, error)
	LoadVoiceTranscript(context.Context, string) (VoiceTranscriptRecord, bool, error)
	SaveVoiceTranscript(context.Context, VoiceTranscriptRecord) error
}

type VoiceSTTPlugin struct {
	client        *http.Client
	mu            sync.Mutex
	semaphore     chan struct{}
	semaphoreSize int
}

func NewVoiceSTTPlugin(client *http.Client) *VoiceSTTPlugin {
	if client == nil {
		client = &http.Client{}
	}
	return &VoiceSTTPlugin{client: client}
}

func (p *VoiceSTTPlugin) Manifest() PluginManifest {
	return PluginManifest{
		ID: voiceSTTPluginID, Name: "语音识别", Version: "0.1.0",
		Description: "将收到的 QQ 语音转写为内部对话文本；支持本地 Whisper 和 OpenAI 兼容音频转写接口。",
		Official:    true, BuiltIn: true,
		Permissions: []string{"message:read", "network:http", "filesystem:temp", "process:media"},
		Settings: []PluginSettingSpec{
			{Key: "backend", Label: "识别后端", Type: PluginSettingTypeSelect, Default: voiceSTTBackendDisabled, Options: []PluginSettingOption{{Value: voiceSTTBackendDisabled, Label: "关闭"}, {Value: voiceSTTBackendLocal, Label: "本地 Whisper"}, {Value: voiceSTTBackendOpenAI, Label: "OpenAI 兼容接口"}}},
			{Key: "endpoint", Label: "转写 API 地址", Type: PluginSettingTypeString, Default: "https://api.openai.com/v1/audio/transcriptions"},
			{Key: "api_key", Label: "API Key", Type: PluginSettingTypeString, Default: "", Secret: true},
			{Key: "model", Label: "模型", Type: PluginSettingTypeString, Default: "whisper-1"},
			{Key: "language", Label: "语言", Type: PluginSettingTypeString, Default: "auto"},
			{Key: "local_command", Label: "本地命令", Type: PluginSettingTypeString, Default: "whisper-cli"},
			{Key: "local_model_path", Label: "本地模型路径", Type: PluginSettingTypeString, Default: ""},
			{Key: "timeout_seconds", Label: "识别超时", Type: PluginSettingTypeNumber, Default: 90, Min: settingRange(5), Max: settingRange(300), Step: 5, Unit: "秒"},
			{Key: "max_audio_mb", Label: "音频大小上限", Type: PluginSettingTypeNumber, Default: 20, Min: settingRange(1), Max: settingRange(100), Step: 1, Unit: "MB"},
			{Key: "max_duration_seconds", Label: "音频时长上限", Type: PluginSettingTypeNumber, Default: 300, Min: settingRange(5), Max: settingRange(1800), Step: 5, Unit: "秒"},
			{Key: "concurrency", Label: "并发识别数", Type: PluginSettingTypeNumber, Default: 2, Min: settingRange(1), Max: settingRange(8), Step: 1},
			{Key: "private_enabled", Label: "允许私聊语音识别", Type: PluginSettingTypeBool, Default: true},
			{Key: "timestamps", Label: "请求时间戳", Type: PluginSettingTypeBool, Default: false},
		},
	}
}

func (p *VoiceSTTPlugin) Handle(context.Context, PluginRequest) (*PluginResponse, error) {
	return nil, nil
}

type voiceSTTConfig struct {
	Backend, Endpoint, APIKey, Model, Language, LocalCommand, LocalModel string
	Timeout                                                              time.Duration
	MaxBytes                                                             int64
	MaxDuration                                                          time.Duration
	Concurrency                                                          int
	PrivateEnabled, Timestamps                                           bool
}

func voiceSTTConfigFromSettings(v SettingValues) voiceSTTConfig {
	return voiceSTTConfig{
		Backend: v.String("backend", voiceSTTBackendDisabled), Endpoint: v.String("endpoint", "https://api.openai.com/v1/audio/transcriptions"), APIKey: v.String("api_key", ""),
		Model: v.String("model", "whisper-1"), Language: v.String("language", "auto"), LocalCommand: v.String("local_command", "whisper-cli"), LocalModel: v.String("local_model_path", ""),
		Timeout: time.Duration(v.Int("timeout_seconds", 90)) * time.Second, MaxBytes: int64(v.Int("max_audio_mb", 20)) << 20, MaxDuration: time.Duration(v.Int("max_duration_seconds", 300)) * time.Second,
		PrivateEnabled: v.Bool("private_enabled", true), Timestamps: v.Bool("timestamps", false), Concurrency: v.Int("concurrency", 2),
	}
}

func (r *Runtime) prepareIncomingVoice(ctx context.Context, event MessageEvent) MessageEvent {
	if (!hasRecordSegment(event.Segments) && (event.Quoted == nil || !hasRecordSegment(event.Quoted.Segments))) || r.plugins == nil {
		return event
	}
	pluginValue, settings, enabled := r.plugins.PluginWithSettings(voiceSTTPluginID, r.pluginOverridesForEvent(event))
	plugin, ok := pluginValue.(*VoiceSTTPlugin)
	if !enabled || !ok {
		return event
	}
	cfg := voiceSTTConfigFromSettings(settings)
	if cfg.Backend == voiceSTTBackendDisabled || (event.Kind == EventKindPrivate && !cfg.PrivateEnabled) {
		return event
	}
	for i := range event.Segments {
		segment := &event.Segments[i]
		if segment.Type != "record" || strings.TrimSpace(segment.Data[voiceSTTTranscriptKey]) != "" || strings.TrimSpace(segment.Data["stt_error_code"]) != "" {
			continue
		}
		started := time.Now()
		transcript, audioHash, duration, cacheHit, code, err := plugin.transcribeSegment(ctx, r, event, *segment, cfg)
		if err != nil {
			segment.Data = cloneSegmentData(segment.Data)
			segment.Data[voiceSTTAudioHashKey] = audioHash
			segment.Data["stt_error_code"] = code
			r.recordVoiceSTT(ctx, event, *segment, cfg, duration, time.Since(started), false, code)
			if voiceSTTErrorIsTransient(code, err) {
				event.voiceSTTErr = fmt.Errorf("voice transcription %s: %w", code, err)
				event.voiceSTTTransient = true
			}
			continue
		}
		segment.Data = cloneSegmentData(segment.Data)
		segment.Data[voiceSTTTranscriptKey] = transcript
		segment.Data[voiceSTTAudioHashKey] = audioHash
		segment.Data["stt_backend"] = cfg.Backend
		segment.Data["stt_model"] = cfg.Model
		r.recordVoiceSTT(ctx, event, *segment, cfg, duration, time.Since(started), cacheHit, "")
	}
	if event.Quoted != nil && hasRecordSegment(event.Quoted.Segments) {
		quoted := *event.Quoted
		quotedEvent := event
		quotedEvent.MessageID = quoted.MessageID
		quotedEvent.UserID = firstNonEmpty(quoted.UserID, event.UserID)
		quotedEvent.GroupID = firstNonEmpty(quoted.GroupID, event.GroupID)
		quotedEvent.Segments = quoted.Segments
		quotedEvent.Quoted = nil
		quotedEvent = r.prepareIncomingVoice(ctx, quotedEvent)
		quoted.Segments = quotedEvent.Segments
		event.Quoted = &quoted
	}
	return event
}

func (p *VoiceSTTPlugin) transcribeSegment(ctx context.Context, r *Runtime, event MessageEvent, segment MessageSegment, cfg voiceSTTConfig) (string, string, time.Duration, bool, string, error) {
	sem, err := p.acquire(ctx, cfg.Concurrency)
	if err != nil {
		return "", "", 0, false, "cancelled", err
	}
	defer func() { <-sem }()
	body, format, err := materializeVoiceBytes(ctx, r, event, segment, cfg.MaxBytes)
	if err != nil {
		return "", "", 0, false, "retrieval_failed", err
	}
	hash := sha256.Sum256(body)
	audioHash := hex.EncodeToString(hash[:])
	cacheKey := voiceSTTCacheKey(audioHash, cfg)
	store := r.voiceTranscriptStore()
	if store != nil {
		if record, found, loadErr := store.LoadVoiceTranscript(ctx, cacheKey); loadErr == nil && found {
			return record.Transcript, audioHash, time.Duration(record.DurationMS) * time.Millisecond, true, "", nil
		}
	}
	workDir, err := os.MkdirTemp("", "diana-stt-")
	if err != nil {
		return "", audioHash, 0, false, "temp_failed", err
	}
	defer os.RemoveAll(workDir)
	source := filepath.Join(workDir, "source"+format)
	if err := os.WriteFile(source, body, 0o600); err != nil {
		return "", audioHash, 0, false, "temp_failed", err
	}
	wav := filepath.Join(workDir, "audio.wav")
	callCtx, cancel := context.WithTimeout(ctx, cfg.Timeout)
	defer cancel()
	if out, err := exec.CommandContext(callCtx, "ffmpeg", "-hide_banner", "-loglevel", "error", "-y", "-i", source, "-t", strconv.Itoa(int(cfg.MaxDuration.Seconds())+1), "-ac", "1", "-ar", "16000", "-c:a", "pcm_s16le", wav).CombinedOutput(); err != nil {
		return "", audioHash, 0, false, "unsupported_or_corrupt", fmt.Errorf("ffmpeg normalize: %w: %s", err, strings.TrimSpace(string(out)))
	}
	duration, _ := voiceDuration(callCtx, wav)
	if duration > cfg.MaxDuration {
		return "", audioHash, duration, false, "duration_exceeded", errors.New("voice duration exceeds configured limit")
	}
	var transcript string
	switch cfg.Backend {
	case voiceSTTBackendLocal:
		transcript, err = localVoiceTranscription(callCtx, wav, cfg, workDir)
	case voiceSTTBackendOpenAI:
		transcript, err = p.openAITranscription(callCtx, wav, cfg)
	default:
		return "", audioHash, duration, false, "disabled", errors.New("STT backend disabled")
	}
	if err != nil {
		code := "provider_rejected"
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(callCtx.Err(), context.DeadlineExceeded) {
			code = "timeout"
		}
		return "", audioHash, duration, false, code, err
	}
	transcript = strings.TrimSpace(transcript)
	if transcript == "" {
		return "", audioHash, duration, false, "empty_transcript", errors.New("empty transcript")
	}
	if store != nil {
		_ = store.SaveVoiceTranscript(ctx, VoiceTranscriptRecord{CacheKey: cacheKey, AudioSHA256: audioHash, Backend: cfg.Backend, Model: cfg.Model, Language: cfg.Language, Transcript: transcript, DurationMS: duration.Milliseconds(), CreatedAt: time.Now().Unix()})
	}
	return transcript, audioHash, duration, false, "", nil
}

func (r *Runtime) voiceTranscriptStore() VoiceTranscriptStore {
	r.mu.RLock()
	defer r.mu.RUnlock()
	store, _ := r.messageStore.(VoiceTranscriptStore)
	return store
}

func materializeVoiceBytes(ctx context.Context, r *Runtime, event MessageEvent, segment MessageSegment, maxBytes int64) ([]byte, string, error) {
	if hash := strings.TrimSpace(segment.Data[voiceSTTBlobHashKey]); hash != "" {
		if store := r.voiceTranscriptStore(); store != nil {
			if body, found, err := store.LoadVoiceBlob(ctx, hash); err == nil && found {
				return body, ".audio", nil
			}
		}
	}
	source := firstNonEmpty(segment.Data["cached_file"], segment.Data["sourcePath"], segment.Data["path"], segment.Data["url"], segment.Data["file"])
	if source == "" {
		enriched, failures := r.enrichMediaSegmentsDetailed(ctx, event, []MessageSegment{segment})
		if len(enriched) > 0 {
			source = firstNonEmpty(enriched[0].Data["cached_file"], enriched[0].Data["path"], enriched[0].Data["url"], enriched[0].Data["file"])
		}
		if source == "" {
			return nil, "", errors.Join(failures...)
		}
	}
	if strings.HasPrefix(source, "base64://") {
		body, err := decodeBase64MediaSource(source, maxBytes)
		return body, ".audio", err
	}
	if strings.HasPrefix(source, "data:audio/") {
		body, err := decodeBase64MediaSource(source, maxBytes)
		return body, ".audio", err
	}
	if path := rawAbsoluteMediaPath(source); path != "" {
		info, err := os.Stat(path)
		if err != nil || info.Size() > maxBytes {
			return nil, "", errors.New("audio file unavailable or oversized")
		}
		body, err := os.ReadFile(path)
		return body, filepath.Ext(path), err
	}
	body, err := downloadVoiceBytes(ctx, source, maxBytes)
	return body, filepath.Ext(strings.Split(source, "?")[0]), err
}

func downloadVoiceBytes(ctx context.Context, source string, maxBytes int64) ([]byte, error) {
	callCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(callCtx, http.MethodGet, source, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "audio/*,application/octet-stream;q=0.8")
	resp, err := netguard.NewPublicHTTPClient(15 * time.Second).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("audio download failed: status=%d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil || len(body) == 0 {
		return nil, errors.New("audio download returned no usable data")
	}
	if int64(len(body)) > maxBytes {
		return nil, errors.New("audio download exceeds configured limit")
	}
	return body, nil
}

func decodeBase64MediaSource(source string, maxBytes int64) ([]byte, error) {
	payload := source
	if strings.HasPrefix(source, "base64://") {
		payload = strings.TrimPrefix(source, "base64://")
	} else if comma := strings.Index(source, ","); comma >= 0 {
		payload = source[comma+1:]
	}
	body, err := base64.StdEncoding.DecodeString(strings.TrimSpace(payload))
	if err != nil {
		body, err = base64.RawStdEncoding.DecodeString(strings.TrimSpace(payload))
	}
	if err != nil || int64(len(body)) > maxBytes {
		return nil, errors.New("invalid or oversized base64 audio")
	}
	return body, nil
}

func localVoiceTranscription(ctx context.Context, wav string, cfg voiceSTTConfig, dir string) (string, error) {
	prefix := filepath.Join(dir, "transcript")
	args := []string{"-f", wav, "-otxt", "-of", prefix}
	if cfg.LocalModel != "" {
		args = append([]string{"-m", cfg.LocalModel}, args...)
	}
	if cfg.Language != "" && cfg.Language != "auto" {
		args = append(args, "-l", cfg.Language)
	}
	out, err := exec.CommandContext(ctx, cfg.LocalCommand, args...).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("local whisper: %w: %s", err, strings.TrimSpace(string(out)))
	}
	body, err := os.ReadFile(prefix + ".txt")
	if err != nil {
		return strings.TrimSpace(string(out)), nil
	}
	return strings.TrimSpace(string(body)), nil
}

func (p *VoiceSTTPlugin) openAITranscription(ctx context.Context, wav string, cfg voiceSTTConfig) (string, error) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	file, err := writer.CreateFormFile("file", "audio.wav")
	if err != nil {
		return "", err
	}
	src, err := os.Open(wav)
	if err != nil {
		return "", err
	}
	_, err = io.Copy(file, src)
	_ = src.Close()
	if err != nil {
		return "", err
	}
	_ = writer.WriteField("model", cfg.Model)
	if cfg.Language != "" && cfg.Language != "auto" {
		_ = writer.WriteField("language", cfg.Language)
	}
	if cfg.Timestamps {
		_ = writer.WriteField("response_format", "verbose_json")
		_ = writer.WriteField("timestamp_granularities[]", "segment")
	}
	_ = writer.Close()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.Endpoint, &body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	if cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", voiceSTTProviderError{StatusCode: resp.StatusCode}
	}
	var parsed struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(data, &parsed) == nil && strings.TrimSpace(parsed.Text) != "" {
		return parsed.Text, nil
	}
	return strings.TrimSpace(string(data)), nil
}

type voiceSTTProviderError struct{ StatusCode int }

func (e voiceSTTProviderError) Error() string {
	return fmt.Sprintf("transcription provider HTTP %d", e.StatusCode)
}

func voiceSTTErrorIsTransient(code string, err error) bool {
	if code == "timeout" || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var providerErr voiceSTTProviderError
	return errors.As(err, &providerErr) && (providerErr.StatusCode == http.StatusTooManyRequests || providerErr.StatusCode >= 500)
}

func (p *VoiceSTTPlugin) acquire(ctx context.Context, concurrency int) (chan struct{}, error) {
	if concurrency < 1 {
		concurrency = 1
	}
	p.mu.Lock()
	if p.semaphore == nil || p.semaphoreSize != concurrency {
		p.semaphore = make(chan struct{}, concurrency)
		p.semaphoreSize = concurrency
	}
	sem := p.semaphore
	p.mu.Unlock()
	select {
	case sem <- struct{}{}:
		return sem, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func voiceDuration(ctx context.Context, path string) (time.Duration, error) {
	out, err := exec.CommandContext(ctx, "ffprobe", "-v", "error", "-show_entries", "format=duration", "-of", "default=noprint_wrappers=1:nokey=1", path).Output()
	if err != nil {
		return 0, err
	}
	value, err := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
	return time.Duration(value * float64(time.Second)), err
}
func voiceSTTCacheKey(audioHash string, cfg voiceSTTConfig) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{audioHash, cfg.Backend, cfg.Model, cfg.Language, strconv.FormatBool(cfg.Timestamps)}, "|")))
	return hex.EncodeToString(sum[:])
}
func hasRecordSegment(segments []MessageSegment) bool {
	for _, s := range segments {
		if s.Type == "record" {
			return true
		}
	}
	return false
}

func (r *Runtime) recordVoiceSTT(ctx context.Context, event MessageEvent, segment MessageSegment, cfg voiceSTTConfig, duration, latency time.Duration, cacheHit bool, errorCode string) {
	writer := r.appLogWriter()
	if writer == nil {
		return
	}
	_ = writer.AppendLog(ctx, applog.Entry{Kind: applog.KindOperation, Level: applog.LevelInfo, Action: "qqbot.voice.stt", Message: "语音识别已处理", Actor: qqEventActor(event), Target: event.MessageID, Metadata: map[string]any{"backend": cfg.Backend, "model": cfg.Model, "duration_ms": duration.Milliseconds(), "latency_ms": latency.Milliseconds(), "cache_hit": cacheHit, "error_code": errorCode, "format": filepath.Ext(firstNonEmpty(segment.Data["file"], segment.Data["path"])), "audio_sha256": segment.Data[voiceSTTAudioHashKey]}})
}
