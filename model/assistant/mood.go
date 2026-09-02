// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"math"
	"strings"
	"time"
)

// 情绪系统：给机器人一个会涨落的心情。
//
// 心情不是新的评估调用——关系评估器本来就在给每条消息判「这次相处是加分还是
// 减分」，把这些增减顺手累计起来就是机器人的心情：被夸了心情变好，被骂了变差，
// 没人惹它就慢慢回落到平静。零额外模型开销是这个设计的核心取舍：心情要是再跑
// 一次模型，等于每条消息付三遍钱。
//
// 心情挂在机器人（配置档）上而不是会话上：一个人在 A 群被骂了，到 B 群也蔫着，
// 这正是拟人的点。它是短时状态，进程重启归零合理——睡一觉起来心情本来就会重置，
// 所以不落库。
//
// 对外只有两档可感知的偏移：开心和低落。中间的大片区域什么都不注入——平静是
// 常态，不该有一条「你现在很平静」的提示词稀释注意力。

const (
	// moodHalfLife 是心情向平静回落的半衰期。两小时没人理，再大的情绪也淡了一半。
	moodHalfLife = 2 * time.Hour
	// moodScoreLimit 封顶心情的绝对值，防止一群人连续夸一晚上把状态顶到回不来。
	moodScoreLimit = 10.0
	// moodHappyThreshold / moodLowThreshold 是两档语气的触发线。加分事件权重小
	//（日常 +1 居多），开心线设得比低落线远一点：变开心该比变难过慢。
	moodHappyThreshold = 3.0
	moodLowThreshold   = -3.0
	// moodNegativeWeight 放大减分事件：被骂一句比被夸一句更影响心情，这也是人。
	moodNegativeWeight = 1.5
)

type moodState struct {
	score     float64
	updatedAt time.Time
}

// decayedScore 按半衰期把分数向 0 回落。
func (state moodState) decayedScore(now time.Time) float64 {
	if state.updatedAt.IsZero() {
		return 0
	}
	elapsed := now.Sub(state.updatedAt)
	if elapsed <= 0 {
		return state.score
	}
	return state.score * math.Pow(0.5, elapsed.Hours()/moodHalfLife.Hours())
}

// bumpMood 把一次关系评估的增减并进心情。delta 为 0 时不动，也不刷新时间——
// 中性消息不该阻止情绪回落。
func (r *Runtime) bumpMood(profileID string, delta int, now time.Time) {
	if delta == 0 || r == nil {
		return
	}
	profileID = strings.TrimSpace(profileID)
	impact := float64(delta)
	if impact < 0 {
		impact *= moodNegativeWeight
	}
	r.moodMu.Lock()
	defer r.moodMu.Unlock()
	if r.moods == nil {
		r.moods = map[string]*moodState{}
	}
	state, ok := r.moods[profileID]
	if !ok {
		state = &moodState{}
		r.moods[profileID] = state
	}
	score := state.decayedScore(now) + impact
	if score > moodScoreLimit {
		score = moodScoreLimit
	}
	if score < -moodScoreLimit {
		score = -moodScoreLimit
	}
	state.score = score
	state.updatedAt = now
}

// moodScore 读出当前衰减后的心情分，测试和语气注入共用。
func (r *Runtime) moodScore(profileID string, now time.Time) float64 {
	if r == nil {
		return 0
	}
	r.moodMu.Lock()
	defer r.moodMu.Unlock()
	state, ok := r.moods[strings.TrimSpace(profileID)]
	if !ok {
		return 0
	}
	return state.decayedScore(now)
}

// moodToneForConfig 返回本轮要注入的心情语气，平静时返回空串。
//
// 注入点和时段语气同一批：两者都是「怎么说」，离生成越近越管用；心情几小时才
// 变一档，不会打散前缀缓存。
func (r *Runtime) moodToneForConfig(cfg BotConfig, profileID string) string {
	if !boolValue(cfg.MoodEnabled, false) {
		return ""
	}
	score := r.moodScore(profileID, r.clock())
	switch {
	case score >= moodHappyThreshold:
		return "你现在心情不错：语气轻快一点，话可以稍微多一点，更愿意接梗。心情是你自己的状态，没人问就不用解释为什么开心。"
	case score <= moodLowThreshold:
		return "你现在情绪有点低落：话少一点、语气蔫一点，该答的照样答准，但不主动接梗、不硬装活泼。没人问就不要解释，也不要卖惨；有人关心你可以承认心情一般，不编具体理由。"
	default:
		return ""
	}
}
