// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"fmt"
	"strings"
)

// GroupInfo 是控制台展示一个群所需的最小信息。
type GroupInfo struct {
	GroupID     string
	GroupName   string
	MemberCount int
}

// GroupInfoChannel 由「知道自己在哪个群里、但没有『列出全部群』接口」的通道实现。
//
// OneBot 有 get_group_list，一次就能拿到全量；Telegram 的 Bot API 没有对应接口，
// 但只要知道群号就能用 getChat 问到这个群此刻的名字。控制台先从本地事件得到群号，
// 再用这个接口补齐名字，就不必等群里下一条消息才显示得出名称，改过名的群也能立刻
// 跟上。
type GroupInfoChannel interface {
	GroupInfo(ctx context.Context, groupID string) (GroupInfo, error)
}

// GroupInfo 用 getChat 查询群的当前信息。
func (c *TelegramChannel) GroupInfo(ctx context.Context, groupID string) (GroupInfo, error) {
	groupID = strings.TrimSpace(groupID)
	if groupID == "" {
		return GroupInfo{}, fmt.Errorf("telegram: group id is required")
	}
	data, err := c.CallAPI(ctx, "getChat", map[string]any{"chat_id": groupID})
	if err != nil {
		return GroupInfo{}, err
	}
	info := GroupInfo{GroupID: groupID}
	// getChat 的响应外面还套一层 result，历史上也出现过直接返回内容的形态，
	// 两种都认。
	fields := data
	if nested, ok := data["result"].(map[string]any); ok {
		fields = nested
	}
	info.GroupName = strings.TrimSpace(stringFromAny(fields["title"]))
	return info, nil
}

// GroupInfoForProfile 找到这台机器人对应的通道，问它某个群的当前信息。
// 通道不支持（比如 OneBot 走的是 get_group_list 那条路）时返回 false。
func (r *Runtime) GroupInfoForProfile(ctx context.Context, profileID, groupID string) (GroupInfo, bool) {
	if r == nil {
		return GroupInfo{}, false
	}
	r.mu.RLock()
	channel := r.channel
	r.mu.RUnlock()
	if channel == nil {
		return GroupInfo{}, false
	}
	target := channel
	if multi, ok := channel.(*MultiChannel); ok {
		binding, err := multi.bindingFor(strings.TrimSpace(profileID), "")
		if err != nil {
			return GroupInfo{}, false
		}
		target = binding.Channel
	}
	provider, ok := target.(GroupInfoChannel)
	if !ok {
		return GroupInfo{}, false
	}
	info, err := provider.GroupInfo(ctx, groupID)
	if err != nil || strings.TrimSpace(info.GroupName) == "" {
		return GroupInfo{}, false
	}
	return info, true
}
