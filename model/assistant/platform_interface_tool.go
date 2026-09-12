// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/SuInk/diana/model/agent"
	"github.com/SuInk/diana/model/applog"
	platformskill "github.com/SuInk/diana/skills/platform"
)

// sortedMapKeys 返回 map 的排序键，用于审计和调试轨迹里只记参数名不记参数值。
func sortedMapKeys(values map[string]any) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

const (
	platformInterfacePluginID    = "official.platform-interface"
	dianaPlatformToolName        = "diana.platform"
	platformInterfaceSkillSource = "builtin:official.platform-interface"

	// 各平台真实的禁言时长上限。OneBot/QQ 是 30 天；Telegram 的 restrictChatMember
	// 超过 366 天或不足 30 秒都被当成「永久」，取 365 天做实际有限禁言的上限。
	oneBotMaxMuteSeconds   = 2592000
	telegramMaxMuteSeconds = 31536000
)

// 平台中立的动词。模型说「禁言某人 600 秒」，工具再按平台挑对应的原生 action，
// 而不是让模型直接拼 set_group_ban / restrictChatMember。
const (
	platformOpGroupInfo  = "group_info"
	platformOpMemberInfo = "member_info"
	platformOpMemberList = "member_list"
	platformOpMute       = "mute"
	platformOpUnmute     = "unmute"
	platformOpKick       = "kick"
)

var platformReadOperations = map[string]bool{
	platformOpGroupInfo:  true,
	platformOpMemberInfo: true,
	platformOpMemberList: true,
}

var platformDestructiveOperations = map[string]bool{
	platformOpMute:   true,
	platformOpUnmute: true,
	platformOpKick:   true,
}

// PlatformInterfacePlugin 把「平台接口」这项内置能力挂进插件目录，供 WebUI 展示和
// 开关。自然语言路由仍由主 Agent 负责，插件本身不处理请求。
type PlatformInterfacePlugin struct{}

func NewPlatformInterfacePlugin() *PlatformInterfacePlugin {
	return &PlatformInterfacePlugin{}
}

func (p *PlatformInterfacePlugin) Manifest() PluginManifest {
	return PluginManifest{
		ID:          platformInterfacePluginID,
		Name:        "平台接口",
		Version:     "0.1.0",
		Description: "官方内置的跨平台群操作能力：读取群资料与成员，主人可在机器人具备管理员身份时禁言、解禁和踢人。支持 OneBot v11 与 Telegram，其余平台不支持的操作会明确说明。",
		Official:    true,
		BuiltIn:     true,
		Permissions: []string{"platform:group:read", "platform:group:moderate:owner"},
	}
}

func (p *PlatformInterfacePlugin) Handle(context.Context, PluginRequest) (*PluginResponse, error) {
	return nil, nil
}

type dianaPlatformTool struct {
	runtime *Runtime
	event   MessageEvent
	owner   bool
}

func newDianaPlatformTool(runtime *Runtime, event MessageEvent) *dianaPlatformTool {
	tool := &dianaPlatformTool{runtime: runtime, event: event}
	if runtime != nil {
		event.Platform = firstNonEmpty(event.Platform, runtime.effectiveConfigForEvent(event).Platform)
		tool.event = event
		tool.owner = runtime.relationshipPolicy(context.Background(), event).Owner
	}
	return tool
}

func (t *dianaPlatformTool) Name() string { return dianaPlatformToolName }

func (t *dianaPlatformTool) Description() string {
	base := "跨平台群操作接口。group_info 读群资料，member_info 按 user_id 实时核验成员，member_list 拉成员候选。只在用户明确要求读取群信息或执行群操作时调用；被拒绝后不要换别的工具绕过，也不要在没有成功结果时声称已完成。"
	if t.owner {
		base += " 禁言（mute）、解禁（unmute）、踢人（kick）仅主人可用，且需要机器人本身是该群管理员；mute 必须给正的时长（秒），unmute 解除禁言，kick 可带 reject_add_request 决定是否拒绝再次加群。只认账号 ID，取自 @ 的结构化信息、被引用消息的发送者或成员查询结果，不按昵称猜；不能对主人或机器人自己下手。不支持该操作的平台会明确说明。"
	}
	return base
}

