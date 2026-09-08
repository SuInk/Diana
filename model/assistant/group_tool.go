// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const (
	defaultOneBotGroupMemberLimit = 50
	maximumOneBotGroupMemberLimit = 100
)

type dianaOneBotGroupTool struct {
	runtime *Runtime
	event   MessageEvent
}

type dianaOneBotGroupResult struct {
	MemberListComplete *bool                        `json:"member_list_complete,omitempty"`
	MemberSource       string                       `json:"member_source,omitempty"`
	MemberCountKnown   *bool                        `json:"member_count_known,omitempty"`
	Warnings           []string                     `json:"warnings,omitempty"`
	OK                 bool                         `json:"ok"`
	Action             string                       `json:"action"`
	Message            string                       `json:"message,omitempty"`
	Group              *OneBotGroupInfo             `json:"group,omitempty"`
	Members            []dianaOneBotGroupMemberItem `json:"members,omitempty"`
	ReplyPolicy        *dianaOneBotGroupReplyPolicy `json:"reply_policy,omitempty"`
	AvatarMatch        *groupMemberAvatarMatch      `json:"avatar_match,omitempty"`
	OperatorRole       string                       `json:"operator_role,omitempty"`
	Total              int                          `json:"total,omitempty"`
	GroupTotal         int                          `json:"group_total,omitempty"`
	Limited            bool                         `json:"limited,omitempty"`
}

type dianaOneBotGroupReplyPolicy struct {
	Participation           *ParticipationPreferences `json:"participation"`
	ProactiveReplyChance    float64                   `json:"proactive_reply_chance"`
	ProactiveReplyThreshold float64                   `json:"proactive_reply_threshold"`
	MinimumReplyMemberLevel int                       `json:"minimum_reply_member_level"`
	ChatInEnabled           bool                      `json:"chat_in_enabled"`
	ChatInLevel             string                    `json:"chat_in_level"`
	ChatInLevelLabel        string                    `json:"chat_in_level_label"`
	ChatInThreshold         float64                   `json:"chat_in_threshold"`
	ChatInChance            float64                   `json:"chat_in_chance"`
	ChatInCooldownSeconds   int                       `json:"chat_in_cooldown_seconds"`
}

type dianaOneBotGroupMemberItem struct {
	Username           string `json:"username,omitempty"`
	IsBot              bool   `json:"is_bot,omitempty"`
	MembershipVerified bool   `json:"membership_verified"`
	AvatarSource       string `json:"avatar_source,omitempty"`
	UserID             string `json:"user_id"`
	DisplayName        string `json:"display_name"`
	Nickname           string `json:"nickname,omitempty"`
	Card               string `json:"card,omitempty"`
	Role               string `json:"role,omitempty"`
	Title              string `json:"title,omitempty"`
	AvatarURL          string `json:"avatar_url,omitempty"`
	// Mention 是可以直接抄进回复的提及标记，出站时按平台翻译。
	Mention string `json:"mention"`
}

func newDianaOneBotGroupTool(runtime *Runtime, event MessageEvent) *dianaOneBotGroupTool {
	if runtime != nil {
		event.Platform = firstNonEmpty(event.Platform, runtime.effectiveConfigForEvent(event).Platform)
	}
	return &dianaOneBotGroupTool{runtime: runtime, event: event}
}

func (t *dianaOneBotGroupTool) Name() string {
	return groupToolName(t.event)
}

func (t *dianaOneBotGroupTool) Description() string {
	if !IsOneBotPlatform(t.event.Platform) {
		return groupToolPrompt(t.event) + " reply_policy/set_reply_policy 可查询或修改回复策略，权限由运行时校验。match_avatar 仅比较已知且能核验的成员头像，不代表全群匹配。"
	}
	return `读取当前群的真实群资料、成员名单和回复策略，也可用本地图片模式匹配判断当前图片是否为某位群成员头像。用户要查群人数、群名、成员、群名片、昵称、账号、头像，或要求真正 @ 某位/多位/其他所有成员时必须调用，不要反过来要求用户先手动 @。头像身份不得靠视觉模型猜测，使用 match_avatar。reply_policy 与 set_reply_policy 只对机器人主人、群主和群管理员开放，工具会实时校验权限。`
}

