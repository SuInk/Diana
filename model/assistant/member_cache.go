// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

const (
	// 群等级按群内活跃度累积，变化极慢，缓存久一点也不会失真。
	defaultMemberCacheTTL = 10 * time.Minute
	// 单机可能同时在很多大群里，加个上限避免成员表把内存吃满。
	defaultMemberCacheMaxEntries = 5000
	// 兜底查询独立超时，绝不能拖住消息主链路。
	memberFetchTimeout = 2 * time.Second
)

// memberInfo 是一个群成员在某个群里的身份快照。
type memberInfo struct {
	Level int
	Role  string
	Title string
	At    time.Time
}

// memberCache 缓存群成员的群等级与身份。
//
// 取值分三层，绝大多数情况一次 API 都不发：
//  1. 消息事件自带 level 时直接被动写入（免费，NapCat 等实现走的就是这条）；
//  2. 命中缓存；
//  3. 都没有才走 get_group_member_info 兜底，异步回填，当前这条消息按
//     调用方的 fail-open 策略放行。
//
// 只存内存，不落库。靠第 1 层被动记录，重启后几分钟就能靠日常聊天重建；
// 而现有的 app_state 是整块覆写的 JSON blob，拿来存成员表会导致每次更新
// 重写全部数据，不能用。
type memberCache struct {
	mu         sync.RWMutex
	entries    map[string]memberInfo
	inflight   map[string]struct{}
	ttl        time.Duration
	maxEntries int

	// call 为 nil 时只走被动记录，不做兜底查询（测试和未连接场景）。
	call oneBotEventAPICaller
	// now 便于测试注入时钟。
	now func() time.Time
}

type oneBotEventAPICaller func(context.Context, MessageEvent, string, map[string]any) (map[string]any, error)

func newMemberCache(call oneBotAPICaller) *memberCache {
	if call == nil {
		return newMemberCacheForEvent(nil)
	}
	return newMemberCacheForEvent(func(ctx context.Context, _ MessageEvent, action string, params map[string]any) (map[string]any, error) {
		return call(ctx, action, params)
	})
}

func newMemberCacheForEvent(call oneBotEventAPICaller) *memberCache {
	return &memberCache{
		entries:    make(map[string]memberInfo),
		inflight:   make(map[string]struct{}),
		ttl:        defaultMemberCacheTTL,
		maxEntries: defaultMemberCacheMaxEntries,
		call:       call,
		now:        time.Now,
	}
}

// memberCacheKey 必须同时含群号——群等级是每个群独立累积的，
// 同一个账号在 A 群 Lv.6、B 群 Lv.1，只按账号缓存会让等级门槛失效。
func memberCacheKey(groupID string, userID string) string {
	return groupID + ":" + userID
}

func (c *memberCache) clock() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

// Observe 把消息事件里自带的成员信息记进缓存。这是零成本的一层，
// 群里正常聊天就能把缓存填满。
func (c *memberCache) Observe(event MessageEvent) {
	if c == nil || event.Kind != EventKindGroup {
		return
	}
	groupID := strings.TrimSpace(event.GroupID)
	userID := strings.TrimSpace(event.UserID)
	if groupID == "" || userID == "" {
		return
	}
	// 等级和身份都没有就没什么可记的，不要用空值把已有缓存冲掉。
	if event.SenderLevel <= 0 && strings.TrimSpace(event.SenderRole) == "" {
		return
	}
	c.store(groupID, userID, memberInfo{
		Level: event.SenderLevel,
		Role:  strings.TrimSpace(event.SenderRole),
		Title: strings.TrimSpace(event.SenderTitle),
		At:    c.clock(),
	})
}

func (c *memberCache) store(groupID string, userID string, info memberInfo) {
	key := memberCacheKey(groupID, userID)
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.entries) >= c.maxEntries {
		if _, exists := c.entries[key]; !exists {
			c.evictLocked()
		}
	}
	c.entries[key] = info
}