func (t *dianaPlatformTool) InputSchema() map[string]any {
	operations := []string{platformOpGroupInfo, platformOpMemberInfo, platformOpMemberList}
	properties := map[string]any{
		"group_id": toolStringParam("目标群 ID；省略时用当前群。"),
		"user_id":  toolStringParam("目标账号 ID，必须取自消息里 @ 的结构化信息、被引用消息的发送者，或成员查询结果。不要按昵称猜 ID，拿不准就先查成员或问清楚。member_info 必填；member_info 省略时用当前引用消息的发送者。"),
	}
	if t.owner {
		operations = append(operations, platformOpMute, platformOpUnmute, platformOpKick)
		properties["duration"] = toolIntParam("mute 专用：禁言时长（秒），必须为正；超过平台上限时按上限执行。", 1, telegramMaxMuteSeconds)
		properties["reject_add_request"] = toolBoolParam("kick 专用：为 true 时同时拒绝该账号再次加群（OneBot 的 reject_add_request；Telegram 保持封禁而非仅移出）。默认 false，只移出、允许再加。")
	}
	properties["operation"] = toolEnumParam("要执行的操作。group_info/member_info/member_list 只读；mute/unmute/kick 是群管理操作，仅主人可用。", operations...)
	return toolObjectSchema([]string{"operation"}, properties)
}

func (t *dianaPlatformTool) Run(ctx context.Context, input map[string]any) (string, error) {
	if t == nil || t.runtime == nil {
		return "", fmt.Errorf("平台接口：运行时不可用")
	}
	owner := t.runtime.relationshipPolicy(ctx, t.event).Owner
	access := "member_read_only"
	if owner {
		access = "owner_full"
	}
	operation := strings.ToLower(strings.TrimSpace(configToolString(input, "operation")))
	if operation == "" {
		return "", fmt.Errorf("operation 不能为空")
	}
	if !platformReadOperations[operation] && !platformDestructiveOperations[operation] {
		return "", fmt.Errorf("operation 必须是 group_info、member_info、member_list、mute、unmute 或 kick")
	}
	if !t.runtime.platformInterfaceEnabled(t.event) {
		err := fmt.Errorf("平台接口未启用，或当前平台不支持群操作")
		t.runtime.recordPlatformInterfaceOperation(t.event, operation, access, owner, "", err)
		return "", err
	}

	if platformDestructiveOperations[operation] {
		return t.runModeration(ctx, input, operation, owner, access)
	}
	return t.runRead(ctx, input, operation, owner, access)
}

func (t *dianaPlatformTool) runRead(ctx context.Context, input map[string]any, operation string, owner bool, access string) (string, error) {
	groupID := t.resolveGroupID(input)
	callCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()

	switch operation {
	case platformOpGroupInfo:
		if groupID == "" {
			return "", fmt.Errorf("group_info 需要群 ID：请在群里调用或指定 group_id")
		}
		info, err := t.runtime.getGroupInfoForEvent(callCtx, t.event, groupID)
		if err != nil {
			t.runtime.recordPlatformInterfaceOperation(t.event, operation, access, owner, "", err)
			return "", err
		}
		t.runtime.recordPlatformInterfaceOperation(t.event, operation, access, owner, "", nil)
		return t.marshal(operation, access, map[string]any{"group": info})
	case platformOpMemberInfo:
		if groupID == "" {
			return "", fmt.Errorf("member_info 需要群 ID：请在群里调用或指定 group_id")
		}
		target := t.resolveTarget(input)
		if target == "" {
			return "", fmt.Errorf("member_info 需要目标账号 ID，或引用对方的消息")
		}
		if err := validatePlatformTargetID(target); err != nil {
			return "", err
		}
		member, err := t.runtime.getGroupMemberInfoForEvent(callCtx, t.event, groupID, target)
		if err != nil {
			t.runtime.recordPlatformInterfaceOperation(t.event, operation, access, owner, target, err)
			return "", err
		}
		t.runtime.recordPlatformInterfaceOperation(t.event, operation, access, owner, target, nil)
		return t.marshal(operation, access, map[string]any{"member": groupMemberToolItem(member)})
	case platformOpMemberList:
		if groupID == "" {
			return "", fmt.Errorf("member_list 需要群 ID：请在群里调用或指定 group_id")
		}
		directory, err := t.runtime.groupDirectoryForEvent(callCtx, t.event, groupID)
		if err != nil {
			t.runtime.recordPlatformInterfaceOperation(t.event, operation, access, owner, "", err)
			return "", err
		}
		items := make([]dianaGroupMemberItem, 0, len(directory.Members))
		for _, member := range directory.Members {
			if member.UserID == "" {
				continue
			}
			items = append(items, groupMemberToolItem(member))
		}
		t.runtime.recordPlatformInterfaceOperation(t.event, operation, access, owner, "", nil)
		return t.marshal(operation, access, map[string]any{
			"members":              items,
			"member_list_complete": directory.Complete,
			"member_source":        directory.Source,
			"member_count_known":   directory.TotalKnown,
			"group_total":          directory.Total,
			"warnings":             directory.Warnings,
		})
	}
	return "", fmt.Errorf("operation 不支持：%s", operation)
}

