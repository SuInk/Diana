// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/SuInk/diana/internal/procgroup"
	"github.com/SuInk/diana/model/agent"
	"github.com/SuInk/diana/model/llm"
)

// 实时语音通话（会话逻辑在 model/voicecall，协议见 docs/voice-call.md）在机器人这一侧
// 只做三件事：用语音识别插槽转写一句话，把它当成主人发来的一条私聊走正常回复流程，
// 用语音合成插槽把回复念出来。
//
// 回复流程和聊天页是同一条（web_chat.go）：人设、记忆、历史都和主人在这个平台上
// 的私聊是同一份，通话里说的话挂断后在私聊记录里也看得到。
const (
	voiceCallMessagePrefix = "call-"
	voiceCallTranscribeCap = 60 * time.Second
	voiceCallMaxAudioBytes = 16 << 20
)

const promptVoiceCall = "你正在和对方实时语音通话，你说的每个字都会被念出来：像打电话一样口语化地回答，一次说两三句，不用 Markdown、列表、表情符号、链接和括号里的动作描写。对方的话来自语音识别，可能有错别字或断句不准，按最合理的意思理解。"

var promptVoiceCallSpec = tailSpec("voice_call", "语音通话", "主人在 WebUI 或 App 里和机器人语音通话时注入：回复会被逐句合成语音，要写成能直接念的口语。",
	promptVoiceCall)

// VoiceCall 是一通电话在机器人这一侧的对接，实现 voicecall.Backend。
type VoiceCall struct {
	runtime *Runtime
	callID  string
	base    MessageEvent
	turns   atomic.Int64
}

// VoiceCallInfo 是开始通话前给前端看的状态。
type VoiceCallInfo struct {
	BotID    string `json:"bot_id"`
	BotName  string `json:"bot_name"`
	Platform string `json:"platform"`
	STT      bool   `json:"stt"`
	TTS      bool   `json:"tts"`
	// Problem 非空时打不了电话，内容是原因。
	Problem string `json:"problem,omitempty"`
}

// VoiceCallInfo 检查这台机器人能不能打电话：要有主人账号、两个语音插槽都配了。
func (r *Runtime) VoiceCallInfo(ctx context.Context, profileID string) VoiceCallInfo {
	cfg, event, err := r.ownerChatEvent(profileID)
	info := VoiceCallInfo{BotID: cfg.ID, BotName: cfg.Name, Platform: cfg.Platform}
	if err != nil {
		info.Problem = err.Error()
		return info
	}
	ctx = withModelConfigEvent(ctx, event)
	info.STT = r.mediaSlotConfigured(ctx, mediaSlotSTT)
	info.TTS = r.mediaSlotConfigured(ctx, mediaSlotTTS)
	switch {
	case !info.STT && !info.TTS:
		info.Problem = "模型分配里还没配语音识别和语音合成"
	case !info.STT:
		info.Problem = "模型分配里还没配语音识别"
	case !info.TTS:
		info.Problem = "模型分配里还没配语音合成"
	}
	return info
}

// NewVoiceCall 为一通电话准备好机器人这一侧。调用方必须是已登录的 WebUI 管理员：
// 通话里说的话按主人发来的私聊处理。
func (r *Runtime) NewVoiceCall(ctx context.Context, profileID, callID string) (*VoiceCall, error) {
	info := r.VoiceCallInfo(ctx, profileID)
	if info.Problem != "" {
		return nil, errors.New(info.Problem)
	}
	_, event, err := r.ownerChatEvent(profileID)
	if err != nil {
		return nil, err
	}
	event.voiceCall = true
	return &VoiceCall{runtime: r, callID: callID, base: event}, nil
}

// Transcribe 用语音识别插槽转写一句话。封装格式（webm、ogg、mp4）先用 ffmpeg 转成
// 16kHz 单声道 WAV：本机 whisper 只认它，云端接口对它也最稳。
func (c *VoiceCall) Transcribe(ctx context.Context, audio []byte, format string) (string, error) {
	if len(audio) == 0 {
		return "", nil
	}
	if len(audio) > voiceCallMaxAudioBytes {
		return "", fmt.Errorf("这句话的音频太大")
	}
	ctx = withLLMUsagePurpose(withLLMUsageContext(withModelConfigEvent(ctx, c.base), c.base), "voice_call")
	ctx, cancel := context.WithTimeout(ctx, voiceCallTranscribeCap)
	defer cancel()
	if format != "wav" {
		wav, err := normalizeVoiceCallAudio(ctx, audio, format)
		if err != nil {
			return "", err
		}
		audio = wav
	}
	resp, _, err := c.runtime.transcribeAudio(ctx, audio, "audio.wav", "")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(resp.Text), nil
}

