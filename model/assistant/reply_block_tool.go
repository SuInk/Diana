// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/SuInk/diana/model/applog"
)

const replyBlockToolName = "diana.reply_block"

// replyBlockSaver 只改机器人级门禁里的屏蔽名单，和 botMarkersSaver 一样窄：
// 聊天里下的屏蔽指令不该有能力覆写整份门禁。
type replyBlockSaver interface {
	SaveBlockedUsers(profileID string, userIDs []string) error
}

type dianaReplyBlockTool struct {
	runtime *Runtime
	event   MessageEvent
}

func newDianaReplyBlockTool(r *Runtime, event MessageEvent) *dianaReplyBlockTool {
	return &dianaReplyBlockTool{runtime: r, event: event}
}

func (*dianaReplyBlockTool) Name() string { return replyBlockToolName }

func (*dianaReplyBlockTool) Description() string {
	return "屏蔽某个人：被屏蔽的人之后说什么都不回复，一直到解除为止，没有时限。block 屏蔽，unblock 解除，list 看当前名单。scope=group 只管当前群，主人或实时核验的群管理员可改；scope=bot 对本机所有群和私聊生效，只有主人能改。只认账号 ID，不认昵称；不操作平台禁言，也不撤回消息。"
}

func (*dianaReplyBlockTool) InputSchema() map[string]any {
	return toolObjectSchema([]string{"operation"}, map[string]any{
		"operation": toolEnumParam("block 加入屏蔽名单，unblock 移出，list 只读当前名单；只有保存成功才报告已生效。", "block", "unblock", "list"),
		"scope":     toolEnumParam("群聊默认 group，私聊默认 bot。group 只影响当前群，bot 对本机所有群和私聊生效且仅主人可改；不能指定别的机器人或别的群。", "group", "bot"),
		"user_id":   toolStringParam("目标账号 ID，必须取自消息里 @ 的结构化信息、被引用消息的发送者，或 diana.group 查到的成员 ID。不要按昵称猜 ID，拿不准就先查成员或问清楚。省略时用当前引用消息的发送者；list 不需要。"),
	})
}

