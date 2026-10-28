// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// maxPlatformFriendListItems 和群列表同一个道理：几千个好友全倒进工具结果只会挤占上下文。
const maxPlatformFriendListItems = 200

type dianaPlatformFriendItem struct {
	UserID   string `json:"user_id"`
	Nickname string `json:"nickname,omitempty"`
	Remark   string `json:"remark,omitempty"`
}

// runOwnerOperation 处理只认机器人主人的操作。它们不属于哪个群的管理权，所以不看
// 发言人的群身份，私聊里也能用；退群可以指定别的群。
func (t *dianaPlatformTool) runOwnerOperation(ctx context.Context, input map[string]any, operation string, owner bool, access string) (string, error) {
	refuse := func(target string, err error) (string, error) {
		t.runtime.recordPlatformInterfaceOperation(t.event, operation, access, owner, target, platformRefusal(err))
		return "", err
	}
	if !owner {
		if operation == platformOpLeaveGroup {
			return refuse("", fmt.Errorf("让机器人退群只有机器人主人能用"))
		}
		return refuse("", fmt.Errorf("机器人的好友列表只有主人能看"))
	}
	platform := t.runtime.currentPlatform(t.event)
	if !platformSupportsOperation(platform, operation) {
		if operation == platformOpFriendList {
			return refuse("", fmt.Errorf("当前平台的接口不提供好友列表，暂不支持此操作"))
		}
		return refuse("", fmt.Errorf("当前平台暂不支持此操作"))
	}
	callCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	switch operation {
	case platformOpLeaveGroup:
		groupID := t.resolveGroupID(input)
		if groupID == "" {
			return refuse("", fmt.Errorf("leave_group 需要群 ID：请在群里调用或指定 group_id"))
		}
		var err error
		if NormalizePlatformID(platform) == PlatformTelegram {
			_, err = t.runtime.callPlatformAPIForEvent(callCtx, t.event, "leaveChat", map[string]any{"chat_id": groupID})
		} else {
			// is_dismiss 固定 false：机器人是群主时 QQ 只能解散不能退，解散整个群不该是一句话的事。
			_, err = t.runtime.callOneBotAPIForEvent(callCtx, t.event, "set_group_leave", map[string]any{"group_id": oneBotIDParam(groupID), "is_dismiss": false})
		}
		if err != nil {
			wrapped := fmt.Errorf("%s 执行失败：%w", operation, err)
			t.runtime.recordPlatformInterfaceOperation(t.event, operation, access, owner, groupID, wrapped)
			return "", wrapped
		}
		t.runtime.recordPlatformInterfaceOperation(t.event, operation, access, owner, groupID, nil)
		message := "已退出该群。"
		if groupID == strings.TrimSpace(t.event.GroupID) {
			message = "已退出当前这个群，之后在这里发不出消息了。"
		}
		return t.marshal(operation, access, map[string]any{"group_id": groupID, "message": message})
	case platformOpFriendList:
		data, err := t.runtime.callOneBotAPIForEvent(callCtx, t.event, "get_friend_list", map[string]any{"no_cache": true})
		if err != nil {
			t.runtime.recordPlatformInterfaceOperation(t.event, operation, access, owner, "", err)
			return "", err
		}
		raw, ok := structuredGroupListItems(data)
		if !ok {
			err := fmt.Errorf("好友列表返回格式无法识别")
			t.runtime.recordPlatformInterfaceOperation(t.event, operation, access, owner, "", err)
			return "", err
		}
		friends := make([]dianaPlatformFriendItem, 0, len(raw))
		for _, value := range raw {
			item, ok := value.(map[string]any)
			if !ok {
				continue
			}
			id := strings.TrimSpace(stringFromAny(item["user_id"]))
			if id == "" {
				continue
			}
			friends = append(friends, dianaPlatformFriendItem{
				UserID:   id,
				Nickname: strings.TrimSpace(stringFromAny(item["nickname"])),
				Remark:   strings.TrimSpace(stringFromAny(item["remark"])),
			})
		}
		t.runtime.recordPlatformInterfaceOperation(t.event, operation, access, owner, "", nil)
		payload := map[string]any{"friend_count": len(friends)}
		if len(friends) > maxPlatformFriendListItems {
			payload["friends_truncated"] = true
			friends = friends[:maxPlatformFriendListItems]
		}
		payload["friends"] = friends
		return t.marshal(operation, access, payload)
	}
	return "", fmt.Errorf("operation 不支持：%s", operation)
}
