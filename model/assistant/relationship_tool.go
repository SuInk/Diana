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
	"time"
)

const (
	defaultRelationshipListLimit    = 20
	maximumRelationshipListLimit    = 50
	defaultRelationshipHistoryLimit = 5
	maximumRelationshipHistoryLimit = 20
)

type dianaRelationshipTool struct {
	runtime *Runtime
	event   MessageEvent
}

type dianaRelationshipResult struct {
	Limited bool                        `json:"limited,omitempty"`
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
	"recent_changes 是最近几次增减分，回答好感度时默认顺带说一下：用一两句自然的话交代最近是涨是跌、大概什么时候、因为什么，不要逐条列成清单；没有记录就一个字都不提。用户追问细节时再把每一条的时间和原因说清楚。" +
	"回复里需要真正 @ 目标时，原样使用结果中的 mention_cq，不要写成普通文本的 @账号。" +
	"portrait 是这个人的长期画像，群里谁都查得到，被问到就照实说；但只在用户问起、或它和当前话题自然相关时才提，不要主动把整份画像念出来。"

type dianaRelationshipSnapshot struct {
	UserID      string `json:"user_id"`
	DisplayName string `json:"display_name"`
	// Mention 是可以直接抄进回复的提及标记，出站时按平台翻译。
	Mention          string `json:"mention"`
	Favorability     int    `json:"favorability"`
	MessageCount     int    `json:"message_count"`
	ScheduleLimit    int    `json:"reminder_schedule_limit"`
	CanGenerateImage bool   `json:"can_generate_image"`
	CanEditImage     bool   `json:"can_edit_image"`
	CanDocumentOCR   bool   `json:"can_document_ocr"`
	// Owner 说的是机器人的主人，不是群主，所以键名写成 bot_owner——群成员角色
	// 里的 owner 是群主，同名会让模型把两者混成一个人。
	Owner         bool                     `json:"bot_owner"`
	HasHistory    bool                     `json:"has_history"`
	RecentChanges []UserFavorabilityChange `json:"recent_changes,omitempty"`
	// Portrait 和好感度一样是群里公开的：谁都查得到别人的。写画像仍然要权限，
	// 见 runPortraitOperation。榜单不带它，那是体积考虑，不是可见性。
	Portrait []UserPortraitTrait `json:"portrait,omitempty"`
}

func newDianaRelationshipTool(runtime *Runtime, event MessageEvent) *dianaRelationshipTool {
	return &dianaRelationshipTool{runtime: runtime, event: event}
}

func (t *dianaRelationshipTool) Name() string {
	return "relationship"
}

func (t *dianaRelationshipTool) Description() string {
	// 「别猜、别说查不了」防模型凭上下文编好感度，或以隐藏数据为由拒查。
	// 能力问题转 capabilities：本工具早年带过权限清单，模型拿它当能力边界（d9b73dbe）。
	return `查好感度、关系等级、互动记录和人员画像；主人可设定或增减好感度；可写画像。` +
		`问自己、被 @ 或指定成员的好感度或关系时必须查，不要猜，也不要说查不了。` +
		`用户让记住或忘掉自己的住处、职业等个人情况时用 portrait_set/portrait_forget。` +
		`不返回能力清单，「你能做什么」用 capabilities。`
}

// InputSchema 声明参数契约。「拿到结果后怎么说话」不在这里，也不在 Description
// 里，而是随结果一起返回（见 relationshipReplyGuidance）。
// list 对群成员开放，说明里点明是防模型以隐私或权限为由拒绝排行榜。
// 画像栏目的完整含义在 portraitFieldSpecs，这里只留会写错的两栏。
func (t *dianaRelationshipTool) InputSchema() map[string]any {
	return toolObjectSchema([]string{"operation"}, map[string]any{
		"operation": toolEnumParam("list 群内好感排行，成员均可查，勿以隐私拒绝；set 设定、adjust 增减好感度，仅主人",
			"get", "list", "set", "adjust", "portrait_set", "portrait_forget"),
		"target_user_id": toolStringParam("目标账号，默认被 @ 者或发言者；set/adjust 必填且不能是主人"),
		"portrait_field": toolEnumParam("画像栏目；residence 只记城市，timezone 填 IANA 名，城市能定时区就一并记", PortraitFieldIDs()...),
		"portrait_value": toolStringParam("portrait_set：≤30 字第三人称短语，覆盖原内容"),
		"history_limit":  toolIntParam("get 返回的最近变化条数，默认 "+itoa(defaultRelationshipHistoryLimit), 1, maximumRelationshipHistoryLimit),
		"value":          toolIntParam("set：目标好感度", MinimumFavorability, MaximumFavorability),
		"delta":          toolIntParam("adjust：增减量，可为负", MinimumFavorability-MaximumFavorability, MaximumFavorability-MinimumFavorability),
		"reason":         toolStringParam("set/adjust 备注"),
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
		snapshot, err := t.relationshipSnapshot(ctx, targetID, member.DisplayName(), relationshipHistoryLimit(input), true)
		if err != nil {
			return "", err
		}
		return marshalDianaRelationshipResult(dianaRelationshipResult{
			OK:      true,
			Action:  "retrieved",
			Message: "已读取目标用户的关系数据；包含关系统计和人员画像，不包含长期记忆正文。",
			Target:  &snapshot,
		})
	case "list", "rank":
		// 榜单只是群内公开互动的统计（好感度、互动次数、关系等级），不含长期
		// 记忆正文，对全体成员开放；set/adjust 这类写操作仍然只有主人可用。
		items, err := t.listGroupRelationships(ctx, relationshipListLimit(input))
		if err != nil {
			return "", err
		}
		message := fmt.Sprintf("已读取当前群内 %d 位有互动记录成员的关系数据；榜单只有统计，要看某个人的画像用 operation=get 单查。", len(items))
		if !IsOneBotPlatform(t.runtime.currentPlatform(t.event)) {
			message += " 仅覆盖已知且能实时核验的成员，不是全群榜单。"
		}
		return marshalDianaRelationshipResult(dianaRelationshipResult{
			OK:      true,
			Action:  "listed",
			Limited: !IsOneBotPlatform(t.runtime.currentPlatform(t.event)),
			Message: message,
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
		ownerID := t.runtime.effectiveConfigForEvent(t.event).OwnerIDForEvent(t.event)
		if targetID == ownerID {
			// 主人的好感度现在也照常记录，但只由日常互动攒出来。挡掉自己给自己
			// 设分有两层理由：自己填的数不叫记录；而且主人说「给他加 5 分」时
			// 模型偶尔会把目标认成主人自己，这里正好兜住。
			return "", fmt.Errorf("主人的好感度由日常互动自动记录，不能自己给自己设置")
		}
		value, err := t.updatedFavorability(ctx, operation, targetID, input)
		if err != nil {
			return "", err
		}
		snapshot, err := t.relationshipSnapshot(ctx, targetID, "", relationshipHistoryLimit(input), true)
		if err != nil {
			return "", err
		}
		return marshalDianaRelationshipResult(dianaRelationshipResult{
			OK:      true,
			Action:  "updated",
			Message: fmt.Sprintf("已由主人将目标用户好感度更新为 %d；未增加互动次数。", value),
			Target:  &snapshot,
		})
	case "portrait_set", "portrait_forget":
		return t.runPortraitOperation(ctx, operation, input)
	default:
		return "", fmt.Errorf("operation 必须是 get、list、set、adjust、portrait_set 或 portrait_forget")
	}
}

// runPortraitOperation 记下或清空画像里的一栏。
//
// 画像归本人所有：默认改的就是当前发言者自己，只有主人能改别人的。这和好感度
// 相反——好感度是机器人对人的评价，只有主人能改；画像是人自己的情况，本人说了算。
func (t *dianaRelationshipTool) runPortraitOperation(ctx context.Context, operation string, input map[string]any) (string, error) {
	targetID := normalizeRelationshipUserID(configToolString(input, "target_user_id"))
	if targetID == "" {
		targetID = strings.TrimSpace(t.event.UserID)
	}
	if targetID == "" {
		return "", fmt.Errorf("没有找到要修改画像的用户")
	}
	if targetID != strings.TrimSpace(t.event.UserID) && !t.runtime.relationshipPolicy(ctx, t.event).Owner {
		return "", fmt.Errorf("只能修改自己的画像")
	}
	field, ok := NormalizePortraitField(configToolString(input, "portrait_field"))
	if !ok {
		return "", fmt.Errorf("portrait_field 必须是画像栏目之一：%s", strings.Join(PortraitFieldIDs(), "、"))
	}

	update := UserMemoryUpdate{Administrative: true}
	message := ""
	if operation == "portrait_set" {
		value := strings.TrimSpace(configToolString(input, "portrait_value"))
		trait, valid := NormalizePortraitTrait(UserPortraitTrait{
			Field:  field,
			Value:  value,
			Source: PortraitSourceManual,
		}, time.Now())
		if !valid {
			return "", fmt.Errorf("portrait_value 不能为空")
		}
		update.PortraitTraits = []UserPortraitTrait{trait}
		message = fmt.Sprintf("已把「%s」记进画像的%s栏。", trait.Value, trait.Label)
	} else {
		update.PortraitRemovals = []UserPortraitField{field}
		message = fmt.Sprintf("已清空画像的%s栏。", PortraitFieldLabel(field))
	}

	profile, written := t.runtime.writeUserMemory(t.targetMemoryEvent(targetID, ""), update)
	if !written {
		return "", fmt.Errorf("保存人员画像失败")
	}
	snapshot, err := t.relationshipSnapshot(ctx, targetID, "", 0, true)
	if err != nil {
		return "", err
	}
	snapshot.Portrait = profile.Portrait
	return marshalDianaRelationshipResult(dianaRelationshipResult{
		OK:      true,
		Action:  operation,
		Message: message,
		Target:  &snapshot,
	})
}

func (t *dianaRelationshipTool) updatedFavorability(ctx context.Context, operation string, targetID string, input map[string]any) (int, error) {
	t.runtime.mu.RLock()
	store := t.runtime.userMemory
	t.runtime.mu.RUnlock()
	if store == nil {
		return 0, fmt.Errorf("当前未启用用户关系存储")
	}
	profile, _, err := store.GetUserMemory(ctx, strings.TrimSpace(t.event.ProfileID), targetID)
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
	if value < MinimumFavorability || value > MaximumFavorability {
		return 0, fmt.Errorf("好感度必须在 %d 到 %d 之间", MinimumFavorability, MaximumFavorability)
	}
	updated, err := t.runtime.saveUserMemory(ctx, store, t.targetMemoryEvent(targetID, profile.DisplayName), UserMemoryUpdate{
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

// targetMemoryEvent 是主人改别人档案（好感度、画像、恋爱状态）时写库用的事件。
// 人员档案按机器人分行存，归属只认事件上的 ProfileID；这里统一从当前消息带过来，
// 别再各处手拼 MessageEvent——漏一个字段，写就落到空归属那一行，读的却还是本机
// 那一行，工具报「已更新」、实际什么都没变。
func (t *dianaRelationshipTool) targetMemoryEvent(targetID, displayName string) MessageEvent {
	return MessageEvent{
		Platform:         t.event.Platform,
		ProfileID:        t.event.ProfileID,
		ContextNamespace: t.event.ContextNamespace,
		Kind:             t.event.Kind,
		GroupID:          t.event.GroupID,
		UserID:           targetID,
		SenderName:       displayName,
		MessageID:        t.event.MessageID,
	}
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

// includePortrait 只管这次要不要把画像塞进结果，与权限无关——画像谁都能看。
func (t *dianaRelationshipTool) relationshipSnapshot(ctx context.Context, userID string, fallbackName string, historyLimit int, includePortrait bool) (dianaRelationshipSnapshot, error) {
	t.runtime.mu.RLock()
	store := t.runtime.userMemory
	t.runtime.mu.RUnlock()
	if store == nil {
		return dianaRelationshipSnapshot{}, fmt.Errorf("当前未启用用户关系存储")
	}
	profile, found, err := store.GetUserMemory(ctx, strings.TrimSpace(t.event.ProfileID), userID)
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
	policyConfig := t.runtime.effectiveConfigForEvent(t.event)
	policyConfig.OwnerID = policyConfig.OwnerIDForEvent(t.event)
	policy := RelationshipPolicyForConfig(policyConfig, profile, userID)
	var recentChanges []UserFavorabilityChange
	if historyLimit > 0 {
		if historyStore, ok := store.(UserFavorabilityHistoryStore); ok {
			recentChanges, err = historyStore.ListUserFavorabilityChanges(ctx, strings.TrimSpace(t.event.ProfileID), userID, historyLimit)
			if err != nil {
				return dianaRelationshipSnapshot{}, fmt.Errorf("读取好感度变化记录失败: %w", err)
			}
		}
	}
	var portrait []UserPortraitTrait
	if includePortrait {
		portrait = profile.Portrait
	}
	return dianaRelationshipSnapshot{
		UserID:           userID,
		DisplayName:      profile.DisplayName,
		Mention:          mentionMarkerFor(userID),
		Favorability:     profile.Favorability,
		MessageCount:     profile.MessageCount,
		ScheduleLimit:    policy.personalScheduleLimit(),
		CanGenerateImage: policy.AllowImageGeneration,
		CanEditImage:     policy.AllowImageEditing,
		CanDocumentOCR:   policy.AllowDocumentOCR,
		Owner:            policy.Owner,
		HasHistory:       found,
		RecentChanges:    recentChanges,
		Portrait:         portrait,
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
	if !IsOneBotPlatform(t.runtime.currentPlatform(t.event)) {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 6*time.Second)
		defer cancel()
	}
	for _, member := range members {
		if ctx.Err() != nil {
			break
		}
		// 榜单不带画像，理由是体积不是权限：一次最多列 50 人，每人七栏画像会把
		// 结果撑到十几 KB，而「谁好感度最高」根本用不到。要看某个人的画像，
		// 用 operation=get 单查，那条路谁都走得通。
		item, err := t.relationshipSnapshot(ctx, member.UserID, member.DisplayName(), 0, false)
		if err != nil {
			return nil, err
		}
		if !item.HasHistory {
			continue
		}
		if !IsOneBotPlatform(t.runtime.currentPlatform(t.event)) {
			if _, err := t.runtime.getGroupMemberInfoForEvent(ctx, t.event, t.event.GroupID, member.UserID); err != nil {
				continue
			}
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
	raw = stripAccountIDMarkup(raw)
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

// stripAccountIDMarkup 去掉账号 ID 外面的空白、@ 和提及标记，不管 ID 本身长什么样。
// 参数里带着提及标记照样认。工具返回给模型的是 [diana-at:ID]，它可能原样抄回来；
// 群消息原文里则是 CQ 码，同样可能被抄进参数。两种都剥掉。
func stripAccountIDMarkup(raw string) string {
	raw = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(raw), "@"))
	if match := dianaMentionMarkerPattern.FindStringSubmatch(raw); match != nil {
		raw = match[1]
	}
	if strings.HasPrefix(raw, "[CQ:at,qq=") && strings.HasSuffix(raw, "]") {
		raw = strings.TrimSuffix(strings.TrimPrefix(raw, "[CQ:at,qq="), "]")
	}
	return strings.TrimSpace(raw)
}

// sameAccountID 判断两个账号 ID 是不是同一个人。
//
// normalizeRelationshipUserID 只认纯数字账号，飞书 ou_xxx、QQ 官方 openid、钉钉和
// 企业微信的 userid 都会被它变成空串——拿它比身份，两个不同的非数字账号就成了
// 「"" == ""」。所以先比去掉标记后的原文，两边都是数字账号时再按数字比；空 ID 不和
// 任何人相同。
func sameAccountID(a, b string) bool {
	rawA, rawB := stripAccountIDMarkup(a), stripAccountIDMarkup(b)
	if rawA == "" || rawB == "" {
		return false
	}
	if rawA == rawB {
		return true
	}
	numericA, numericB := normalizeRelationshipUserID(a), normalizeRelationshipUserID(b)
	return numericA != "" && numericA == numericB
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
