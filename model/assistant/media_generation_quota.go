// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"
)

// MediaGenerationKind 是按天限次的媒体种类。计数、预占和超限提示都按种类分开，
// 生图和以后的视频生成共用这一套，各自只需要一个 kind 和一组上限字段。
type MediaGenerationKind string

const (
	MediaGenerationImage MediaGenerationKind = "image"
	MediaGenerationVideo MediaGenerationKind = "video"
)

// mediaGenerationReservationTTL 是一笔预占最长挂多久。任务正常结束、失败、取消
// 都会结清；这里只兜实在没人来结清的情况，不然那几次额度要一直占到重启。
const mediaGenerationReservationTTL = time.Hour

// MediaGenerationKey 定位一笔计数：哪台机器人、哪个平台、哪种媒体、哪一天，
// 归到哪个群（私聊为空）和哪个人。Day 是日界线时区下的自然日（2006-01-02）。
type MediaGenerationKey struct {
	ProfileID string
	Platform  string
	Kind      MediaGenerationKind
	Day       string
	GroupID   string
	UserID    string
}

// MediaGenerationCounts 是某一天已经记下的成功次数：Group 是这个群的，User 是
// 这个人在这台机器人名下所有群和私聊的合计。
type MediaGenerationCounts struct {
	Group int64
	User  int64
}

// MediaGenerationUsageStore 持久化成功次数，重启后接着算。
type MediaGenerationUsageStore interface {
	MediaGenerationCounts(ctx context.Context, key MediaGenerationKey) (MediaGenerationCounts, error)
	AddMediaGeneration(ctx context.Context, key MediaGenerationKey, count int64) error
}

// EffectiveMediaGenerationLimits 算出一个群实际生效的每日上限，0 表示不限。
// 每群上限：群里设了（含 0 = 本群不限）以群为准，没设跟随机器人；每人上限只看机器人。
func EffectiveMediaGenerationLimits(bot BotConfig, group GroupConfig, kind MediaGenerationKind) (groupLimit, userLimit int64) {
	switch kind {
	case MediaGenerationImage:
		groupLimit = bot.ImageGenerationDailyGroupLimit
		if override := group.ImageGenerationDailyGroupLimit; override != nil && *override >= 0 {
			groupLimit = *override
		}
		userLimit = bot.ImageGenerationDailyUserLimit
	case MediaGenerationVideo:
		groupLimit = bot.VideoGenerationDailyGroupLimit
		if override := group.VideoGenerationDailyGroupLimit; override != nil && *override >= 0 {
			groupLimit = *override
		}
		userLimit = bot.VideoGenerationDailyUserLimit
	}
	return max(groupLimit, 0), max(userLimit, 0)
}

// DailyLimitLocation 是每日次数的日界线时区：填了单独的日界线时区以它为准，否则跟随
// 机器人时区。填错的时区名退到下一档，不让一个拼写错误把限制弄失效。
func DailyLimitLocation(bot BotConfig) *time.Location {
	if location := loadBotLocation(bot.DailyLimitTimezone); location != nil {
		return location
	}
	return bot.Location()
}

func mediaGenerationKindLabel(kind MediaGenerationKind) string {
	switch kind {
	case MediaGenerationImage:
		return "生图"
	case MediaGenerationVideo:
		return "视频生成"
	}
	return string(kind) + " 生成"
}

// mediaGenerationQuotaError 是超限的结论，带着能原样念给用户的那句话。
type mediaGenerationQuotaError struct {
	Kind MediaGenerationKind
	// Group 为 true 是本群的上限，否则是这个人的上限。
	Group     bool
	Used      int64
	Limit     int64
	Requested int64
}

func (e *mediaGenerationQuotaError) Error() string { return e.Notice() }

// Notice 是给用户的说明：用了多少、上限多少、什么时候恢复。
func (e *mediaGenerationQuotaError) Notice() string {
	who := "你"
	if e.Group {
		who = "本群"
	}
	label := mediaGenerationKindLabel(e.Kind)
	if e.Used >= e.Limit {
		return fmt.Sprintf("今天%s的%s次数已用完（%d/%d），明天再来", who, label, min(e.Used, e.Limit), e.Limit)
	}
	return fmt.Sprintf("今天%s的%s次数只剩 %d 次（已用 %d/%d），这次需要 %d 次，超出了剩余次数；可以少要一些，或者明天再来", who, label, e.Limit-e.Used, e.Used, e.Limit, e.Requested)
}

