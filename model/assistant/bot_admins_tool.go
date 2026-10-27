// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// 主人在聊天里增减管理员。只有主人能用：管理员拿到的是全部管理权限，让管理员再
// 去加管理员，等于主人以外的人也能把权限一路发下去。

const botAdminsToolName = "bot_admins"

type botAdminsSaver interface {
	SaveAdminIDs(profileID string, ids []string) error
}

type dianaBotAdminsTool struct {
	runtime *Runtime
	event   MessageEvent
}

func (*dianaBotAdminsTool) Name() string { return botAdminsToolName }
func (*dianaBotAdminsTool) Description() string {
	return "主人增减当前机器人的管理员：add 设为管理员，remove 取消，list 查看名单。管理员拥有宿主命令、插件开关、配置和扩展管理等全部管理权限，但不是主人，也不能增减管理员。仅在主人明确要求时调用，目标按账号 ID 指定或引用对方消息，不按昵称猜。"
}
func (*dianaBotAdminsTool) InputSchema() map[string]any {
	return toolObjectSchema([]string{"operation"}, map[string]any{
		"operation": toolEnumParam("add 设为管理员；remove 取消；list 查看", "add", "remove", "list"),
		"user_id":   toolStringParam("目标账号 ID，默认被引用消息的发送者；Telegram 可写 @用户名"),
	})
}

func (t *dianaBotAdminsTool) Run(ctx context.Context, input map[string]any) (string, error) {
	if t == nil || t.runtime == nil {
		return "", fmt.Errorf("机器人运行时不可用")
	}
	r := t.runtime
	base := r.effectiveConfigForEvent(t.event)
	if !base.IsOwnerEvent(t.event) && !isAdminChatRun(ctx) {
		return "", fmt.Errorf("只有机器人主人可以增减管理员，管理员也不行")
	}
	op := configToolString(input, "operation")
	if op == "list" {
		return botAdminsJSON(op, "", base.AdminIDs)
	}
	if op != "add" && op != "remove" {
		return "", fmt.Errorf("operation 必须为 add、remove 或 list")
	}
	id := strings.TrimSpace(configToolString(input, "user_id"))
	if id == "" && t.event.Quoted != nil {
		id = strings.TrimSpace(t.event.Quoted.UserID)
	}
	if id == "" {
		return "", fmt.Errorf("请指定目标账号 ID 或引用对方的消息")
	}
	if err := validateAdminID(id); err != nil {
		return "", err
	}
	if op == "add" {
		if id == strings.TrimSpace(base.OwnerID) {
			return "", fmt.Errorf("这个账号是主人，本来就有全部权限，不用设为管理员")
		}
		if id == strings.TrimSpace(base.BotAccount) || id == strings.TrimSpace(t.event.SelfID) {
			return "", fmt.Errorf("不能把机器人自己设为管理员")
		}
	}
	ids, err := r.updateAdminID(base.ID, base.OwnerID, id, op == "add")
	if err != nil {
		return "", err
	}
	return botAdminsJSON(op, id, ids)
}

// validateAdminID 只收账号 ID 或 Telegram 用户名：昵称会重名，也会被人改成别人的名字。
func validateAdminID(id string) error {
	value := strings.TrimPrefix(id, "@")
	if value == "" || len(value) > 64 {
		return fmt.Errorf("目标必须是账号 ID")
	}
	for _, c := range value {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '_' || c == '-') {
			return fmt.Errorf("目标必须是账号 ID，不能使用昵称")
		}
	}
	return nil
}

// updateAdminID 改名单并落盘。保存前按落盘那一刻的配置再核一次主人：入口判过之后
// 主人可能已经在 WebUI 里换掉了。
func (r *Runtime) updateAdminID(profileID, ownerID, userID string, add bool) ([]string, error) {
	r.mu.RLock()
	saver, ok := r.configSaver.(botAdminsSaver)
	r.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("当前未接入可确认持久化结果的机器人配置存储")
	}
	var ids []string
	_, err := r.commitProfileChange(profileID, func(profile *BotConfig) error {
		if strings.TrimSpace(profile.OwnerID) != strings.TrimSpace(ownerID) {
			return fmt.Errorf("机器人主人已变更，请重新确认后再操作")
		}
		ids = append([]string(nil), profile.AdminIDs...)
		if add {
			ids = append(ids, userID)
		} else {
			ids = slices.DeleteFunc(ids, func(id string) bool { return strings.TrimSpace(id) == userID })
		}
		ids = normalizeAdminIDs(ids, profile.OwnerID)
		profile.AdminIDs = append([]string(nil), ids...)
		return nil
	}, func(BotConfig) error {
		return saver.SaveAdminIDs(profileID, ids)
	})
	if err != nil {
		return nil, err
	}
	return ids, nil
}

func botAdminsJSON(op, id string, ids []string) (string, error) {
	if ids == nil {
		ids = []string{}
	}
	body, err := json.Marshal(map[string]any{"ok": true, "operation": op, "user_id": id, "admin_ids": ids})
	return string(body), err
}
