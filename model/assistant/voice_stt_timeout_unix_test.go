// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

//go:build !windows

package assistant

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// ffmpeg 卡住被时限杀掉，不是音频坏了。以前记成 unsupported_or_corrupt，被当成永久
// 失败不再重试；现在记成 timeout，按瞬时故障重排。
func TestVoiceSTTFFmpegTimeoutIsNotReportedAsCorruptAudio(t *testing.T) {
	bin := t.TempDir()
	fake := filepath.Join(bin, "ffmpeg")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nsleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	wav := filepath.Join(t.TempDir(), "sample.wav")
	writeSilentWAV(t, wav, 1600)
	runtime := NewRuntime(BotConfig{}, nil, NewPluginManager(), nil, nil, nil, nil)
	plugin := NewVoiceSTTPlugin(nil)
	cfg := voiceSTTConfig{Backend: voiceSTTBackendOpenAI, Model: "whisper-test", Concurrency: 1, Timeout: 200 * time.Millisecond, MaxBytes: 1 << 20, MaxDuration: time.Minute}
	event := MessageEvent{Kind: EventKindGroup, GroupID: "1", MessageID: "voice-timeout"}
	segment := MessageSegment{Type: "record", Data: map[string]string{"file": wav}}

	started := time.Now()
	_, _, _, _, code, err := plugin.transcribeSegment(context.Background(), runtime, event, segment, cfg)
	if err == nil {
		t.Fatal("expected the stuck ffmpeg to fail")
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("transcription waited %s for a stuck ffmpeg", elapsed)
	}
	if code != "timeout" {
		t.Fatalf("code = %q, want timeout (err=%v)", code, err)
	}
	if !voiceSTTErrorIsTransient(code, err) {
		t.Fatal("an ffmpeg timeout must be retried")
	}
}
