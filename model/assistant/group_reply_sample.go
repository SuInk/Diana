// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"fmt"
	"math/rand"
)

// EffectiveGroupReplySamplePercent 算出一个群实际生效的回复抽样率：群里填了以群为准，
// 留空或 0 跟随机器人，两边都没填（或填了 100 以上）返回 100，表示不抽样。
//
// 运行时判定和控制台展示都走这里，和额度上限一个道理。
func EffectiveGroupReplySamplePercent(bot BotConfig, group GroupConfig) int {
	percent := bot.ReplySamplePercent
	if group.ReplySamplePercent > 0 {
		percent = group.ReplySamplePercent
	}
	if percent <= 0 || percent > 100 {
		return 100
	}
	return percent
}

// groupReplySampleSkips 判断这条群消息是不是没抽中、不必交给路由模型判断。
//
// 抽样只管「机器人要不要主动接话」这一路：被 @、被引用、叫了名字的消息在进这里
// 之前就已经确定要回，不参与抽样——被点名却随机不理人，看起来就是坏了。每条群
// 消息都要过一次路由模型，这才是闲聊群里调用次数的大头，抽样省的正是这一次。
// 主人不参与抽样，和额度的规矩一样。
func (r *Runtime) groupReplySampleSkips(event MessageEvent) (string, bool) {
	if r == nil || event.Kind != EventKindGroup {
		return "", false
	}
	botCfg := r.effectiveConfigForEvent(event)
	if botCfg.IsOwnerEvent(event) {
		return "", false
	}
	groupCfg, _ := r.groupConfigForEvent(event)
	percent := EffectiveGroupReplySamplePercent(botCfg, groupCfg)
	if percent >= 100 {
		return "", false
	}
	roll := r.replySampleRoll
	if roll == nil {
		roll = func() int { return rand.Intn(100) }
	}
	if roll() < percent {
		return "", false
	}
	return fmt.Sprintf("未抽中本群 %d%% 的回复抽样，没有交给模型判断是否接话", percent), true
}
