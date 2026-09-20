// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"strings"
	"time"
)

// 好友名册：判断某个 QQ 号是不是这台机器人的好友。
//
// 这件事只有一个用处，但那个用处决定了消息发不发得出去：QQ 给非好友发私聊要走
// 临时会话，send_private_msg 必须带上共同群的 group_id；对好友反过来，带了
// group_id 会把消息塞进临时会话那个独立的对话框。所以「是不是好友」不能猜，
// 也不能等发送失败再试另一种——失败一次就已经是用户看得见的沉默了。
//
// get_friend_list 一次返回全量，逐个 user_id 去问反而更贵，所以整份缓存下来。
// 缓存会过期，但两种过期的代价都不致命：刚加上的好友被当成非好友，消息走临时
// 会话仍然送达；刚被删的好友被当成好友，普通私聊失败并把平台的原话报回去。
const (
	oneBotFriendListTTL     = 5 * time.Minute
	oneBotFriendListTimeout = 6 * time.Second
)

type oneBotFriendRoster struct {
	fetchedAt time.Time
	ids       map[string]bool
}

// oneBotFriendship 报告 target 是不是这台机器人的好友。
//
// known 为 false 表示问不出来：平台不是 OneBot、接口不支持、调用失败，或者响应
// 不是认得出的名册形状。调用方据此走「不确定」的那条路，而不是把问不出来当成
// 「不是好友」——后者会让所有好友的私聊都被推去走临时会话。
func (r *Runtime) oneBotFriendship(ctx context.Context, event MessageEvent, target string) (friend bool, known bool) {
	target = strings.TrimSpace(target)
	if r == nil || target == "" {
		return false, false
	}
	if !IsOneBotPlatform(firstNonEmpty(NormalizePlatformID(event.Platform), r.effectiveConfigForEvent(event).Platform)) {
		return false, false
	}
	roster, ok := r.oneBotFriendRoster(ctx, event)
	if !ok {
		return false, false
	}
	return roster[target], true
}

// oneBotFriendRoster 取这台机器人的好友 ID 集合，命中缓存就不再问平台。
func (r *Runtime) oneBotFriendRoster(ctx context.Context, event MessageEvent) (map[string]bool, bool) {
	key := r.oneBotFriendRosterKey(event)
	now := time.Now()
	r.friendRosterMu.Lock()
	cached, hit := r.friendRosters[key]
	r.friendRosterMu.Unlock()
	if hit && now.Sub(cached.fetchedAt) < oneBotFriendListTTL {
		return cached.ids, true
	}
	callCtx, cancel := context.WithTimeout(ctx, oneBotFriendListTimeout)
	defer cancel()
	data, err := r.callOneBotAPIForEvent(callCtx, event, "get_friend_list", map[string]any{})
	if err != nil {
		// 问不出来时宁可用过期的名册，也不要把所有人都当成身份不明：名册变动
		// 是低频事件，而「不确定」会让每一条私聊都改走临时会话。
		if hit {
			return cached.ids, true
		}
		return nil, false
	}
	items, recognized := structuredGroupListItems(data)
	if !recognized {
		return nil, false
	}
	// 空名册更可能是账号异常的产物，不能据此断定「谁都不是好友」。
	if len(items) == 0 {
		if hit {
			return cached.ids, true
		}
		return nil, false
	}
	ids := make(map[string]bool, len(items))
	for _, raw := range items {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if id := strings.TrimSpace(stringFromAny(item["user_id"])); id != "" {
			ids[id] = true
		}
	}
	if len(ids) == 0 {
		return nil, false
	}
	r.friendRosterMu.Lock()
	if r.friendRosters == nil {
		r.friendRosters = map[string]oneBotFriendRoster{}
	}
	r.friendRosters[key] = oneBotFriendRoster{fetchedAt: now, ids: ids}
	r.friendRosterMu.Unlock()
	return ids, true
}

// oneBotFriendRosterKey 按账号分桶：一台 Diana 上可以挂着多个机器人，好友名册
// 是各自的。
func (r *Runtime) oneBotFriendRosterKey(event MessageEvent) string {
	profile := strings.TrimSpace(event.ProfileID)
	platform := NormalizePlatformID(firstNonEmpty(event.Platform, r.effectiveConfigForEvent(event).Platform))
	return profile + "|" + platform
}

// forgetOneBotFriendRoster 丢掉缓存的好友名册，下一次重新问平台。刚加上好友时
// 必须调用它：否则缓存还说「不是好友」，补发的私聊又会被推去走临时会话。
func (r *Runtime) forgetOneBotFriendRoster(event MessageEvent) {
	key := r.oneBotFriendRosterKey(event)
	r.friendRosterMu.Lock()
	delete(r.friendRosters, key)
	r.friendRosterMu.Unlock()
}
