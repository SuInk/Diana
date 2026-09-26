// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"fmt"
	"strings"
	"time"
)

const (
	defaultRecallMessagesCount = 10
	// maxRecallMessagesCount 限住一次批量撤回的规模：清刷屏够用，又不至于一句话清空半个群。
	maxRecallMessagesCount = 50
	maxGroupAnnounceRunes  = 1000
	maxGroupCardRunes      = 60
	maxGroupTitleRunes     = 30
)

// platformWriteOperationSupport 是各平台真正实现了的写操作。读操作走跨平台只读层，
// 不在这里；表里没有的操作一律回「当前平台暂不支持」，不拿近似动作凑数。
//
// Telegram 没有群公告，名片也不存在（显示名只归用户自己改），这几项如实不支持；
// 精华映射到置顶，头衔映射到管理员自定义头衔（只对机器人提拔的管理员生效）。
var platformWriteOperationSupport = map[string]map[string]bool{
	PlatformOneBotV11: {
		platformOpRecall: true, platformOpMute: true, platformOpUnmute: true, platformOpKick: true,
		platformOpAnnounce: true, platformOpAnnounceList: true, platformOpAnnounceDelete: true,
		platformOpEssenceSet: true, platformOpEssenceUnset: true,
		platformOpSetCard: true, platformOpSetTitle: true,
		platformOpMuteAll: true, platformOpUnmuteAll: true,
		platformOpRecallMessages: true,
	},
	PlatformTelegram: {
		platformOpRecall: true, platformOpMute: true, platformOpUnmute: true, platformOpKick: true,
		platformOpEssenceSet: true, platformOpEssenceUnset: true,
		platformOpSetTitle: true,
		platformOpMuteAll:  true, platformOpUnmuteAll: true,
		platformOpRecallMessages: true,
	},
}

func platformSupportsOperation(platform, operation string) bool {
	return platformWriteOperationSupport[NormalizePlatformID(platform)][operation]
}

// botGroupRole 确认机器人在这个群是管理员或群主，否则返回可以直接转述给人的错误。
// 所有群管动作都先过这一关：普通成员身份去调接口，只会换来一个看不懂的平台报错。
func (r *Runtime) botGroupRole(ctx context.Context, event MessageEvent, groupID string) (GroupRole, error) {
	base := r.effectiveConfigForEvent(event)
	selfID := firstNonEmpty(strings.TrimSpace(event.SelfID), strings.TrimSpace(base.BotAccount))
	if selfID == "" {
		return "", fmt.Errorf("无法确认机器人在本群的身份，暂不执行")
	}
	adminCtx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	member, err := r.getGroupMemberInfoForEvent(adminCtx, event, groupID, selfID)
	if err != nil {
		return "", fmt.Errorf("无法确认机器人在本群的身份，暂不执行：%w", err)
	}
	role := NormalizeGroupRole(member.Role)
	if !GroupRoleCanConfigure(role) {
		return role, fmt.Errorf("我不是这个群的管理员，做不到")
	}
	return role, nil
}

