// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/SuInk/diana/model/llm"
)

// 语音合成、语音识别和视频生成是模型分配里的三个插槽。它们和生图一样是单一用途，
// 但比生图更进一步：不配就是没有这项能力，不回落到对话模型——对话模型接不了
// /audio/speech，回落过去只会每次都先失败一次。
const (
	mediaSlotTTS   = llm.GroupTTS
	mediaSlotSTT   = llm.GroupSTT
	mediaSlotVideo = llm.GroupVideo
)

// 插槽参数的键。界面上按插槽给出对应的几项，运行时只认这些。
const (
	mediaParamAPI          = "api"
	mediaParamVoice        = "voice"
	mediaParamFormat       = "format"
	mediaParamSpeed        = "speed"
	mediaParamInstructions = "instructions"
	mediaParamLanguage     = "language"
	mediaParamPrompt       = "prompt"
	mediaParamSize         = "size"
	mediaParamSeconds      = "seconds"
	mediaParamTimeout      = "timeout_seconds"
	mediaParamPollInterval = "poll_interval_seconds"
)

// mediaSlotRoute 是插槽解析出来的一条路由：主路由在前，后备按顺序跟在后面。
type mediaSlotRoute struct {
	Name   string
	Config llm.ProviderConfig
	Model  string
	// Params 是主路由上配的参数，后备路由沿用同一份。
	Params map[string]string
}

func (route mediaSlotRoute) label() string {
	parts := make([]string, 0, 2)
	if name := strings.TrimSpace(route.Name); name != "" {
		parts = append(parts, "「"+name+"」")
	}
	if route.Model != "" {
		parts = append(parts, route.Model)
	}
	return strings.Join(parts, " ")
}

func (route mediaSlotRoute) param(key string) string {
	return strings.TrimSpace(route.Params[key])
}

func (route mediaSlotRoute) paramFloat(key string) float64 {
	value, err := strconv.ParseFloat(route.param(key), 64)
	if err != nil {
		return 0
	}
	return value
}

func (route mediaSlotRoute) paramInt(key string) int {
	value, err := strconv.Atoi(route.param(key))
	if err != nil || value < 0 {
		return 0
	}
	return value
}

func (route mediaSlotRoute) paramSeconds(key string) time.Duration {
	return time.Duration(route.paramFloat(key) * float64(time.Second))
}

// mediaSlotRoutes 按当前机器人的模型分配解析某个插槽。没配、或者配成了「跟随对话」
// 都返回空：这几个插槽不存在可跟随的对话模型。
func (r *Runtime) mediaSlotRoutes(ctx context.Context, slot string) []mediaSlotRoute {
	r.mu.RLock()
	store := r.llmStore
	r.mu.RUnlock()
	if store == nil {
		return nil
	}
	role, ok := r.modelRolesForContext(ctx)[slot]
	if !ok || role.FollowChat {
		return nil
	}
	set := store.Profiles().WithDefaults()
	routes := make([]mediaSlotRoute, 0, 1+len(role.Fallbacks))
	seen := map[string]bool{}
	for _, binding := range append([]ModelRole{role}, role.Fallbacks...) {
		model := mediaSlotModel(binding)
		for _, profile := range mediaSlotProfiles(set, binding) {
			key := profile.ID + "\x00" + model
			if seen[key] {
				continue
			}
			seen[key] = true
			routes = append(routes, mediaSlotRoute{Name: profile.Name, Config: profile.Config.WithDefaults(), Model: model, Params: role.Params})
		}
	}
	return routes
}

func mediaSlotModel(binding ModelRole) string {
	if binding.ProviderID != "" {
		return strings.TrimPrefix(firstNonEmpty(binding.ModelID, binding.Model), binding.ProviderID+":")
	}
	return strings.TrimSpace(binding.Model)
}

func mediaSlotProfiles(set llm.ProfileSet, binding ModelRole) []llm.Profile {
	id := firstNonEmpty(binding.ProviderID, binding.ProfileID)
	if id == "" && binding.Group != "" {
		return set.GroupProfiles(binding.Group)
	}
	for _, profile := range set.Profiles {
		if profile.ID == id {
			return []llm.Profile{profile}
		}
	}
	return nil
}

// mediaSlotConfigured 报告插槽有没有可用的路由，工具据此决定挂不挂。
func (r *Runtime) mediaSlotConfigured(ctx context.Context, slot string) bool {
	return len(r.mediaSlotRoutes(ctx, slot)) > 0
}

var errMediaSlotNotConfigured = errors.New("模型分配里没有配置这个插槽")

// runMediaSlot 按路由顺序调用，失败了换下一条。调用方取消和内容被拒不换：
// 前者没人在等了，后者换一家多半照样被拒，只会多扣一次钱。
func runMediaSlot[T any](ctx context.Context, routes []mediaSlotRoute, label string, call func(mediaSlotRoute) (T, error)) (T, mediaSlotRoute, error) {
	var zero T
	if len(routes) == 0 {
		return zero, mediaSlotRoute{}, fmt.Errorf("%s：%w", label, errMediaSlotNotConfigured)
	}
	var errs []error
	for _, route := range routes {
		if err := ctx.Err(); err != nil {
			return zero, route, err
		}
		result, err := call(route)
		if err == nil {
			return result, route, nil
		}
		if errors.Is(err, context.Canceled) {
			return zero, route, err
		}
		errs = append(errs, fmt.Errorf("%s %s 调用失败：%w", label, route.label(), err))
		if llm.MediaErrorKindOf(err) == llm.MediaErrorContentPolicy {
			break
		}
	}
	return zero, mediaSlotRoute{}, errors.Join(errs...)
}