func (t *dianaReplyBlockTool) Run(ctx context.Context, input map[string]any) (string, error) {
	if t == nil || t.runtime == nil {
		return "", fmt.Errorf("机器人运行时不可用")
	}
	op := strings.TrimSpace(configToolString(input, "operation"))
	if op != "block" && op != "unblock" && op != "list" {
		return "", fmt.Errorf("operation 必须为 block、unblock 或 list")
	}
	scope := strings.TrimSpace(configToolString(input, "scope"))
	if scope == "" {
		if t.event.Kind == EventKindGroup {
			scope = "group"
		} else {
			scope = "bot"
		}
	}
	if scope != "group" && scope != "bot" {
		return "", fmt.Errorf("scope 必须为 group 或 bot")
	}
	base, err := t.runtime.modelConfigForEvent(t.event)
	if err != nil {
		return "", err
	}
	role, group := "bot_owner", GroupConfig{}
	if scope == "group" {
		if t.event.Kind != EventKindGroup || strings.TrimSpace(t.event.GroupID) == "" {
			return "", fmt.Errorf("群级屏蔽只能在目标群内操作")
		}
		// 和 diana.bot_config 同一套核验：非主人一律清掉上报的身份，逼着走一次
		// 实时成员查询，不拿入站事件里那个可以伪造的 sender_role 当权限凭据。
		authEvent := t.event
		if !base.IsOwnerEvent(authEvent) {
			authEvent.SenderRole = ""
		}
		if role, err = t.runtime.canConfigureGroup(ctx, authEvent); err != nil {
			return "", err
		}
		var ok bool
		if group, ok = t.runtime.groupConfigForEvent(t.event); !ok {
			group = DefaultGroupConfig(t.event.GroupID, base)
		}
		group.BotProfileID = base.ID
	} else if !base.IsOwnerEvent(t.event) {
		return "", fmt.Errorf("只有机器人主人可以修改机器人级屏蔽名单")
	}

	// current 是这一层自己那份名单，inherited 是从机器人级并下来的。
	// 两份必须分开报：并下来的那些在本群解除不了，混在一起报会让模型
	// 一边说「已解除」一边人还被屏蔽着。
	var current, inherited []string
	if scope == "group" {
		if group.ReplyGate != nil {
			current = append([]string(nil), group.ReplyGate.BlockedUsers...)
		}
		if base.ReplyGate != nil {
			inherited = append([]string(nil), base.ReplyGate.BlockedUsers...)
		}
	} else if base.ReplyGate != nil {
		current = append([]string(nil), base.ReplyGate.BlockedUsers...)
	}

	target := strings.TrimSpace(configToolString(input, "user_id"))
	if op != "list" {
		if target == "" && t.event.Quoted != nil {
			target = strings.TrimSpace(t.event.Quoted.UserID)
		}
		if target == "" {
			return "", fmt.Errorf("请指定目标账号 ID，或引用对方的消息")
		}
		for _, char := range target {
			if !(char >= '0' && char <= '9' || char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char == '_' || char == '-') {
				return "", fmt.Errorf("目标必须是账号 ID，不能使用昵称")
			}
		}
	}

	next, changed, message := current, false, ""
	switch op {
	case "list":
		message = "已读取当前屏蔽名单"
	case "block":
		// 主人和本机账号一律挡在写入之前。屏蔽主人等于把唯一能改回来的人关在门外；
		// 屏蔽本机账号会让机器人不认自己的消息，回复链路上到处都要绕开这一条。
		// （门禁判断里主人另有豁免，所以名单里就算混进主人的 ID 也不会真的生效，
		// 但名单本身会一直摆在那儿误导人。）
		if owner := strings.TrimSpace(base.OwnerIDForEvent(t.event)); target == owner || target == strings.TrimSpace(base.OwnerID) {
			return "", fmt.Errorf("不能屏蔽机器人主人")
		}
		if target == strings.TrimSpace(base.BotAccount) || target == strings.TrimSpace(t.event.SelfID) {
			return "", fmt.Errorf("不能屏蔽机器人自己的账号")
		}
		switch {
		case containsString(current, target):
			message = "该用户此前已在" + replyBlockScopeWord(scope) + "屏蔽名单里，名单没有变动"
		case containsString(inherited, target):
			message = "该用户已被机器人级屏蔽，本群本来就收不到他的回复，名单没有变动"
		default:
			next, changed = append(append([]string(nil), current...), target), true
		}
	case "unblock":
		switch {
		case containsString(current, target):
			next = slices.DeleteFunc(append([]string(nil), current...), func(id string) bool { return id == target })
			changed = true
		case containsString(inherited, target):
			return "", fmt.Errorf("该用户是在机器人级屏蔽的，本群解除不了；请机器人主人用 scope=bot 解除")
		default:
			message = "该用户本来就不在" + replyBlockScopeWord(scope) + "屏蔽名单里，名单没有变动"
		}
	}

	if changed {
		if scope == "group" {
			gate := group.ReplyGate
			if gate == nil {
				gate = base.ReplyGate.InheritedThresholds()
			}
			group.ReplyGate = gate.WithBlockedUsers(next)
			t.runtime.mu.RLock()
			writer, ok := t.runtime.groupConfigs.(GroupConfigWriter)
			t.runtime.mu.RUnlock()
			if !ok {
				return "", fmt.Errorf("当前未接入可写的群配置存储")
			}
			saved, err := writer.SaveGroupConfig(group, base)
			if err != nil {
				return "", err
			}
			if saved.ReplyGate != nil {
				next = append([]string(nil), saved.ReplyGate.BlockedUsers...)
			}
		} else if err := t.runtime.saveBotBlockedUsers(base, t.event.UserID, next); err != nil {
			return "", err
		}
		// 走到这里保存已经落库并返回成功，下面这句话才敢说。
		if op == "block" {
			message = "已屏蔽该用户，之后在" + replyBlockScopeWord(scope) + "不再回复他，直到解除"
		} else {
			message = "已解除屏蔽，之后会正常回复该用户"
		}
		t.runtime.recordReplyBlockChanged(ctx, t.event, base, role, scope, op, target, next)
	}

	result := map[string]any{
		"ok": true, "operation": op, "scope": scope, "bot_profile_id": base.ID,
		"operator_role": role, "changed": changed, "blocked_users": next, "message": message,
	}
	if scope == "group" {
		result["group_id"] = t.event.GroupID
		result["inherited_blocked_users"] = inherited
		if len(inherited) > 0 {
			result["inherited_note"] = "inherited_blocked_users 来自机器人级屏蔽名单，本群同样不回他们，但在本群解除不了，只有机器人主人能用 scope=bot 解除。"
		}
	}
	if op != "list" {
		result["user_id"] = target
	}
	encoded, err := json.Marshal(result)
	return string(encoded), err
}

