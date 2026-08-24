// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

const (
	defaultRelationshipListLimit    = 20
	maximumRelationshipListLimit    = 50
	defaultRelationshipHistoryLimit = 5
	maximumRelationshipHistoryLimit = 20

	// 好感度的可写区间。schema 文案和写入校验引用同一份常量。
	minimumFavorability = -100
	maximumFavorability = 200
)

type dianaRelationshipTool struct {
	runtime *Runtime
	event   MessageEvent
}

type dianaRelationshipResult struct {
	OK      bool                        `json:"ok"`
	Action  string                      `json:"action"`
	Message string                      `json:"message,omitempty"`
	Target  *dianaRelationshipSnapshot  `json:"target,omitempty"`
	Items   []dianaRelationshipSnapshot `json:"items,omitempty"`
	// ReplyGuidance 是「拿到数据之后怎么说」的约束。它不是工具文档，放在
	// Description 里等于每轮 planning 都为它付 token，而且送达时机也不对——
	// 模型在挑工具时读到「别抄成清单」，等真要写回复时早被上下文冲淡了。
	// 放在返回值里只在真调用了才付钱，且正好在要用它的那一刻送到。
	ReplyGuidance string `json:"reply_guidance,omitempty"`
}

// relationshipReplyGuidance 约束模型怎么把这份数据说出来。
const relationshipReplyGuidance = "围绕用户实际问的那件事回答，用自然的中文，不要把结果按字段抄成清单。" +
	"reminder_schedule_limit 只在用户明确问「能建几个」时才说，平时不要主动报出来——真建满时创建工具会当场说明。" +
	"只有用户问最近变化时才讲 recent_changes 里的增减分、时间和原因。" +
	"回复里需要真正 @ 目标时，原样使用结果中的 mention_cq，不要写成普通文本的 @账号。"

type dianaRelationshipSnapshot struct {
	UserID      string `json:"user_id"`
	DisplayName string `json:"display_name"`
	// Mention 是可以直接抄进回复的提及标记，出站时按平台翻译。
	Mention          string                   `json:"mention"`
	Favorability     int                      `json:"favorability"`
	MessageCount     int                      `json:"message_count"`
	RelationshipTier RelationshipTier         `json:"relationship_tier"`
	RelationshipName string                   `json:"relationship_name"`
	ScheduleLimit    int                      `json:"reminder_schedule_limit"`
	CanGenerateImage bool                     `json:"can_generate_image"`
	CanEditImage     bool                     `json:"can_edit_image"`
	CanDocumentOCR   bool                     `json:"can_document_ocr"`
	Owner            bool                     `json:"owner"`
	HasHistory       bool                     `json:"has_history"`
	RecentChanges    []UserFavorabilityChange `json:"recent_changes,omitempty"`
}

func newDianaRelationshipTool(runtime *Runtime, event MessageEvent) *dianaRelationshipTool {
	return &dianaRelationshipTool{runtime: runtime, event: event}
}

func (t *dianaRelationshipTool) Name() string {
	return "diana.relationship"
}

func (t *dianaRelationshipTool) Description() string {
	return `查询 Diana 对用户的好感度、关系等级、互动次数和最近的增减分记录。用户询问自己、被 @ 成员或指定群成员的好感度或关系时必须调用，不要根据上下文猜测，也不要声称无法查询隐藏数据。本工具不返回能力清单——用户问「你能做什么」应改用 diana.capabilities，基础能力对所有关系等级一律开放。`
}

// InputSchema 声明参数契约。「拿到结果后怎么说话」不在这里，也不在 Description
// 里，而是随结果一起返回（见 relationshipReplyGuidance）。
func (t *dianaRelationshipTool) InputSchema() map[string]any {
	return toolObjectSchema([]string{"operation"}, map[string]any{
		"operation": toolEnumParam("要执行的操作：get 查单个用户；list 查当前群内已有互动记录的成员并按好感度排序（群内成员均可使用，不得以隐私或权限为由拒绝）；set 直接设置、adjust 增减好感度，后两者仅机器人主人可用且不增加互动次数。",
			"get", "list", "set", "adjust"),
		"target_user_id": toolStringParam("目标账号。get 可省略：消息里 @ 了成员就查该成员，否则查当前发言者；set 和 adjust 必填，且不能指向主人自己。"),
		"history_limit":  toolIntParam("get 返回的最近变化条数，默认 "+itoa(defaultRelationshipHistoryLimit)+"。", 1, maximumRelationshipHistoryLimit),
		"value":          toolIntParam("set 专用：直接设置成的好感度数值。", minimumFavorability, maximumFavorability),
		"delta":          toolIntParam("adjust 专用：好感度增减量，可为负数；结果会被夹在可写区间内。", minimumFavorability-maximumFavorability, maximumFavorability-minimumFavorability),
		"reason":         toolStringParam("set 和 adjust 可选：这次调整的备注原因。"),
	})
}