// mediaGenerationQuota 只管内存里的在途预占。已成功的次数在存储里，读写都在锁外，
// 一个群的存储慢不会拖住别的群；在途的只在内存里，进程重启时在途任务本来也跟着没了。
//
// 锁外读库和锁内判定之间可能有别的任务刚记完账、又从在途里移走，两头都数不到它。
// commits 每记一次账加一，判定时发现它变了就重读，保证不超发。
type mediaGenerationQuota struct {
	mu      sync.Mutex
	pending map[*mediaGenerationReservation]struct{}
	commits uint64
	// memory 是没接持久存储时的退路（测试、未配数据库的嵌入场景）。
	memory memoryMediaGenerationUsage
}

// mediaGenerationReservation 是一笔预占。生成成功后 commit 实际张数，失败或没跑成
// release；两者都只生效一次。nil 表示这次不计数（主人），方法照常可调。
type mediaGenerationReservation struct {
	quota   *mediaGenerationQuota
	store   MediaGenerationUsageStore
	key     MediaGenerationKey
	amount  int64
	expires time.Time
	// tracked 表示登记进了在途预占；没设上限的那些不登记，结清时也不用进锁。
	tracked bool
	once    sync.Once
}

func (r *Runtime) mediaGenerationStore() MediaGenerationUsageStore {
	r.mu.RLock()
	store, ok := r.messageStore.(MediaGenerationUsageStore)
	r.mu.RUnlock()
	if ok && store != nil {
		return store
	}
	return &r.mediaQuota.memory
}

// reserveMediaGeneration 在真正调用生成接口之前预占 amount 次。超限时返回
// *mediaGenerationQuotaError；成功时返回的预占必须在任务结束时 commit 或 release。
//
// 没设上限时不读库、不进锁，但成功后照样记账：主人白天中途打开限制时，当天
// 已经画过的要算进去。读不到已用次数时放行，和模型额度一个规矩：限制是省钱用的，
// 不该因为存储出错就让所有人都画不了。
func (r *Runtime) reserveMediaGeneration(ctx context.Context, event MessageEvent, kind MediaGenerationKind, amount int) (*mediaGenerationReservation, error) {
	if r == nil || amount <= 0 {
		return nil, nil
	}
	botCfg := r.effectiveConfigForEvent(event)
	// 主人不受限，也不占群里的次数：主人自己试图、调设置，不该把群友当天的份额用掉。
	if botCfg.IsOwnerEvent(event) {
		return nil, nil
	}
	groupCfg, _ := r.groupConfigForEvent(event)
	groupLimit, userLimit := EffectiveMediaGenerationLimits(botCfg, groupCfg, kind)
	now := r.clock()
	key := MediaGenerationKey{
		ProfileID: strings.TrimSpace(r.eventProfileID(event)),
		Platform:  NormalizePlatformID(event.Platform),
		Kind:      kind,
		Day:       now.In(DailyLimitLocation(botCfg)).Format(time.DateOnly),
		UserID:    strings.TrimSpace(event.UserID),
	}
	if event.Kind == EventKindGroup {
		key.GroupID = strings.TrimSpace(event.GroupID)
	}
	if key.GroupID == "" {
		groupLimit = 0
	}
	if key.UserID == "" {
		userLimit = 0
	}
	q := &r.mediaQuota
	reservation := &mediaGenerationReservation{
		quota: q, store: r.mediaGenerationStore(), key: key,
		amount: int64(amount), expires: now.Add(mediaGenerationReservationTTL),
	}
	if groupLimit <= 0 && userLimit <= 0 {
		return reservation, nil
	}
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		q.mu.Lock()
		seen := q.commits
		q.mu.Unlock()
		readCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		counts, err := reservation.store.MediaGenerationCounts(readCtx, key)
		cancel()
		q.mu.Lock()
		if err == nil && q.commits != seen {
			q.mu.Unlock()
			continue
		}
		if err != nil {
			log.Printf("diana media quota: 读取%s次数失败，本次放行：%v", mediaGenerationKindLabel(kind), err)
		} else {
			pendingGroup, pendingUser := q.pendingLocked(key, now)
			if groupLimit > 0 && counts.Group+pendingGroup+int64(amount) > groupLimit {
				q.mu.Unlock()
				return nil, &mediaGenerationQuotaError{Kind: kind, Group: true, Used: counts.Group + pendingGroup, Limit: groupLimit, Requested: int64(amount)}
			}
			if userLimit > 0 && counts.User+pendingUser+int64(amount) > userLimit {
				q.mu.Unlock()
				return nil, &mediaGenerationQuotaError{Kind: kind, Used: counts.User + pendingUser, Limit: userLimit, Requested: int64(amount)}
			}
		}
		if q.pending == nil {
			q.pending = map[*mediaGenerationReservation]struct{}{}
		}
		q.pending[reservation] = struct{}{}
		reservation.tracked = true
		q.mu.Unlock()
		return reservation, nil
	}
}

