// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package llm

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func noMediaRetryWait(t *testing.T) {
	t.Helper()
	previous := mediaRetrySleep
	mediaRetrySleep = func(ctx context.Context, _ time.Duration) error { return ctx.Err() }
	t.Cleanup(func() { mediaRetrySleep = previous })
}

func mediaTestConfig(baseURL string) ProviderConfig {
	return ProviderConfig{Provider: ProviderOpenAICompatible, APIKey: "sk-test", BaseURL: baseURL + "/v1", Headers: map[string]string{"X-Team": "diana"}}
}

func TestSynthesizeSpeechOpenAIRequestShape(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/audio/speech" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer sk-test" {
			t.Errorf("Authorization = %q", got)
		}
		if got := r.Header.Get("X-Team"); got != "diana" {
			t.Errorf("custom header = %q", got)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		want := map[string]any{"model": "tts-1", "input": "你好", "voice": "alloy", "response_format": "wav", "speed": 1.25, "instructions": "轻快"}
		for key, value := range want {
			if body[key] != value {
				t.Errorf("body[%s] = %#v, want %#v", key, body[key], value)
			}
		}
		if _, ok := body["stream"]; ok {
			t.Errorf("non-streaming request must not set stream")
		}
		w.Header().Set("Content-Type", "audio/wav")
		_, _ = w.Write([]byte("RIFF....WAVE"))
	}))
	defer server.Close()

	resp, err := SynthesizeSpeech(context.Background(), mediaTestConfig(server.URL), SpeechRequest{Model: "tts-1", Input: " 你好 ", Voice: "alloy", Format: "wav", Speed: 1.25, Instructions: "轻快"})
	if err != nil {
		t.Fatal(err)
	}
	if string(resp.Audio) != "RIFF....WAVE" || resp.MediaType != "audio/wav" || resp.Format != "wav" || resp.API != SpeechAPIOpenAI {
		t.Fatalf("unexpected response %#v", resp)
	}
}

func TestSynthesizeSpeechElevenLabsRequestShape(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/text-to-speech/voice-123" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if got := r.URL.Query().Get("output_format"); got != "mp3_44100_128" {
			t.Errorf("output_format = %q", got)
		}
		if got := r.Header.Get("xi-api-key"); got != "sk-test" {
			t.Errorf("xi-api-key = %q", got)
		}
		if got := r.Header.Get("Authorization"); got != "" {
			t.Errorf("ElevenLabs must not receive a bearer token, got %q", got)
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["text"] != "hello" || body["model_id"] != "eleven_multilingual_v2" {
			t.Errorf("body = %#v", body)
		}
		if settings, _ := body["voice_settings"].(map[string]any); settings["speed"] != 0.9 {
			t.Errorf("voice_settings = %#v", body["voice_settings"])
		}
		w.Header().Set("Content-Type", "audio/mpeg")
		_, _ = w.Write([]byte("ID3audio"))
	}))
	defer server.Close()

	// 地址不带 /v1 也要落到 /v1 下面。
	cfg := ProviderConfig{Provider: ProviderOpenAICompatible, APIKey: "sk-test", BaseURL: server.URL}
	resp, err := SynthesizeSpeech(context.Background(), cfg, SpeechRequest{API: SpeechAPIElevenLabs, Model: "eleven_multilingual_v2", Input: "hello", Voice: "voice-123", Speed: 0.9})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Format != "mp3" || resp.API != SpeechAPIElevenLabs || string(resp.Audio) != "ID3audio" {
		t.Fatalf("unexpected response %#v", resp)
	}
}