// InputSchema 声明参数契约。取值范围引用与校验同一份常量。
func (t *dianaOneBotGroupTool) InputSchema() map[string]any {
	return toolObjectSchema([]string{"operation"}, map[string]any{
		"operation": toolEnumParam("要执行的操作：info 读群资料；members 获取或检索成员候选；member 按 user_id 实时核验成员；match_avatar 将当前图片与可用成员头像做本地模式匹配；reply_policy 读取本群回复策略；set_reply_policy 修改回复策略（支持局部更新，只传要改的项）。",
			"info", "members", "member", "match_avatar", "reply_policy", "set_reply_policy"),
		"user_id":                toolStringParam("member 专用：要实时核验的成员账号；不能凭昵称猜账号。"),
		"query":                  toolStringParam("members 专用：按群名片、昵称或账号筛选成员。"),
		"exclude_current_sender": toolBoolParam("members 专用：排除当前发言者，用户说「其他人」「除了我」时置 true。"),
		"exclude_user_ids":       toolStringArrayParam("members 专用：排除指定账号。"),
		"limit":                  toolIntParam("members 专用：返回条数，默认 "+itoa(defaultOneBotGroupMemberLimit)+"。", 1, maximumOneBotGroupMemberLimit),
		"minimum_reply_member_level": toolIntParam("最低回复群等级；低于该等级的成员只有主动 @ 机器人时才会被回复。",
			0, maximumReplyMemberLevel),
		"chat_in_level": toolEnumParam("发言偏好预设，由模型按偏好判断是否主动接话；off 表示不主动插话。选择预设会替换自定义滑杆。",
			string(ChatInLevelOff), string(ChatInLevelLow), string(ChatInLevelMedium), string(ChatInLevelHigh), string(ChatInLevelMax)),
	})
}

func (t *dianaOneBotGroupTool) Run(ctx context.Context, input map[string]any) (string, error) {
	if t == nil || t.runtime == nil {
		return "", fmt.Errorf("diana qq group: runtime is not configured")
	}
	if t.event.Kind != EventKindGroup || strings.TrimSpace(t.event.GroupID) == "" {
		return "", fmt.Errorf("群信息工具只能在 群聊中使用")
	}
	operation := strings.ToLower(strings.TrimSpace(configToolString(input, "operation")))
	if operation == "" {
		operation = "members"
	}
	switch operation {
	case "member":
		id := strings.TrimSpace(configToolString(input, "user_id"))
		if id == "" {
			return "", fmt.Errorf("member 需要 user_id")
		}
		member, err := t.runtime.getGroupMemberInfoForEvent(ctx, t.event, t.event.GroupID, id)
		if err != nil {
			return "", err
		}
		return marshalDianaOneBotGroupResult(dianaOneBotGroupResult{OK: true, Action: "member", Message: "已按账号实时核验当前群成员。", Members: []dianaOneBotGroupMemberItem{groupMemberToolItem(member)}, Total: 1})
	case "info", "group":
		group, err := t.runtime.getGroupInfoForEvent(ctx, t.event, t.event.GroupID)
		if err != nil {
			return "", fmt.Errorf("读取群信息失败: %w", err)
		}
		return marshalDianaOneBotGroupResult(dianaOneBotGroupResult{
			OK:      true,
			Action:  "info",
			Message: "已读取当前群的实时资料。",
			Group:   &group,
		})
	case "members", "list", "search", "resolve":
		return t.listMembers(ctx, input)
	case "match_avatar", "avatar_match":
		match, err := t.runtime.matchCurrentGroupMemberAvatar(ctx, t.event)
		if err != nil {
			return "", err
		}
		message := "当前图片未达到可靠的群成员头像匹配阈值。"
		if match.Matched {
			message = fmt.Sprintf("当前图片与群成员 %s 的头像匹配。", match.DisplayName)
		}
		return marshalDianaOneBotGroupResult(dianaOneBotGroupResult{
			OK: true, Action: "match_avatar", Message: message, AvatarMatch: &match, Limited: !match.CandidatesComplete,
		})
	case "reply_policy", "policy":
		return t.replyPolicy(ctx, input, false)
	case "set_reply_policy", "update_reply_policy":
		return t.replyPolicy(ctx, input, true)
	default:
		return "", fmt.Errorf("operation 必须是 info、members、match_avatar、reply_policy 或 set_reply_policy")
	}
}

