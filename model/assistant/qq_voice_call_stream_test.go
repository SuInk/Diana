// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"bytes"
	"encoding/binary"
	"math"
	"testing"
	"time"

	"github.com/SuInk/diana/model/voicecall"
)

// testWAV 造一段 440Hz 正弦波 WAV。dataSize 为负时照流式合成那样填占位长度。
func testWAV(rate, channels, bits, samples int, placeholder bool) []byte {
	var b bytes.Buffer
	frame := channels * bits / 8
	data := samples * frame
	size := uint32(data)
	if placeholder {
		size = 0xFFFFFFFF - 36
	}
	b.WriteString("RIFF")
	_ = binary.Write(&b, binary.LittleEndian, size+36)
	b.WriteString("WAVEfmt ")
	_ = binary.Write(&b, binary.LittleEndian, uint32(16))
	_ = binary.Write(&b, binary.LittleEndian, uint16(1))
	_ = binary.Write(&b, binary.LittleEndian, uint16(channels))
	_ = binary.Write(&b, binary.LittleEndian, uint32(rate))
	_ = binary.Write(&b, binary.LittleEndian, uint32(rate*frame))
	_ = binary.Write(&b, binary.LittleEndian, uint16(frame))
	_ = binary.Write(&b, binary.LittleEndian, uint16(bits))
	b.WriteString("data")
	_ = binary.Write(&b, binary.LittleEndian, size)
	for i := range samples {
		v := int16(8000 * math.Sin(2*math.Pi*440*float64(i)/float64(rate)))
		for range channels {
			if bits == 16 {
				_ = binary.Write(&b, binary.LittleEndian, v)
			} else {
				b.Write(make([]byte, bits/8))
			}
		}
	}
	return b.Bytes()
}

func feedWAV(t *testing.T, wav []byte, piece int) []byte {
	t.Helper()
	w := &callWAVStream{}
	var out []byte
	for len(wav) > 0 {
		n := min(piece, len(wav))
		pcm, ok := w.write(wav[:n])
		if !ok {
			t.Fatal("16-bit WAV was rejected")
		}
		out = append(out, pcm...)
		wav = wav[n:]
	}
	return out
}

func TestCallWAVStreamResamplesTo16kWhateverTheChunking(t *testing.T) {
	// GPT-SoVITS 流式：32kHz 单声道，头里是占位长度。
	wav := testWAV(32000, 1, 16, 32000, true)
	whole := feedWAV(t, wav, len(wav))
	if got := len(whole) / 2; got < 15990 || got > 16000 {
		t.Fatalf("1s of 32kHz became %d samples at 16kHz", got)
	}
	// 切成奇数字节、半个头这样的碎片，结果和一次喂完一样。
	for _, piece := range []int{7, 30, 1001} {
		if got := feedWAV(t, wav, piece); !bytes.Equal(got, whole) {
			t.Fatalf("piece=%d: output differs from one-shot conversion", piece)
		}
	}
	// 降采样后还是那段 440Hz 正弦，幅度没走样。
	peak := 0
	for i := 0; i < len(whole); i += 2 {
		peak = max(peak, int(int16(binary.LittleEndian.Uint16(whole[i:]))))
	}
	if peak < 7000 || peak > 8001 {
		t.Fatalf("peak = %d, want about 8000", peak)
	}
}

func TestCallWAVStreamMixesStereoAndPassesThrough16k(t *testing.T) {
	out := feedWAV(t, testWAV(16000, 2, 16, 1600, false), 333)
	if len(out) != 1600*2 {
		t.Fatalf("got %d bytes, want %d", len(out), 1600*2)
	}
	out = feedWAV(t, testWAV(24000, 1, 16, 2400, false), 100)
	if got := len(out) / 2; got < 1595 || got > 1600 {
		t.Fatalf("0.1s of 24kHz became %d samples", got)
	}
}

func TestCallWAVStreamRejectsWhatItCannotDecode(t *testing.T) {
	w := &callWAVStream{}
	if _, ok := w.write(testWAV(32000, 1, 24, 100, false)); ok {
		t.Fatal("24-bit WAV should go back to ffmpeg")
	}
	w = &callWAVStream{}
	if _, ok := w.write([]byte("ID3 not a wav at all")); ok {
		t.Fatal("non-WAV should go back to ffmpeg")
	}
	// 头还没收全时先等着。
	w = &callWAVStream{}
	if pcm, ok := w.write([]byte("RIFF")); !ok || len(pcm) != 0 {
		t.Fatal("partial header should wait for more bytes")
	}
}

func TestQQVoiceCallTransportPlaysWAVAsItArrives(t *testing.T) {
	out := &qqCallTransport{}
	wav := testWAV(32000, 1, 16, 32000, true)
	_ = out.WriteJSON(voicecall.ServerMessage{Type: voicecall.ServerAudioStart, MIME: "audio/wav"})
	before := time.Now()
	if err := out.WriteBinary(wav[:len(wav)/2]); err != nil {
		t.Fatal(err)
	}
	// 一句还没合成完，前半句已经交出去了：播放时间往后推了约半秒。
	if ahead := out.playUntil.Sub(before); ahead < 400*time.Millisecond {
		t.Fatalf("first half was not played yet, playUntil ahead by %s", ahead)
	}
	if out.buf.Len() != 0 {
		t.Fatal("streamed WAV was buffered for ffmpeg")
	}
	_ = out.WriteBinary(wav[len(wav)/2:])
	if err := out.WriteJSON(voicecall.ServerMessage{Type: voicecall.ServerAudioEnd}); err != nil {
		t.Fatal(err)
	}
	if ahead := out.playUntil.Sub(before); ahead < 900*time.Millisecond {
		t.Fatalf("whole sentence not accounted, playUntil ahead by %s", ahead)
	}
}
