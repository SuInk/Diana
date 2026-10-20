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
// 减分」，把这些增减顺手累计起来就是机器人的心情：被夸了更开心，被骂了开心被
// 抵掉，没人惹它就慢慢回到本来的开心。零额外模型开销是这个设计的核心取舍：心情
// 要是再跑一次模型，等于每条消息付三遍钱。
//
// 心情挂在机器人（配置档）上而不是会话上，是短时状态，不落库：重启就回到基线。
//
// 这里只维护供管理页观察的短时分数，不再将心情扩写为每轮表达提示词。
// 被骂得多时分数降低，过一阵自己
// 回来。心情最低就是平静，没有「低落」档：以前有过，一个人在一个群里连骂十几句
// 就把心情顶到下限，六个群一起话少、不接梗好几个小时（线上 2026-09-29 一个人
// 14 次减分，108 轮回复带着低落语气）。对这个人的态度留给他自己的好感度去管。

const (
	// moodHalfLife 是心情向基线回落的半衰期：离基线差多少，两小时后只差一半。
	moodHalfLife = 2 * time.Hour
	// moodScoreLimit 封顶心情，防止一群人连续夸一晚上把状态顶到回不来。
	moodScoreLimit = 10.0
	// moodBaseline 是没人惹它时心情回到的值，比开心线高出一截：被轻轻怼一句
	//（-1 按 -1.5 记）还开心，连着被骂两三句才掉出开心，最多两个多小时回来。
	moodBaseline = 5.0
	// moodHappyThreshold 是开心语气的触发线。
	moodHappyThreshold = 3.0
	// moodNegativeWeight 放大减分事件：被骂一句比被夸一句更能把好心情抵掉。
	moodNegativeWeight = 1.5
)

type moodState struct {
	score     float64
	updatedAt time.Time
}

// decayedScore 按半衰期把分数向基线回落。
func (state moodState) decayedScore(now time.Time) float64 {
	if state.updatedAt.IsZero() {
		return moodBaseline
	}
	elapsed := now.Sub(state.updatedAt)
	if elapsed <= 0 {
		return state.score
	}
	return moodBaseline + (state.score-moodBaseline)*math.Pow(0.5, elapsed.Hours()/moodHalfLife.Hours())
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
	if score < 0 {
		score = 0
	}
	state.score = score
	state.updatedAt = now
}

// moodScore 读出当前衰减后的心情分，测试和语气注入共用。
func (r *Runtime) moodScore(profileID string, now time.Time) float64 {
	if r == nil {
		return moodBaseline
	}
	r.moodMu.Lock()
	defer r.moodMu.Unlock()
	state, ok := r.moods[strings.TrimSpace(profileID)]
	if !ok {
		return moodBaseline
	}
	return state.decayedScore(now)
}
