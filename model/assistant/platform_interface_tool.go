// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/SuInk/diana/internal/secretmask"
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
	// platformOpGroupList 查机器人加入了哪些群（#1165）。群数谁都能问，带群名的明细
	// 只给主人：别的群叫什么、有多少人，不该被随便哪个群友问出来。
	platformOpGroupList = "group_list"
	platformOpMute      = "mute"
	platformOpUnmute    = "unmute"
	platformOpKick      = "kick"

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

	// 群资料与成员层级：改群名、生成邀请链接、设置或取消管理员。
	platformOpSetGroupName = "set_group_name"
	platformOpInviteLink   = "invite_link"
	platformOpSetAdmin     = "set_admin"
	platformOpUnsetAdmin   = "unset_admin"

	// 账号层面的操作只给主人：让机器人退群、看机器人的好友列表。
	platformOpLeaveGroup = "leave_group"
	platformOpFriendList = "friend_list"
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
	platformOpGroupList:    true,
	platformOpAnnounceList: true,
	platformOpFriendList:   true,
}

// platformOwnerOnlyOperations 只认机器人主人，群主和群管理员也不行：退群和好友列表
// 是机器人账号自己的事，不属于任何一个群的管理权。
var platformOwnerOnlyOperations = map[string]bool{
	platformOpLeaveGroup: true,
	platformOpFriendList: true,
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
	platformOpSetGroupName:   true,
	platformOpInviteLink:     true,
	platformOpSetAdmin:       true,
	platformOpUnsetAdmin:     true,
	platformOpLeaveGroup:     true,
}

// platformModerationOperations 按固定顺序列出群管操作，工具 schema 和安全模式规则共用。
var platformModerationOperations = []string{
	platformOpMute, platformOpUnmute, platformOpKick,
	platformOpAnnounce, platformOpAnnounceDelete,
	platformOpEssenceSet, platformOpEssenceUnset,
	platformOpSetCard, platformOpSetTitle,
	platformOpMuteAll, platformOpUnmuteAll,
	platformOpRecallMessages,
	platformOpSetGroupName, platformOpInviteLink,
	platformOpSetAdmin, platformOpUnsetAdmin,
	platformOpLeaveGroup,
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
		Version:     "0.1.4",
		Description: "官方内置的跨平台群操作能力：读取群资料、成员与公告（Telegram 为当前置顶），撤回机器人自己发出的消息；主人、群主和群管理员可在机器人具备管理员身份时禁言、解禁、踢人，发删群公告、设精华、改名片和头衔、改群名、生成邀请链接、开关全员禁言、撤回成员消息；主人和群主可任免管理员；主人可让机器人退群、查看好友列表，并审批好友与入群申请。支持 OneBot v11、Telegram 与 QQ 官方机器人（QQ 群可撤回自己的消息、禁言和踢人，其余群管在 QQ 频道里可用），平台做不到的操作会明确说明。",
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
	// moderator 只决定尾部要不要带群管说明；真正的门禁在 Run 里实时核验。
	// schema 和描述不看它：工具定义排在请求最前面，按发言者身份增删群管操作，
	// 同一个群里管理员和成员交替说话时整段前缀缓存（连同后面的历史）每次都作废。
	moderator bool
	// crossProfileOwner 标记这是主人从另一台机器人发起的跨机器人操作。event 已经换成
	// 目标机器人上的会话，发言人在那边没有账号，主人身份只能由发起侧核验后带过来。
	crossProfileOwner bool
}

func newDianaPlatformTool(ctx context.Context, runtime *Runtime, event MessageEvent) *dianaPlatformTool {
	tool := &dianaPlatformTool{runtime: runtime, event: event}
	if runtime != nil {
		event.Platform = firstNonEmpty(event.Platform, runtime.effectiveConfigForEvent(event).Platform)
		tool.event = event
		tool.owner = runtime.relationshipPolicy(ctx, event).Owner
		tool.moderator = runtime.platformModerationVisible(ctx, event, tool.owner)
	}
	return tool
}

func (t *dianaPlatformTool) Name() string { return dianaPlatformToolName }

