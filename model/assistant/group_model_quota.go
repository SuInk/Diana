// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/SuInk/diana/model/applog"
)

// groupModelQuotaWindow 是额度的统计窗口：滚动 5 小时，和上游订阅套餐的窗口
// 一致。
const groupModelQuotaWindow = 5 * time.Hour

// groupModelQuotaCacheTTL 是用量读数的缓存时长。每条消息都去扫一遍日志太贵，
// 而额度本来就是个粗口径的闸门，半分钟的滞后不影响结论。
const groupModelQuotaCacheTTL = 30 * time.Second

type groupQuotaReading struct {
	usage  applog.GroupUsage
	readAt time.Time
}

type groupModelQuotaCache struct {
	mu       sync.Mutex
	readings map[string]groupQuotaReading
}

func (c *groupModelQuotaCache) get(key string, now time.Time) (groupQuotaReading, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	reading, ok := c.readings[key]
	if !ok || now.Sub(reading.readAt) > groupModelQuotaCacheTTL {
		return groupQuotaReading{}, false
	}
	return reading, true
}

func (c *groupModelQuotaCache) put(key string, reading groupQuotaReading) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.readings == nil {
		c.readings = map[string]groupQuotaReading{}
	}
	c.readings[key] = reading
}

// groupQuotaVerdict 是一次额度判断的结论。
type groupQuotaVerdict struct {
	Exceeded bool
	Reason   string
	Usage    applog.GroupUsage
	Calls    int64
}

// GroupModelQuotaWindow 是额度的统计窗口，控制台画进度条时要用同一个口径。
func GroupModelQuotaWindow() time.Duration { return groupModelQuotaWindow }

// EffectiveGroupModelQuota 算出一个群实际生效的调用次数上限：群里填了以群为准，
// 留空或 0 跟随机器人，两边都没填返回 0 表示不限。
//
// 运行时判定和控制台展示都走这里——展示出来的上限要是和真正拦人的那个不一样，
// 进度条就成了误导。
func EffectiveGroupModelQuota(bot BotConfig, group GroupConfig) int64 {
	if group.ModelCallQuota > 0 {
		return group.ModelCallQuota
	}
	return bot.ModelCallQuota
}

// groupModelQuotaExceeded 判断这个群是不是已经用超了本窗口的额度。
//
// 读不到用量时一律放行：额度是省钱用的，不该因为日志存储不可用就让整个群哑掉。
func (r *Runtime) groupModelQuotaExceeded(ctx context.Context, event MessageEvent) groupQuotaVerdict {
	if r == nil || event.Kind != EventKindGroup {
		return groupQuotaVerdict{}
	}
	groupID := strings.TrimSpace(event.GroupID)
	if groupID == "" {
		return groupQuotaVerdict{}
	}
	// 群里填了以群为准，留空跟随机器人那一档：和这张表单里其它「留空跟随机器人」
	// 的设置一个规矩，不必为额度单独记一套。
	botCfg := r.effectiveConfigForEvent(event)
	groupCfg, _ := r.groupConfigForEvent(event)
	callQuota := EffectiveGroupModelQuota(botCfg, groupCfg)
	if callQuota <= 0 {
		return groupQuotaVerdict{}
	}
	verdict := groupQuotaVerdict{Calls: callQuota}
	// 主人不受限：额度用完之后改配置、查用量这些还得靠主人，锁死自己没有道理。
	if botCfg.IsOwnerEvent(event) {
		return verdict
	}
	reader, ok := r.appLogWriter().(applog.GroupUsageReader)
	if !ok || reader == nil {
		return verdict
	}
	profileID := strings.TrimSpace(r.eventProfileID(event))
	key := profileID + "\x00" + groupID
	now := time.Now()
	reading, fresh := r.groupQuota.get(key, now)
	if !fresh {
		readCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		usage, err := reader.GroupLLMUsageSince(readCtx, profileID, groupID, now.Add(-groupModelQuotaWindow), now)
		cancel()
		if err != nil {
			return verdict
		}
		reading = groupQuotaReading{usage: usage, readAt: now}
		r.groupQuota.put(key, reading)
	}
	verdict.Usage = reading.usage
	if reading.usage.Calls >= callQuota {
		verdict.Exceeded = true
		verdict.Reason = fmt.Sprintf("调用次数 %d/%d", reading.usage.Calls, callQuota)
	}
	return verdict
}

// recordGroupModelQuotaExceeded 把超额写进应用日志。只在读数刚刷新时写一次，
// 靠缓存天然去重：窗口里每条消息都写一行的话，日志里全是同一件事。
func (r *Runtime) recordGroupModelQuotaExceeded(ctx context.Context, event MessageEvent, verdict groupQuotaVerdict) {
	writer := r.appLogWriter()
	if writer == nil {
		return
	}
	_ = writer.AppendLog(ctx, applog.Entry{
		Kind:    applog.KindOperation,
		Level:   applog.LevelInfo,
		Action:  "group_model_quota_exceeded",
		Message: "本群模型额度已用完，暂停花 token 的环节",
		Detail:  verdict.Reason,
		Actor:   oneBotEventActor(event),
		Target:  event.GroupID,
		Metadata: map[string]any{
			"group_id":       event.GroupID,
			"bot_profile_id": event.ProfileID,
			"used_calls":     verdict.Usage.Calls,
			"quota_calls":    verdict.Calls,
			"window":         groupModelQuotaWindow.String(),
		},
	})
}
