// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/SuInk/diana/internal/procgroup"
	"github.com/SuInk/diana/model/applog"
	"github.com/SuInk/diana/model/llm"
	"github.com/SuInk/diana/model/netguard"
)

// 语音识别以前是一个插件，有自己的后端、地址、Key 和模型。现在识别服务归模型分配
// 里的「语音识别」插槽管：OpenAI 兼容接口、ElevenLabs、MiMo 是云端提供商，本机的
// whisper.cpp 命令是内置的「本机 Whisper」。插槽配了就转写，没配就不转。
const (
	voiceSTTTranscriptKey  = "transcript"
	voiceSTTAudioHashKey   = "audio_sha256"
	voiceSTTBlobHashKey    = "cached_blob_sha256"
	voiceSourceMaxParts    = 2
	voiceSourceMaxDuration = 60 * time.Second

	// 插槽参数里给转写流程用的几项。
	mediaParamPrivateEnabled = "private_enabled"
	mediaParamMaxDuration    = "max_duration_seconds"
	mediaParamCommand        = "command"
	mediaParamModelPath      = "model_path"

	defaultVoiceSTTTimeout     = 90 * time.Second
	defaultVoiceSTTMaxBytes    = 20 << 20
	defaultVoiceSTTMaxDuration = 300 * time.Second
	defaultVoiceSTTConcurrency = 2
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
	DeleteVoiceBlob(context.Context, string) error
	LoadVoiceTranscript(context.Context, string) (VoiceTranscriptRecord, bool, error)
	SaveVoiceTranscript(context.Context, VoiceTranscriptRecord) error
}

// voiceSTTLimiter 限制同时在跑的转写：一段语音要 ffmpeg 转码再上传，群里一下子
// 来十几条语音时别全挤在一起。
type voiceSTTLimiter struct {
	once      sync.Once
	semaphore chan struct{}
}