// runGovernance 处理 mute/unmute/kick 以外的群管操作。门禁和 runModeration 一致：
// 只在群里、主人或实时核验的群主/管理员、平台支持、目标层级、机器人是管理员，都过了才碰接口。
func (t *dianaPlatformTool) runGovernance(ctx context.Context, input map[string]any, operation string, owner bool, access string) (string, error) {
	fail := func(target string, err error) (string, error) {
		t.runtime.recordPlatformInterfaceOperation(t.event, operation, access, owner, target, err)
		return "", err
	}
	if t.event.Kind != EventKindGroup || strings.TrimSpace(t.event.GroupID) == "" {
		return "", fmt.Errorf("群管理操作只能在群里执行")
	}
	groupID := t.resolveGroupID(input)
	actor, err := t.authorizeModeration(ctx, groupID)
	if err != nil {
		return fail("", err)
	}
	access = actor.access()
	platform := t.runtime.currentPlatform(t.event)
	if !platformSupportsOperation(platform, operation) {
		return fail("", fmt.Errorf("当前平台暂不支持此操作"))
	}

	req, err := t.governanceRequest(input, operation)
	if err != nil {
		return fail(req.target, err)
	}
	// 名片、头衔、撤回针对具体的人，走层级约束；公告、精华、全员禁言不针对个人。
	if err := t.checkModerationTarget(ctx, actor, groupID, req.target, false); err != nil {
		return fail(req.target, err)
	}
	role, err := t.runtime.botGroupRole(ctx, t.event, groupID)
	if err != nil {
		return fail(req.target, err)
	}
	// QQ 的专属头衔只有群主能发，管理员调接口会被静默忽略，先说清楚。
	if operation == platformOpSetTitle && role != GroupRoleOwner && NormalizePlatformID(platform) == PlatformOneBotV11 {
		return fail(req.target, fmt.Errorf("QQ 群的专属头衔只有群主能设置，我在这个群只是管理员"))
	}

	callCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if operation == platformOpRecallMessages {
		return t.runRecallMessages(callCtx, req, access, owner)
	}
	data, err := t.dispatchGovernance(callCtx, platform, operation, groupID, req)
	if err != nil {
		return fail(req.target, fmt.Errorf("%s 执行失败：%w", operation, err))
	}
	t.runtime.recordPlatformInterfaceOperation(t.event, operation, access, owner, req.target, nil)
	payload := map[string]any{"group_id": groupID, "data": data, "message": governanceSuccessMessage(operation, req)}
	if req.target != "" {
		payload["user_id"] = req.target
	}
	if req.messageID != "" {
		payload["message_id"] = req.messageID
	}
	return t.marshal(operation, access, payload)
}

type governanceRequest struct {
	target    string
	messageID string
	text      string
	count     int
	// messageIDs 是 recall_messages 解析出来的待撤回列表，新的在前。
	messageIDs []string
}

// governanceRequest 按操作校验参数。这一步不碰平台接口，参数不对就在门口拒掉。
func (t *dianaPlatformTool) governanceRequest(input map[string]any, operation string) (governanceRequest, error) {
	var req governanceRequest
	switch operation {
	case platformOpAnnounce:
		req.text = strings.TrimSpace(configToolString(input, "content"))
		if req.text == "" {
			return req, fmt.Errorf("announce 需要公告正文 content")
		}
		if len([]rune(req.text)) > maxGroupAnnounceRunes {
			return req, fmt.Errorf("群公告不能超过 %d 字", maxGroupAnnounceRunes)
		}
	case platformOpAnnounceDelete:
		req.text = strings.TrimSpace(configToolString(input, "notice_id"))
		if req.text == "" {
			return req, fmt.Errorf("announce_delete 需要 notice_id，先用 announce_list 查")
		}
	case platformOpEssenceSet, platformOpEssenceUnset:
		req.messageID = t.resolveMessageID(input)
		if req.messageID == "" {
			return req, fmt.Errorf("%s 需要 message_id，或引用那条消息", operation)
		}
	case platformOpSetCard, platformOpSetTitle:
		req.target = t.resolveTarget(input)
		if req.target == "" {
			return req, fmt.Errorf("请指定目标账号 ID，或引用对方的消息")
		}
		if err := validatePlatformTargetID(req.target); err != nil {
			return req, err
		}
		field, limit := "card", maxGroupCardRunes
		if operation == platformOpSetTitle {
			field, limit = "title", maxGroupTitleRunes
		}
		req.text = strings.TrimSpace(configToolString(input, field))
		if len([]rune(req.text)) > limit {
			return req, fmt.Errorf("%s 不能超过 %d 字", field, limit)
		}
	case platformOpRecallMessages:
		return t.recallMessagesRequest(input)
	}
	return req, nil
}

func (t *dianaPlatformTool) resolveMessageID(input map[string]any) string {
	if id := strings.TrimSpace(configToolString(input, "message_id")); id != "" {
		return id
	}
	if t.event.Quoted != nil {
		return strings.TrimSpace(t.event.Quoted.MessageID)
	}
	return ""
}