func normalizeVoiceCallAudio(ctx context.Context, audio []byte, format string) ([]byte, error) {
	dir, err := os.MkdirTemp("", "diana-call-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	source := filepath.Join(dir, "source."+strings.Trim(format, "."))
	if err := os.WriteFile(source, audio, 0o600); err != nil {
		return nil, err
	}
	wav := filepath.Join(dir, "audio.wav")
	if out, err := procgroup.CommandContext(ctx, "ffmpeg", "-hide_banner", "-loglevel", "error", "-y", "-i", source, "-ac", "1", "-ar", "16000", "-c:a", "pcm_s16le", wav).CombinedOutput(); err != nil {
		return nil, fmt.Errorf("ffmpeg 转码失败: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return os.ReadFile(wav)
}

// Reply 把这句话当成主人发来的一条私聊，同步走完一轮回复，每条回复的文字交给 emit。
func (c *VoiceCall) Reply(ctx context.Context, text string, emit func(string)) error {
	event := c.base
	event.MessageID = voiceCallMessagePrefix + c.callID + "-" + strconv.FormatInt(c.turns.Add(1), 10)
	return c.runtime.replyAsOwner(ctx, event, text, func(msg WebChatMessage) {
		if msg.Text != "" {
			emit(msg.Text)
		}
	})
}

// Synthesize 用语音合成插槽念一句话。OpenAI 兼容和 ElevenLabs 边合成边交出音频，
// 其余各家整句合成完再交出。一条路由失败换下一条，但已经交出过音频就不换：
// 换了会从头再念一遍。
func (c *VoiceCall) Synthesize(ctx context.Context, text string, chunk func(mime string, data []byte) error) error {
	r := c.runtime
	ctx = withLLMUsagePurpose(withLLMUsageContext(withModelConfigEvent(ctx, c.base), c.base), "voice_call")
	routes := r.resolveMediaSlotRoutes(ctx, mediaSlotTTS, true)
	if len(routes) == 0 {
		return fmt.Errorf("语音合成：%w", errMediaSlotNotConfigured)
	}
	emitted := false
	tracked := func(mime string, data []byte) error {
		emitted = true
		return chunk(mime, data)
	}
	var errs []error
	for _, route := range routes {
		err := r.synthesizeCallSpeech(ctx, route, text, tracked)
		if err == nil {
			return nil
		}
		if emitted || ctx.Err() != nil {
			return err
		}
		errs = append(errs, fmt.Errorf("语音合成 %s 调用失败：%w", route.label(), err))
	}
	return errors.Join(errs...)
}

// voiceCallSpeechFormat 选下发的音频格式：插槽上配的格式浏览器和手机放不了（裸 PCM、
// QQ 的 silk/amr）时改用 mp3。
func voiceCallSpeechFormat(format string) string {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "", "pcm", "silk", "amr":
		return "mp3"
	}
	return format
}

func (r *Runtime) synthesizeCallSpeech(ctx context.Context, route mediaSlotRoute, text string, chunk func(mime string, data []byte) error) error {
	started := time.Now()
	req := llm.SpeechRequest{
		API:          route.speechAPI(),
		Model:        route.Model,
		Input:        text,
		Voice:        route.param(mediaParamVoice),
		Format:       voiceCallSpeechFormat(route.param(mediaParamFormat)),
		Speed:        route.paramFloat(mediaParamSpeed),
		Instructions: route.param(mediaParamInstructions),
		Timeout:      route.paramSeconds(mediaParamTimeout),
		Options:      route.Params,
	}
	opts := r.llmClientOptionsFor(route.Config)
	if api := llm.ResolveSpeechAPI(string(req.API), route.Config); api == llm.SpeechAPIOpenAI || api == llm.SpeechAPIElevenLabs {
		stream, err := llm.StreamSpeech(ctx, route.Config, req, opts...)
		if err != nil {
			return err
		}
		defer stream.Close()
		mime := firstNonEmpty(stream.MediaType, "audio/mpeg")
		buf := make([]byte, 16<<10)
		for {
			n, readErr := stream.Read(buf)
			if n > 0 {
				if err := chunk(mime, append([]byte(nil), buf[:n]...)); err != nil {
					return err
				}
			}
			if errors.Is(readErr, io.EOF) {
				break
			}
			if readErr != nil {
				return readErr
			}
		}
		r.recordMediaSlotUsage(ctx, route, "tts", started)
		return nil
	}
	resp, err := llm.SynthesizeSpeech(ctx, route.Config, req, opts...)
	if err != nil {
		return err
	}
	r.recordMediaSlotUsage(ctx, route, "tts", started)
	return chunk(voiceCallAudioMIME(resp), resp.Audio)
}

// voiceCallAudioMIME 给整段合成的音频定类型：接口报的不是音频类型时嗅探内容，
// 都认不出按扩展名推。
func voiceCallAudioMIME(resp *llm.SpeechResponse) string {
	for _, mime := range []string{resp.MediaType, agent.SniffMediaType(resp.Audio)} {
		if strings.HasPrefix(strings.ToLower(mime), "audio/") {
			return mime
		}
	}
	switch ext := slotSpeechExtension(resp); ext {
	case "mp3":
		return "audio/mpeg"
	default:
		return "audio/" + ext
	}
}