func TestResolveSpeechAPIDetectsElevenLabsHost(t *testing.T) {
	if got := ResolveSpeechAPI("", ProviderConfig{BaseURL: "https://api.elevenlabs.io/v1"}); got != SpeechAPIElevenLabs {
		t.Fatalf("got %q", got)
	}
	if got := ResolveSpeechAPI("", ProviderConfig{BaseURL: "http://cosyvoice:50000/v1"}); got != SpeechAPIOpenAI {
		t.Fatalf("got %q", got)
	}
	if got := ResolveSpeechAPI("openai", ProviderConfig{BaseURL: "https://api.elevenlabs.io/v1"}); got != SpeechAPIOpenAI {
		t.Fatalf("explicit api must win, got %q", got)
	}
}

func TestSynthesizeSpeechRetriesTransientFailures(t *testing.T) {
	noMediaRetryWait(t)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch calls.Add(1) {
		case 1:
			w.Header().Set("Retry-After", "3")
			http.Error(w, `{"error":"slow down"}`, http.StatusTooManyRequests)
		case 2:
			http.Error(w, "bad gateway", http.StatusBadGateway)
		default:
			_, _ = w.Write([]byte("audio"))
		}
	}))
	defer server.Close()

	resp, err := SynthesizeSpeech(context.Background(), mediaTestConfig(server.URL), SpeechRequest{Model: "tts-1", Input: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 3 || string(resp.Audio) != "audio" {
		t.Fatalf("calls=%d resp=%#v", calls.Load(), resp)
	}
}

func TestSynthesizeSpeechDoesNotRetryPermanentFailures(t *testing.T) {
	noMediaRetryWait(t)
	cases := []struct {
		status int
		body   string
		kind   MediaErrorKind
	}{
		{http.StatusBadRequest, `{"error":{"message":"unknown voice"}}`, MediaErrorInvalid},
		{http.StatusUnauthorized, `{"error":"bad key"}`, MediaErrorAuth},
		{http.StatusBadRequest, `{"error":{"code":"content_policy_violation"}}`, MediaErrorContentPolicy},
	}
	for _, tc := range cases {
		var calls atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			http.Error(w, tc.body, tc.status)
		}))
		_, err := SynthesizeSpeech(context.Background(), mediaTestConfig(server.URL), SpeechRequest{Model: "tts-1", Input: "hi"})
		server.Close()
		if MediaErrorKindOf(err) != tc.kind || IsRetryableMediaError(err) {
			t.Fatalf("status %d: err=%v kind=%q", tc.status, err, MediaErrorKindOf(err))
		}
		if calls.Load() != 1 {
			t.Fatalf("status %d retried %d times", tc.status, calls.Load())
		}
	}
}

func TestSynthesizeSpeechGivesUpAfterRetryBudget(t *testing.T) {
	noMediaRetryWait(t)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer server.Close()

	_, err := SynthesizeSpeech(context.Background(), mediaTestConfig(server.URL), SpeechRequest{Model: "tts-1", Input: "hi"})
	var apiErr *MediaAPIError
	if !errors.As(err, &apiErr) || apiErr.Kind != MediaErrorUpstream || apiErr.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("err = %v", err)
	}
	if want := int32(DefaultMediaRetryPolicy.MaxRetries + 1); calls.Load() != want {
		t.Fatalf("calls = %d, want %d", calls.Load(), want)
	}
}

