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
	dianaPlatformToolName        = "platform"
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

	// 细粒度群管：群公告、精华、名片与头衔、全员禁言，以及撤回别人的消息。
	platformOpAnnounce       = "announce"
	platformOpAnnounceList   = "announce_list"
	platformOpAnnounceDelete = "announce_delete"
	platformOpEssenceSet     = "essence_set"
	platformOpEssenceUnset   = "essence_unset"
	platformOpSetCard        = "set_card"
	platformOpSetTitle       = "set_title"
	platformOpMuteAll        = "mute_all"
	platformOpUnmuteAll      = "unmute_all"
	platformOpRecallMessages = "recall_messages"
)

// 撤回自成一类：不是只读，但也不是对别人动手的群管理操作，所以不归主人专属。
// 它只能作用于机器人自己刚发出的消息，谁都可以让它改口。
var platformSelfOperations = map[string]bool{
	platformOpRecall: true,
}

var platformReadOperations = map[string]bool{
	platformOpGroupInfo:    true,
	platformOpMemberInfo:   true,
	platformOpMemberList:   true,
	platformOpAnnounceList: true,
}

var platformDestructiveOperations = map[string]bool{
	platformOpMute:           true,
	platformOpUnmute:         true,
	platformOpKick:           true,
	platformOpAnnounce:       true,
	platformOpAnnounceDelete: true,
	platformOpEssenceSet:     true,
	platformOpEssenceUnset:   true,
	platformOpSetCard:        true,
	platformOpSetTitle:       true,
	platformOpMuteAll:        true,
	platformOpUnmuteAll:      true,
	platformOpRecallMessages: true,
}

// platformModerationOperations 按固定顺序列出群管操作，工具 schema 和安全模式规则共用。
var platformModerationOperations = []string{
	platformOpMute, platformOpUnmute, platformOpKick,
	platformOpAnnounce, platformOpAnnounceDelete,
	platformOpEssenceSet, platformOpEssenceUnset,
	platformOpSetCard, platformOpSetTitle,
	platformOpMuteAll, platformOpUnmuteAll,
	platformOpRecallMessages,
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
		Version:     "0.1.2",
		Description: "官方内置的跨平台群操作能力：读取群资料与成员，撤回机器人自己发出的消息；主人可在机器人具备管理员身份时禁言、解禁、踢人，发删群公告、设精华、改名片和头衔、开关全员禁言、撤回成员消息。支持 OneBot v11 与 Telegram，平台做不到的操作会明确说明。",
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
	base := "跨平台群操作接口。group_info 读群资料，member_info 按 user_id 实时核验成员，member_list 拉成员候选。只在用户明确要求读取群信息或执行群操作时调用；被拒绝后不要换别的工具绕过，也不要在没有成功结果时声称已完成。recall 撤回我自己刚发出的消息：发现自己说错、发错内容，要真的撤回就必须调用它，只写「当我没说」「收回刚才那句」并不会让消息消失，没调用成功就不许说自己撤回了。它只能作用于我自己发出的消息，message_id 取自历史里我自己的发言，不按内容猜；撤回失败（不支持、超时限、没权限）时原消息仍在，如实说明并直接发更正内容。"
	if t.owner {
		base += " 群管操作仅主人可用，且需要机器人本身是该群管理员：mute 必须给正的时长（秒），unmute 解除禁言，kick 可带 reject_add_request 决定是否拒绝再次加群；announce 发群公告（content），announce_list 查公告，announce_delete 按 notice_id 删公告；essence_set/essence_unset 设置或取消精华（message_id，省略时取被引用的消息）；set_card 改群名片（card 为空表示清除），set_title 改专属头衔（title）；mute_all/unmute_all 开关全员禁言；recall_messages 撤回成员消息：给 message_id 撤一条，或给 user_id 加 count 撤这个人在本会话最近的 N 条，用来清理刷屏和广告。只认账号 ID，取自 @ 的结构化信息、被引用消息的发送者或成员查询结果，不按昵称猜；禁言和踢人不能对主人或机器人自己下手。不支持该操作的平台会明确说明。"
	}
	return base
}