func (l *voiceSTTLimiter) acquire(ctx context.Context) (func(), error) {
	l.once.Do(func() { l.semaphore = make(chan struct{}, defaultVoiceSTTConcurrency) })
	select {
	case l.semaphore <- struct{}{}:
		return func() { <-l.semaphore }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

type voiceSTTConfig struct {
	// Backend 是实际走的接口形态（openai、mimo、whisper_cli……），记进缓存键和日志。
	Backend, Model, Language string
	Timeout                  time.Duration
	MaxBytes                 int64
	MaxDuration              time.Duration
	PrivateEnabled           bool
}

// voiceSTTConfigForEvent 读这条消息生效的语音识别插槽；没配时 ok 为 false。
func (r *Runtime) voiceSTTConfigForEvent(ctx context.Context, event MessageEvent) (voiceSTTConfig, bool) {
	routes := r.mediaSlotRoutes(withModelConfigEvent(ctx, event), mediaSlotSTT)
	if len(routes) == 0 {
		return voiceSTTConfig{}, false
	}
	route := routes[0]
	cfg := voiceSTTConfig{
		Backend:        string(route.speechAPI()),
		Model:          route.Model,
		Language:       route.param(mediaParamLanguage),
		Timeout:        defaultVoiceSTTTimeout,
		MaxBytes:       defaultVoiceSTTMaxBytes,
		MaxDuration:    defaultVoiceSTTMaxDuration,
		PrivateEnabled: !strings.EqualFold(route.param(mediaParamPrivateEnabled), "false"),
	}
	if timeout := route.paramSeconds(mediaParamTimeout); timeout > 0 {
		cfg.Timeout = timeout
	}
	if duration := route.paramSeconds(mediaParamMaxDuration); duration > 0 {
		cfg.MaxDuration = duration
	}
	return cfg, true
}

func (r *Runtime) prepareIncomingVoice(ctx context.Context, event MessageEvent) MessageEvent {
	if !hasRecordSegment(event.Segments) && (event.Quoted == nil || !hasRecordSegment(event.Quoted.Segments)) {
		return event
	}
	// 彻底休眠的群不转写：转写要花钱，而这条消息既不会被回复，也不会进记忆提取。
	if r.groupDormant(event) {
		return event
	}
	ctx = withModelConfigEvent(ctx, event)
	cfg, ok := r.voiceSTTConfigForEvent(ctx, event)
	if !ok || (event.Kind == EventKindPrivate && !cfg.PrivateEnabled) {
		return event
	}
	for i := range event.Segments {
		segment := &event.Segments[i]
		if segment.Type != "record" || strings.TrimSpace(segment.Data[voiceSTTTranscriptKey]) != "" || strings.TrimSpace(segment.Data["stt_error_code"]) != "" {
			continue
		}
		started := time.Now()
		transcript, audioHash, duration, cacheHit, code, err := r.transcribeVoiceSegment(ctx, event, *segment, cfg)
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
		delete(segment.Data, voiceSTTBlobHashKey)
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

func (r *Runtime) transcribeVoiceSegment(ctx context.Context, event MessageEvent, segment MessageSegment, cfg voiceSTTConfig) (string, string, time.Duration, bool, string, error) {
	release, err := r.voiceSTT.acquire(ctx)
	if err != nil {
		return "", "", 0, false, "cancelled", err
	}
	defer release()
	store := r.voiceTranscriptStore()
	audioHash := firstNonEmpty(strings.TrimSpace(segment.Data[voiceSTTAudioHashKey]), strings.TrimSpace(segment.Data[voiceSTTBlobHashKey]))
	if audioHash != "" && store != nil {
		cacheKey := voiceSTTCacheKey(audioHash, cfg)
		if record, found, loadErr := store.LoadVoiceTranscript(ctx, cacheKey); loadErr == nil && found {
			if err := store.DeleteVoiceBlob(ctx, audioHash); err != nil {
				return "", audioHash, 0, false, "cache_cleanup_failed", err
			}
			return record.Transcript, audioHash, time.Duration(record.DurationMS) * time.Millisecond, true, "", nil
		}
	}
	body, format, err := materializeVoiceBytes(ctx, r, event, segment, cfg.MaxBytes)
	if err != nil {
		return "", audioHash, 0, false, "retrieval_failed", err
	}
	hash := sha256.Sum256(body)
	audioHash = hex.EncodeToString(hash[:])
	cacheKey := voiceSTTCacheKey(audioHash, cfg)
	if store != nil {
		if record, found, loadErr := store.LoadVoiceTranscript(ctx, cacheKey); loadErr == nil && found {
			if err := store.DeleteVoiceBlob(ctx, audioHash); err != nil {
				return "", audioHash, 0, false, "cache_cleanup_failed", err
			}
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
	if out, err := procgroup.CommandContext(callCtx, "ffmpeg", "-hide_banner", "-loglevel", "error", "-y", "-i", source, "-t", strconv.Itoa(int(cfg.MaxDuration.Seconds())+1), "-ac", "1", "-ar", "16000", "-c:a", "pcm_s16le", wav).CombinedOutput(); err != nil {
		// ffmpeg 是被时限或上层取消杀掉的，就不是音频坏了：记成「坏文件」会被当成
		// 永久失败，不再重试，也让排查的人去怀疑一段其实没问题的语音。
		code := "unsupported_or_corrupt"
		if ctxErr := callCtx.Err(); ctxErr != nil {
			code = voiceSTTContextCode(ctxErr)
			err = fmt.Errorf("%w (%w)", ctxErr, err)
		}
		return "", audioHash, 0, false, code, fmt.Errorf("ffmpeg normalize: %w: %s", err, strings.TrimSpace(string(out)))
	}
	duration, _ := voiceDuration(callCtx, wav)
	if duration > cfg.MaxDuration {
		return "", audioHash, duration, false, "duration_exceeded", errors.New("voice duration exceeds configured limit")
	}
	transcript, model, err := r.slotVoiceTranscription(callCtx, wav, cfg)
	if err == nil && model != "" && model != cfg.Model {
		// 后备路由顶上时，结果记在实际应答的模型名下，别让主模型的缓存键
		// 命中一份不是它转写的结果。
		cfg.Model = model
		cacheKey = voiceSTTCacheKey(audioHash, cfg)
	}
	if err != nil {
		code := "provider_rejected"
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(callCtx.Err(), context.DeadlineExceeded) {
			code = "timeout"
		} else if callCtx.Err() != nil {
			// 上层取消（停机、重载配置）不是服务商拒绝了这段语音。
			code = voiceSTTContextCode(callCtx.Err())
		}
		return "", audioHash, duration, false, code, err
	}
	transcript = strings.TrimSpace(transcript)
	if transcript == "" {
		return "", audioHash, duration, false, "empty_transcript", errors.New("empty transcript")
	}
	if store != nil {
		if err := store.SaveVoiceTranscript(ctx, VoiceTranscriptRecord{CacheKey: cacheKey, AudioSHA256: audioHash, Backend: cfg.Backend, Model: cfg.Model, Language: cfg.Language, Transcript: transcript, DurationMS: duration.Milliseconds(), CreatedAt: time.Now().Unix()}); err != nil {
			return "", audioHash, duration, false, "cache_save_failed", err
		}
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

func voiceCharacteristicQuestion(text string) bool {
	text = strings.ToLower(strings.TrimSpace(text))
	if text == "" {
		return false
	}
	for _, marker := range []string{
		"音色", "语气", "语调", "声线", "口音", "语速", "音高", "音调", "情绪", "腔调", "咬字", "发音", "停顿", "气息", "哭腔", "颤音", "声纹",
		"声音特征", "声音听起来", "说话方式", "说话快", "说话慢", "男声", "女声", "谁说的", "谁的声音", "是不是本人",
		"timbre", "tone of voice", "prosody", "accent", "pitch", "speaking rate", "vocal emotion", "speaker identity",
	} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

func voiceTranscriptOnlyHistory(event MessageEvent) MessageEvent {
	event.Segments = voiceTranscriptOnlySegments(event.Segments)
	if event.Quoted != nil {
		quoted := *event.Quoted
		quoted.Segments = voiceTranscriptOnlySegments(quoted.Segments)
		event.Quoted = &quoted
	}
	return event
}

func voiceTranscriptOnlySegments(segments []MessageSegment) []MessageSegment {
	out := append([]MessageSegment(nil), segments...)
	for index := range out {
		if out[index].Type != "record" || strings.TrimSpace(out[index].Data[voiceSTTTranscriptKey]) == "" {
			continue
		}
		data := cloneSegmentData(out[index].Data)
		for _, key := range []string{voiceSTTBlobHashKey, "cached_file", "sourcePath", "path", "url", "base64"} {
			delete(data, key)
		}
		if source := strings.TrimSpace(data["file"]); strings.HasPrefix(source, "base64://") || strings.HasPrefix(source, "data:audio/") || normalizedHTTPURL(source) != "" || rawAbsoluteMediaPath(source) != "" {
			delete(data, "file")
		}
		out[index].Data = data
	}
	return out
}

func (r *Runtime) voiceSourceAnalysisParts(ctx context.Context, event MessageEvent, text string, cfg voiceSTTConfig) ([]llm.ContentPart, string) {
	if !voiceCharacteristicQuestion(text) {
		return nil, ""
	}
	segments := append([]MessageSegment(nil), event.Segments...)
	if event.Quoted != nil {
		segments = append(segments, event.Quoted.Segments...)
	}
	voiceSegments := make([]MessageSegment, 0, voiceSourceMaxParts)
	seen := map[string]bool{}
	for _, segment := range segments {
		if segment.Type != "record" {
			continue
		}
		key := firstNonEmpty(segment.Data[voiceSTTAudioHashKey], segment.Data[voiceSTTBlobHashKey], segment.Data["file"], segment.Data["url"], segment.Data["path"])
		if key != "" && seen[key] {
			continue
		}
		seen[key] = true
		voiceSegments = append(voiceSegments, segment)
		if len(voiceSegments) == voiceSourceMaxParts {
			break
		}
	}
	if len(voiceSegments) == 0 {
		return nil, ""
	}
	parts := make([]llm.ContentPart, 0, len(voiceSegments))
	failed := 0
	for _, segment := range voiceSegments {
		data, err := r.voiceSourceAnalysisWAV(ctx, event, segment, cfg)
		if err != nil {
			failed++
			continue
		}
		parts = append(parts, llm.ContentPart{Type: llm.ContentPartInputAudio, AudioData: base64.StdEncoding.EncodeToString(data), AudioFormat: "wav"})
	}
	if len(parts) == 0 {
		return nil, "【语音源文件提示】当前问题涉及音色、语气等声音特征，但引用语音的源文件已不可读取。本轮只能使用持久化转写文字；不得根据文字猜测声音特征，应如实说明无法判断。"
	}
	notice := fmt.Sprintf("【语音源文件提示】当前问题涉及音色、语气等声音特征，已按需重新读取 %d 段源语音作为真实音频附件。必须直接听取附件后回答，不得只根据转写文字推测。", len(parts))
	if failed > 0 {
		notice += fmt.Sprintf("另有 %d 段源语音已失效并被跳过。", failed)
	}
	return parts, notice
}

func (r *Runtime) voiceSourceAnalysisWAV(ctx context.Context, event MessageEvent, segment MessageSegment, cfg voiceSTTConfig) ([]byte, error) {
	body, format, err := materializeVoiceBytes(ctx, r, event, segment, cfg.MaxBytes)
	if err != nil {
		return nil, err
	}
	workDir, err := os.MkdirTemp("", "diana-voice-source-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(workDir)
	if format == "" {
		format = ".audio"
	}
	source := filepath.Join(workDir, "source"+format)
	if err := os.WriteFile(source, body, 0o600); err != nil {
		return nil, err
	}
	durationLimit := voiceSourceMaxDuration
	if cfg.MaxDuration > 0 && cfg.MaxDuration < durationLimit {
		durationLimit = cfg.MaxDuration
	}
	wav := filepath.Join(workDir, "analysis.wav")
	out, err := procgroup.CommandContext(ctx, "ffmpeg", "-hide_banner", "-loglevel", "error", "-y", "-i", source, "-t", strconv.Itoa(int(durationLimit.Seconds())), "-vn", "-ac", "1", "-ar", "24000", "-c:a", "pcm_s16le", wav).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("ffmpeg prepare voice source: %w: %s", err, strings.TrimSpace(string(out)))
	}
	data, err := os.ReadFile(wav)
	if err != nil || len(data) == 0 {
		return nil, errors.New("voice source conversion returned no audio")
	}
	return data, nil
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

// slotVoiceTranscription 返回转写文字和实际应答的模型。
func (r *Runtime) slotVoiceTranscription(ctx context.Context, wav string, cfg voiceSTTConfig) (string, string, error) {
	audio, err := os.ReadFile(wav)
	if err != nil {
		return "", "", err
	}
	resp, route, err := r.transcribeAudio(ctx, audio, "audio.wav", "")
	if err != nil {
		return "", "", err
	}
	return resp.Text, route.Model, nil
}

// transcribeWithWhisperCLI 调本机的 whisper.cpp 命令（whisper-cli）转写一段音频。
// 转写流程送进来的已经是 ffmpeg 规整过的 16kHz 单声道 WAV，正是它要的格式。
func transcribeWithWhisperCLI(ctx context.Context, route mediaSlotRoute, audio []byte) (*llm.TranscriptionResponse, error) {
	dir, err := os.MkdirTemp("", "diana-whisper-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	wav := filepath.Join(dir, "audio.wav")
	if err := os.WriteFile(wav, audio, 0o600); err != nil {
		return nil, err
	}
	text, err := localVoiceTranscription(ctx, wav, dir, route)
	if err != nil {
		return nil, err
	}
	return &llm.TranscriptionResponse{API: llm.SpeechAPIWhisperCLI, Model: route.Model, Text: text}, nil
}

func localVoiceTranscription(ctx context.Context, wav, dir string, route mediaSlotRoute) (string, error) {
	prefix := filepath.Join(dir, "transcript")
	args := []string{"-f", wav, "-otxt", "-of", prefix}
	if model := route.param(mediaParamModelPath); model != "" {
		args = append([]string{"-m", model}, args...)
	}
	if language := route.param(mediaParamLanguage); language != "" && language != "auto" {
		args = append(args, "-l", language)
	}
	out, err := procgroup.CommandContext(ctx, firstNonEmpty(route.param(mediaParamCommand), "whisper-cli"), args...).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("local whisper: %w: %s", err, strings.TrimSpace(string(out)))
	}
	body, err := os.ReadFile(prefix + ".txt")
	if err != nil {
		return strings.TrimSpace(string(out)), nil
	}
	return strings.TrimSpace(string(body)), nil
}

// voiceSTTContextCode 把 ctx 结束的原因翻成失败码：到时限是 timeout（按瞬时故障
// 重试），被上层取消是 cancelled（和排队时被取消同一个码）。两种都不是语音本身的问题。
func voiceSTTContextCode(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	return "cancelled"
}

func voiceSTTErrorIsTransient(code string, err error) bool {
	if code == "timeout" || code == "cache_save_failed" || code == "cache_cleanup_failed" || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	return llm.IsRetryableMediaError(err)
}

func voiceDuration(ctx context.Context, path string) (time.Duration, error) {
	out, err := procgroup.CommandContext(ctx, "ffprobe", "-v", "error", "-show_entries", "format=duration", "-of", "default=noprint_wrappers=1:nokey=1", path).Output()
	if err != nil {
		return 0, err
	}
	value, err := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
	return time.Duration(value * float64(time.Second)), err
}
func voiceSTTCacheKey(audioHash string, cfg voiceSTTConfig) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{audioHash, cfg.Backend, cfg.Model, cfg.Language}, "|")))
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
	_ = writer.AppendLog(ctx, applog.Entry{Kind: applog.KindOperation, Level: applog.LevelInfo, Action: "voice_stt", Message: "语音识别已处理", Actor: oneBotEventActor(event), Target: event.MessageID, Metadata: map[string]any{"backend": cfg.Backend, "model": cfg.Model, "duration_ms": duration.Milliseconds(), "latency_ms": latency.Milliseconds(), "cache_hit": cacheHit, "error_code": errorCode, "format": filepath.Ext(firstNonEmpty(segment.Data["file"], segment.Data["path"])), "audio_sha256": segment.Data[voiceSTTAudioHashKey]}})
}
