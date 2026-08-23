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

	// 回复策略的取值边界。schema 文案和下面的校验都引用这里，避免同一个数字
	// 在散文、参数声明和校验代码里各写一份然后各自漂移。
	minimumReplyChance          = 0.05
	maximumReplyChance          = 1.0
	minimumReplyThreshold       = 0.5
	maximumReplyThreshold       = 1.0
	maximumChatInCooldownSecond = 3600
)

type dianaOneBotGroupTool struct {
	runtime *Runtime
	event   MessageEvent
}

type dianaOneBotGroupResult struct {
	OK           bool                         `json:"ok"`
	Action       string                       `json:"action"`
	Message      string                       `json:"message,omitempty"`
	Group        *OneBotGroupInfo             `json:"group,omitempty"`
	Members      []dianaOneBotGroupMemberItem `json:"members,omitempty"`
	ReplyPolicy  *dianaOneBotGroupReplyPolicy `json:"reply_policy,omitempty"`
	OperatorRole string                       `json:"operator_role,omitempty"`
	Total        int                          `json:"total,omitempty"`
	GroupTotal   int                          `json:"group_total,omitempty"`
	Limited      bool                         `json:"limited,omitempty"`
}

type dianaOneBotGroupReplyPolicy struct {
	ProactiveReplyChance       float64 `json:"proactive_reply_chance"`
	ProactiveReplyThreshold    float64 `json:"proactive_reply_threshold"`
	MinimumReplyMemberLevel    int     `json:"minimum_reply_member_level"`
	ChatInEnabled              bool    `json:"chat_in_enabled"`
	ChatInLevel                string  `json:"chat_in_level"`
	ChatInLevelLabel           string  `json:"chat_in_level_label"`
	ChatInThreshold            float64 `json:"chat_in_threshold"`
	ChatInChance               float64 `json:"chat_in_chance"`
	ChatInCooldownSeconds      int     `json:"chat_in_cooldown_seconds"`
	NaturalInterjectionEnabled bool    `json:"natural_interjection_enabled"`
}

type dianaOneBotGroupMemberItem struct {
	UserID      string `json:"user_id"`
	DisplayName string `json:"display_name"`
	Nickname    string `json:"nickname,omitempty"`
	Card        string `json:"card,omitempty"`
	Role        string `json:"role,omitempty"`
	Title       string `json:"title,omitempty"`
	AvatarURL   string `json:"avatar_url,omitempty"`
	MentionCQ   string `json:"mention_cq"`
}

func newDianaOneBotGroupTool(runtime *Runtime, event MessageEvent) *dianaOneBotGroupTool {
	return &dianaOneBotGroupTool{runtime: runtime, event: event}
}

func (t *dianaOneBotGroupTool) Name() string {
	return "diana.onebot_group"
}

func (t *dianaOneBotGroupTool) Description() string {
	return `读取当前群的真实群资料、成员名单和回复策略，也可修改回复策略。用户要查群人数、群名、成员、群名片、昵称、账号、头像，或要求真正 @ 某位/多位/其他所有成员时必须调用，不要反过来要求用户先手动 @。reply_policy 与 set_reply_policy 只对机器人主人、群主和群管理员开放，工具会实时校验权限。`
}