func (t *dianaPlatformTool) dispatchGovernance(ctx context.Context, platform, operation, groupID string, req governanceRequest) (map[string]any, error) {
	call := func(action string, params map[string]any) (map[string]any, error) {
		if NormalizePlatformID(platform) == PlatformOneBotV11 {
			return t.runtime.callOneBotAPIForEvent(ctx, t.event, action, params)
		}
		return t.runtime.callPlatformAPIForEvent(ctx, t.event, action, params)
	}
	switch NormalizePlatformID(platform) {
	case PlatformOneBotV11:
		group := oneBotIDParam(groupID)
		switch operation {
		case platformOpAnnounce:
			return call("_send_group_notice", map[string]any{"group_id": group, "content": req.text})
		case platformOpAnnounceDelete:
			return call("_del_group_notice", map[string]any{"group_id": group, "notice_id": req.text})
		case platformOpEssenceSet:
			return call("set_essence_msg", map[string]any{"message_id": oneBotIDParam(req.messageID)})
		case platformOpEssenceUnset:
			return call("delete_essence_msg", map[string]any{"message_id": oneBotIDParam(req.messageID)})
		case platformOpSetCard:
			return call("set_group_card", map[string]any{"group_id": group, "user_id": oneBotIDParam(req.target), "card": req.text})
		case platformOpSetTitle:
			// duration=-1 是永久；QQ 头衔本来就不会自己过期，给别的值反而容易被实现误读。
			return call("set_group_special_title", map[string]any{"group_id": group, "user_id": oneBotIDParam(req.target), "special_title": req.text, "duration": -1})
		case platformOpMuteAll, platformOpUnmuteAll:
			return call("set_group_whole_ban", map[string]any{"group_id": group, "enable": operation == platformOpMuteAll})
		}
	case PlatformTelegram:
		switch operation {
		case platformOpEssenceSet:
			return call("pinChatMessage", map[string]any{"chat_id": groupID, "message_id": oneBotIDParam(req.messageID), "disable_notification": true})
		case platformOpEssenceUnset:
			return call("unpinChatMessage", map[string]any{"chat_id": groupID, "message_id": oneBotIDParam(req.messageID)})
		case platformOpSetTitle:
			return call("setChatAdministratorCustomTitle", map[string]any{"chat_id": groupID, "user_id": oneBotIDParam(req.target), "custom_title": req.text})
		case platformOpMuteAll:
			return call("setChatPermissions", map[string]any{"chat_id": groupID, "permissions": telegramMutedPermissions()})
		case platformOpUnmuteAll:
			return call("setChatPermissions", map[string]any{"chat_id": groupID, "permissions": telegramMemberDefaultPermissions()})
		}
	}
	return nil, fmt.Errorf("当前平台暂不支持此操作")
}

// telegramMemberDefaultPermissions 是解除全员禁言后恢复的群默认权限。只放开发言类
// 和邀请，改群资料、置顶、管理话题这几项仍然关着——群默认权限给了所有成员，
// 照搬 telegramFullPermissions 会让解禁顺手把改群名的权力发给每个人。
func telegramMemberDefaultPermissions() map[string]any {
	perms := telegramFullPermissions()
	perms["can_change_info"] = false
	perms["can_pin_messages"] = false
	perms["can_manage_topics"] = false
	return perms
}

func governanceSuccessMessage(operation string, req governanceRequest) string {
	switch operation {
	case platformOpAnnounce:
		return "群公告已发布。"
	case platformOpAnnounceDelete:
		return "群公告已删除。"
	case platformOpEssenceSet:
		return "已设为精华消息。"
	case platformOpEssenceUnset:
		return "已取消精华消息。"
	case platformOpSetCard:
		if req.text == "" {
			return "已清除该成员的群名片。"
		}
		return "已修改该成员的群名片。"
	case platformOpSetTitle:
		if req.text == "" {
			return "已清除该成员的专属头衔。"
		}
		return "已设置该成员的专属头衔。"
	case platformOpMuteAll:
		return "已开启全员禁言。"
	case platformOpUnmuteAll:
		return "已关闭全员禁言。"
	}
	return "操作已完成。"
}