func (t *dianaRelationshipTool) Run(ctx context.Context, input map[string]any) (string, error) {
	if t == nil || t.runtime == nil {
		return "", fmt.Errorf("diana relationship: runtime is not configured")
	}
	operation := strings.ToLower(strings.TrimSpace(configToolString(input, "operation")))
	if operation == "" {
		operation = "get"
	}
	switch operation {
	case "get", "show":
		targetID := normalizeRelationshipUserID(configToolString(input, "target_user_id"))
		if targetID == "" {
			targetID = t.defaultTargetUserID()
		}
		if targetID == "" {
			return "", fmt.Errorf("没有找到要查询的用户")
		}
		member, err := t.resolveTargetMember(ctx, targetID)
		if err != nil {
			return "", err
		}
		snapshot, err := t.relationshipSnapshot(ctx, targetID, member.DisplayName(), relationshipHistoryLimit(input))
		if err != nil {
			return "", err
		}
		return marshalDianaRelationshipResult(dianaRelationshipResult{
			OK:      true,
			Action:  "retrieved",
			Message: "已读取目标用户的关系数据；只包含关系统计，不包含长期记忆正文。",
			Target:  &snapshot,
		})
	case "list", "rank":
		// 榜单只是群内公开互动的统计（好感度、互动次数、关系等级），不含长期
		// 记忆正文，对全体成员开放；set/adjust 这类写操作仍然只有主人可用。
		items, err := t.listGroupRelationships(ctx, relationshipListLimit(input))
		if err != nil {
			return "", err
		}
		return marshalDianaRelationshipResult(dianaRelationshipResult{
			OK:      true,
			Action:  "listed",
			Message: fmt.Sprintf("已读取当前群内 %d 位有互动记录成员的关系数据。", len(items)),
			Items:   items,
		})
	case "set", "adjust":
		requester := t.runtime.relationshipPolicy(ctx, t.event)
		if !requester.Owner {
			return "", fmt.Errorf("只有主人可以修改其他用户的好感度")
		}
		targetID := normalizeRelationshipUserID(configToolString(input, "target_user_id"))
		if targetID == "" {
			targetID = t.defaultTargetUserID()
		}
		if targetID == "" {
			return "", fmt.Errorf("修改好感度时必须提供有效的 target_user_id 或 @ 目标用户")
		}
		ownerID := strings.TrimSpace(t.runtime.effectiveConfigForEvent(t.event).OwnerID)
		if targetID == ownerID {
			return "", fmt.Errorf("主人的关系等级固定，不能修改主人自己的好感度")
		}
		value, err := t.updatedFavorability(ctx, operation, targetID, input)
		if err != nil {
			return "", err
		}
		snapshot, err := t.relationshipSnapshot(ctx, targetID, "", relationshipHistoryLimit(input))
		if err != nil {
			return "", err
		}
		return marshalDianaRelationshipResult(dianaRelationshipResult{
			OK:      true,
			Action:  "updated",
			Message: fmt.Sprintf("已由主人将目标用户好感度更新为 %d；未增加互动次数。", value),
			Target:  &snapshot,
		})
	default:
		return "", fmt.Errorf("operation 必须是 get、list、set 或 adjust")
	}
}

