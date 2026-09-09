// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

const (
	defaultGroupMemberLimit = 50
	maximumGroupMemberLimit = 100
)

type dianaGroupTool struct {
	runtime *Runtime
	event   MessageEvent
}

type dianaGroupResult struct {
	MemberListComplete *bool                   `json:"member_list_complete,omitempty"`
	MemberSource       string                  `json:"member_source,omitempty"`
	MemberCountKnown   *bool                   `json:"member_count_known,omitempty"`
	Warnings           []string                `json:"warnings,omitempty"`
	OK                 bool                    `json:"ok"`
	Action             string                  `json:"action"`
	Message            string                  `json:"message,omitempty"`
	Group              *OneBotGroupInfo        `json:"group,omitempty"`
	Members            []dianaGroupMemberItem  `json:"members,omitempty"`
	AvatarMatch        *groupMemberAvatarMatch `json:"avatar_match,omitempty"`
	Total              int                     `json:"total,omitempty"`
	GroupTotal         int                     `json:"group_total,omitempty"`
	Limited            bool                    `json:"limited,omitempty"`
}

type dianaGroupMemberItem struct {
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

func newDianaGroupTool(runtime *Runtime, event MessageEvent) *dianaGroupTool {
	if runtime != nil {
		event.Platform = firstNonEmpty(event.Platform, runtime.effectiveConfigForEvent(event).Platform)
	}
	return &dianaGroupTool{runtime: runtime, event: event}
}

func (t *dianaGroupTool) Name() string {
	return groupToolName(t.event)
}

func (t *dianaGroupTool) Description() string {
	if !IsOneBotPlatform(t.event.Platform) {
		return groupToolPrompt(t.event) + " 此工具只读；Diana 回复设置使用 diana.bot_config。match_avatar 仅比较已知且能核验的成员头像，不代表全群匹配。"
	}
	return `使用 match_avatar 做本地群成员头像匹配，不凭视觉猜身份。OneBot 群资料、名单和成员查询按 onebot-v11 skill 使用 diana.onebot_v11；Diana 回复设置使用 diana.bot_config。此工具只读。`
}

// InputSchema 声明参数契约。取值范围引用与校验同一份常量。
func (t *dianaGroupTool) InputSchema() map[string]any {
	if IsOneBotPlatform(t.event.Platform) {
		return toolObjectSchema([]string{"operation"}, map[string]any{"operation": toolEnumParam("本地头像匹配；原生群查询使用 diana.onebot_v11。", "match_avatar")})
	}
	return toolObjectSchema([]string{"operation"}, map[string]any{
		"operation": toolEnumParam("要执行的操作：info 读群资料；members 获取或检索成员候选；member 按 user_id 实时核验成员；match_avatar 将当前图片与可用成员头像做本地模式匹配。",
			"info", "members", "member", "match_avatar"),
		"user_id":                toolStringParam("member 专用：要实时核验的成员账号；不能凭昵称猜账号。"),
		"query":                  toolStringParam("members 专用：按群名片、昵称或账号筛选成员。"),
		"exclude_current_sender": toolBoolParam("members 专用：排除当前发言者，用户说「其他人」「除了我」时置 true。"),
		"exclude_user_ids":       toolStringArrayParam("members 专用：排除指定账号。"),
		"limit":                  toolIntParam("members 专用：返回条数，默认 "+itoa(defaultGroupMemberLimit)+"。", 1, maximumGroupMemberLimit),
	})
}

func (t *dianaGroupTool) Run(ctx context.Context, input map[string]any) (string, error) {
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
	if IsOneBotPlatform(t.event.Platform) && operation != "match_avatar" && operation != "avatar_match" {
		return "", fmt.Errorf("OneBot 群查询和平台操作请使用 diana.onebot_v11；回复设置请使用 diana.bot_config")
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
		return marshalDianaGroupResult(dianaGroupResult{OK: true, Action: "member", Message: "已按账号实时核验当前群成员。", Members: []dianaGroupMemberItem{groupMemberToolItem(member)}, Total: 1})
	case "info", "group":
		group, err := t.runtime.getGroupInfoForEvent(ctx, t.event, t.event.GroupID)
		if err != nil {
			return "", fmt.Errorf("读取群信息失败: %w", err)
		}
		return marshalDianaGroupResult(dianaGroupResult{
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
		return marshalDianaGroupResult(dianaGroupResult{
			OK: true, Action: "match_avatar", Message: message, AvatarMatch: &match, Limited: !match.CandidatesComplete,
		})
	default:
		return "", fmt.Errorf("operation 必须是 info、members、member 或 match_avatar；回复设置使用 diana.bot_config")
	}
}

func (t *dianaGroupTool) listMembers(ctx context.Context, input map[string]any) (string, error) {
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
	items := make([]dianaGroupMemberItem, 0, min(limit, len(members)))
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
	return marshalDianaGroupResult(dianaGroupResult{
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
		limit = defaultGroupMemberLimit
	}
	if limit > maximumGroupMemberLimit {
		limit = maximumGroupMemberLimit
	}
	return limit
}

func marshalDianaGroupResult(result dianaGroupResult) (string, error) {
	body, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return "", err
	}
	return string(body), nil
}