func (t *dianaOneBotGroupTool) replyPolicy(ctx context.Context, input map[string]any, update bool) (string, error) {
	if !IsOneBotPlatform(t.runtime.currentPlatform(t.event)) && update {
		if _, ok := input["minimum_reply_member_level"]; ok {
			return "", fmt.Errorf("当前平台不提供 QQ 群等级，不能设置最低回复群等级")
		}
	}
	role, err := t.runtime.canConfigureGroup(ctx, t.event)
	if err != nil {
		return "", err
	}
	cfg, ok := t.runtime.groupConfigForEvent(t.event)
	if !ok {
		cfg = DefaultGroupConfig(t.event.GroupID, t.runtime.effectiveConfigForEvent(t.event))
	}
	if !update {
		policy := dianaOneBotGroupReplyPolicyFromConfig(cfg)
		return marshalDianaOneBotGroupResult(dianaOneBotGroupResult{
			OK:           true,
			Action:       "reply_policy",
			Message:      "已读取本群回复策略。",
			ReplyPolicy:  &policy,
			OperatorRole: role,
		})
	}

	changed := false
	if value, present := input["minimum_reply_member_level"]; present {
		level, err := groupToolInteger(value)
		if err != nil || level < 0 || level > maximumReplyMemberLevel {
			return "", fmt.Errorf("minimum_reply_member_level 必须是 0 到 %d 的整数", maximumReplyMemberLevel)
		}
		cfg.MinimumReplyMemberLevel = level
		changed = true
	}
	chatInChanged := false
	if value, present := input["chat_in_level"]; present {
		level := ChatInLevel(fmt.Sprintf("%v", value)).Normalized()
		if level == "" {
			return "", fmt.Errorf("chat_in_level 必须是 off、low、medium、high 或 max 之一")
		}
		cfg.ChatInLevel = level
		cfg.Participation = nil
		cfg.ChatInEnabled = boolPointer(level != ChatInLevelOff)
		cfg.ChatInThreshold = 0
		cfg.ChatInChance = 0
		cfg.ChatInCooldownSeconds = 0
		cfg.ProactiveReplyChance = 0
		cfg.ProactiveReplyThreshold = 0
		cfg.NaturalInterjectionEnabled = boolPointer(false)
		changed, chatInChanged = true, true
	}
	if !changed {
		return "", fmt.Errorf("至少提供一项要修改的回复策略")
	}
	message := "已更新本群回复策略。"
	if chatInChanged {
		message = "已更新本群回复欲望。"
	}
	// 预设回复模式会在运行时重新套用自己的插话档位，把这里刚写进去的值覆盖掉。
	// 既然调用方明确要求改插话，就把本群切到自定义，让修改真正生效。
	if chatInChanged && cfg.ResponseMode.Normalized() != ResponseModeCustom && strings.TrimSpace(string(cfg.ResponseMode)) != "" {
		cfg.ResponseMode = ResponseModeCustom
		message = "已更新本群回复欲望。"
	}
	saved, err := t.runtime.saveGroupConfig(cfg)
	if err != nil {
		return "", err
	}
	t.runtime.cancelProactiveReplyBatch(t.event)
	saved = saved.WithDefaults(t.event.GroupID, t.runtime.effectiveConfigForEvent(t.event))
	t.runtime.recordGroupReplyPolicyChanged(ctx, t.event, role, saved)
	policy := dianaOneBotGroupReplyPolicyFromConfig(saved)
	return marshalDianaOneBotGroupResult(dianaOneBotGroupResult{
		OK:           true,
		Action:       "set_reply_policy",
		Message:      message,
		ReplyPolicy:  &policy,
		OperatorRole: role,
	})
}

func dianaOneBotGroupReplyPolicyFromConfig(cfg GroupConfig) dianaOneBotGroupReplyPolicy {
	// 报告最终生效值，而不是原始字段：预设回复模式、档位预设和自定义覆盖依次合并
	// 之后才是机器人真正的行为。少算预设那一层会把「已改成 max」这类假象报给用户。
	resolved := BotConfig{
		Participation: copyParticipation(cfg.Participation),
		ChatInEnabled: cfg.ChatInEnabled, ChatInLevel: cfg.ChatInLevel, ChatInThreshold: cfg.ChatInThreshold,
		ChatInChance: cfg.ChatInChance, ChatInCooldownSeconds: cfg.ChatInCooldownSeconds,
		NaturalInterjectionEnabled: cfg.NaturalInterjectionEnabled,
	}
	if strings.TrimSpace(string(cfg.ResponseMode)) != "" {
		cfg.ResponseMode.Normalized().apply(&resolved)
	}
	chatIn := resolved.chatInSettings()
	return dianaOneBotGroupReplyPolicy{
		Participation:           chatIn.Participation,
		ProactiveReplyChance:    cfg.ProactiveReplyChance,
		ProactiveReplyThreshold: cfg.ProactiveReplyThreshold,
		MinimumReplyMemberLevel: cfg.MinimumReplyMemberLevel,
		ChatInEnabled:           chatIn.Enabled,
		ChatInLevel:             string(chatIn.Level),
		ChatInLevelLabel:        chatIn.Level.Label(),
		ChatInThreshold:         chatIn.Threshold,
		ChatInChance:            chatIn.Chance,
		ChatInCooldownSeconds:   int(chatIn.Cooldown / time.Second),
	}
}