// evictLocked 先清过期项，还超限就随意丢一些。这是软缓存，
// 淘汰精度不重要，丢了下次被动记录或兜底查询会补回来。
func (c *memberCache) evictLocked() {
	now := c.clock()
	for key, info := range c.entries {
		if now.Sub(info.At) > c.ttl {
			delete(c.entries, key)
		}
	}
	for key := range c.entries {
		if len(c.entries) < c.maxEntries {
			return
		}
		delete(c.entries, key)
	}
}

// lookup 读缓存，过期视为未命中。
func (c *memberCache) lookup(groupID string, userID string) (memberInfo, bool) {
	key := memberCacheKey(groupID, userID)
	c.mu.RLock()
	info, ok := c.entries[key]
	ttl := c.ttl
	c.mu.RUnlock()
	if !ok {
		return memberInfo{}, false
	}
	if c.clock().Sub(info.At) > ttl {
		return memberInfo{}, false
	}
	return info, true
}

// LevelFor 返回群等级，以及这个值是否可信。
//
// 返回 false 表示「查不到」而不是「等级为 0」——两者的区别很关键：
// OneBot 实现之间差异很大，把查不到当成 0 去拒绝会让整群失联。
// 调用方应按 LevelUnknownPolicy 决定放行还是拦截，默认放行。
func (c *memberCache) LevelFor(event MessageEvent) (int, bool) {
	if c == nil || event.Kind != EventKindGroup {
		return 0, false
	}
	groupID := strings.TrimSpace(event.GroupID)
	userID := strings.TrimSpace(event.UserID)
	if groupID == "" || userID == "" {
		return 0, false
	}
	if event.SenderLevel > 0 {
		c.Observe(event)
		return event.SenderLevel, true
	}
	if info, ok := c.lookup(groupID, userID); ok && info.Level > 0 {
		return info.Level, true
	}
	// 异步回填，本条消息不等待。
	c.refreshAsync(event)
	return 0, false
}

// refreshAsync 后台查一次 get_group_member_info。
//
// 刻意不复用消息的 ctx：消息处理完 ctx 就被取消了，而这是 fire-and-forget
// 的回填，必须有自己独立的生命周期和超时。
func (c *memberCache) refreshAsync(event MessageEvent) {
	if c == nil || c.call == nil {
		return
	}
	groupID := strings.TrimSpace(event.GroupID)
	userID := strings.TrimSpace(event.UserID)
	key := memberCacheKey(groupID, userID)
	c.mu.Lock()
	if _, busy := c.inflight[key]; busy {
		// 群里刷屏时同一个人会连续触发，去重避免打出一串重复请求。
		c.mu.Unlock()
		return
	}
	c.inflight[key] = struct{}{}
	c.mu.Unlock()

	go func() {
		defer func() {
			c.mu.Lock()
			delete(c.inflight, key)
			c.mu.Unlock()
		}()
		ctx, cancel := context.WithTimeout(context.Background(), memberFetchTimeout)
		defer cancel()
		data, err := c.call(ctx, event, "get_group_member_info", map[string]any{
			"group_id": groupID,
			"user_id":  userID,
			"no_cache": false,
		})
		if err != nil || data == nil {
			return
		}
		info := memberInfo{
			Level: parseGroupLevel(data["level"]),
			Role:  strings.ToLower(strings.TrimSpace(fmt.Sprint(data["role"]))),
			Title: strings.TrimSpace(fmt.Sprint(data["title"])),
			At:    c.clock(),
		}
		if info.Role == "<nil>" {
			info.Role = ""
		}
		if info.Title == "<nil>" {
			info.Title = ""
		}
		if info.Level <= 0 && info.Role == "" {
			// 这个实现就是不给等级，记空值只会让下次继续白跑一趟。
			return
		}
		c.store(groupID, userID, info)
	}()
}