func (t *dianaPlatformTool) runModeration(ctx context.Context, input map[string]any, operation string, owner bool, access string) (string, error) {
	// 主人门禁在 Run 里强制，不靠提示词或工具作用域。群管理员、群主若不是机器人主人一律拒绝。
	if !owner {
		err := fmt.Errorf("禁言、解禁和踢人只有机器人主人能用，群管理员或群主也不行")
		t.runtime.recordPlatformInterfaceOperation(t.event, operation, access, false, "", err)
		return "", err
	}
	if t.event.Kind != EventKindGroup || strings.TrimSpace(t.event.GroupID) == "" {
		return "", fmt.Errorf("群管理操作只能在群里执行")
	}
	groupID := t.resolveGroupID(input)

	target := t.resolveTarget(input)
	if target == "" {
		return "", fmt.Errorf("请指定目标账号 ID，或引用对方的消息")
	}
	if err := validatePlatformTargetID(target); err != nil {
		return "", err
	}
	// 主人和本机账号一律挡在动作之前：对主人下手等于自断退路，对自己下手会砸掉回复链路。
	base := t.runtime.effectiveConfigForEvent(t.event)
	if ownerID := strings.TrimSpace(base.OwnerIDForEvent(t.event)); target == ownerID || target == strings.TrimSpace(base.OwnerID) {
		err := fmt.Errorf("不能对机器人主人执行群管理操作")
		t.runtime.recordPlatformInterfaceOperation(t.event, operation, access, true, target, err)
		return "", err
	}
	selfID := firstNonEmpty(strings.TrimSpace(t.event.SelfID), strings.TrimSpace(base.BotAccount))
	if target == selfID && selfID != "" {
		err := fmt.Errorf("不能对机器人自己的账号执行群管理操作")
		t.runtime.recordPlatformInterfaceOperation(t.event, operation, access, true, target, err)
		return "", err
	}

	platform := t.runtime.currentPlatform(t.event)
	if !platformSupportsModeration(platform) {
		err := fmt.Errorf("当前平台暂不支持此操作")
		t.runtime.recordPlatformInterfaceOperation(t.event, operation, access, true, target, err)
		return "", err
	}

	// 破坏性操作前先确认机器人自己是这个群的管理员/群主；普通成员直接拒绝，不去碰接口。
	if selfID == "" {
		err := fmt.Errorf("无法确认机器人在本群的身份，暂不执行")
		t.runtime.recordPlatformInterfaceOperation(t.event, operation, access, true, target, err)
		return "", err
	}
	adminCtx, adminCancel := context.WithTimeout(ctx, 6*time.Second)
	selfMember, err := t.runtime.getGroupMemberInfoForEvent(adminCtx, t.event, groupID, selfID)
	adminCancel()
	if err != nil {
		wrapped := fmt.Errorf("无法确认机器人在本群的身份，暂不执行：%w", err)
		t.runtime.recordPlatformInterfaceOperation(t.event, operation, access, true, target, wrapped)
		return "", wrapped
	}
	if selfMember.Role != "owner" && selfMember.Role != "admin" {
		err := fmt.Errorf("我不是这个群的管理员，做不到")
		t.runtime.recordPlatformInterfaceOperation(t.event, operation, access, true, target, err)
		return "", err
	}

	duration := 0
	clamped := false
	if operation == platformOpMute {
		duration = intFromAny(input["duration"])
		if duration <= 0 {
			return "", fmt.Errorf("禁言需要一个正的时长（秒）")
		}
		if cap := platformMaxMuteSeconds(platform); duration > cap {
			duration = cap
			clamped = true
		}
	}

	callCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	rejectAdd := toolInputBool(input, "reject_add_request")
	data, err := t.dispatchModeration(callCtx, platform, operation, groupID, target, duration, rejectAdd)
	if err != nil {
		wrapped := fmt.Errorf("%s 执行失败：%w", operation, err)
		t.runtime.recordPlatformInterfaceOperation(t.event, operation, access, true, target, wrapped)
		return "", wrapped
	}
	t.runtime.recordPlatformInterfaceOperation(t.event, operation, access, true, target, nil)

	payload := map[string]any{"user_id": target, "group_id": groupID, "data": data}
	message := ""
	switch operation {
	case platformOpMute:
		payload["duration"] = duration
		message = fmt.Sprintf("已禁言该账号 %d 秒。", duration)
		if clamped {
			message += "（超过平台上限，已按上限执行。）"
		}
	case platformOpUnmute:
		message = "已解除该账号的禁言。"
	case platformOpKick:
		payload["reject_add_request"] = rejectAdd
		if rejectAdd {
			message = "已将该账号踢出群，并拒绝其再次加群。"
		} else {
			message = "已将该账号踢出群。"
		}
	}
	payload["message"] = message
	return t.marshal(operation, access, payload)
}