func (r *Runtime) recordMediaSlotUsage(ctx context.Context, route mediaSlotRoute, purpose string, started time.Time) {
	var event MessageEvent
	if state := llmUsageFromContext(ctx); state != nil {
		event = state.event
	}
	r.recordLLMUsage(ctx, event, route.Config.Provider, route.Model, llm.Usage{}, purpose, time.Since(started), 0)
}

// synthesizeSpeech 用语音合成插槽把文字合成为音频。format 非空时盖过插槽上的格式，
// 给那些只收得下某种格式的调用方用。
func (r *Runtime) synthesizeSpeech(ctx context.Context, text, format string) (*llm.SpeechResponse, error) {
	resp, _, err := runMediaSlot(ctx, r.mediaSlotRoutes(ctx, mediaSlotTTS), "语音合成", func(route mediaSlotRoute) (*llm.SpeechResponse, error) {
		started := time.Now()
		resp, err := llm.SynthesizeSpeech(ctx, route.Config, llm.SpeechRequest{
			API:          llm.SpeechAPI(route.param(mediaParamAPI)),
			Model:        route.Model,
			Input:        text,
			Voice:        route.param(mediaParamVoice),
			Format:       firstNonEmpty(format, route.param(mediaParamFormat)),
			Speed:        route.paramFloat(mediaParamSpeed),
			Instructions: route.param(mediaParamInstructions),
			Timeout:      route.paramSeconds(mediaParamTimeout),
		}, r.llmClientOptionsFor(route.Config)...)
		if err == nil {
			r.recordMediaSlotUsage(ctx, route, "tts", started)
		}
		return resp, err
	})
	return resp, err
}

// slotSpeechSynthesizer 是交给语音合成插件的那个函数：格式按插槽上配的来。
func (r *Runtime) slotSpeechSynthesizer(ctx context.Context, text string) (*llm.SpeechResponse, error) {
	return r.synthesizeSpeech(ctx, text, "")
}

// transcribeAudio 用语音识别插槽转写一段音频。language 非空时盖过插槽上的语言。
func (r *Runtime) transcribeAudio(ctx context.Context, audio []byte, filename, language string) (*llm.TranscriptionResponse, error) {
	resp, _, err := runMediaSlot(ctx, r.mediaSlotRoutes(ctx, mediaSlotSTT), "语音识别", func(route mediaSlotRoute) (*llm.TranscriptionResponse, error) {
		started := time.Now()
		resp, err := llm.TranscribeAudio(ctx, route.Config, llm.TranscriptionRequest{
			API:      llm.SpeechAPI(route.param(mediaParamAPI)),
			Model:    route.Model,
			Audio:    audio,
			Filename: filename,
			Language: firstNonEmpty(language, route.param(mediaParamLanguage)),
			Prompt:   route.param(mediaParamPrompt),
			Timeout:  route.paramSeconds(mediaParamTimeout),
		}, r.llmClientOptionsFor(route.Config)...)
		if err == nil {
			r.recordMediaSlotUsage(ctx, route, "stt", started)
		}
		return resp, err
	})
	return resp, err
}

// defaultVideoSlotTimeout 是插槽没配总时限时，一个视频任务从提交到取回成品的上限。
const defaultVideoSlotTimeout = 10 * time.Minute

func (route mediaSlotRoute) videoJobTimeout() time.Duration {
	if timeout := route.paramSeconds(mediaParamTimeout); timeout > 0 {
		return timeout
	}
	return defaultVideoSlotTimeout
}

// generateVideo 用视频生成插槽跑完一个异步任务，返回成品和实际用到的路由。
func (r *Runtime) generateVideo(ctx context.Context, req llm.VideoGenerateRequest, onProgress func(llm.VideoJob)) (*llm.VideoResult, mediaSlotRoute, error) {
	return runMediaSlot(ctx, r.mediaSlotRoutes(ctx, mediaSlotVideo), "视频生成", func(route mediaSlotRoute) (*llm.VideoResult, error) {
		generator, err := llm.NewVideoGenerator(route.Config, llm.VideoAPI(route.param(mediaParamAPI)), 0, r.llmClientOptionsFor(route.Config)...)
		if err != nil {
			return nil, err
		}
		request := req
		request.Model = route.Model
		if request.Size == "" {
			request.Size = route.param(mediaParamSize)
		}
		if request.Seconds == 0 {
			request.Seconds = route.paramInt(mediaParamSeconds)
		}
		started := time.Now()
		result, err := llm.GenerateVideo(ctx, generator, request, llm.VideoPollOptions{
			Interval:   route.paramSeconds(mediaParamPollInterval),
			Timeout:    route.videoJobTimeout(),
			OnProgress: onProgress,
		})
		if err == nil {
			r.recordMediaSlotUsage(ctx, route, "video_generate", started)
		}
		return result, err
	})
}