func (t *dianaOneBotGroupTool) listMembers(ctx context.Context, input map[string]any) (string, error) {
	directory, err := t.runtime.groupDirectoryForEvent(ctx, t.event, t.event.GroupID)
	if err != nil {
		return "", fmt.Errorf("读取群成员列表失败: %w", err)
	}
	members := directory.Members
	query := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(configToolString(input, "query"))), "@")
	excluded := make(map[string]bool)
	if groupToolBool(input, "exclude_current_sender") {
		excluded[strings.TrimSpace(t.event.UserID)] = true
	}
	for _, userID := range groupToolStringList(input["exclude_user_ids"]) {
		excluded[userID] = true
	}
	cfg := t.runtime.effectiveConfigForEvent(t.event)
	for _, userID := range []string{t.event.SelfID, cfg.BotAccount} {
		if userID = strings.TrimSpace(userID); userID != "" {
			excluded[userID] = true
		}
	}

	limit := groupToolLimit(input)
	items := make([]dianaOneBotGroupMemberItem, 0, min(limit, len(members)))
	matched := 0
	for _, member := range members {
		if member.UserID == "" || excluded[member.UserID] || !oneBotGroupMemberMatches(member, query) {
			continue
		}
		matched++
		if len(items) >= limit {
			continue
		}
		items = append(items, groupMemberToolItem(member))
	}
	message := fmt.Sprintf("已读取当前群成员，匹配 %d 人，返回 %d 人。", matched, len(items))
	if !directory.Complete {
		message += " 这是当前接口可见的成员候选，不是完整成员名单；membership_verified 未标为 true 的账号可能已离群，不能据此判断权限或声称已 @ 全部成员。核验指定账号请调用 member。"
	}
	return marshalDianaOneBotGroupResult(dianaOneBotGroupResult{
		OK:                 true,
		Action:             "members",
		Message:            message,
		MemberListComplete: &directory.Complete, MemberSource: directory.Source, MemberCountKnown: &directory.TotalKnown, Warnings: directory.Warnings,
		Members:    items,
		Total:      matched,
		GroupTotal: directory.Total,
		Limited:    !directory.Complete || matched > len(items),
	})
}

func oneBotGroupMemberMatches(member OneBotGroupMemberInfo, query string) bool {
	if query == "" {
		return true
	}
	for _, value := range []string{member.UserID, member.Username, member.Card, member.Nickname, member.DisplayName()} {
		if strings.Contains(strings.ToLower(strings.TrimSpace(value)), query) {
			return true
		}
	}
	return false
}

func groupToolBool(input map[string]any, key string) bool {
	switch value := input[key].(type) {
	case bool:
		return value
	case string:
		parsed, _ := strconv.ParseBool(strings.TrimSpace(value))
		return parsed
	default:
		return false
	}
}

func groupToolInteger(value any) (int, error) {
	raw := strings.TrimSpace(fmt.Sprint(value))
	parsed, err := strconv.Atoi(raw)
	if err == nil {
		return parsed, nil
	}
	decimal, floatErr := strconv.ParseFloat(raw, 64)
	if floatErr != nil || decimal != float64(int(decimal)) {
		return 0, fmt.Errorf("not an integer")
	}
	return int(decimal), nil
}

func groupToolStringList(value any) []string {
	var raw []string
	switch items := value.(type) {
	case []any:
		for _, item := range items {
			raw = append(raw, stringFromAny(item))
		}
	case []string:
		raw = append(raw, items...)
	case string:
		raw = strings.FieldsFunc(items, func(r rune) bool { return r == ',' || r == '，' || r == ' ' })
	}
	var out []string
	for _, item := range raw {
		if userID := normalizeRelationshipUserID(item); userID != "" {
			out = appendUniqueStrings(out, userID)
		}
	}
	return out
}

func groupToolLimit(input map[string]any) int {
	limit := intFromAny(input["limit"])
	if limit <= 0 {
		limit = defaultOneBotGroupMemberLimit
	}
	if limit > maximumOneBotGroupMemberLimit {
		limit = maximumOneBotGroupMemberLimit
	}
	return limit
}

func marshalDianaOneBotGroupResult(result dianaOneBotGroupResult) (string, error) {
	body, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return "", err
	}
	return string(body), nil
}