// pendingLocked 统计同一天、同一种类里还在途的预占，顺手清掉过期的。
func (q *mediaGenerationQuota) pendingLocked(key MediaGenerationKey, now time.Time) (group, user int64) {
	for reservation := range q.pending {
		if now.After(reservation.expires) {
			delete(q.pending, reservation)
			continue
		}
		other := reservation.key
		if other.ProfileID != key.ProfileID || other.Platform != key.Platform || other.Kind != key.Kind || other.Day != key.Day {
			continue
		}
		if key.GroupID != "" && other.GroupID == key.GroupID {
			group += reservation.amount
		}
		if key.UserID != "" && other.UserID == key.UserID {
			user += reservation.amount
		}
	}
	return group, user
}

// commit 记下实际成功的张数（不超过预占数）并结清预占。先在锁外写库，再进锁移出
// 在途：中间那一小段两头都算上，只会多算不会少算。
func (res *mediaGenerationReservation) commit(ctx context.Context, produced int) {
	if res == nil {
		return
	}
	res.once.Do(func() {
		count := min(max(int64(produced), 0), res.amount)
		if count > 0 {
			// 任务可能是超时收尾的，ctx 已经取消；成品已经发出去了，这笔账还得记上。
			writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
			if err := res.store.AddMediaGeneration(writeCtx, res.key, count); err != nil {
				log.Printf("diana media quota: 记录%s次数失败：%v", mediaGenerationKindLabel(res.key.Kind), err)
			}
			cancel()
		}
		if !res.tracked {
			return
		}
		q := res.quota
		q.mu.Lock()
		delete(q.pending, res)
		if count > 0 {
			q.commits++
		}
		q.mu.Unlock()
	})
}

// release 放弃预占：生成失败、被拒、复用了已有任务或任务没能跑起来。
func (res *mediaGenerationReservation) release() {
	res.commit(context.Background(), 0)
}

type memoryMediaGenerationUsage struct {
	mu     sync.Mutex
	counts map[MediaGenerationKey]int64
}

func (m *memoryMediaGenerationUsage) MediaGenerationCounts(_ context.Context, key MediaGenerationKey) (MediaGenerationCounts, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var counts MediaGenerationCounts
	if key.GroupID != "" {
		counts.Group = m.counts[MediaGenerationKey{ProfileID: key.ProfileID, Platform: key.Platform, Kind: key.Kind, Day: key.Day, GroupID: key.GroupID}]
	}
	if key.UserID != "" {
		counts.User = m.counts[MediaGenerationKey{ProfileID: key.ProfileID, Platform: key.Platform, Kind: key.Kind, Day: key.Day, UserID: key.UserID}]
	}
	return counts, nil
}

func (m *memoryMediaGenerationUsage) AddMediaGeneration(_ context.Context, key MediaGenerationKey, count int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.counts == nil {
		m.counts = map[MediaGenerationKey]int64{}
	}
	if key.GroupID != "" {
		m.counts[MediaGenerationKey{ProfileID: key.ProfileID, Platform: key.Platform, Kind: key.Kind, Day: key.Day, GroupID: key.GroupID}] += count
	}
	if key.UserID != "" {
		m.counts[MediaGenerationKey{ProfileID: key.ProfileID, Platform: key.Platform, Kind: key.Kind, Day: key.Day, UserID: key.UserID}] += count
	}
	return nil
}
