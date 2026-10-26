// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package voicecall

import (
	"encoding/binary"
	"math"
)

// VADConfig 是按音量做的静音分段参数。通话只要求整句转写，不要求流式识别，所以
// 分段只需回答两个问题：人开始说话了没有（用来打断播报），这句话说完了没有（用来
// 送去转写）。时长单位都是毫秒。
type VADConfig struct {
	SampleRate int
	// FrameMS 是判断的粒度。
	FrameMS int
	// StartMS 是连续多久有声才算开口，太短会被咳嗽、碰麦克风触发打断。
	StartMS int
	// EndSilenceMS 是开口后连续多久没声算一句说完。
	EndSilenceMS int
	// MinSpeechMS 以下的整段丢掉，不送转写。
	MinSpeechMS int
	// MaxUtteranceMS 到了就强制切一段，免得有人一直说话或背景一直有声。
	MaxUtteranceMS int
	// PreRollMS 是开口前保留的音频，补回判定开口前被吃掉的字头。
	PreRollMS int
	// MinRMS 是 16 位采样下最低的有声门槛，再安静的环境也不低于它。
	MinRMS float64
	// NoiseRatio 是有声门槛相对底噪的倍数。
	NoiseRatio float64
}

// DefaultVADConfig 按浏览器开了回声消除和降噪的麦克风调的。
func DefaultVADConfig(sampleRate int) VADConfig {
	return VADConfig{
		SampleRate:     sampleRate,
		FrameMS:        20,
		StartMS:        200,
		EndSilenceMS:   700,
		MinSpeechMS:    300,
		MaxUtteranceMS: 30_000,
		PreRollMS:      300,
		MinRMS:         350,
		NoiseRatio:     3,
	}
}

// VADEventKind 是分段器报出的事件。
type VADEventKind int

const (
	// VADSpeechStart 表示判定开口，Audio 为空。
	VADSpeechStart VADEventKind = iota + 1
	// VADSpeechEnd 表示一句说完，Audio 是这一句的 PCM（含开口前的预留）。
	VADSpeechEnd
	// VADSpeechDiscarded 表示开口后的这段太短，没有送转写。
	VADSpeechDiscarded
)

type VADEvent struct {
	Kind  VADEventKind
	Audio []byte
}

// VAD 是 16 位小端单声道 PCM 的能量分段器。不是并发安全的，由会话串行喂数据。
type VAD struct {
	cfg        VADConfig
	frameBytes int
	pending    []byte
	noise      float64

	speaking   bool
	voicedRun  int
	silenceRun int
	preRoll    [][]byte
	utterance  []byte
	speechMS   int
	totalMS    int
}

func NewVAD(cfg VADConfig) *VAD {
	if cfg.SampleRate <= 0 {
		cfg.SampleRate = 16000
	}
	defaults := DefaultVADConfig(cfg.SampleRate)
	if cfg.FrameMS <= 0 {
		cfg.FrameMS = defaults.FrameMS
	}
	if cfg.MinRMS <= 0 {
		cfg.MinRMS = defaults.MinRMS
	}
	if cfg.NoiseRatio <= 0 {
		cfg.NoiseRatio = defaults.NoiseRatio
	}
	frameBytes := cfg.SampleRate * cfg.FrameMS / 1000 * 2
	return &VAD{cfg: cfg, frameBytes: frameBytes}
}

// Speaking 报告当前是否处在一句话里。
func (v *VAD) Speaking() bool {
	return v.speaking
}

// Push 喂一段 PCM，返回这段数据里发生的事件。数据可以按任意长度切开送进来。
func (v *VAD) Push(pcm []byte) []VADEvent {
	v.pending = append(v.pending, pcm...)
	var events []VADEvent
	for len(v.pending) >= v.frameBytes {
		frame := append([]byte(nil), v.pending[:v.frameBytes]...)
		v.pending = v.pending[v.frameBytes:]
		if event, ok := v.frame(frame); ok {
			events = append(events, event)
		}
	}
	return events
}

