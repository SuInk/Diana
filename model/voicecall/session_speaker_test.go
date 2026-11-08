// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package voicecall

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
)

// groupBackend 是群聊通话：一句话有两个成员接，每人用自己的声音念。
type groupBackend struct {
	fakeBackend
	mu     sync.Mutex
	voices []string
}

func (b *groupBackend) ReplyBySpeaker(ctx context.Context, text string, emit func(Speaker, string)) error {
	emit(Speaker{ID: "nijika", Name: "虹夏"}, "七点开始排练。记得带上")
	emit(Speaker{ID: "ryo", Name: "凉"}, "我带贝斯。")
	return nil
}

func (b *groupBackend) SynthesizeAs(ctx context.Context, speaker Speaker, text string, chunk func(string, []byte) error) error {
	b.mu.Lock()
	b.voices = append(b.voices, speaker.ID+":"+text)
	b.mu.Unlock()
	return chunk("audio/mpeg", []byte("<"+text+">"))
}

func TestSessionGroupCallTagsEverySentenceWithItsSpeaker(t *testing.T) {
	backend := &groupBackend{}
	backend.reply = func(context.Context, string, func(string)) error {
		t.Error("群聊通话不该走单人的 Reply")
		return nil
	}
	session, out := startSession(t, backend, ClientMessage{Segmentation: SegmentByCmd})
	if err := session.HandleControl(ClientMessage{Type: ClientText, Text: "今晚几点排练"}); err != nil {
		t.Fatal(err)
	}
	out.waitFor(t, "turn_end done", turnEnd(1, TurnDone))
	var replies, audio []string
	for _, frame := range out.snapshot() {
		switch frame.Type {
		case ServerReply:
			replies = append(replies, fmt.Sprintf("%s(%s):%s", frame.SpeakerName, frame.Speaker, frame.Text))
		case ServerAudioStart:
			audio = append(audio, frame.Speaker)
		}
	}
	// 虹夏没说完的半句不会并进凉的话里。
	want := "虹夏(nijika):七点开始排练。|虹夏(nijika):记得带上|凉(ryo):我带贝斯。"
	if got := strings.Join(replies, "|"); got != want {
		t.Fatalf("reply = %s\nwant %s", got, want)
	}
	if got := strings.Join(audio, ","); got != "nijika,nijika,ryo" {
		t.Fatalf("audio_start speakers = %s", got)
	}
	if got := strings.Join(backend.voices, "|"); got != "nijika:七点开始排练。|nijika:记得带上|ryo:我带贝斯。" {
		t.Fatalf("voices = %s", got)
	}
}