func TestSynthesizeSpeechAttemptTimeoutIsRetried(t *testing.T) {
	noMediaRetryWait(t)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		if calls.Add(1) == 1 {
			select {
			case <-r.Context().Done():
			case <-time.After(2 * time.Second):
			}
			return
		}
		_, _ = w.Write([]byte("audio"))
	}))
	defer server.Close()

	resp, err := SynthesizeSpeech(context.Background(), mediaTestConfig(server.URL), SpeechRequest{Model: "tts-1", Input: "hi", Timeout: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 || string(resp.Audio) != "audio" {
		t.Fatalf("calls=%d", calls.Load())
	}
}

func TestSynthesizeSpeechRejectsJSONErrorOn200(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"error":"speaker not found"}`))
	}))
	defer server.Close()

	_, err := SynthesizeSpeech(context.Background(), mediaTestConfig(server.URL), SpeechRequest{Model: "cosyvoice", Input: "hi"})
	if MediaErrorKindOf(err) != MediaErrorBadResponse || !strings.Contains(err.Error(), "speaker not found") {
		t.Fatalf("err = %v", err)
	}
}

func TestSynthesizeSpeechCallerCancellationIsNotAnUpstreamError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 读完请求体服务端才会去探测断连，否则 r.Context() 不会随客户端取消。
		_, _ = io.Copy(io.Discard, r.Body)
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(50 * time.Millisecond); cancel() }()
	_, err := SynthesizeSpeech(ctx, mediaTestConfig(server.URL), SpeechRequest{Model: "tts-1", Input: "hi"})
	if !errors.Is(err, context.Canceled) || MediaErrorKindOf(err) != "" {
		t.Fatalf("err = %v", err)
	}
}

func TestStreamSpeechReturnsBodyAsItArrives(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["stream"] != true {
			t.Errorf("stream flag missing: %#v", body)
		}
		w.Header().Set("Content-Type", "audio/mpeg")
		flusher := w.(http.Flusher)
		for _, chunk := range []string{"chunk1", "chunk2"} {
			_, _ = w.Write([]byte(chunk))
			flusher.Flush()
		}
	}))
	defer server.Close()

	stream, err := StreamSpeech(context.Background(), mediaTestConfig(server.URL), SpeechRequest{Model: "tts-1", Input: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	data, err := io.ReadAll(stream)
	if err != nil || string(data) != "chunk1chunk2" || stream.MediaType != "audio/mpeg" {
		t.Fatalf("data=%q err=%v stream=%#v", data, err, stream)
	}
}

func TestStreamSpeechElevenLabsUsesStreamEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/text-to-speech/v1/stream" {
			t.Errorf("path = %s", r.URL.Path)
		}
		_, _ = w.Write([]byte("x"))
	}))
	defer server.Close()
	stream, err := StreamSpeech(context.Background(), mediaTestConfig(server.URL), SpeechRequest{API: SpeechAPIElevenLabs, Input: "hi", Voice: "v1"})
	if err != nil {
		t.Fatal(err)
	}
	_ = stream.Close()
}

func TestTranscribeAudioOpenAIMultipart(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/audio/transcriptions" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Fatal(err)
		}
		if r.FormValue("model") != "whisper-1" || r.FormValue("language") != "zh" || r.FormValue("prompt") != "Diana" || r.FormValue("response_format") != "json" {
			t.Errorf("form = %#v", r.MultipartForm.Value)
		}
		file, header, err := r.FormFile("file")
		if err != nil {
			t.Fatal(err)
		}
		data, _ := io.ReadAll(file)
		if header.Filename != "voice.amr" || string(data) != "audio-bytes" {
			t.Errorf("file %q = %q", header.Filename, data)
		}
		_, _ = w.Write([]byte(`{"text":" 你好 ","language":"zh","duration":1.5}`))
	}))
	defer server.Close()

	resp, err := TranscribeAudio(context.Background(), mediaTestConfig(server.URL), TranscriptionRequest{Model: "whisper-1", Audio: []byte("audio-bytes"), Filename: "/tmp/voice.amr", Language: "zh", Prompt: "Diana"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "你好" || resp.Language != "zh" || resp.Duration != 1500*time.Millisecond {
		t.Fatalf("resp = %#v", resp)
	}
}

func TestTranscribeAudioAcceptsPlainTextAndOmitsAutoLanguage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseMultipartForm(1 << 20)
		if _, ok := r.MultipartForm.Value["language"]; ok {
			t.Errorf("auto language must not be sent")
		}
		_, _ = w.Write([]byte("plain transcript"))
	}))
	defer server.Close()
	resp, err := TranscribeAudio(context.Background(), mediaTestConfig(server.URL), TranscriptionRequest{Model: "funasr", Audio: []byte("a"), Language: "auto"})
	if err != nil || resp.Text != "plain transcript" {
		t.Fatalf("resp=%#v err=%v", resp, err)
	}
}

func TestTranscribeAudioElevenLabs(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/speech-to-text" || r.Header.Get("xi-api-key") != "sk-test" {
			t.Errorf("path=%s key=%q", r.URL.Path, r.Header.Get("xi-api-key"))
		}
		_ = r.ParseMultipartForm(1 << 20)
		if r.FormValue("model_id") != "scribe_v1" || r.FormValue("language_code") != "ja" {
			t.Errorf("form = %#v", r.MultipartForm.Value)
		}
		_, _ = w.Write([]byte(`{"text":"こんにちは","language_code":"ja"}`))
	}))
	defer server.Close()
	resp, err := TranscribeAudio(context.Background(), mediaTestConfig(server.URL), TranscriptionRequest{API: SpeechAPIElevenLabs, Audio: []byte("a"), Language: "ja"})
	if err != nil || resp.Text != "こんにちは" || resp.Language != "ja" {
		t.Fatalf("resp=%#v err=%v", resp, err)
	}
}

func TestTranscribeAudioEmptyTranscriptIsAnError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"text":"  "}`))
	}))
	defer server.Close()
	_, err := TranscribeAudio(context.Background(), mediaTestConfig(server.URL), TranscriptionRequest{Model: "whisper-1", Audio: []byte("a")})
	if MediaErrorKindOf(err) != MediaErrorBadResponse {
		t.Fatalf("err = %v", err)
	}
}

