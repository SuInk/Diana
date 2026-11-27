// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"
)

// 跨群能读到什么，按当前发言者能看到什么算：只读他自己也在的那几个群。
//
// 生产 2026-11-27：A 群有人问「ray酱是什么」，提示词里带进了 B 群四条「其他群公共
// 记忆」，全是只在 B 群说话的 Ray 的日常；再问 QQ 号，chat_history 跨群检索的结果已被
// 拦掉，resolved_senders 却照样给出了 Ray 的账号，机器人就把号码贴进了 A 群。提问的人
// 不在 B 群。其他群公共记忆、跨群检索和按昵称找人现在都只在这个范围里取。主人照旧不受限。
const (
	crossGroupVisibilityTTL        = 5 * time.Minute
	crossGroupVisibilityBudget     = 3 * time.Second
	crossGroupVisibilityMaxEntries = 2000
)

// crossGroupView 是当前发言者在跨群读取时能看到的范围。
type crossGroupView struct {
	// restricted=false 表示不受限（主人）。
	restricted bool
	// sessions 是核实过他也在的其他群的会话键，不含当前会话。
	sessions []string
}

// searchSessions 是跨群检索可用的会话：当前会话加上可见的其他群。不受限时为 nil。
func (v crossGroupView) searchSessions(current string) []string {
	if !v.restricted {
		return nil
	}
	return append([]string{current}, v.sessions...)
}

func (v crossGroupView) allowsSession(session string) bool {
	if !v.restricted {
		return true
	}
	for _, item := range v.sessions {
		if item == session {
			return true
		}
	}
	return false
}

type crossGroupVisibilityEntry struct {
	sessions []string
	at       time.Time
}

// crossGroupVisibilityCache 按人记住可见范围，零值可用。每条回复都要组装记忆检索条件，
// 不缓存的话每条都得把他所在的群挨个查一遍成员接口。
type crossGroupVisibilityCache struct {
	mu      sync.Mutex
	entries map[string]crossGroupVisibilityEntry
}

func (c *crossGroupVisibilityCache) get(key string, now time.Time) ([]string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key]
	if !ok || now.Sub(entry.at) > crossGroupVisibilityTTL {
		return nil, false
	}
	return append([]string(nil), entry.sessions...), true
}

func (c *crossGroupVisibilityCache) put(key string, sessions []string, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil || len(c.entries) >= crossGroupVisibilityMaxEntries {
		c.entries = make(map[string]crossGroupVisibilityEntry)
	}
	c.entries[key] = crossGroupVisibilityEntry{sessions: append([]string(nil), sessions...), at: now}
}

// crossGroupVisibility 算出当前发言者能看到的其他群。
//
// 候选是他在发言名录里出现过的群，再用群成员接口逐个核实他现在还在：退了群的不算，
// 从没说过话的群也不算——宁可少带，不把他看不到的群带进来。查询失败按看不到处理。
func (r *Runtime) crossGroupVisibility(ctx context.Context, event MessageEvent) crossGroupView {
	if r.relationshipPolicy(ctx, event).Owner {
		return crossGroupView{}
	}
	view := crossGroupView{restricted: true}
	userID := strings.TrimSpace(event.UserID)
	prefix := groupHistorySessionPrefix(event)
	current := sessionKey(event)
	if userID == "" || prefix == "" {
		return view
	}
	key := strings.Join([]string{strings.TrimSpace(event.ProfileID), prefix, userID}, "\x00")
	if cached, ok := r.crossGroupVisible.get(key, time.Now()); ok {
		view.sessions = removeSession(cached, current)
		return view
	}
	r.mu.RLock()
	store, _ := r.messageStore.(SpeakerSessionStore)
	r.mu.RUnlock()
	if store == nil {
		return view
	}
	// 调用方的 ctx 常常只剩一两秒（记忆检索整体 2 秒）。成员查询被它截断会被当成
	// 「不在」缓存五分钟，所以这里用自己的预算。
	base := context.WithoutCancel(ctx)
	listCtx, cancel := context.WithTimeout(base, speakerLookupTimeout)
	candidates, err := store.ListSpeakerSessions(listCtx, userID, prefix)
	cancel()
	if err != nil {
		return view
	}
	checkCtx, cancel := context.WithTimeout(base, crossGroupVisibilityBudget)
	defer cancel()
	var (
		mu      sync.Mutex
		wg      sync.WaitGroup
		visible []string
	)
	for _, session := range candidates {
		groupID := strings.TrimSpace(strings.TrimPrefix(session, prefix))
		if session == current || groupID == "" || strings.Contains(groupID, ":") {
			continue
		}
		if r.members != nil && IsOneBotPlatform(r.currentPlatform(event)) {
			if _, cached := r.members.lookup(groupID, userID); cached {
				mu.Lock()
				visible = append(visible, session)
				mu.Unlock()
				continue
			}
		}
		wg.Add(1)
		go func() {
			defer recoverGoroutinePanic("cross_group_visibility.check")
			defer wg.Done()
			member, err := r.getGroupMemberInfoForEvent(checkCtx, event, groupID, userID)
			if err != nil || strings.TrimSpace(member.UserID) != userID {
				return
			}
			mu.Lock()
			visible = append(visible, session)
			mu.Unlock()
		}()
	}
	wg.Wait()
	sort.Strings(visible)
	r.crossGroupVisible.put(key, visible, time.Now())
	view.sessions = visible
	return view
}

func removeSession(sessions []string, session string) []string {
	out := make([]string, 0, len(sessions))
	for _, item := range sessions {
		if item != session {
			out = append(out, item)
		}
	}
	return out
}