// dispatchModeration 把平台中立的动词映射到各平台的原生动作。
func (t *dianaPlatformTool) dispatchModeration(ctx context.Context, platform, operation, groupID, target string, duration int, rejectAdd bool) (map[string]any, error) {
	switch platform {
	case PlatformOneBotV11:
		switch operation {
		case platformOpMute:
			return t.runtime.callOneBotAPIForEvent(ctx, t.event, "set_group_ban", map[string]any{
				"group_id": oneBotIDParam(groupID),
				"user_id":  oneBotIDParam(target),
				"duration": duration,
			})
		case platformOpUnmute:
			return t.runtime.callOneBotAPIForEvent(ctx, t.event, "set_group_ban", map[string]any{
				"group_id": oneBotIDParam(groupID),
				"user_id":  oneBotIDParam(target),
				"duration": 0,
			})
		case platformOpKick:
			return t.runtime.callOneBotAPIForEvent(ctx, t.event, "set_group_kick", map[string]any{
				"group_id":           oneBotIDParam(groupID),
				"user_id":            oneBotIDParam(target),
				"reject_add_request": rejectAdd,
			})
		}
	case PlatformTelegram:
		switch operation {
		case platformOpMute:
			return t.runtime.callPlatformAPIForEvent(ctx, t.event, "restrictChatMember", map[string]any{
				"chat_id":     groupID,
				"user_id":     oneBotIDParam(target),
				"until_date":  time.Now().Add(time.Duration(duration) * time.Second).Unix(),
				"permissions": telegramMutedPermissions(),
			})
		case platformOpUnmute:
			return t.runtime.callPlatformAPIForEvent(ctx, t.event, "restrictChatMember", map[string]any{
				"chat_id":     groupID,
				"user_id":     oneBotIDParam(target),
				"permissions": telegramFullPermissions(),
			})
		case platformOpKick:
			data, err := t.runtime.callPlatformAPIForEvent(ctx, t.event, "banChatMember", map[string]any{
				"chat_id": groupID,
				"user_id": oneBotIDParam(target),
			})
			if err != nil {
				return nil, err
			}
			// reject_add_request=false 是「踢出但允许再加」：Telegram 只能先封禁再解封实现。
			if !rejectAdd {
				if _, unbanErr := t.runtime.callPlatformAPIForEvent(ctx, t.event, "unbanChatMember", map[string]any{
					"chat_id":        groupID,
					"user_id":        oneBotIDParam(target),
					"only_if_banned": true,
				}); unbanErr != nil {
					return nil, unbanErr
				}
			}
			return data, nil
		}
	}
	return nil, fmt.Errorf("当前平台暂不支持此操作")
}

func (t *dianaPlatformTool) resolveGroupID(input map[string]any) string {
	if id := strings.TrimSpace(configToolString(input, "group_id")); id != "" {
		return id
	}
	return strings.TrimSpace(t.event.GroupID)
}

func (t *dianaPlatformTool) resolveTarget(input map[string]any) string {
	if target := strings.TrimSpace(configToolString(input, "user_id")); target != "" {
		return target
	}
	if t.event.Quoted != nil {
		return strings.TrimSpace(t.event.Quoted.UserID)
	}
	return ""
}