func (t *dianaPlatformTool) Description() string {
	// recall 那句来自 #690：模型说错话后只写「当我没说」，消息其实还在。
	// requester_access 那句来自 #877：模型把请求人的权限当成机器人自己的群身份。
	// 各操作配哪些参数写在参数说明里，平台不支持、参数缺失由 Run 返回具体错误。
	base := "读群资料、核验成员、拉成员名单、撤回我自己的消息，以及群管操作（各平台通用）。只在用户明确要求时调用；被拒绝不要换工具绕过，没成功不要说已完成。" +
		"说错话要撤回必须调 recall，光说「当我没说」消息不会消失；撤回失败就如实说并直接发更正。" +
		"结果里的 requester_access 是请求人的权限，不是机器人的。"
	base += "OneBot 的 member_info 在当前群未查到成员时继续查 QQ 个人资料；profile 只说明账号资料，不能据此断言在群或自动 @。"
	base += "group_list 查机器人加了几个群，明细只有主人能看，别人问只报群数。"
	base += "群管操作仅主人、群主、群管理员可用（实时核验），且机器人须是管理员；不能对主人或机器人下手，管理员不能动群主和其他管理员。私聊里不替群管执行。"
	base += "set_admin/unset_admin 仅主人和群主；leave_group 让机器人退群、friend_list 看好友列表，仅主人。"
	base += "主人可带 profile_id 操作另一台机器人（另一个平台）上的群。"
	return base
}

func (t *dianaPlatformTool) InputSchema() map[string]any {
	// 群管操作对谁都列出来，理由见 moderator 字段。
	operations := append([]string{platformOpGroupInfo, platformOpMemberInfo, platformOpMemberList, platformOpGroupList, platformOpAnnounceList, platformOpFriendList, platformOpRecall}, platformModerationOperations...)
	properties := map[string]any{
		"message_id":         toolStringParam("目标消息 ID，取自历史，勿编；recall 默认我最近一条，其余默认被引用消息"),
		"group_id":           toolStringParam("目标群 ID，默认当前群"),
		"user_id":            toolStringParam("目标账号，取自 @、引用或成员查询，勿按昵称猜；默认引用消息发送者"),
		"duration":           toolIntParam("mute：禁言秒数，超平台上限按上限", 1, telegramMaxMuteSeconds),
		"reject_add_request": toolBoolParam("kick：同时拒绝再次加群，默认 false"),
		"content":            toolStringParam("announce：公告正文"),
		"notice_id":          toolStringParam("announce_delete：公告 ID，取自 announce_list"),
		"card":               toolStringParam("set_card：新群名片，空串清除"),
		"title":              toolStringParam("set_title：新头衔，空串清除"),
		"count":              toolIntParam("recall_messages 按 user_id 撤回的条数，默认 10", 1, maxRecallMessagesCount),
		"name":               toolStringParam("set_group_name：新群名"),
		"profile_id":         toolStringParam("仅主人：操作另一台机器人（另一个平台）的群，ID 取自 cross_platform 的 bots；默认当前机器人"),
	}
	properties["operation"] = toolEnumParam("recall 撤回我自己的消息；recall_messages 撤成员消息（清刷屏）", operations...)
	return toolObjectSchema([]string{"operation"}, properties)
}