// recallMessagesRequest 定位要撤回的成员消息，只在本会话历史里找：
//
//   - 给了 message_id：必须是本会话里看得到的消息，或当前被引用的那条。凭空给的 ID
//     一律拒绝，模型编一个数字就可能删掉别人正常的话。
//   - 给了 user_id：取这个人在本会话最近的 count 条。
//   - 都没给：撤回当前被引用的那条。
func (t *dianaPlatformTool) recallMessagesRequest(input map[string]any) (governanceRequest, error) {
	var req governanceRequest
	messageID := strings.TrimSpace(configToolString(input, "message_id"))
	target := strings.TrimSpace(configToolString(input, "user_id"))
	quoted := ""
	if t.event.Quoted != nil {
		quoted = strings.TrimSpace(t.event.Quoted.MessageID)
	}
	history := t.runtime.sessionHistorySnapshot(t.event)

	if messageID == "" && target == "" {
		if quoted == "" {
			return req, fmt.Errorf("recall_messages 需要 message_id、user_id，或引用要撤回的消息")
		}
		messageID = quoted
	}
	if messageID != "" {
		req.messageID = messageID
		if messageID == quoted && t.event.Quoted != nil {
			req.target = strings.TrimSpace(t.event.Quoted.UserID)
			req.messageIDs = []string{messageID}
			return req, nil
		}
		for i := len(history) - 1; i >= 0; i-- {
			if strings.TrimSpace(history[i].MessageID) == messageID && !strings.HasPrefix(messageID, localOutboundIDPrefix) {
				req.target = strings.TrimSpace(history[i].UserID)
				req.messageIDs = []string{messageID}
				return req, nil
			}
		}
		return req, fmt.Errorf("本次会话里找不到消息 %s：message_id 必须取自历史里的消息标识，不要按内容猜", messageID)
	}

	if err := validatePlatformTargetID(target); err != nil {
		return req, err
	}
	req.target = target
	req.count = intFromAny(input["count"])
	if req.count <= 0 {
		req.count = defaultRecallMessagesCount
	}
	req.count = min(req.count, maxRecallMessagesCount)
	for i := len(history) - 1; i >= 0 && len(req.messageIDs) < req.count; i-- {
		event := history[i]
		id := strings.TrimSpace(event.MessageID)
		if event.Outbound || id == "" || strings.HasPrefix(id, localOutboundIDPrefix) || isPokeHistoryEvent(event) {
			continue
		}
		if strings.TrimSpace(event.UserID) == target {
			req.messageIDs = append(req.messageIDs, id)
		}
	}
	if len(req.messageIDs) == 0 {
		return req, fmt.Errorf("本次会话里没有这个账号可撤回的消息")
	}
	return req, nil
}

// runRecallMessages 逐条撤回。单条失败（超时限、已被别人撤掉）不影响其余的，
// 结果如实报成功和失败各几条，不把部分成功说成全部完成。
func (t *dianaPlatformTool) runRecallMessages(ctx context.Context, req governanceRequest, access string, owner bool) (string, error) {
	recalled, failed := t.runtime.recallGroupMessages(ctx, t.event, req.messageIDs)
	if len(recalled) == 0 {
		err := fmt.Errorf("recall_messages 执行失败：%s", strings.Join(failed, "；"))
		t.runtime.recordPlatformInterfaceOperation(t.event, platformOpRecallMessages, access, owner, req.target, err)
		return "", err
	}
	t.runtime.recordPlatformInterfaceOperation(t.event, platformOpRecallMessages, access, owner, req.target, nil)
	message := fmt.Sprintf("已撤回 %d 条消息。", len(recalled))
	if len(failed) > 0 {
		message = fmt.Sprintf("已撤回 %d 条，另有 %d 条没撤掉（可能超过平台时限或已被撤回）。", len(recalled), len(failed))
	}
	return t.marshal(platformOpRecallMessages, access, map[string]any{
		"user_id":     req.target,
		"recalled":    recalled,
		"failed":      len(failed),
		"message":     message,
		"group_id":    t.event.GroupID,
		"total_found": len(req.messageIDs),
	})
}

// recallGroupMessages 撤回一批消息，返回撤掉的 ID 和失败原因。规则防御和工具共用。
func (r *Runtime) recallGroupMessages(ctx context.Context, event MessageEvent, messageIDs []string) (recalled []string, failed []string) {
	for _, id := range messageIDs {
		if _, err := r.deletePlatformMessage(ctx, event, id); err != nil {
			failed = append(failed, fmt.Sprintf("%s：%v", id, err))
			continue
		}
		recalled = append(recalled, id)
	}
	return recalled, failed
}

func (r *Runtime) sessionHistorySnapshot(event MessageEvent) []MessageEvent {
	if r == nil {
		return nil
	}
	session := sessionKey(event)
	r.mu.RLock()
	defer r.mu.RUnlock()
	return append([]MessageEvent(nil), r.history[session]...)
}