func (t *dianaRelationshipTool) updatedFavorability(ctx context.Context, operation string, targetID string, input map[string]any) (int, error) {
	t.runtime.mu.RLock()
	store := t.runtime.userMemory
	t.runtime.mu.RUnlock()
	if store == nil {
		return 0, fmt.Errorf("当前未启用用户关系存储")
	}
	profile, _, err := store.GetUserMemory(ctx, targetID)
	if err != nil {
		return 0, fmt.Errorf("读取用户关系失败: %w", err)
	}
	valueKey := "value"
	value := 0
	if operation == "adjust" {
		valueKey = "delta"
		value = profile.Favorability
	}
	change, err := strconv.Atoi(strings.TrimSpace(configToolString(input, valueKey)))
	if err != nil {
		return 0, fmt.Errorf("%s 必须是整数", valueKey)
	}
	value += change
	if value < minimumFavorability || value > maximumFavorability {
		return 0, fmt.Errorf("好感度必须在 %d 到 %d 之间", minimumFavorability, maximumFavorability)
	}
	updated, err := store.UpdateUserMemory(ctx, MessageEvent{
		Kind:       t.event.Kind,
		GroupID:    t.event.GroupID,
		UserID:     targetID,
		SenderName: profile.DisplayName,
		MessageID:  t.event.MessageID,
	}, UserMemoryUpdate{
		OwnerID:                    t.runtime.effectiveConfigForEvent(t.event).OwnerID,
		SetFavorability:            &value,
		FavorabilityChangeSource:   "owner_" + operation,
		FavorabilityChangeReason:   relationshipChangeReason(operation, input),
		FavorabilityChangeOperator: strings.TrimSpace(t.event.UserID),
		Administrative:             true,
	})
	if err != nil {
		return 0, fmt.Errorf("保存用户好感度失败: %w", err)
	}
	return updated.Favorability, nil
}

func (t *dianaRelationshipTool) defaultTargetUserID() string {
	cfg := t.runtime.effectiveConfigForEvent(t.event)
	botIDs := map[string]bool{}
	for _, id := range []string{t.event.SelfID, cfg.BotAccount} {
		if id = strings.TrimSpace(id); id != "" {
			botIDs[id] = true
		}
	}
	for _, id := range mentionedUserIDs(t.event.Segments) {
		if !botIDs[id] {
			return id
		}
	}
	return strings.TrimSpace(t.event.UserID)
}

func (t *dianaRelationshipTool) resolveTargetMember(ctx context.Context, targetID string) (OneBotGroupMemberInfo, error) {
	if t.event.Kind != EventKindGroup || strings.TrimSpace(t.event.GroupID) == "" {
		if targetID != strings.TrimSpace(t.event.UserID) && !t.runtime.relationshipPolicy(ctx, t.event).Owner {
			return OneBotGroupMemberInfo{}, fmt.Errorf("私聊中只能查询自己的关系数据")
		}
		return OneBotGroupMemberInfo{UserID: targetID}, nil
	}

	directlyMentioned := false
	for _, id := range mentionedUserIDs(t.event.Segments) {
		if id == targetID {
			directlyMentioned = true
			break
		}
	}
	member, err := t.runtime.getGroupMemberInfoForEvent(ctx, t.event, t.event.GroupID, targetID)
	if err == nil && member.UserID != "" {
		return member, nil
	}
	if directlyMentioned || targetID == strings.TrimSpace(t.event.UserID) {
		return OneBotGroupMemberInfo{GroupID: t.event.GroupID, UserID: targetID}, nil
	}
	if err != nil {
		return OneBotGroupMemberInfo{}, fmt.Errorf("无法确认 QQ %s 是当前群成员: %w", targetID, err)
	}
	return OneBotGroupMemberInfo{}, fmt.Errorf("QQ %s 不是当前群成员", targetID)
}

func (t *dianaRelationshipTool) relationshipSnapshot(ctx context.Context, userID string, fallbackName string, historyLimit int) (dianaRelationshipSnapshot, error) {
	t.runtime.mu.RLock()
	store := t.runtime.userMemory
	t.runtime.mu.RUnlock()
	if store == nil {
		return dianaRelationshipSnapshot{}, fmt.Errorf("当前未启用用户关系存储")
	}
	profile, found, err := store.GetUserMemory(ctx, userID)
	if err != nil {
		return dianaRelationshipSnapshot{}, fmt.Errorf("读取用户关系失败: %w", err)
	}
	if !found {
		profile = UserMemoryProfile{UserID: userID}
	}
	profile.UserID = userID
	if strings.TrimSpace(profile.DisplayName) == "" {
		profile.DisplayName = firstNonEmpty(strings.TrimSpace(fallbackName), relationshipEventDisplayName(t.event, userID), userID)
	}
	policy := RelationshipPolicyFor(profile, t.runtime.effectiveConfigForEvent(t.event).OwnerID, userID)
	var recentChanges []UserFavorabilityChange
	if historyLimit > 0 {
		if historyStore, ok := store.(UserFavorabilityHistoryStore); ok {
			recentChanges, err = historyStore.ListUserFavorabilityChanges(ctx, userID, historyLimit)
			if err != nil {
				return dianaRelationshipSnapshot{}, fmt.Errorf("读取好感度变化记录失败: %w", err)
			}
		}
	}
	return dianaRelationshipSnapshot{
		UserID:           userID,
		DisplayName:      profile.DisplayName,
		Mention:          mentionMarkerFor(userID),
		Favorability:     profile.Favorability,
		MessageCount:     profile.MessageCount,
		RelationshipTier: policy.Tier,
		RelationshipName: policy.Name,
		ScheduleLimit:    policy.personalScheduleLimit(),
		CanGenerateImage: policy.AllowImageGeneration,
		CanEditImage:     policy.AllowImageEditing,
		CanDocumentOCR:   policy.AllowDocumentOCR,
		Owner:            policy.Owner,
		HasHistory:       found,
		RecentChanges:    recentChanges,
	}, nil
}