func (t *dianaPlatformTool) Run(ctx context.Context, input map[string]any) (string, error) {
	if t == nil || t.runtime == nil {
		return "", fmt.Errorf("平台接口：运行时不可用")
	}
	if profileID := strings.TrimSpace(configToolString(input, "profile_id")); profileID != "" && profileID != strings.TrimSpace(t.event.ProfileID) {
		return t.runCrossProfile(ctx, profileID, input)
	}
	owner := t.isOwner(ctx)
	// 只读操作也按发言人的群身份标：以前群主查成员也被标成 member_read_only，模型把它
	// 读成「机器人自己没权限」，明明是管理员却拒绝禁言。群管操作在下面按实时核验改写。
	access := platformActor{owner: owner}.access()
	if !owner {
		access = platformActor{role: t.runtime.promptSenderGroupRole(ctx, t.event)}.access()
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
	if platformOwnerOnlyOperations[operation] {
		return t.runOwnerOperation(ctx, input, operation, owner, access)
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
		member, err := t.runtime.getCurrentGroupMemberForTool(callCtx, t.event, groupID, target)
		if err != nil {
			t.runtime.recordPlatformInterfaceOperation(t.event, operation, access, owner, target, err)
			if IsOneBotPlatform(t.runtime.currentPlatform(t.event)) {
				// 列表查询有独立时限，资料查询也使用独立预算，合计最多十秒。
				profile, profileErr := t.runtime.getOneBotAccountProfile(ctx, t.event, target)
				t.runtime.recordPlatformInterfaceOperation(t.event, "user_profile", access, owner, target, profileErr)
				if profileErr == nil {
					return t.marshal(operation, access, map[string]any{
						"profile": profile, "membership_verified": false, "member_lookup_error": err.Error(),
						"guidance": "这是 QQ 个人资料，不证明该账号在当前群；按 member_lookup_error 说明群籍未找到或无法确认，不称其为本群成员，不自动 @。",
					})
				}
				return "", fmt.Errorf("%v；QQ 个人资料查询也未成功：%w", err, profileErr)
			}
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
	case platformOpGroupList:
		return t.runGroupList(callCtx, operation, owner, access)
	case platformOpAnnounceList:
		if groupID == "" {
			return "", fmt.Errorf("announce_list 需要群 ID：请在群里调用或指定 group_id")
		}
		platform := t.runtime.currentPlatform(t.event)
		if !platformSupportsOperation(platform, operation) {
			err := fmt.Errorf("当前平台没有群公告，暂不支持此操作")
			t.runtime.recordPlatformInterfaceOperation(t.event, operation, access, owner, "", platformRefusal(err))
			return "", err
		}
		var data map[string]any
		var err error
		if NormalizePlatformID(platform) == PlatformTelegram {
			data, err = t.runtime.callPlatformAPIForEvent(callCtx, t.event, "getChat", map[string]any{"chat_id": groupID})
		} else {
			data, err = t.runtime.callOneBotAPIForEvent(callCtx, t.event, "_get_group_notice", map[string]any{"group_id": oneBotIDParam(groupID)})
		}
		if err != nil {
			t.runtime.recordPlatformInterfaceOperation(t.event, operation, access, owner, "", err)
			return "", err
		}
		if NormalizePlatformID(platform) == PlatformTelegram {
			pinned, parseErr := telegramPinnedMessageFromChat(data)
			if parseErr != nil {
				t.runtime.recordPlatformInterfaceOperation(t.event, operation, access, owner, "", parseErr)
				return "", parseErr
			}
			payload := map[string]any{
				"group_id": groupID,
				"data":     pinned,
				"message":  "Telegram Bot API 只提供当前最新置顶，不提供历史置顶列表。",
			}
			if pinned == nil {
				payload["message"] = "当前没有置顶消息；Telegram Bot API 不提供历史置顶列表。"
			}
			t.runtime.recordPlatformInterfaceOperation(t.event, operation, access, owner, "", nil)
			return t.marshal(operation, access, payload)
		}
		t.runtime.recordPlatformInterfaceOperation(t.event, operation, access, owner, "", nil)
		return t.marshal(operation, access, map[string]any{"group_id": groupID, "data": data})
	}
	return "", fmt.Errorf("operation 不支持：%s", operation)
}

// maxPlatformGroupListItems 限制明细条数：几百个群全倒进工具结果只会挤占上下文，
// 主人真要全量去控制台的群管理页看。
const maxPlatformGroupListItems = 200

type dianaPlatformGroupItem struct {
	GroupID     string `json:"group_id"`
	GroupName   string `json:"group_name,omitempty"`
	MemberCount int    `json:"member_count,omitempty"`
}

func (t *dianaPlatformTool) runGroupList(ctx context.Context, operation string, owner bool, access string) (string, error) {
	platform := t.runtime.currentPlatform(t.event)
	if NormalizePlatformID(platform) == PlatformQQOfficial {
		return t.runQQOfficialGuildList(ctx, operation, owner, access)
	}
	if !IsOneBotPlatform(platform) {
		// Telegram Bot API 没有「列出已加入的群」。
		err := fmt.Errorf("当前平台的接口不提供机器人已加入的群列表，暂不支持此操作")
		t.runtime.recordPlatformInterfaceOperation(t.event, operation, access, owner, "", platformRefusal(err))
		return "", err
	}
	// 不带 no_cache 的话 NapCat / SnowLuma 返回的是启动时的快照，新加的群不在里面。
	data, err := t.runtime.callOneBotAPIForEvent(ctx, t.event, "get_group_list", map[string]any{"no_cache": true})
	if err != nil {
		t.runtime.recordPlatformInterfaceOperation(t.event, operation, access, owner, "", err)
		return "", err
	}
	raw, ok := structuredGroupListItems(data)
	if !ok {
		err := fmt.Errorf("群列表返回格式无法识别")
		t.runtime.recordPlatformInterfaceOperation(t.event, operation, access, owner, "", err)
		return "", err
	}
	groups := make([]dianaPlatformGroupItem, 0, len(raw))
	for _, value := range raw {
		item, ok := value.(map[string]any)
		if !ok {
			continue
		}
		id := strings.TrimSpace(stringFromAny(item["group_id"]))
		if id == "" {
			continue
		}
		count, _ := strconv.Atoi(strings.TrimSpace(stringFromAny(item["member_count"])))
		groups = append(groups, dianaPlatformGroupItem{
			GroupID:     id,
			GroupName:   strings.TrimSpace(stringFromAny(item["group_name"])),
			MemberCount: count,
		})
	}
	t.runtime.recordPlatformInterfaceOperation(t.event, operation, access, owner, "", nil)
	return t.marshalGroupList(operation, access, owner, groups, nil)
}

// marshalGroupList 给群列表结果套上可见性规则：群数谁都能看，明细只给主人。
func (t *dianaPlatformTool) marshalGroupList(operation, access string, owner bool, groups []dianaPlatformGroupItem, extra map[string]any) (string, error) {
	payload := map[string]any{"group_count": len(groups)}
	for key, value := range extra {
		payload[key] = value
	}
	if !owner {
		payload["groups_hidden"] = "群明细只有主人能看，这里只报群数。"
		return t.marshal(operation, access, payload)
	}
	if len(groups) > maxPlatformGroupListItems {
		payload["groups_truncated"] = true
		groups = groups[:maxPlatformGroupListItems]
	}
	payload["groups"] = groups
	return t.marshal(operation, access, payload)
}

func (t *dianaPlatformTool) runModeration(ctx context.Context, input map[string]any, operation string, owner bool, access string) (string, error) {
	if t.event.Kind != EventKindGroup || strings.TrimSpace(t.event.GroupID) == "" {
		return "", fmt.Errorf("群管理操作只能在群里执行")
	}
	groupID := t.resolveGroupID(input)
	// 身份门禁在 Run 里强制，不靠提示词或工具作用域：主人直接放行，其余人实时查群身份。
	actor, err := t.authorizeModeration(ctx, groupID)
	if err != nil {
		t.runtime.recordPlatformInterfaceOperation(t.event, operation, access, owner, "", platformRefusal(err))
		return "", err
	}
	access = actor.access()

	target := t.resolveTarget(input)
	if target == "" {
		return "", fmt.Errorf("请指定目标账号 ID，或引用对方的消息")
	}
	if err := validatePlatformTargetID(target); err != nil {
		return "", err
	}
	// 主人和本机账号一律挡在动作之前：对主人下手等于自断退路，对自己下手会砸掉回复链路。
	if err := t.checkModerationTarget(ctx, actor, groupID, target, true); err != nil {
		t.runtime.recordPlatformInterfaceOperation(t.event, operation, access, owner, target, platformRefusal(err))
		return "", err
	}

	platform := t.runtime.currentPlatform(t.event)
	if !platformSupportsOperation(platform, operation) {
		err := fmt.Errorf("当前平台暂不支持此操作")
		t.runtime.recordPlatformInterfaceOperation(t.event, operation, access, owner, target, platformRefusal(err))
		return "", err
	}
	if err := t.qqOfficialSceneSupports(platform, operation); err != nil {
		t.runtime.recordPlatformInterfaceOperation(t.event, operation, access, owner, target, platformRefusal(err))
		return "", err
	}

	// 破坏性操作前先确认机器人自己是这个群的管理员/群主；普通成员直接拒绝，不去碰接口。
	if _, err := t.runtime.botGroupRole(ctx, t.event, groupID); err != nil {
		t.runtime.recordPlatformInterfaceOperation(t.event, operation, access, owner, target, platformRefusal(err))
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
		t.runtime.recordPlatformInterfaceOperation(t.event, operation, access, owner, target, wrapped)
		return "", wrapped
	}
	t.runtime.recordPlatformInterfaceOperation(t.event, operation, access, owner, target, nil)

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
	case PlatformQQOfficial:
		return t.dispatchQQOfficialModeration(ctx, operation, groupID, target, duration, rejectAdd)
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
	payload := map[string]any{"ok": true, "operation": operation, "requester_access": access}
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

// platformSupportsInterfaceTool 判断某平台是否挂载 platform 工具。各平台做不到的
// 操作由 platformWriteOperationSupport 挡下，返回明确的不支持说明。
func platformSupportsInterfaceTool(platform string) bool {
	switch NormalizePlatformID(platform) {
	case PlatformOneBotV11, PlatformTelegram, PlatformQQOfficial:
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
		Description:      "Read group info and members, recall the bot's own messages, and let the owner and group admins moderate (mute, kick, notices, essence, card/title, group name, invite links, admins) when the bot is a group admin; the owner can also leave groups or list friends.",
		ShortDescription: "跨平台群资料读取、撤回自己发出的消息与主人及群管理员的群管",
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
		detail = platformFailureDetail(callErr)
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

// platformFailureDetail 给审计日志写失败原因。
//
// 以前这里只写一句固定的英文，日志里分不清是权限不够、平台不支持还是接口报错，排查
// 只能去翻调试记录（线上 10/22 两次 set_title 失败就是这样）。但接入端的报错原文
// 不能进日志：OneBot 实现和 Telegram 会把请求参数、地址甚至令牌回显在里面。
// 所以只记 Diana 自己写的那层原因（权限、平台不支持、机器人不是管理员……），被它
// 包着的接入端原文剥掉，换成类别和返回码。
func platformFailureDetail(err error) string {
	if err == nil {
		return ""
	}
	var refusal *platformRefusalError
	if !errors.As(err, &refusal) {
		category := platformAdapterErrorCategory(err)
		if category == "" {
			category = "接入端报错"
		}
		prefix := ""
		if inner := errors.Unwrap(err); inner != nil {
			// 「kick 执行失败：<接入端原文>」：前半句是 Diana 写的，留下。
			prefix = strings.TrimRight(strings.TrimSpace(strings.TrimSuffix(err.Error(), inner.Error())), "：:；; ")
		}
		if prefix == "" {
			return category + "（原文不入日志）"
		}
		return secretmask.Text(prefix) + "：" + category + "（原文不入日志）"
	}
	text := refusal.err.Error()
	inner := errors.Unwrap(refusal.err)
	if inner == nil {
		return secretmask.Text(text)
	}
	// 门禁本身也会包着接口报错（「无法确认机器人在本群的身份：<原文>」）。
	category := platformAdapterErrorCategory(inner)
	if category == "" {
		category = "接入端报错"
	}
	text = strings.TrimRight(strings.TrimSpace(strings.TrimSuffix(text, inner.Error())), "：:；; ")
	return secretmask.Text(text) + "：" + category + "（原文不入日志）"
}

// platformRefusalError 标记门禁环节的拒绝：权限、目标校验、平台不支持、机器人
// 不是管理员。这些原因是 Diana 自己写给人看的，审计日志可以照录。
type platformRefusalError struct{ err error }

func (e *platformRefusalError) Error() string { return e.err.Error() }
func (e *platformRefusalError) Unwrap() error { return e.err }

func platformRefusal(err error) error {
	if err == nil {
		return nil
	}
	return &platformRefusalError{err: err}
}

func platformAdapterErrorCategory(err error) string {
	var oneBotErr *oneBotActionError
	var telegramErr *telegramAPIError
	var netErr net.Error
	switch {
	case errors.As(err, &oneBotErr):
		return fmt.Sprintf("OneBot 接入端拒绝，retcode=%d", oneBotErr.retCode)
	case errors.As(err, &telegramErr):
		return fmt.Sprintf("Telegram 接口报错，code=%d", telegramErr.Code)
	case errors.Is(err, context.DeadlineExceeded):
		return "接口超时"
	case errors.As(err, &netErr):
		return "网络错误"
	}
	return ""
}

// CanonicalOperation 是 Run 实际执行的操作，按操作拦截时用同一套换算。
func (*dianaPlatformTool) CanonicalOperation(input map[string]any) string {
	return strings.ToLower(strings.TrimSpace(configToolString(input, "operation")))
}
