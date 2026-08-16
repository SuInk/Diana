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
	defaultQQGroupMemberLimit = 50
	maximumQQGroupMemberLimit = 100
)

type dianaQQGroupTool struct {
	runtime *Runtime
	event   MessageEvent
}

type dianaQQGroupResult struct {
	OK           bool                     `json:"ok"`
	Action       string                   `json:"action"`
	Message      string                   `json:"message,omitempty"`
	Group        *QQGroupInfo             `json:"group,omitempty"`
	Members      []dianaQQGroupMemberItem `json:"members,omitempty"`
	ReplyPolicy  *dianaQQGroupReplyPolicy `json:"reply_policy,omitempty"`
	OperatorRole string                   `json:"operator_role,omitempty"`
	Total        int                      `json:"total,omitempty"`
	GroupTotal   int                      `json:"group_total,omitempty"`
	Limited      bool                     `json:"limited,omitempty"`
}

type dianaQQGroupReplyPolicy struct {
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

type dianaQQGroupMemberItem struct {
	UserID      string `json:"user_id"`
	DisplayName string `json:"display_name"`
	Nickname    string `json:"nickname,omitempty"`
	Card        string `json:"card,omitempty"`
	Role        string `json:"role,omitempty"`
	Title       string `json:"title,omitempty"`
	AvatarURL   string `json:"avatar_url,omitempty"`
	MentionCQ   string `json:"mention_cq"`
}

func newDianaQQGroupTool(runtime *Runtime, event MessageEvent) *dianaQQGroupTool {
	return &dianaQQGroupTool{runtime: runtime, event: event}
}

func (t *dianaQQGroupTool) Name() string {
	return "diana.qq_group"
}

func (t *dianaQQGroupTool) Description() string {
	return `读取当前 QQ 群的真实群信息、成员和回复策略。用户要求查群人数、群名、成员、群名片、昵称、QQ号、头像，或要求真正 @ 某位/多位/其他所有成员时必须调用；不要要求用户先手动 @。operation=info 读取群资料；operation=members 获取或检索成员，结果 group_total 是包含机器人账号的真实群成员总数，total 是按查询和排除条件匹配的人数；operation=reply_policy 读取本群插话概率、判断阈值和最低回复群等级；operation=set_reply_policy 修改这些设置。只有机器人主人、群主或群管理员可读取或修改 reply policy，工具会实时校验权限。set_reply_policy 支持局部更新，proactive_reply_chance 范围 0.05~1，proactive_reply_threshold 范围 0.5~1，minimum_reply_member_level 范围 0~1000；低于最低等级的成员仅在主动 @ 机器人时可回复。闲聊插话（没人 @ 机器人时主动接话）由 chat_in_enabled 开关和 chat_in_level 档位控制，档位可选 off、low、medium、high、max，越高越爱说话；需要精细调节时再用 chat_in_threshold（0.5~1）、chat_in_chance（0.05~1）和 chat_in_cooldown_seconds（0~3600）覆盖档位预设。natural_interjection_enabled=true 会切换为自然插话模式：只要能生成可靠且有实质内容的回复就放行，不再受置信度、抽样率和冷却限制。用户说“只要有话能回就回复”时开启自然插话；说“恢复原来的插话频率”时关闭。members 支持 query 按群名片/昵称/QQ号筛选，exclude_current_sender 排除当前发言者，exclude_user_ids 排除指定 QQ，limit 默认 50、最大 100。结果中的 mention_cq 可直接用于最终回复，提及多人时依次原样输出。input: {"operation":"info|members|reply_policy|set_reply_policy","query":"可选","exclude_current_sender":false,"exclude_user_ids":["QQ号"],"limit":50,"proactive_reply_chance":0.5,"proactive_reply_threshold":0.9,"minimum_reply_member_level":10,"chat_in_enabled":true,"chat_in_level":"medium","natural_interjection_enabled":true}`
}

func (t *dianaQQGroupTool) Run(ctx context.Context, input map[string]any) (string, error) {
	if t == nil || t.runtime == nil {
		return "", fmt.Errorf("diana qq group: runtime is not configured")
	}
	if t.event.Kind != EventKindGroup || strings.TrimSpace(t.event.GroupID) == "" {
		return "", fmt.Errorf("群信息工具只能在 QQ 群聊中使用")
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
		return marshalDianaQQGroupResult(dianaQQGroupResult{
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

func (t *dianaQQGroupTool) replyPolicy(ctx context.Context, input map[string]any, update bool) (string, error) {
	role, err := t.runtime.canConfigureGroup(ctx, t.event)
	if err != nil {
		return "", err
	}
	cfg, ok := t.runtime.groupConfigForEvent(t.event)
	if !ok {
		cfg = DefaultGroupConfig(t.event.GroupID, t.runtime.effectiveConfigForEvent(t.event))
	}
	if !update {
		policy := dianaQQGroupReplyPolicyFromConfig(cfg)
		return marshalDianaQQGroupResult(dianaQQGroupResult{
			OK:           true,
			Action:       "reply_policy",
			Message:      "已读取本群回复策略。",
			ReplyPolicy:  &policy,
			OperatorRole: role,
		})
	}

	changed := false
	if chance, present, err := groupToolFloatWithLegacy(input, "proactive_reply_chance", "passive_reply_chance"); err != nil {
		return "", err
	} else if present {
		if chance < 0.05 || chance > 1 {
			return "", fmt.Errorf("proactive_reply_chance 必须在 0.05 到 1 之间")
		}
		cfg.ProactiveReplyChance = chance
		changed = true
	}
	if threshold, present, err := groupToolFloatWithLegacy(input, "proactive_reply_threshold", "passive_reply_threshold"); err != nil {
		return "", err
	} else if present {
		if threshold < 0.5 || threshold > 1 {
			return "", fmt.Errorf("proactive_reply_threshold 必须在 0.5 到 1 之间")
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
	if _, present := input["chat_in_enabled"]; present {
		cfg.ChatInEnabled = boolPointer(groupToolBool(input, "chat_in_enabled"))
		changed = true
	}
	if _, present := input["natural_interjection_enabled"]; present {
		cfg.NaturalInterjectionEnabled = boolPointer(groupToolBool(input, "natural_interjection_enabled"))
		changed = true
	}
	if value, present := input["chat_in_level"]; present {
		level := ChatInLevel(fmt.Sprintf("%v", value)).Normalized()
		if level == "" {
			return "", fmt.Errorf("chat_in_level 必须是 off、low、medium、high 或 max 之一")
		}
		cfg.ChatInLevel = level
		changed = true
	}
	if threshold, present, err := groupToolFloat(input, "chat_in_threshold"); err != nil {
		return "", err
	} else if present {
		if threshold < 0.5 || threshold > 1 {
			return "", fmt.Errorf("chat_in_threshold 必须在 0.5 到 1 之间")
		}
		cfg.ChatInThreshold = threshold
		changed = true
	}
	if chance, present, err := groupToolFloat(input, "chat_in_chance"); err != nil {
		return "", err
	} else if present {
		if chance < 0.05 || chance > 1 {
			return "", fmt.Errorf("chat_in_chance 必须在 0.05 到 1 之间")
		}
		cfg.ChatInChance = chance
		changed = true
	}
	if value, present := input["chat_in_cooldown_seconds"]; present {
		seconds, err := groupToolInteger(value)
		if err != nil || seconds < 0 || seconds > 3600 {
			return "", fmt.Errorf("chat_in_cooldown_seconds 必须是 0 到 3600 的整数")
		}
		cfg.ChatInCooldownSeconds = seconds
		changed = true
	}
	if !changed {
		return "", fmt.Errorf("至少提供一项要修改的回复策略")
	}
	saved, err := t.runtime.saveGroupConfig(cfg)
	if err != nil {
		return "", err
	}
	t.runtime.cancelProactiveReplyBatch(t.event)
	saved = saved.WithDefaults(t.event.GroupID, t.runtime.effectiveConfigForEvent(t.event))
	t.runtime.recordGroupReplyPolicyChanged(ctx, t.event, role, saved)
	policy := dianaQQGroupReplyPolicyFromConfig(saved)
	return marshalDianaQQGroupResult(dianaQQGroupResult{
		OK:           true,
		Action:       "set_reply_policy",
		Message:      "已更新本群回复策略。",
		ReplyPolicy:  &policy,
		OperatorRole: role,
	})
}

func dianaQQGroupReplyPolicyFromConfig(cfg GroupConfig) dianaQQGroupReplyPolicy {
	// 报告最终生效值，而不是原始字段：档位预设和自定义覆盖合并后才是机器人真正的行为。
	chatIn := (BotConfig{
		ChatInEnabled: cfg.ChatInEnabled, ChatInLevel: cfg.ChatInLevel, ChatInThreshold: cfg.ChatInThreshold,
		ChatInChance: cfg.ChatInChance, ChatInCooldownSeconds: cfg.ChatInCooldownSeconds,
		NaturalInterjectionEnabled: cfg.NaturalInterjectionEnabled,
	}).chatInSettings()
	return dianaQQGroupReplyPolicy{
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

func (t *dianaQQGroupTool) listMembers(ctx context.Context, input map[string]any) (string, error) {
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
	for _, userID := range []string{t.event.SelfID, cfg.BotQQ} {
		if userID = strings.TrimSpace(userID); userID != "" {
			excluded[userID] = true
		}
	}

	limit := groupToolLimit(input)
	items := make([]dianaQQGroupMemberItem, 0, min(limit, len(members)))
	matched := 0
	for _, member := range members {
		if member.UserID == "" || excluded[member.UserID] || !qqGroupMemberMatches(member, query) {
			continue
		}
		matched++
		if len(items) >= limit {
			continue
		}
		items = append(items, dianaQQGroupMemberItem{
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
	return marshalDianaQQGroupResult(dianaQQGroupResult{
		OK:         true,
		Action:     "members",
		Message:    fmt.Sprintf("已通过 OneBot v11 读取当前群成员，匹配 %d 人，返回 %d 人。", matched, len(items)),
		Members:    items,
		Total:      matched,
		GroupTotal: len(members),
		Limited:    matched > len(items),
	})
}

func qqGroupMemberMatches(member QQGroupMemberInfo, query string) bool {
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

func groupToolFloatWithLegacy(input map[string]any, key string, legacyKey string) (float64, bool, error) {
	value, present, err := groupToolFloat(input, key)
	if present {
		return value, true, err
	}
	return groupToolFloat(input, legacyKey)
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
		limit = defaultQQGroupMemberLimit
	}
	if limit > maximumQQGroupMemberLimit {
		limit = maximumQQGroupMemberLimit
	}
	return limit
}

func marshalDianaQQGroupResult(result dianaQQGroupResult) (string, error) {
	body, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return "", err
	}
	return string(body), nil
}
