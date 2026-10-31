// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package llm

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestBreezeSpeechFormatsAndPayload(t *testing.T) {
	for _, format := range []string{"", "mp3", "wav", "pcm"} {
		t.Run("format="+format, func(t *testing.T) {
			wantFormat := format
			if wantFormat == "" {
				wantFormat = "mp3"
			}
			server := vendorServer(t, func(t *testing.T, w http.ResponseWriter, r *http.Request, body []byte) {
				if r.Method != "POST" || r.URL.Path != "/v1/text-to-speech/voc_test/stream" || r.URL.Query().Get("output_format") != wantFormat {
					t.Errorf("unexpected request: %s %s", r.Method, r.URL)
				}
				if r.Header.Get("xi-api-key") != "sk-test" || r.Header.Get("Authorization") != "" {
					t.Error("wrong authentication")
				}
				payload := decodeJSONBody(t, body)
				if payload["text"] != "你好" || payload["model_id"] != "breeze-tts-2" || payload["instructions"] != "轻快" || payload["language_code"] != "zh" {
					t.Errorf("payload = %#v", payload)
				}
				settings, _ := payload["voice_settings"].(map[string]any)
				if settings["speed"] != 1.25 || payload["stream"] != nil {
					t.Errorf("payload = %#v", payload)
				}
				w.Header().Set("Content-Type", "application/octet-stream")
				_, _ = w.Write([]byte("audio"))
			})
			for _, base := range []string{server.URL, server.URL + "/v1", server.URL + "/v1/"} {
				cfg := mediaTestConfig(server.URL)
				cfg.BaseURL = base
				req := SpeechRequest{API: SpeechAPIBreeze, Model: " breeze-tts-2 ", Input: " 你好 ", Voice: " voc_test ", Format: format, Speed: 1.25, Instructions: " 轻快 ", Options: map[string]string{"language": "zh"}}
				resp := synthesize(t, cfg, req)
				if string(resp.Audio) != "audio" || resp.Format != wantFormat || resp.MediaType != speechMediaType("", wantFormat) || resp.API != SpeechAPIBreeze {
					t.Fatalf("response = %#v", resp)
				}
				stream, err := StreamSpeech(context.Background(), cfg, req)
				if err != nil {
					t.Fatal(err)
				}
				audio, err := io.ReadAll(stream)
				stream.Close()
				if err != nil || string(audio) != "audio" || stream.Format != wantFormat {
					t.Fatalf("stream = %#v, audio=%q, err=%v", stream, audio, err)
				}
			}
		})
	}
}

func TestBreezeSpeechOptionalFieldsAndValidation(t *testing.T) {
	server := vendorServer(t, func(t *testing.T, w http.ResponseWriter, r *http.Request, body []byte) {
		payload := decodeJSONBody(t, body)
		if len(payload) != 1 || payload["text"] != "你好" {
			t.Errorf("optional fields should be omitted: %#v", payload)
		}
		_, _ = w.Write([]byte("audio"))
	})
	cfg := mediaTestConfig(server.URL)
	synthesize(t, cfg, SpeechRequest{API: SpeechAPIBreeze, Voice: "voc_test", Input: "你好", Options: map[string]string{"language": "auto"}})
	for _, tc := range []struct {
		voice, format string
		speed         float64
		want          string
	}{
		{"", "mp3", 0, "voice id"}, {"voc_test", "opus", 0, "format"}, {"voc_test", "mp3_44100_128", 0, "format"}, {"voc_test", "mp3", 0.4, "speed"}, {"voc_test", "mp3", 2.1, "speed"},
	} {
		_, err := SynthesizeSpeech(context.Background(), cfg, SpeechRequest{API: SpeechAPIBreeze, Input: "你好", Voice: tc.voice, Format: tc.format, Speed: tc.speed})
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("expected %s error, got %v", tc.want, err)
		}
	}
}

func TestBreezeSpeechReportsAuthenticationError(t *testing.T) {
	server := vendorServer(t, func(t *testing.T, w http.ResponseWriter, r *http.Request, body []byte) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"ok":false,"error":"invalid_api_key","detail":"Invalid API key"}`))
	})
	cfg := mediaTestConfig(server.URL)
	req := SpeechRequest{API: SpeechAPIBreeze, Voice: "voc_test", Input: "你好"}
	_, err := SynthesizeSpeech(context.Background(), cfg, req)
	var apiErr *MediaAPIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != 401 {
		t.Fatalf("error = %v", err)
	}
	_, err = StreamSpeech(context.Background(), cfg, req)
	if !errors.As(err, &apiErr) || apiErr.StatusCode != 401 {
		t.Fatalf("stream error = %v", err)
	}
}
