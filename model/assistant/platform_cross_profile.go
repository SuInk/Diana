// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"fmt"
	"strings"
)

// isOwner 判断发起人是不是主人。跨机器人操作时 event 已是目标机器人上的会话，
// 主人身份由发起侧核验过后通过 crossProfileOwner 带过来。
func (t *dianaPlatformTool) isOwner(ctx context.Context) bool {
	return t.crossProfileOwner || t.runtime.relationshipPolicy(ctx, t.event).Owner
}

// runCrossProfile 让主人对另一台机器人上的群执行 platform 操作，例如在 QQ 私聊里
// 让机器人去 Telegram 群里禁言某人。
//
// 只给主人：群主和群管理员的权力只在自己所在的那个群里有效，见 authorizeModeration。
// 做法是把工具的「当前会话」换成目标机器人上的那个群，其余门禁（平台支持、机器人
// 是不是管理员、不能对主人和机器人自己下手）原样再走一遍。
func (t *dianaPlatformTool) runCrossProfile(ctx context.Context, profileID string, input map[string]any) (string, error) {
	operation := t.CanonicalOperation(input)
	if !t.runtime.relationshipPolicy(ctx, t.event).Owner {
		err := fmt.Errorf("操作别的机器人那边的群只有机器人主人能用")
		t.runtime.recordPlatformInterfaceOperation(t.event, operation, "member_read_only", false, "", platformRefusal(err))
		return "", err
	}
	if platformSelfOperations[operation] {
		return "", fmt.Errorf("recall 只撤回当前会话里我自己刚发的消息，不能跨机器人用")
	}
	groupID := strings.TrimSpace(configToolString(input, "group_id"))
	var target crossBotTarget
	var err error
	switch {
	case groupID != "":
		target, err = t.runtime.ownedCrossBotTarget(t.event, profileID, EventKindGroup, groupID)
	case operation == platformOpGroupList || operation == platformOpFriendList:
		target, err = t.runtime.ownedCrossBotTarget(t.event, profileID, "", "")
	default:
		err = fmt.Errorf("跨机器人操作需要 group_id：先用 cross_platform 的 groups 查那台机器人在哪些群")
	}
	if err != nil {
		return "", err
	}
	event := target.event
	// 审计日志要记真正的发起人，不是目标机器人上一个不存在的账号。
	event.UserID = strings.TrimSpace(t.event.UserID)
	event.SenderName = t.event.SenderName
	cross := &dianaPlatformTool{runtime: t.runtime, event: event, owner: true, moderator: true, crossProfileOwner: true}
	forwarded := make(map[string]any, len(input))
	for key, value := range input {
		if key != "profile_id" {
			forwarded[key] = value
		}
	}
	return cross.Run(ctx, forwarded)
}