func (t *dianaPlatformTool) marshal(operation, access string, extra map[string]any) (string, error) {
	payload := map[string]any{"ok": true, "operation": operation, "access": access}
	for key, value := range extra {
		payload[key] = value
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return string(body), nil
}

// validatePlatformTargetID 只接受账号 ID 形状的目标，昵称一律拒绝。和 diana.reply_block
// 同一套判断：@ 结构化信息、引用消息发送者或成员查询给出的都是 ID。
func validatePlatformTargetID(target string) error {
	for _, char := range target {
		if !(char >= '0' && char <= '9' || char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char == '_' || char == '-') {
			return fmt.Errorf("目标必须是账号 ID，不能使用昵称")
		}
	}
	return nil
}

func platformSupportsModeration(platform string) bool {
	switch NormalizePlatformID(platform) {
	case PlatformOneBotV11, PlatformTelegram:
		return true
	}
	return false
}

func platformMaxMuteSeconds(platform string) int {
	if NormalizePlatformID(platform) == PlatformTelegram {
		return telegramMaxMuteSeconds
	}
	return oneBotMaxMuteSeconds
}

func telegramMutedPermissions() map[string]any {
	return map[string]any{
		"can_send_messages":         false,
		"can_send_audios":           false,
		"can_send_documents":        false,
		"can_send_photos":           false,
		"can_send_videos":           false,
		"can_send_video_notes":      false,
		"can_send_voice_notes":      false,
		"can_send_polls":            false,
		"can_send_other_messages":   false,
		"can_add_web_page_previews": false,
		"can_change_info":           false,
		"can_invite_users":          false,
		"can_pin_messages":          false,
		"can_manage_topics":         false,
	}
}

func telegramFullPermissions() map[string]any {
	perms := telegramMutedPermissions()
	for key := range perms {
		perms[key] = true
	}
	return perms
}

// platformSupportsInterfaceTool 判断某平台是否挂载 diana.platform 工具。读操作走跨平台
// 只读层，破坏性操作只有 OneBot 和 Telegram 真正实现，其余平台会返回明确的不支持说明。
func platformSupportsInterfaceTool(platform string) bool {
	switch NormalizePlatformID(platform) {
	case PlatformOneBotV11, PlatformTelegram, PlatformFeishu, PlatformDingTalk, PlatformWeCom, PlatformQQOfficial:
		return true
	}
	return false
}

func (r *Runtime) platformInterfaceEnabled(event MessageEvent) bool {
	if r == nil || r.plugins == nil {
		return false
	}
	if !r.plugins.EnabledWithOverrides(platformInterfacePluginID, r.pluginOverridesForEvent(event)) {
		return false
	}
	return platformSupportsInterfaceTool(r.currentPlatform(event))
}

// callPlatformAPIForEvent 把请求发到产生该事件的那条通道，不限定平台协议。
// callOneBotAPIForEvent 是它的 OneBot 专用变体（会拒绝非 OneBot 平台）。
func (r *Runtime) callPlatformAPIForEvent(ctx context.Context, event MessageEvent, action string, params map[string]any) (map[string]any, error) {
	channel, _, err := r.outboundChannelForEvent(event)
	if err != nil {
		return nil, err
	}
	return channel.CallAPI(ctx, action, params)
}

func (r *Runtime) platformInterfaceBuiltinSkills(event MessageEvent) []agent.SkillMetadata {
	if !r.platformInterfaceEnabled(event) {
		return nil
	}
	return []agent.SkillMetadata{{
		Name:             "platform",
		Description:      "Read group information and members, and perform owner-only moderation (mute, unmute, kick) through the current platform when the bot is a group administrator.",
		ShortDescription: "跨平台群资料读取与主人专属禁言/踢人",
		Path:             "builtin://platform/SKILL.md",
		Source:           platformInterfaceSkillSource,
		Content:          platformskill.Markdown(),
	}}
}

func (r *Runtime) recordPlatformInterfaceOperation(event MessageEvent, operation, access string, owner bool, target string, callErr error) {
	if r == nil {
		return
	}
	writer := r.appLogWriter()
	if writer == nil {
		return
	}
	destructive := platformDestructiveOperations[operation]
	kind := applog.KindOperation
	level := applog.LevelInfo
	message := "平台接口操作成功"
	detail := ""
	if callErr != nil {
		kind = applog.KindError
		level = applog.LevelError
		message = "平台接口操作被拒绝或失败"
		detail = "platform interface operation failed or was denied"
	}
	logCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = writer.AppendLog(logCtx, applog.Entry{
		Kind:    kind,
		Level:   level,
		Action:  "diana.platform.operation",
		Message: message,
		Detail:  detail,
		Actor:   oneBotEventActor(event),
		Target:  firstNonEmpty(target, operation),
		Metadata: map[string]any{
			"operation":   operation,
			"access":      access,
			"owner":       owner,
			"destructive": destructive,
			"platform":    event.Platform,
			"profile_id":  event.ProfileID,
			"group_id":    event.GroupID,
			"user_id":     event.UserID,
			"target":      target,
			"message_id":  event.MessageID,
		},
	})
}