// Flush 把还在进行的一句当作说完。挂断或客户端说「这句完了」时用。
func (v *VAD) Flush() []VADEvent {
	if !v.speaking {
		v.reset()
		return nil
	}
	return []VADEvent{v.finish()}
}

func (v *VAD) frame(frame []byte) (VADEvent, bool) {
	rms := frameRMS(frame)
	threshold := math.Max(v.cfg.MinRMS, v.noise*v.cfg.NoiseRatio)
	voiced := rms >= threshold
	if !v.speaking {
		if !voiced {
			// 底噪只在没人说话时慢慢跟，说话的声音不能把门槛抬上去。
			v.noise = v.noise*0.95 + rms*0.05
		}
		v.preRoll = append(v.preRoll, frame)
		if limit := v.cfg.PreRollMS / v.cfg.FrameMS; len(v.preRoll) > limit {
			v.preRoll = v.preRoll[len(v.preRoll)-limit:]
		}
		if !voiced {
			v.voicedRun = 0
			return VADEvent{}, false
		}
		v.voicedRun += v.cfg.FrameMS
		if v.voicedRun < v.cfg.StartMS {
			return VADEvent{}, false
		}
		v.speaking = true
		v.silenceRun = 0
		v.speechMS = v.voicedRun
		for _, buffered := range v.preRoll {
			v.utterance = append(v.utterance, buffered...)
		}
		v.totalMS = len(v.preRoll) * v.cfg.FrameMS
		v.preRoll = nil
		return VADEvent{Kind: VADSpeechStart}, true
	}
	v.utterance = append(v.utterance, frame...)
	v.totalMS += v.cfg.FrameMS
	if voiced {
		v.silenceRun = 0
		v.speechMS += v.cfg.FrameMS
	} else {
		v.silenceRun += v.cfg.FrameMS
	}
	if v.silenceRun >= v.cfg.EndSilenceMS || (v.cfg.MaxUtteranceMS > 0 && v.totalMS >= v.cfg.MaxUtteranceMS) {
		return v.finish(), true
	}
	return VADEvent{}, false
}

func (v *VAD) finish() VADEvent {
	audio, speech := v.utterance, v.speechMS
	v.reset()
	if speech < v.cfg.MinSpeechMS {
		return VADEvent{Kind: VADSpeechDiscarded}
	}
	return VADEvent{Kind: VADSpeechEnd, Audio: audio}
}

func (v *VAD) reset() {
	v.speaking = false
	v.voicedRun, v.silenceRun, v.speechMS, v.totalMS = 0, 0, 0, 0
	v.utterance = nil
	v.preRoll = nil
}

func frameRMS(frame []byte) float64 {
	samples := len(frame) / 2
	if samples == 0 {
		return 0
	}
	var sum float64
	for i := 0; i+1 < len(frame); i += 2 {
		sample := float64(int16(binary.LittleEndian.Uint16(frame[i:])))
		sum += sample * sample
	}
	return math.Sqrt(sum / float64(samples))
}

// PCMToWAV 给 16 位单声道 PCM 套上 WAV 头，语音识别接口都认它。
func PCMToWAV(pcm []byte, sampleRate int) []byte {
	out := make([]byte, 44+len(pcm))
	copy(out[0:], "RIFF")
	binary.LittleEndian.PutUint32(out[4:], uint32(36+len(pcm)))
	copy(out[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(out[16:], 16)
	binary.LittleEndian.PutUint16(out[20:], 1)
	binary.LittleEndian.PutUint16(out[22:], 1)
	binary.LittleEndian.PutUint32(out[24:], uint32(sampleRate))
	binary.LittleEndian.PutUint32(out[28:], uint32(sampleRate*2))
	binary.LittleEndian.PutUint16(out[32:], 2)
	binary.LittleEndian.PutUint16(out[34:], 16)
	copy(out[36:], "data")
	binary.LittleEndian.PutUint32(out[40:], uint32(len(pcm)))
	copy(out[44:], pcm)
	return out
}
