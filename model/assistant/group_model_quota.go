// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/SuInk/diana/model/applog"
)

// groupModelQuotaWindow 是额度的统计窗口：滚动 5 小时，和上游按 token 计费的
// 套餐窗口一致。
const groupModelQuotaWindow = 5 * time.Hour

// groupModelQuotaCacheTTL 是用量读数的缓存时长。每条消息都去扫一遍日志太贵，
// 而额度本来就是个粗口径的闸门，半分钟的滞后不影响结论。
const groupModelQuotaCacheTTL = 30 * time.Second

type groupQuotaReading struct {
	tokens  int64
	readAt  time.Time
	overrun bool
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

// groupModelQuotaExceeded 判断这个群是不是已经用超了本窗口的额度。
//
// 读不到用量时一律放行：额度是省钱用的，不该因为日志存储不可用就让整个群哑掉。
func (r *Runtime) groupModelQuotaExceeded(ctx context.Context, event MessageEvent) (bool, int64, int64) {
	if r == nil || event.Kind != EventKindGroup {
		return false, 0, 0
	}
	groupID := strings.TrimSpace(event.GroupID)
	if groupID == "" {
		return false, 0, 0
	}
	groupCfg, ok := r.groupConfigForEvent(event)
	if !ok || groupCfg.ModelTokenQuota <= 0 {
		return false, 0, 0
	}
	// 主人不受限：额度用完之后改配置、查用量这些还得靠主人，锁死自己没有道理。
	if r.effectiveConfigForEvent(event).IsOwnerEvent(event) {
		return false, 0, groupCfg.ModelTokenQuota
	}
	reader, ok := r.appLogWriter().(applog.GroupUsageReader)
	if !ok || reader == nil {
		return false, 0, groupCfg.ModelTokenQuota
	}
	profileID := strings.TrimSpace(r.eventProfileID(event))
	key := profileID + "\x00" + groupID
	now := time.Now()
	if reading, fresh := r.groupQuota.get(key, now); fresh {
		return reading.overrun, reading.tokens, groupCfg.ModelTokenQuota
	}
	readCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	tokens, err := reader.GroupLLMTokensSince(readCtx, profileID, groupID, now.Add(-groupModelQuotaWindow), now)
	cancel()
	if err != nil {
		return false, 0, groupCfg.ModelTokenQuota
	}
	overrun := tokens >= groupCfg.ModelTokenQuota
	r.groupQuota.put(key, groupQuotaReading{tokens: tokens, readAt: now, overrun: overrun})
	return overrun, tokens, groupCfg.ModelTokenQuota
}

// recordGroupModelQuotaExceeded 把超额写进应用日志。只在读数刚刷新时写一次，
// 靠缓存天然去重：窗口里每条消息都写一行的话，日志里全是同一件事。
func (r *Runtime) recordGroupModelQuotaExceeded(ctx context.Context, event MessageEvent, used, quota int64) {
	writer := r.appLogWriter()
	if writer == nil {
		return
	}
	_ = writer.AppendLog(ctx, applog.Entry{
		Kind:    applog.KindOperation,
		Level:   applog.LevelInfo,
		Action:  "group_model_quota_exceeded",
		Message: "本群模型额度已用完，暂停花 token 的环节",
		Actor:   oneBotEventActor(event),
		Target:  event.GroupID,
		Metadata: map[string]any{
			"group_id":       event.GroupID,
			"bot_profile_id": event.ProfileID,
			"used_tokens":    used,
			"quota_tokens":   quota,
			"window":         groupModelQuotaWindow.String(),
		},
	})
}
