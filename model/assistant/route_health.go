// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"errors"
	"fmt"

	"github.com/SuInk/diana/model/llm"
)

// noteRouteCall 把不走文本 provider 链的调用结果（生图、改图、语音合成、语音识别、
// 视频生成）记进模型冷却表。
//
// 这几类以前各自挨个换模型，失败不记冷却：智能切换拿不到数据，界面只好不给这个选项，
// 主模型连着超时或 5xx 时每次都先在它那里等满超时才换下一个。记进同一张冷却表后，
// 智能切换把冷却中的候选往后挪，模型页的运行状态也看得到这些失败。
//
// 不是候选自己的问题就不记：调用方整轮取消或超时、内容被审核拒绝、原图读不了。
// 单次请求自己的超时按「模型请求超时」记，和对话那边一样会进冷却。
func (r *Runtime) noteRouteCall(ctx context.Context, group string, profile llm.Profile, err error, notTheRoute bool) {
	if ctx.Err() != nil || errors.Is(err, context.Canceled) {
		return
	}
	if err == nil {
		r.llmCooldowns.noteCall(profile, true)
		r.llmCooldowns.clear(profile)
		return
	}
	if notTheRoute {
		return
	}
	if errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, errModelRequestTimeout) {
		err = fmt.Errorf("%w: %w", errModelRequestTimeout, err)
	}
	noteLLMCandidateFailure(&r.llmCooldowns, group, profile, err, r.modelRecoveryPolicyForContext(ctx, group))
}

// imageRouteProfile 还原生图候选对应的提供商配置档：冷却表按配置档 ID 和模型记，
// 和智能切换排序时查的是同一把键。模型取生图模型：没单独配生图用途时，候选是对话
// 配置档，Config.Model 是对话模型，拿它做键会把生图的成败记到对话模型头上。
func imageRouteProfile(cfg llm.ProviderConfig) llm.Profile {
	profile := llm.Profile{ID: cfg.RequestLimitID, Config: cfg}
	profile.Config.Model = cfg.ImageModelWithDefault()
	return profile
}
