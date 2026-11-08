// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"encoding/binary"
	"strings"

	"github.com/SuInk/diana/model/callbridge"
)

// 通话插件的电话边合成边播。GPT-SoVITS 流式合成先吐一个 WAV 头，再一段段吐 PCM；整句合成完
// 要三四秒，第一段只要一秒左右。agent 只收 16kHz 单声道 PCM，以前每句收齐后交给
// ffmpeg 转一次，所以电话那头总要等整句合成完才出声。
//
// callWAVStream 在 Go 里直接解 WAV 头、降采样，每收到一段就交出一段，不起 ffmpeg。
// 只处理 16 位整数 PCM（GPT-SoVITS、绝大多数 WAV 都是）；别的格式交回 ffmpeg 整句转。
// 头里的数据长度不看：流式时它是占位值，读到哪算哪。
type callWAVStream struct {
	head     []byte
	ready    bool
	failed   bool
	channels int
	inRate   int
	// odd 是上一段末尾没凑成整帧的字节。
	odd []byte
	// 降采样：先按 avg 个样本滑动平均挡一下混叠，再线性插值到 16kHz。
	avg     int
	history []float64
	pending []float64
	pos     float64
}

func isWAVMIME(mime string) bool {
	mime = strings.ToLower(strings.TrimSpace(mime))
	return strings.HasPrefix(mime, "audio/wav") || strings.HasPrefix(mime, "audio/x-wav") || strings.HasPrefix(mime, "audio/wave")
}

// callWAVHeadLimit 以内找不到 data 块就放弃，交回 ffmpeg。
const callWAVHeadLimit = 64 << 10

// write 喂进一段字节，返回能播的 16kHz PCM。ok 为假表示这不是能直接处理的 WAV，
// 调用方应该把 raw() 和后面的字节一起交给 ffmpeg。
func (w *callWAVStream) write(data []byte) (pcm []byte, ok bool) {
	if w.failed {
		return nil, false
	}
	if !w.ready {
		w.head = append(w.head, data...)
		body, done, good := w.parseHead()
		if !good {
			w.failed = true
			return nil, false
		}
		if !done {
			return nil, true
		}
		w.ready = true
		data = body
	}
	return w.convert(data), true
}

// raw 是还没解出 data 块时攒下的字节，放弃流式时交给 ffmpeg。
func (w *callWAVStream) raw() []byte { return w.head }

func (w *callWAVStream) parseHead() (body []byte, done, ok bool) {
	b := w.head
	if len(b) < 12 {
		return nil, false, len(b) < 4 || string(b[:4]) == "RIFF"
	}
	if string(b[:4]) != "RIFF" || string(b[8:12]) != "WAVE" {
		return nil, false, false
	}
	off := 12
	for {
		if off+8 > len(b) {
			return nil, false, len(b) < callWAVHeadLimit
		}
		id := string(b[off : off+4])
		size := int(binary.LittleEndian.Uint32(b[off+4 : off+8]))
		off += 8
		if id == "data" {
			if w.channels == 0 {
				return nil, false, false
			}
			return b[off:], true, true
		}
		if off+size > len(b) {
			return nil, false, len(b) < callWAVHeadLimit
		}
		if id == "fmt " {
			if size < 16 {
				return nil, false, false
			}
			format := binary.LittleEndian.Uint16(b[off:])
			channels := int(binary.LittleEndian.Uint16(b[off+2:]))
			rate := int(binary.LittleEndian.Uint32(b[off+4:]))
			bits := binary.LittleEndian.Uint16(b[off+14:])
			// 0xFFFE 是 WAVE_FORMAT_EXTENSIBLE，16 位时内容也是整数 PCM。
			if (format != 1 && format != 0xFFFE) || bits != 16 || channels < 1 || channels > 8 || rate < 8000 || rate > 192000 {
				return nil, false, false
			}
			w.channels, w.inRate = channels, rate
			w.avg = max(1, (rate+callbridge.SampleRate/2)/callbridge.SampleRate)
		}
		off += size + size%2
	}
}

func (w *callWAVStream) convert(data []byte) []byte {
	frame := 2 * w.channels
	if len(w.odd) > 0 {
		data = append(w.odd, data...)
		w.odd = nil
	}
	whole := len(data) / frame * frame
	if whole < len(data) {
		w.odd = append([]byte(nil), data[whole:]...)
	}
	for i := 0; i < whole; i += frame {
		sum := 0
		for c := range w.channels {
			sum += int(int16(binary.LittleEndian.Uint16(data[i+2*c:])))
		}
		sample := float64(sum) / float64(w.channels)
		if w.avg > 1 {
			w.history = append(w.history, sample)
			if len(w.history) > w.avg {
				w.history = w.history[1:]
			}
			total := 0.0
			for _, v := range w.history {
				total += v
			}
			sample = total / float64(len(w.history))
		}
		w.pending = append(w.pending, sample)
	}
	if w.inRate == callbridge.SampleRate {
		out := encodePCM16(w.pending)
		w.pending = w.pending[:0]
		return out
	}
	step := float64(w.inRate) / float64(callbridge.SampleRate)
	var out []float64
	for w.pos+1 < float64(len(w.pending)) {
		i := int(w.pos)
		frac := w.pos - float64(i)
		out = append(out, w.pending[i]*(1-frac)+w.pending[i+1]*frac)
		w.pos += step
	}
	if drop := min(int(w.pos), len(w.pending)); drop > 0 {
		w.pending = append(w.pending[:0], w.pending[drop:]...)
		w.pos -= float64(drop)
	}
	return encodePCM16(out)
}

func encodePCM16(samples []float64) []byte {
	out := make([]byte, 2*len(samples))
	for i, v := range samples {
		v = min(max(v, -32768), 32767)
		binary.LittleEndian.PutUint16(out[2*i:], uint16(int16(v)))
	}
	return out
}