func TestGenerateVideoSubmitPollAndFetch(t *testing.T) {
	noMediaRetryWait(t)
	var polls atomic.Int32
	pixel := base64.StdEncoding.EncodeToString([]byte("\x89PNG\r\n\x1a\nfake"))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/videos":
			if err := r.ParseMultipartForm(1 << 20); err != nil {
				t.Fatal(err)
			}
			if r.FormValue("model") != "sora-2" || r.FormValue("prompt") != "一只猫在跳舞" || r.FormValue("seconds") != "8" || r.FormValue("size") != "1280x720" {
				t.Errorf("form = %#v", r.MultipartForm.Value)
			}
			file, header, err := r.FormFile("input_reference")
			if err != nil {
				t.Fatalf("image-to-video must upload input_reference: %v", err)
			}
			data, _ := io.ReadAll(file)
			if !strings.HasPrefix(string(data), "\x89PNG") || header.Header.Get("Content-Type") != "image/png" {
				t.Errorf("reference part = %q %q", header.Header.Get("Content-Type"), data)
			}
			_, _ = w.Write([]byte(`{"id":"video_1","object":"video","status":"queued","progress":0,"model":"sora-2"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/videos/video_1":
			switch polls.Add(1) {
			case 1:
				_, _ = w.Write([]byte(`{"id":"video_1","status":"in_progress","progress":40}`))
			case 2:
				// 轮询中途的瞬时故障要被重试吸收，不能让整个任务失败。
				http.Error(w, "busy", http.StatusServiceUnavailable)
			default:
				_, _ = w.Write([]byte(`{"id":"video_1","status":"completed","progress":100}`))
			}
		case r.Method == http.MethodGet && r.URL.Path == "/v1/videos/video_1/content":
			w.Header().Set("Content-Type", "video/mp4")
			_, _ = w.Write([]byte("mp4-bytes"))
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	generator, err := NewVideoGenerator(mediaTestConfig(server.URL), VideoAPIOpenAI, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	var seen []VideoJobStatus
	result, err := GenerateVideo(context.Background(), generator, VideoGenerateRequest{
		Model: "sora-2", Prompt: "一只猫在跳舞", Seconds: 8, Size: "1280x720", Image: "data:image/png;base64," + pixel,
	}, VideoPollOptions{Interval: time.Millisecond, OnProgress: func(job VideoJob) { seen = append(seen, job.Status) }})
	if err != nil {
		t.Fatal(err)
	}
	if string(result.Video) != "mp4-bytes" || result.MediaType != "video/mp4" || result.Job.ID != "video_1" {
		t.Fatalf("result = %#v", result)
	}
	if len(seen) < 3 || seen[0] != VideoJobQueued || seen[len(seen)-1] != VideoJobCompleted {
		t.Fatalf("progress = %v", seen)
	}
}

func TestGenerateVideoTextOnlyOmitsReference(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/videos":
			_ = r.ParseMultipartForm(1 << 20)
			if len(r.MultipartForm.File) != 0 {
				t.Errorf("text-to-video must not upload files")
			}
			if _, ok := r.MultipartForm.Value["seconds"]; ok {
				t.Errorf("zero seconds must be left to the server")
			}
			// 有的网关提交即完成，并直接给出成品地址。
			_, _ = w.Write([]byte(`{"id":"v2","status":"succeeded","video_url":"` + "http://" + r.Host + `/cdn/v2.mp4"}`))
		case "/cdn/v2.mp4":
			if r.Header.Get("Authorization") != "" {
				t.Errorf("direct download must not carry the API key")
			}
			_, _ = w.Write([]byte("cdn-video"))
		default:
			t.Errorf("unexpected %s", r.URL.Path)
		}
	}))
	defer server.Close()
	generator, _ := NewVideoGenerator(mediaTestConfig(server.URL), "", 0)
	result, err := GenerateVideo(context.Background(), generator, VideoGenerateRequest{Prompt: "sunset"}, VideoPollOptions{Interval: time.Millisecond})
	if err != nil || string(result.Video) != "cdn-video" || result.MediaType != "video/mp4" {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}