func (t *dianaPlatformTool) InputSchema() map[string]any {
	operations := []string{platformOpGroupInfo, platformOpMemberInfo, platformOpMemberList, platformOpRecall}
	properties := map[string]any{
		"message_id": toolStringParam("recall 专用：要撤回的消息 ID，只能是我自己刚发出的那条，取自历史里我自己的发言标识。省略时撤回我在本会话最近发出的一条。不要按内容或印象编 ID。"),
		"group_id":   toolStringParam("目标群 ID；省略时用当前群。"),
		"user_id":    toolStringParam("目标账号 ID，必须取自消息里 @ 的结构化信息、被引用消息的发送者，或成员查询结果。不要按昵称猜 ID，拿不准就先查成员或问清楚。member_info 必填；member_info 省略时用当前引用消息的发送者。"),
	}
	if t.owner {
		operations = append(operations, platformOpAnnounceList)
		operations = append(operations, platformModerationOperations...)
		properties["message_id"] = toolStringParam("recall：要撤回的我自己的消息 ID，省略时撤回我在本会话最近发出的一条。essence_set/essence_unset/recall_messages：目标消息 ID，取自历史里的消息标识，省略时用当前被引用的消息。不要按内容或印象编 ID。")
		properties["duration"] = toolIntParam("mute 专用：禁言时长（秒），必须为正；超过平台上限时按上限执行。", 1, telegramMaxMuteSeconds)
		properties["reject_add_request"] = toolBoolParam("kick 专用：为 true 时同时拒绝该账号再次加群（OneBot 的 reject_add_request；Telegram 保持封禁而非仅移出）。默认 false，只移出、允许再加。")
		properties["content"] = toolStringParam("announce 专用：群公告正文。")
		properties["notice_id"] = toolStringParam("announce_delete 专用：要删除的公告 ID，取自 announce_list 结果。")
		properties["card"] = toolStringParam("set_card 专用：新的群名片；空串表示清除名片。")
		properties["title"] = toolStringParam("set_title 专用：新的专属头衔；空串表示清除头衔。")
		properties["count"] = toolIntParam("recall_messages 按 user_id 撤回时的条数，默认 10。只撤本会话里看得到的消息。", 1, maxRecallMessagesCount)
	}
	properties["operation"] = toolEnumParam("要执行的操作。group_info/member_info/member_list/announce_list 只读；recall 撤回我自己刚发出的消息；其余是群管理操作，仅主人可用。", operations...)
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
	operation := t.CanonicalOperation(input)
	if operation == "" {
		return "", fmt.Errorf("operation 不能为空")
	}
	if !platformReadOperations[operation] && !platformDestructiveOperations[operation] && !platformSelfOperations[operation] {
		return "", fmt.Errorf("operation 不支持：%s", operation)
	}
	if !t.runtime.platformInterfaceEnabled(t.event) {
		err := fmt.Errorf("平台接口未启用，或当前平台不支持群操作")
		t.runtime.recordPlatformInterfaceOperation(t.event, operation, access, owner, "", err)
		return "", err
	}

	if platformSelfOperations[operation] {
		return t.runRecall(ctx, input, access, owner)
	}
	switch operation {
	case platformOpMute, platformOpUnmute, platformOpKick:
		return t.runModeration(ctx, input, operation, owner, access)
	}
	if platformDestructiveOperations[operation] {
		return t.runGovernance(ctx, input, operation, owner, access)
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
	case platformOpAnnounceList:
		if groupID == "" {
			return "", fmt.Errorf("announce_list 需要群 ID：请在群里调用或指定 group_id")
		}
		if !platformSupportsOperation(t.runtime.currentPlatform(t.event), operation) {
			err := fmt.Errorf("当前平台没有群公告，暂不支持此操作")
			t.runtime.recordPlatformInterfaceOperation(t.event, operation, access, owner, "", err)
			return "", err
		}
		data, err := t.runtime.callOneBotAPIForEvent(callCtx, t.event, "_get_group_notice", map[string]any{"group_id": oneBotIDParam(groupID)})
		if err != nil {
			t.runtime.recordPlatformInterfaceOperation(t.event, operation, access, owner, "", err)
			return "", err
		}
		t.runtime.recordPlatformInterfaceOperation(t.event, operation, access, owner, "", nil)
		return t.marshal(operation, access, map[string]any{"group_id": groupID, "data": data})
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
	if !platformSupportsOperation(platform, operation) {
		err := fmt.Errorf("当前平台暂不支持此操作")
		t.runtime.recordPlatformInterfaceOperation(t.event, operation, access, true, target, err)
		return "", err
	}

	// 破坏性操作前先确认机器人自己是这个群的管理员/群主；普通成员直接拒绝，不去碰接口。
	if _, err := t.runtime.botGroupRole(ctx, t.event, groupID); err != nil {
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

// validatePlatformTargetID 只接受账号 ID 形状的目标，昵称一律拒绝。和 reply_block
// 同一套判断：@ 结构化信息、引用消息发送者或成员查询给出的都是 ID。
func validatePlatformTargetID(target string) error {
	for _, char := range target {
		if !(char >= '0' && char <= '9' || char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char == '_' || char == '-') {
			return fmt.Errorf("目标必须是账号 ID，不能使用昵称")
		}
	}
	return nil
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

// platformSupportsInterfaceTool 判断某平台是否挂载 platform 工具。读操作走跨平台
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
		Description:      "Read group information and members, recall the bot's own recently sent messages, and perform owner-only moderation (mute, unmute, kick, announcements, essence, member card and title, whole-group mute, recalling members' messages) through the current platform when the bot is a group administrator.",
		ShortDescription: "跨平台群资料读取、撤回自己发出的消息与主人专属群管",
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
		Action:  "platform_operation",
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

// CanonicalOperation 是 Run 实际执行的操作，按操作拦截时用同一套换算。
func (*dianaPlatformTool) CanonicalOperation(input map[string]any) string {
	return strings.ToLower(strings.TrimSpace(configToolString(input, "operation")))
}