func (t *dianaRelationshipTool) listGroupRelationships(ctx context.Context, limit int) ([]dianaRelationshipSnapshot, error) {
	if t.event.Kind != EventKindGroup || strings.TrimSpace(t.event.GroupID) == "" {
		return nil, fmt.Errorf("关系榜单只能在群聊中查询")
	}
	members, err := t.runtime.getGroupMemberListForEvent(ctx, t.event, t.event.GroupID)
	if err != nil {
		return nil, fmt.Errorf("读取群成员列表失败: %w", err)
	}
	items := make([]dianaRelationshipSnapshot, 0, len(members))
	for _, member := range members {
		item, err := t.relationshipSnapshot(ctx, member.UserID, member.DisplayName(), 0)
		if err != nil {
			return nil, err
		}
		if !item.HasHistory {
			continue
		}
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Favorability != items[j].Favorability {
			return items[i].Favorability > items[j].Favorability
		}
		if items[i].MessageCount != items[j].MessageCount {
			return items[i].MessageCount > items[j].MessageCount
		}
		return items[i].UserID < items[j].UserID
	})
	if len(items) > limit {
		items = items[:limit]
	}
	return items, nil
}

func relationshipListLimit(input map[string]any) int {
	limit, err := strconv.Atoi(strings.TrimSpace(configToolString(input, "limit")))
	if err != nil || limit <= 0 {
		return defaultRelationshipListLimit
	}
	if limit > maximumRelationshipListLimit {
		return maximumRelationshipListLimit
	}
	return limit
}

func relationshipHistoryLimit(input map[string]any) int {
	limit, err := strconv.Atoi(strings.TrimSpace(configToolString(input, "history_limit")))
	if err != nil || limit <= 0 {
		return defaultRelationshipHistoryLimit
	}
	if limit > maximumRelationshipHistoryLimit {
		return maximumRelationshipHistoryLimit
	}
	return limit
}

func relationshipChangeReason(operation string, input map[string]any) string {
	if reason := strings.TrimSpace(configToolString(input, "reason")); reason != "" {
		return reason
	}
	if operation == "set" {
		return "主人手动设置好感度"
	}
	return "主人手动调整好感度"
}

func normalizeRelationshipUserID(raw string) string {
	raw = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(raw), "@"))
	// 参数里带着提及标记照样认。工具返回给模型的是 [diana-at:ID]，它可能原样
	// 抄回来；群消息原文里则是 CQ 码，同样可能被抄进参数。两种都剥掉。
	if match := dianaMentionMarkerPattern.FindStringSubmatch(raw); match != nil {
		raw = match[1]
	}
	if strings.HasPrefix(raw, "[CQ:at,qq=") && strings.HasSuffix(raw, "]") {
		raw = strings.TrimSuffix(strings.TrimPrefix(raw, "[CQ:at,qq="), "]")
	}
	if raw == "" {
		return ""
	}
	for _, char := range raw {
		if char < '0' || char > '9' {
			return ""
		}
	}
	return raw
}

func relationshipEventDisplayName(event MessageEvent, userID string) string {
	if strings.TrimSpace(event.UserID) == userID {
		return event.SenderNameOrID()
	}
	return ""
}

func marshalDianaRelationshipResult(result dianaRelationshipResult) (string, error) {
	if result.OK && result.ReplyGuidance == "" {
		result.ReplyGuidance = relationshipReplyGuidance
	}
	body, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return "", err
	}
	return string(body), nil
}
