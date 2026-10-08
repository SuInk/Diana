// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"errors"
	"log"
	"strings"
	"sync"
	"time"
)

// 人员档案里的「AI 率」：这个账号像不像机器人。
//
// 只有平台标记（Telegram 的 is_bot、QQ 官方机器人）和主人手动标记是确定的，
// 其余靠猜。按速度、按单句、按关键词统计都试过，分不开：真人也会写长段、群友
// 对真人也会问「这是什么」。人能读出来的是整体——固定人设台词、工具报错、替人
// 总结解释、旁人把它当工具使唤或吐槽它是 bot——这些 LLM 也读得出来。
//
// 所以不单独跑一次模型，搭长期记忆门控的车：门控本来就在读这个人的一批消息和
// 前后上下文，顺带给一个 0 到 1 的判断和一句理由。单次判断会飘，按指数平滑攒
// 起来，攒够几次才算数。主人在人员页上可以直接改成「真人」或「机器人」，改过的
// 不再被自动判断覆盖。
//
// 判出来的只用在一个地方：学群友怎么说话时跳过它（群风格笔记、腔调、句尾）。
// 不影响回不回复——误判一个真人，代价只是学说话时少参考一个人。

// UserAIOverride 是主人在人员页上的手动结论。
type UserAIOverride string

const (
	UserAIOverrideNone  UserAIOverride = ""
	UserAIOverrideHuman UserAIOverride = "human"
	UserAIOverrideBot   UserAIOverride = "bot"
)

// UserAIJudgment 是人员档案里关于「像不像 AI」的那一栏。
type UserAIJudgment struct {
	// Likelihood 是平滑后的 AI 率，0 到 1。
	Likelihood float64 `json:"likelihood"`
	// Reason 是最近一次判断给出的理由。
	Reason string `json:"reason,omitempty"`
	// Observations 是攒了几次判断。
	Observations int            `json:"observations"`
	Override     UserAIOverride `json:"override,omitempty"`
	UpdatedAt    time.Time      `json:"updated_at,omitempty"`
	// Likely 是读出来时按 LikelyAI 算好的结论，给控制台直接显示，不落库。
	Likely bool `json:"likely"`
}

const (
	// userAISmoothing 是新一次判断的权重：一次判断只挪三成，偶尔一批消息像 AI
	// 不会立刻翻案。
	userAISmoothing = 0.3
	// userAIMinObservations 是攒够几次判断才拿来用。
	userAIMinObservations = 3
	// userAIThreshold 是算作 AI 的门槛。定得高：学说话时宁可多参考一个机器人，
	// 也别把爱写长段的真人排除掉。
	userAIThreshold      = 0.8
	userAIReasonMaxRunes = 80
)

// Observe 把一次新判断并进来。
func (j UserAIJudgment) Observe(likelihood float64, reason string, now time.Time) UserAIJudgment {
	likelihood = clampUnit(likelihood)
	if j.Observations == 0 {
		j.Likelihood = likelihood
	} else {
		j.Likelihood = (1-userAISmoothing)*j.Likelihood + userAISmoothing*likelihood
	}
	j.Observations++
	if reason = strings.TrimSpace(reason); reason != "" {
		if runes := []rune(reason); len(runes) > userAIReasonMaxRunes {
			reason = string(runes[:userAIReasonMaxRunes]) + "…"
		}
		j.Reason = reason
	}
	j.UpdatedAt = now.UTC()
	return j
}

// LikelyAI 判断这个人现在算不算 AI：手动结论优先，否则看攒够了的平滑值。
func (j UserAIJudgment) LikelyAI() bool {
	switch j.Override {
	case UserAIOverrideBot:
		return true
	case UserAIOverrideHuman:
		return false
	}
	return j.Observations >= userAIMinObservations && j.Likelihood >= userAIThreshold
}

func clampUnit(value float64) float64 {
	if value < 0 {
		return 0
	}
	if value > 1 {
		return 1
	}
	return value
}

// UserAIJudgmentStore 是存 AI 判断的可选接口，存储没实现时这项功能不生效。
type UserAIJudgmentStore interface {
	// ObserveUserAI 把一次判断并进档案（档案不存在时新建），返回并完之后的结果。
	ObserveUserAI(ctx context.Context, botProfileID, userID, displayName string, likelihood float64, reason string) (UserAIJudgment, error)
	// SetUserAIOverride 写入或清除主人的手动结论。
	SetUserAIOverride(ctx context.Context, botProfileID, userID string, override UserAIOverride) (UserAIJudgment, error)
	// ListLikelyAIUsers 列出所有当前算作 AI 的人，启动时填缓存用。
	ListLikelyAIUsers(ctx context.Context) ([]UserAIRef, error)
}