func replyBlockScopeWord(scope string) string {
	if scope == "group" {
		return "本群"
	}
	return "本机所有群和私聊的"
}

// saveBotBlockedUsers 落库机器人级屏蔽名单，成功后同步运行时里的那几份配置。
func (r *Runtime) saveBotBlockedUsers(expected BotConfig, actorID string, userIDs []string) error {
	r.modelConfigMu.Lock()
	defer r.modelConfigMu.Unlock()
	r.mu.Lock()
	defer r.mu.Unlock()
	saver, ok := r.configSaver.(replyBlockSaver)
	if !ok {
		return fmt.Errorf("配置存储不支持机器人级屏蔽名单更新")
	}
	profile, exists := r.profileConfigs[expected.ID]
	if !exists {
		if r.cfg.ID != expected.ID {
			return fmt.Errorf("目标机器人不存在")
		}
		profile = r.cfg
	}
	// 保存前再核一次主人：工具入口已经判过一遍，这里防的是以后有别的调用方接进来。
	if owner := strings.TrimSpace(profile.OwnerID); owner == "" || owner != strings.TrimSpace(actorID) {
		return fmt.Errorf("只有机器人主人可以修改机器人级屏蔽名单")
	}
	if err := saver.SaveBlockedUsers(expected.ID, userIDs); err != nil {
		return err
	}
	gate := profile.ReplyGate.WithBlockedUsers(userIDs)
	profile.ReplyGate = gate
	if r.profileConfigs == nil {
		r.profileConfigs = map[string]BotConfig{}
	}
	r.profileConfigs[expected.ID] = profile
	if r.cfg.ID == expected.ID {
		r.cfg.ReplyGate = gate.Clone()
	}
	r.updatedAt = time.Now()
	return nil
}

func (r *Runtime) recordReplyBlockChanged(ctx context.Context, event MessageEvent, base BotConfig, role, scope, op, target string, blocked []string) {
	writer := r.appLogWriter()
	if writer == nil {
		return
	}
	message := "已通过聊天屏蔽该用户，此后不再回复"
	if op == "unblock" {
		message = "已通过聊天解除该用户的屏蔽"
	}
	_ = writer.AppendLog(ctx, applog.Entry{
		Kind:    applog.KindOperation,
		Level:   applog.LevelInfo,
		Action:  "diana.reply_block." + op,
		Message: message,
		Actor:   oneBotEventActor(event),
		Target:  target,
		Metadata: map[string]any{
			"scope":          scope,
			"group_id":       event.GroupID,
			"bot_profile_id": base.ID,
			"operator_id":    event.UserID,
			"operator_role":  role,
			"blocked_users":  blocked,
		},
	})
}
