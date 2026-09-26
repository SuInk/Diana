// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/SuInk/diana/model/agent"
)

// 群管操作的身份门禁：主人之外，群主和群管理员也能在自己的群里用机器人做群管。
//
// 身份一律实时向平台查，不取消息事件自带的 SenderRole——那份可能是入群时的旧快照，
// 撤了管理员的人拿着它还能继续踢人。层级按平台自身的规矩来：管理员动不了群主和别的
// 管理员，群主可以动管理员；主人和机器人自己谁都不能借机器人去动。

// platformActor 是发起群管操作的人。主人不看群身份；其余人的 role 是刚从平台查到的。
type platformActor struct {
	owner bool
	role  GroupRole
}

func (a platformActor) access() string {
	switch {
	case a.owner:
		return "owner_full"
	case a.role == GroupRoleOwner:
		return "group_owner"
	case a.role == GroupRoleAdmin:
		return "group_admin"
	}
	return "member_read_only"
}

// authorizeModeration 判定当前发言者能不能在 groupID 做群管操作。
func (t *dianaPlatformTool) authorizeModeration(ctx context.Context, groupID string) (platformActor, error) {
	if t.runtime.relationshipPolicy(ctx, t.event).Owner {
		return platformActor{owner: true}, nil
	}
	if t.event.Kind != EventKindGroup || strings.TrimSpace(t.event.GroupID) == "" {
		return platformActor{}, fmt.Errorf("群管理操作只能在群里执行")
	}
	// 群管理员的权力只在自己管的群里有效，不能借机器人去管别的群。
	if strings.TrimSpace(groupID) != strings.TrimSpace(t.event.GroupID) {
		return platformActor{}, fmt.Errorf("群管理员只能管理当前这个群")
	}
	role, err := t.runtime.liveGroupRole(ctx, t.event, groupID, t.event.UserID)
	if err != nil {
		return platformActor{}, fmt.Errorf("无法确认你在本群的身份，暂不执行：%w", err)
	}
	if !GroupRoleCanConfigure(role) {
		return platformActor{role: role}, fmt.Errorf("群管理操作只有机器人主人、群主或群管理员能用")
	}
	return platformActor{role: role}, nil
}

// checkModerationTarget 按层级挡住不该动的目标。protectFromOwner 表示主人发起时
// 也要挡主人和机器人自己（禁言、踢人）；改名片、头衔这类不伤人的操作主人可以对自己做。
func (t *dianaPlatformTool) checkModerationTarget(ctx context.Context, actor platformActor, groupID, target string, protectFromOwner bool) error {
	target = strings.TrimSpace(target)
	if target == "" || (actor.owner && !protectFromOwner) {
		return nil
	}
	base := t.runtime.effectiveConfigForEvent(t.event)
	if ownerID := strings.TrimSpace(base.OwnerIDForEvent(t.event)); target == ownerID || target == strings.TrimSpace(base.OwnerID) {
		return fmt.Errorf("不能对机器人主人执行群管理操作")
	}
	if selfID := firstNonEmpty(strings.TrimSpace(t.event.SelfID), strings.TrimSpace(base.BotAccount)); selfID != "" && target == selfID {
		return fmt.Errorf("不能对机器人自己的账号执行群管理操作")
	}
	if actor.owner || actor.role != GroupRoleAdmin || target == strings.TrimSpace(t.event.UserID) {
		return nil
	}
	role, err := t.runtime.liveGroupRole(ctx, t.event, groupID, target)
	if err != nil {
		return fmt.Errorf("无法确认对方在本群的身份，暂不执行：%w", err)
	}
	if GroupRoleCanConfigure(role) {
		return fmt.Errorf("群管理员不能对群主或其他管理员执行群管理操作")
	}
	return nil
}

// liveGroupRole 实时查询某账号在群里的身份。
func (r *Runtime) liveGroupRole(ctx context.Context, event MessageEvent, groupID, userID string) (GroupRole, error) {
	lookupCtx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	member, err := r.getGroupMemberInfoForEvent(lookupCtx, event, groupID, userID)
	if err != nil {
		return "", err
	}
	return NormalizeGroupRole(member.Role), nil
}

// platformModerationShown 让群管提示词和本轮 platform 工具的 schema 保持一致：工具里
// 没列群管操作，提示词也不提。拿不到工具实例（没有注册表）时退回只对主人注入。
func platformModerationShown(registry *agent.ToolRegistry, owner bool) bool {
	if registry == nil {
		return owner
	}
	tool, ok := registry.Get(dianaPlatformToolName)
	if !ok {
		return false
	}
	if platform, ok := tool.(*dianaPlatformTool); ok {
		return platform.moderator
	}
	return owner
}

// platformModerationVisible 决定群管操作要不要出现在工具 schema 和提示词里。这只管展示，
// 门禁在 Run 里实时核验：事件自带身份时直接用，没带（Telegram 普通消息）才查一次。
func (r *Runtime) platformModerationVisible(ctx context.Context, event MessageEvent, owner bool) bool {
	if owner {
		return true
	}
	if event.Kind != EventKindGroup || strings.TrimSpace(event.GroupID) == "" || strings.TrimSpace(event.UserID) == "" {
		return false
	}
	if role := NormalizeGroupRole(event.SenderRole); role != "" {
		return GroupRoleCanConfigure(role)
	}
	// OneBot 的群消息总带 sender.role，缺了就是普通成员，不为展示再多查一次。
	if IsOneBotPlatform(r.currentPlatform(event)) {
		return false
	}
	lookupCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	role, err := r.liveGroupRole(lookupCtx, event, event.GroupID, event.UserID)
	return err == nil && GroupRoleCanConfigure(role)
}