// UserAIRef 指一台机器人档案下的一个人。
type UserAIRef struct {
	BotProfileID string
	UserID       string
}

// likelyAIState 缓存当前算作 AI 的人，学说话时每条历史都要问一次，不能每次查库。
type likelyAIState struct {
	once  sync.Once
	mu    sync.RWMutex
	users map[string]bool
}

func (s *likelyAIState) set(key string, likely bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.users == nil {
		s.users = map[string]bool{}
	}
	if likely {
		s.users[key] = true
	} else {
		delete(s.users, key)
	}
}

var errUserAIUnsupported = errors.New("人员档案的存储不支持 AI 判断")

func userAIKey(botProfileID, userID string) string {
	return strings.TrimSpace(botProfileID) + "\x00" + strings.TrimSpace(userID)
}

func (r *Runtime) userAIStore() UserAIJudgmentStore {
	r.mu.RLock()
	defer r.mu.RUnlock()
	store, _ := r.userMemory.(UserAIJudgmentStore)
	return store
}

// loadLikelyAIUsers 在第一次用到时从库里填缓存；之后靠每次写入同步。
func (r *Runtime) loadLikelyAIUsers() {
	r.likelyAI.once.Do(func() {
		store := r.userAIStore()
		if store == nil {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		refs, err := store.ListLikelyAIUsers(ctx)
		if err != nil {
			log.Printf("diana likely-AI users load failed: %v", err)
			return
		}
		for _, ref := range refs {
			r.likelyAI.set(userAIKey(ref.BotProfileID, ref.UserID), true)
		}
	})
}

func (r *Runtime) rememberUserAI(botProfileID, userID string, judgment UserAIJudgment) {
	r.likelyAI.set(userAIKey(botProfileID, userID), judgment.LikelyAI())
}

func (r *Runtime) userLikelyAI(botProfileID, userID string) bool {
	r.loadLikelyAIUsers()
	r.likelyAI.mu.RLock()
	defer r.likelyAI.mu.RUnlock()
	return r.likelyAI.users[userAIKey(botProfileID, userID)]
}

// observeSpeakerAI 落一次门控给出的判断。
func (r *Runtime) observeSpeakerAI(ctx context.Context, event MessageEvent, observation *memorySpeakerAI) {
	if observation == nil || strings.TrimSpace(event.UserID) == "" {
		return
	}
	store := r.userAIStore()
	if store == nil {
		return
	}
	profileID := r.eventProfileID(event)
	judgment, err := store.ObserveUserAI(ctx, profileID, event.UserID, event.SenderNameOrID(), observation.Likelihood, observation.Reason)
	if err != nil {
		log.Printf("diana user AI judgment write failed: %v", err)
		return
	}
	r.rememberUserAI(profileID, event.UserID, judgment)
}

// SetUserAIOverrideForProfile 给控制台写主人的手动结论。
func (r *Runtime) SetUserAIOverrideForProfile(ctx context.Context, botProfileID, userID string, override UserAIOverride) (UserAIJudgment, error) {
	store := r.userAIStore()
	if store == nil {
		return UserAIJudgment{}, errUserAIUnsupported
	}
	judgment, err := store.SetUserAIOverride(ctx, botProfileID, userID, override)
	if err != nil {
		return UserAIJudgment{}, err
	}
	r.rememberUserAI(botProfileID, userID, judgment)
	return judgment, nil
}

// otherBotFilter 给学说话的几处用：认出群里别的机器人发的消息。平台标了机器人、
// 主人标记过，或者人员档案里攒出来算作 AI，都跳过，只照真人学。
func (r *Runtime) otherBotFilter(event MessageEvent, cfg BotConfig) func(MessageEvent) bool {
	marked := map[string]bool{}
	for _, id := range cfg.MarkedBotIDs {
		if id = strings.TrimSpace(id); id != "" {
			marked[id] = true
		}
	}
	profileID := r.eventProfileID(event)
	return func(item MessageEvent) bool {
		if item.SenderIsBot {
			return true
		}
		userID := strings.TrimSpace(item.UserID)
		if userID == "" {
			return false
		}
		return marked[userID] || r.userLikelyAI(profileID, userID)
	}
}
