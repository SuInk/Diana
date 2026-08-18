// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"fmt"
	"sync"
	"time"
)

const (
	// 前几次失败不锁定，留给手滑和记错密码。
	authThrottleFreeAttempts = 5
	authThrottleBaseDelay    = 30 * time.Second
	authThrottleMaxDelay     = 30 * time.Minute
	// 一段时间没有新的失败就把该来源的计数清零。
	authThrottleIdleReset = 30 * time.Minute
	// 全局兜底：挡住换 IP 的分布式撞库。阈值留得很松，单管理员场景正常碰不到。
	authThrottleGlobalWindow   = time.Minute
	authThrottleGlobalLimit    = 50
	authThrottleGlobalCooldown = time.Minute
	// 限制跟踪的来源数量，避免被伪造来源撑爆内存。
	authThrottleMaxTracked = 4096
)

type authThrottleEntry struct {
	failures  int
	lockUntil time.Time
	lastSeen  time.Time
}

// authThrottle 是登录与改密共用的失败退避器。按来源计数，另有一层全局兜底。
// 状态只存在内存里：重启后清零可以接受，爆破本来就打不穿一次重启。
type authThrottle struct {
	mu      sync.Mutex
	entries map[string]*authThrottleEntry

	globalFailures    int
	globalWindowStart time.Time
	globalUntil       time.Time
}

func newAuthThrottle() *authThrottle {
	return &authThrottle{entries: make(map[string]*authThrottleEntry)}
}

// RetryAfter 返回调用方还需等待多久，0 表示可以放行。
func (t *authThrottle) RetryAfter(key string, now time.Time) time.Duration {
	t.mu.Lock()
	defer t.mu.Unlock()
	if wait := t.globalUntil.Sub(now); wait > 0 {
		return wait
	}
	entry := t.entries[key]
	if entry == nil {
		return 0
	}
	if wait := entry.lockUntil.Sub(now); wait > 0 {
		return wait
	}
	return 0
}

// Fail 记一次失败，返回由此产生的锁定时长（0 表示还在免锁次数内）。
func (t *authThrottle) Fail(key string, now time.Time) time.Duration {
	t.mu.Lock()
	defer t.mu.Unlock()

	if now.Sub(t.globalWindowStart) >= authThrottleGlobalWindow {
		t.globalWindowStart = now
		t.globalFailures = 0
	}
	t.globalFailures++
	if t.globalFailures >= authThrottleGlobalLimit {
		t.globalUntil = now.Add(authThrottleGlobalCooldown)
		t.globalFailures = 0
		t.globalWindowStart = now
	}

	t.pruneLocked(now)
	entry := t.entries[key]
	if entry == nil {
		entry = &authThrottleEntry{}
		t.entries[key] = entry
	}
	// 闲置从「锁定结束」而不是「最后一次失败」起算。否则最长那档锁定和闲置
	// 窗口一样长，锁一解除计数就被清零，等于每 30 分钟白送一轮免锁次数。
	if now.Sub(entryIdleSince(entry)) >= authThrottleIdleReset {
		entry.failures = 0
	}
	entry.failures++
	entry.lastSeen = now
	if entry.failures <= authThrottleFreeAttempts {
		return 0
	}
	delay := authThrottleBaseDelay << (entry.failures - authThrottleFreeAttempts - 1)
	if delay > authThrottleMaxDelay || delay <= 0 {
		delay = authThrottleMaxDelay
	}
	entry.lockUntil = now.Add(delay)
	return delay
}

// Reset 在一次成功验证后清空该来源的计数。
func (t *authThrottle) Reset(key string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.entries, key)
}

// pruneLocked 清掉过期条目；真到了上限就把最旧的挤出去。
func (t *authThrottle) pruneLocked(now time.Time) {
	for key, entry := range t.entries {
		if now.Sub(entryIdleSince(entry)) >= authThrottleIdleReset {
			delete(t.entries, key)
		}
	}
	for len(t.entries) >= authThrottleMaxTracked {
		oldestKey := ""
		var oldestSeen time.Time
		for key, entry := range t.entries {
			if oldestKey == "" || entry.lastSeen.Before(oldestSeen) {
				oldestKey, oldestSeen = key, entry.lastSeen
			}
		}
		delete(t.entries, oldestKey)
	}
}

// entryIdleSince 返回该来源「安静下来」的起点：最后一次失败与锁定到期取更晚的。
func entryIdleSince(entry *authThrottleEntry) time.Time {
	if entry.lockUntil.After(entry.lastSeen) {
		return entry.lockUntil
	}
	return entry.lastSeen
}

// formatRetryAfter 把等待时长写成给人看的说法。
func formatRetryAfter(wait time.Duration) string {
	if wait < time.Minute {
		seconds := int(wait.Seconds())
		if seconds < 1 {
			seconds = 1
		}
		return fmt.Sprintf("%d 秒", seconds)
	}
	minutes := int((wait + time.Minute - 1) / time.Minute)
	return fmt.Sprintf("%d 分钟", minutes)
}