// InputSchema 声明参数契约。取值范围引用与校验同一份常量。
func (t *dianaOneBotGroupTool) InputSchema() map[string]any {
	return toolObjectSchema([]string{"operation"}, map[string]any{
		"operation": toolEnumParam("要执行的操作：info 读群资料；members 获取或检索成员；reply_policy 读取本群回复策略；set_reply_policy 修改回复策略（支持局部更新，只传要改的项）。",
			"info", "members", "reply_policy", "set_reply_policy"),
		"query":                  toolStringParam("members 专用：按群名片、昵称或账号筛选成员。"),
		"exclude_current_sender": toolBoolParam("members 专用：排除当前发言者，用户说「其他人」「除了我」时置 true。"),
		"exclude_user_ids":       toolStringArrayParam("members 专用：排除指定账号。"),
		"limit":                  toolIntParam("members 专用：返回条数，默认 "+itoa(defaultOneBotGroupMemberLimit)+"。", 1, maximumOneBotGroupMemberLimit),
		"proactive_reply_chance": toolNumberParam("主动回复采样率：判断放行后实际回复的比例。",
			minimumReplyChance, maximumReplyChance),
		"proactive_reply_threshold": toolNumberParam("主动回复置信度阈值，越高越克制。",
			minimumReplyThreshold, maximumReplyThreshold),
		"minimum_reply_member_level": toolIntParam("最低回复群等级；低于该等级的成员只有主动 @ 机器人时才会被回复。",
			0, maximumReplyMemberLevel),
		"chat_in_enabled": toolBoolParam("闲聊插话总开关：没人 @ 机器人时是否主动接话。"),
		"chat_in_level": toolEnumParam("闲聊插话档位，越高越爱说话；日常调节改这一项就够，不必动下面三个细项。",
			string(ChatInLevelOff), string(ChatInLevelLow), string(ChatInLevelMedium), string(ChatInLevelHigh), string(ChatInLevelMax)),
		"chat_in_threshold": toolNumberParam("覆盖档位预设的插话置信度阈值，需要精细调节时才用。",
			minimumReplyThreshold, maximumReplyThreshold),
		"chat_in_chance": toolNumberParam("覆盖档位预设的插话采样率，需要精细调节时才用。",
			minimumReplyChance, maximumReplyChance),
		"chat_in_cooldown_seconds": toolIntParam("覆盖档位预设的插话冷却秒数，需要精细调节时才用。",
			0, maximumChatInCooldownSecond),
		"natural_interjection_enabled": toolBoolParam("自然插话模式：置 true 后只要能生成可靠且有实质内容的回复就放行，不再受置信度、采样率和冷却限制。用户说「只要有话能回就回复」时开启，说「恢复原来的插话频率」时关闭。"),
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
	case "info", "group":
		group, err := t.runtime.getGroupInfoForEvent(ctx, t.event, t.event.GroupID)
		if err != nil {
			return "", fmt.Errorf("读取群信息失败: %w", err)
		}
		return marshalDianaOneBotGroupResult(dianaOneBotGroupResult{
			OK:      true,
			Action:  "info",
			Message: "已通过 OneBot v11 读取当前群资料。",
			Group:   &group,
		})
	case "members", "list", "search", "resolve":
		return t.listMembers(ctx, input)
	case "reply_policy", "policy":
		return t.replyPolicy(ctx, input, false)
	case "set_reply_policy", "update_reply_policy":
		return t.replyPolicy(ctx, input, true)
	default:
		return "", fmt.Errorf("operation 必须是 info、members、reply_policy 或 set_reply_policy")
	}
}

func (t *dianaOneBotGroupTool) replyPolicy(ctx context.Context, input map[string]any, update bool) (string, error) {
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
	if chance, present, err := groupToolFloat(input, "proactive_reply_chance"); err != nil {
		return "", err
	} else if present {
		if chance < minimumReplyChance || chance > maximumReplyChance {
			return "", fmt.Errorf("proactive_reply_chance 必须在 %g 到 %g 之间", minimumReplyChance, maximumReplyChance)
		}
		cfg.ProactiveReplyChance = chance
		changed = true
	}
	if threshold, present, err := groupToolFloat(input, "proactive_reply_threshold"); err != nil {
		return "", err
	} else if present {
		if threshold < minimumReplyThreshold || threshold > maximumReplyThreshold {
			return "", fmt.Errorf("proactive_reply_threshold 必须在 %g 到 %g 之间", minimumReplyThreshold, maximumReplyThreshold)
		}
		cfg.ProactiveReplyThreshold = threshold
		changed = true
	}
	if value, present := input["minimum_reply_member_level"]; present {
		level, err := groupToolInteger(value)
		if err != nil || level < 0 || level > maximumReplyMemberLevel {
			return "", fmt.Errorf("minimum_reply_member_level 必须是 0 到 %d 的整数", maximumReplyMemberLevel)
		}
		cfg.MinimumReplyMemberLevel = level
		changed = true
	}
	chatInChanged := false
	if _, present := input["chat_in_enabled"]; present {
		cfg.ChatInEnabled = boolPointer(groupToolBool(input, "chat_in_enabled"))
		changed, chatInChanged = true, true
	}
	if _, present := input["natural_interjection_enabled"]; present {
		cfg.NaturalInterjectionEnabled = boolPointer(groupToolBool(input, "natural_interjection_enabled"))
		changed, chatInChanged = true, true
	}
	if value, present := input["chat_in_level"]; present {
		level := ChatInLevel(fmt.Sprintf("%v", value)).Normalized()
		if level == "" {
			return "", fmt.Errorf("chat_in_level 必须是 off、low、medium、high 或 max 之一")
		}
		cfg.ChatInLevel = level
		changed, chatInChanged = true, true
	}
	if threshold, present, err := groupToolFloat(input, "chat_in_threshold"); err != nil {
		return "", err
	} else if present {
		if threshold < minimumReplyThreshold || threshold > maximumReplyThreshold {
			return "", fmt.Errorf("chat_in_threshold 必须在 %g 到 %g 之间", minimumReplyThreshold, maximumReplyThreshold)
		}
		cfg.ChatInThreshold = threshold
		changed, chatInChanged = true, true
	}
	if chance, present, err := groupToolFloat(input, "chat_in_chance"); err != nil {
		return "", err
	} else if present {
		if chance < minimumReplyChance || chance > maximumReplyChance {
			return "", fmt.Errorf("chat_in_chance 必须在 %g 到 %g 之间", minimumReplyChance, maximumReplyChance)
		}
		cfg.ChatInChance = chance
		changed, chatInChanged = true, true
	}
	if value, present := input["chat_in_cooldown_seconds"]; present {
		seconds, err := groupToolInteger(value)
		if err != nil || seconds < 0 || seconds > maximumChatInCooldownSecond {
			return "", fmt.Errorf("chat_in_cooldown_seconds 必须是 0 到 %d 的整数", maximumChatInCooldownSecond)
		}
		cfg.ChatInCooldownSeconds = seconds
		changed, chatInChanged = true, true
	}
	if !changed {
		return "", fmt.Errorf("至少提供一项要修改的回复策略")
	}
	message := "已更新本群回复策略。"
	// 预设回复模式会在运行时重新套用自己的插话档位，把这里刚写进去的值覆盖掉。
	// 既然调用方明确要求改插话，就把本群切到自定义，让修改真正生效。
	if chatInChanged && cfg.ResponseMode.Normalized() != ResponseModeCustom && strings.TrimSpace(string(cfg.ResponseMode)) != "" {
		cfg.ResponseMode = ResponseModeCustom
		message = "已更新本群回复策略，并把本群回复模式切换为自定义，否则预设模式会覆盖插话设置。"
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
		ChatInEnabled: cfg.ChatInEnabled, ChatInLevel: cfg.ChatInLevel, ChatInThreshold: cfg.ChatInThreshold,
		ChatInChance: cfg.ChatInChance, ChatInCooldownSeconds: cfg.ChatInCooldownSeconds,
		NaturalInterjectionEnabled: cfg.NaturalInterjectionEnabled,
	}
	if strings.TrimSpace(string(cfg.ResponseMode)) != "" {
		cfg.ResponseMode.Normalized().apply(&resolved)
	}
	chatIn := resolved.chatInSettings()
	return dianaOneBotGroupReplyPolicy{
		ProactiveReplyChance:       cfg.ProactiveReplyChance,
		ProactiveReplyThreshold:    cfg.ProactiveReplyThreshold,
		MinimumReplyMemberLevel:    cfg.MinimumReplyMemberLevel,
		ChatInEnabled:              chatIn.Enabled,
		ChatInLevel:                string(chatIn.Level),
		ChatInLevelLabel:           chatIn.Level.Label(),
		ChatInThreshold:            chatIn.Threshold,
		ChatInChance:               chatIn.Chance,
		ChatInCooldownSeconds:      int(chatIn.Cooldown / time.Second),
		NaturalInterjectionEnabled: chatIn.Natural,
	}
}

func (t *dianaOneBotGroupTool) listMembers(ctx context.Context, input map[string]any) (string, error) {
	members, err := t.runtime.getGroupMemberListForEvent(ctx, t.event, t.event.GroupID)
	if err != nil {
		return "", fmt.Errorf("读取群成员列表失败: %w", err)
	}
	query := strings.ToLower(strings.TrimSpace(configToolString(input, "query")))
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
		items = append(items, dianaOneBotGroupMemberItem{
			UserID:      member.UserID,
			DisplayName: member.DisplayName(),
			Nickname:    member.Nickname,
			Card:        member.Card,
			Role:        member.Role,
			Title:       member.Title,
			AvatarURL:   member.AvatarURL,
			MentionCQ:   "[CQ:at,qq=" + member.UserID + "]",
		})
	}
	return marshalDianaOneBotGroupResult(dianaOneBotGroupResult{
		OK:         true,
		Action:     "members",
		Message:    fmt.Sprintf("已通过 OneBot v11 读取当前群成员，匹配 %d 人，返回 %d 人。", matched, len(items)),
		Members:    items,
		Total:      matched,
		GroupTotal: len(members),
		Limited:    matched > len(items),
	})
}

func oneBotGroupMemberMatches(member OneBotGroupMemberInfo, query string) bool {
	if query == "" {
		return true
	}
	for _, value := range []string{member.UserID, member.Card, member.Nickname, member.DisplayName()} {
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

func groupToolFloat(input map[string]any, key string) (float64, bool, error) {
	value, present := input[key]
	if !present {
		return 0, false, nil
	}
	parsed, err := strconv.ParseFloat(strings.TrimSpace(fmt.Sprint(value)), 64)
	if err != nil {
		return 0, true, fmt.Errorf("%s 必须是数字", key)
	}
	return parsed, true, nil
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
