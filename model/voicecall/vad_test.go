// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package voicecall

import (
	"encoding/binary"
	"math"
	"testing"
)

const testRate = 16000

// tone 生成 ms 毫秒的正弦波，amplitude 是 16 位采样的峰值。
func tone(ms int, amplitude float64) []byte {
	samples := testRate * ms / 1000
	out := make([]byte, samples*2)
	for i := 0; i < samples; i++ {
		value := int16(amplitude * math.Sin(2*math.Pi*440*float64(i)/testRate))
		binary.LittleEndian.PutUint16(out[i*2:], uint16(value))
	}
	return out
}

func silence(ms int) []byte {
	return make([]byte, testRate*ms/1000*2)
}

func kinds(events []VADEvent) []VADEventKind {
	out := make([]VADEventKind, 0, len(events))
	for _, event := range events {
		out = append(out, event.Kind)
	}
	return out
}

func TestVADSegmentsUtteranceOnSilence(t *testing.T) {
	vad := NewVAD(DefaultVADConfig(testRate))
	var events []VADEvent
	events = append(events, vad.Push(silence(500))...)
	if len(events) != 0 {
		t.Fatalf("静音不该触发事件：%v", kinds(events))
	}
	// 按 10ms 一片喂，确认切片长度和判断粒度无关。
	speech := tone(800, 8000)
	for i := 0; i < len(speech); i += 320 {
		events = append(events, vad.Push(speech[i:min(i+320, len(speech))])...)
	}
	if got := kinds(events); len(got) != 1 || got[0] != VADSpeechStart {
		t.Fatalf("开口后应只有 speech start：%v", got)
	}
	if !vad.Speaking() {
		t.Fatal("说话中 Speaking() 应为 true")
	}
	// 说话中间短暂停顿不算说完。
	events = vad.Push(silence(300))
	events = append(events, vad.Push(tone(300, 8000))...)
	if len(events) != 0 {
		t.Fatalf("短停顿不该切句：%v", kinds(events))
	}
	events = vad.Push(silence(800))
	if got := kinds(events); len(got) != 1 || got[0] != VADSpeechEnd {
		t.Fatalf("长静音后应切出一句：%v", got)
	}
	// 一句里包括开口前的预留、两段说话和中间停顿、句尾静音。
	audioMS := len(events[0].Audio) / 2 * 1000 / testRate
	if audioMS < 1400 || audioMS > 2200 {
		t.Fatalf("切出的音频时长 %dms 不合理", audioMS)
	}
}

func TestVADIgnoresShortNoiseAndLowLevel(t *testing.T) {
	vad := NewVAD(DefaultVADConfig(testRate))
	// 底噪一直在，低于门槛，不算说话。
	if events := vad.Push(tone(2000, 200)); len(events) != 0 {
		t.Fatalf("底噪不该触发：%v", kinds(events))
	}
	// 100ms 的一下响动不够开口。
	events := vad.Push(tone(100, 9000))
	events = append(events, vad.Push(silence(1000))...)
	if len(events) != 0 {
		t.Fatalf("短促响动不该算开口：%v", kinds(events))
	}
}

func TestVADDiscardsTooShortSpeechAndCapsLongOnes(t *testing.T) {
	cfg := DefaultVADConfig(testRate)
	cfg.MinSpeechMS = 500
	vad := NewVAD(cfg)
	events := vad.Push(tone(240, 9000))
	events = append(events, vad.Push(silence(800))...)
	if got := kinds(events); len(got) != 2 || got[0] != VADSpeechStart || got[1] != VADSpeechDiscarded {
		t.Fatalf("太短的一句应报 discarded：%v", got)
	}

	cfg = DefaultVADConfig(testRate)
	cfg.MaxUtteranceMS = 1000
	vad = NewVAD(cfg)
	events = vad.Push(tone(1500, 9000))
	// 切开后声音还在，紧接着又算开口。
	if got := kinds(events); len(got) != 3 || got[0] != VADSpeechStart || got[1] != VADSpeechEnd || got[2] != VADSpeechStart {
		t.Fatalf("超长的一句应被强制切开：%v", got)
	}
}

func TestVADFlushEndsOpenUtterance(t *testing.T) {
	vad := NewVAD(DefaultVADConfig(testRate))
	vad.Push(tone(600, 9000))
	events := vad.Flush()
	if got := kinds(events); len(got) != 1 || got[0] != VADSpeechEnd {
		t.Fatalf("Flush 应交出进行中的一句：%v", got)
	}
	if events := vad.Flush(); len(events) != 0 {
		t.Fatalf("没在说话时 Flush 不该有事件：%v", kinds(events))
	}
}

func TestPCMToWAVHeader(t *testing.T) {
	wav := PCMToWAV(silence(100), testRate)
	if string(wav[0:4]) != "RIFF" || string(wav[8:12]) != "WAVE" || string(wav[36:40]) != "data" {
		t.Fatalf("WAV 头不对：%q", wav[:44])
	}
	if got := binary.LittleEndian.Uint32(wav[24:]); got != testRate {
		t.Fatalf("采样率 = %d", got)
	}
	if got := binary.LittleEndian.Uint32(wav[40:]); int(got) != len(wav)-44 {
		t.Fatalf("数据长度 = %d", got)
	}
}