func TestGenerateVideoFailedJob(t *testing.T) {
	cases := map[string]MediaErrorKind{
		`{"code":"internal","message":"render crashed"}`:                     MediaErrorJobFailed,
		`{"code":"moderation_blocked","message":"content policy violation"}`: MediaErrorContentPolicy,
	}
	for failure, kind := range cases {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost {
				_, _ = w.Write([]byte(`{"id":"v3","status":"queued"}`))
				return
			}
			_, _ = w.Write([]byte(`{"id":"v3","status":"failed","error":` + failure + `}`))
		}))
		generator, _ := NewVideoGenerator(mediaTestConfig(server.URL), VideoAPIOpenAI, 0)
		_, err := GenerateVideo(context.Background(), generator, VideoGenerateRequest{Prompt: "x"}, VideoPollOptions{Interval: time.Millisecond})
		server.Close()
		if MediaErrorKindOf(err) != kind {
			t.Fatalf("failure %s: err=%v kind=%q", failure, err, MediaErrorKindOf(err))
		}
	}
}

func TestGenerateVideoJobTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id":"v4","status":"in_progress"}`))
	}))
	defer server.Close()
	generator, _ := NewVideoGenerator(mediaTestConfig(server.URL), VideoAPIOpenAI, 0)
	_, err := GenerateVideo(context.Background(), generator, VideoGenerateRequest{Prompt: "x"}, VideoPollOptions{Interval: 10 * time.Millisecond, Timeout: 80 * time.Millisecond})
	if MediaErrorKindOf(err) != MediaErrorTimeout {
		t.Fatalf("err = %v", err)
	}
}

func TestNewVideoGeneratorRejectsUnknownAPI(t *testing.T) {
	if _, err := NewVideoGenerator(mediaTestConfig("http://example.com"), "runway", 0); err == nil {
		t.Fatal("unknown video api must be rejected until it is implemented")
	}
}

func TestMediaCallsRequireCredentials(t *testing.T) {
	_, err := SynthesizeSpeech(context.Background(), ProviderConfig{Provider: ProviderOpenAICompatible, BaseURL: "http://example.com/v1"}, SpeechRequest{Model: "tts-1", Input: "hi"})
	if !errors.Is(err, ErrMissingAPIKey) {
		t.Fatalf("err = %v", err)
	}
}
