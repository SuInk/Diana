// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"strconv"
	"strings"
	"time"
)

// telegramMentionLookupTimeout 限制发送前现查一个成员的时间。查不到也只是退回 @<id>，
// 不值得为它把整条回复拖住。
const telegramMentionLookupTimeout = 3 * time.Second

// mentionIdentityChannel 给出站提及补「昵称 + 用户名」。会话内存里的近期消息覆盖不到
// 的人（提醒投递、很久没说话的群友）原先只能显示成 @<数字ID>。
type mentionIdentityChannel interface {
	MentionIdentity(ctx context.Context, groupID, userID string) (name, username string, ok bool)
}

// MentionIdentity 先查通道记住的群成员（每条群消息都会更新，不走网络），没有再用
// getChatMember 现查一次。私聊没有群可查，直接返回 false。
func (c *TelegramChannel) MentionIdentity(ctx context.Context, groupID, userID string) (string, string, bool) {
	groupID = strings.TrimSpace(groupID)
	id, err := strconv.ParseInt(strings.TrimSpace(userID), 10, 64)
	if groupID == "" || err != nil || id <= 0 {
		return "", "", false
	}
	c.mu.RLock()
	known, found := c.knownMembers[groupID][id]
	c.mu.RUnlock()
	if found {
		return telegramDisplayName(&known.User), known.User.Username, true
	}
	ctx, cancel := context.WithTimeout(ctx, telegramMentionLookupTimeout)
	defer cancel()
	info, err := c.GroupMember(ctx, groupID, userID)
	if err != nil {
		return "", "", false
	}
	return info.Nickname, info.Username, true
}
