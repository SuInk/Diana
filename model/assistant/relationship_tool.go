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
	"回复里需要真正 @ 目标时，原样使用结果中的 mention_cq，不要写成普通文本的 @账号。" +
	"portrait 是这个人的长期画像，群里谁都查得到，被问到就照实说；但只在用户问起、或它和当前话题自然相关时才提，不要主动把整份画像念出来。"

type dianaRelationshipSnapshot struct {
	UserID      string `json:"user_id"`
	DisplayName string `json:"display_name"`
	// Mention 是可以直接抄进回复的提及标记，出站时按平台翻译。
	Mention          string           `json:"mention"`
	Favorability     int              `json:"favorability"`
	MessageCount     int              `json:"message_count"`
	RelationshipTier RelationshipTier `json:"relationship_tier"`
	RelationshipName string           `json:"relationship_name"`
	ScheduleLimit    int              `json:"reminder_schedule_limit"`
	CanGenerateImage bool             `json:"can_generate_image"`
	CanEditImage     bool             `json:"can_edit_image"`
	CanDocumentOCR   bool             `json:"can_document_ocr"`
	// Owner 说的是机器人的主人，不是群主，所以键名写成 bot_owner——群成员角色
	// 里的 owner 是群主，同名会让模型把两者混成一个人。
	Owner      bool `json:"bot_owner"`
	HasHistory bool `json:"has_history"`
	// Romance 系列只在人机恋开启且目标是恋人时才有值，见 RelationshipPolicy。
	Romance       bool                     `json:"romance,omitempty"`
	RomanceDays   int                      `json:"romance_days,omitempty"`
	RomanceNote   string                   `json:"romance_note,omitempty"`
	RecentChanges []UserFavorabilityChange `json:"recent_changes,omitempty"`
	// Portrait 和好感度一样是群里公开的：谁都查得到别人的。写画像仍然要权限，
	// 见 runPortraitOperation。榜单不带它，那是体积考虑，不是可见性。
	Portrait []UserPortraitTrait `json:"portrait,omitempty"`
}

func newDianaRelationshipTool(runtime *Runtime, event MessageEvent) *dianaRelationshipTool {
	return &dianaRelationshipTool{runtime: runtime, event: event}
}

func (t *dianaRelationshipTool) Name() string {
	return "diana.relationship"
}

func (t *dianaRelationshipTool) Description() string {
	return `查询 Diana 对用户的好感度、关系等级、互动次数、最近的增减分记录和人员画像（居住地点、职业、作息、生活习惯、兴趣爱好、家庭关系）。用户说“记住我住在……/我是做……的”，或要求改掉、忘掉画像里的某一栏时，调用本工具的 portrait_set / portrait_forget。用户询问自己、被 @ 成员或指定群成员的好感度或关系时必须调用，不要根据上下文猜测，也不要声称无法查询隐藏数据。人机恋模式开启时，用户本人明确表白用 romance_start，明确提出分手用 romance_end。本工具不返回能力清单——用户问「你能做什么」应改用 diana.capabilities，基础能力对所有关系等级一律开放。`
}

// InputSchema 声明参数契约。「拿到结果后怎么说话」不在这里，也不在 Description
// 里，而是随结果一起返回（见 relationshipReplyGuidance）。
func (t *dianaRelationshipTool) InputSchema() map[string]any {
	return toolObjectSchema([]string{"operation"}, map[string]any{
		"operation": toolEnumParam("要执行的操作：get 查单个用户；list 查当前群内已有互动记录的成员并按好感度排序（群内成员均可使用，不得以隐私或权限为由拒绝）；set 直接设置、adjust 增减好感度，后两者仅机器人主人可用且不增加互动次数；portrait_set 记下画像里的一栏，portrait_forget 清空画像里的一栏；romance_start 在当前发言者本人明确表白时确立恋人关系（单偶：已有恋人或好感度不够都会被婉拒），romance_end 在本人提出分手时解除，两者都要求人机恋模式已开启。",
			"get", "list", "set", "adjust", "portrait_set", "portrait_forget", "romance_start", "romance_end"),
		"target_user_id": toolStringParam("目标账号。get 可省略：消息里 @ 了成员就查该成员，否则查当前发言者；set 和 adjust 必填，且不能指向主人自己（主人的好感度由互动自动记录）；画像操作省略表示当前发言者，只有主人能改别人的画像。"),
		"portrait_field": toolEnumParam("portrait_set 和 portrait_forget 必填：要写或要清空的画像栏目，"+portraitFieldSchemaHint(), PortraitFieldIDs()...),
		"portrait_value": toolStringParam("portrait_set 必填：这一栏的新内容，写成不超过 30 字的第三人称短语，例如“住在杭州”。同一栏原有内容会被顶掉。"),
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
		return marshalDianaRelationshipResult(dianaRelationshipResult{
			OK:      true,
			Action:  "listed",
			Message: fmt.Sprintf("已读取当前群内 %d 位有互动记录成员的关系数据；榜单只有统计，要看某个人的画像用 operation=get 单查。", len(items)),
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
	case "romance_start", "romance_end":
		return t.runRomanceOperation(ctx, operation, input)
	default:
		return "", fmt.Errorf("operation 必须是 get、list、set、adjust、portrait_set、portrait_forget、romance_start 或 romance_end")
	}
}

// runRomanceOperation 确立或解除恋人关系。
//
// 恋爱是当事人自己的事：romance_start 只对当前发言者本人生效，主人也不能替别人
// 表白；romance_end 本人随时可用，主人可以替任何人解除（处理骚扰或代已离群的人
// 收尾）。达不到门槛时返回 declined 而不是错误——「还不到时候」是关系的正常状态，
// 不是故障，模型拿到它才能好好把话说软。
func (t *dianaRelationshipTool) runRomanceOperation(ctx context.Context, operation string, input map[string]any) (string, error) {
	cfg := t.runtime.effectiveConfigForEvent(t.event)
	if !boolValue(cfg.RomanceEnabled, false) {
		return "", fmt.Errorf("人机恋模式未开启：需要主人先在控制台的机器人设置里打开恋爱模式")
	}
	speakerID := strings.TrimSpace(t.event.UserID)
	targetID := normalizeRelationshipUserID(configToolString(input, "target_user_id"))
	if targetID == "" {
		targetID = speakerID
	}
	if targetID == "" {
		return "", fmt.Errorf("没有找到要操作的用户")
	}
	if operation == "romance_start" && targetID != speakerID {
		return "", fmt.Errorf("恋人关系只能由本人当面确立，不能替别人表白")
	}
	if operation == "romance_end" && targetID != speakerID && !t.runtime.relationshipPolicy(ctx, t.event).Owner {
		return "", fmt.Errorf("只能解除自己的恋人关系")
	}

	t.runtime.mu.RLock()
	store := t.runtime.userMemory
	t.runtime.mu.RUnlock()
	if store == nil {
		return "", fmt.Errorf("当前未启用用户关系存储")
	}
	profile, _, err := store.GetUserMemory(ctx, strings.TrimSpace(t.event.ProfileID), targetID)
	if err != nil {
		return "", fmt.Errorf("读取用户关系失败: %w", err)
	}

	writeState := func(state *UserRomanceState) error {
		_, err := store.UpdateUserMemory(ctx, MessageEvent{
			Kind:       t.event.Kind,
			GroupID:    t.event.GroupID,
			UserID:     targetID,
			SenderName: profile.DisplayName,
			MessageID:  t.event.MessageID,
			ProfileID:  t.event.ProfileID,
		}, UserMemoryUpdate{
			OwnerID:        cfg.OwnerID,
			SetRomance:     state,
			Administrative: true,
		})
		return err
	}

	if operation == "romance_end" {
		if !romanceActive(profile) {
			return marshalDianaRelationshipResult(dianaRelationshipResult{
				OK:      true,
				Action:  "noop",
				Message: "目标用户和机器人当前不是恋人关系，无需解除。",
			})
		}
		if err := writeState(&UserRomanceState{Active: false}); err != nil {
			return "", fmt.Errorf("保存恋爱关系失败: %w", err)
		}
		snapshot, err := t.relationshipSnapshot(ctx, targetID, "", 0, false)
		if err != nil {
			return "", err
		}
		return marshalDianaRelationshipResult(dianaRelationshipResult{
			OK:      true,
			Action:  "romance_ended",
			Message: "恋人关系已解除。好感度、画像和记忆都保留，按当前关系等级正常相处；语气尊重对方的决定，好聚好散，不纠缠、不报复性冷淡。",
			Target:  &snapshot,
		})
	}

	if romanceActive(profile) {
		snapshot, err := t.relationshipSnapshot(ctx, targetID, "", 0, false)
		if err != nil {
			return "", err
		}
		return marshalDianaRelationshipResult(dianaRelationshipResult{
			OK:      true,
			Action:  "noop",
			Message: "你们已经是恋人了，不需要再确立一次；可以顺着这份心意回应对方。",
			Target:  &snapshot,
		})
	}
	// 恋爱是单偶的：已经有恋人时，任何人的表白都被婉拒，好感度再高也一样。
	// 这个理由比门槛更根本，所以排在门槛之前——拒绝的原因要说真话。
	if _, taken := t.runtime.currentRomancePartner(ctx, t.event.ProfileID, targetID); taken {
		return marshalDianaRelationshipResult(dianaRelationshipResult{
			OK:     true,
			Action: "declined",
			Message: "机器人婉拒了这次表白：它已经有确立关系的恋人了，同一时间只会有一位。" +
				"用自己的语气温柔而明确地拒绝：可以说明自己已经有喜欢的人，不要透露对方是谁，" +
				"不要嘲讽，也不要留下模糊的希望；对方可以继续做重要的朋友。",
		})
	}
	// 主人身份免门槛没有道理：恋爱看的是相处，不是权限。门槛对谁都一样。
	if profile.Favorability < romanceStartMinFavorability || profile.MessageCount < romanceStartMinMessages {
		return marshalDianaRelationshipResult(dianaRelationshipResult{
			OK:     true,
			Action: "declined",
			Message: fmt.Sprintf(
				"机器人婉拒了这次表白：当前好感度 %d、累计互动 %d 次，还没到确立恋人关系的门槛（好感度 %d 且互动 %d 次以上）。用自己的语气温柔地把话说软：说明现在还想再多相处、多了解一下，不要报出具体数字和门槛，不要嘲讽，也不要把话说死。",
				profile.Favorability, profile.MessageCount, romanceStartMinFavorability, romanceStartMinMessages),
		})
	}
	if err := writeState(&UserRomanceState{Active: true, Since: time.Now().UTC(), StartedBy: "user"}); err != nil {
		return "", fmt.Errorf("保存恋爱关系失败: %w", err)
	}
	snapshot, err := t.relationshipSnapshot(ctx, targetID, "", 0, false)
	if err != nil {
		return "", err
	}
	return marshalDianaRelationshipResult(dianaRelationshipResult{
		OK:      true,
		Action:  "romance_started",
		Message: "恋人关系已确立，从现在开始记纪念日。用自己的语气自然地答应下来；关系只改变语气和相处方式，不改变任何权限。",
		Target:  &snapshot,
	})
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

	profile, written := t.runtime.writeUserMemory(MessageEvent{
		Kind:      t.event.Kind,
		GroupID:   t.event.GroupID,
		UserID:    targetID,
		MessageID: t.event.MessageID,
		ProfileID: t.event.ProfileID,
	}, update)
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

// portraitFieldSchemaHint 把栏目表拼成一句枚举说明，字段和含义只维护在
// portraitFieldSpecs 一处。
func portraitFieldSchemaHint() string {
	parts := make([]string, 0, len(portraitFieldSpecs))
	for _, spec := range portraitFieldSpecs {
		parts = append(parts, string(spec.Field)+"="+spec.Label+"（"+spec.Hint+"）")
	}
	return strings.Join(parts, "；")
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
	policy := RelationshipPolicyForConfig(t.runtime.effectiveConfigForEvent(t.event), profile, userID)
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
		RelationshipTier: policy.Tier,
		RelationshipName: policy.Name,
		ScheduleLimit:    policy.personalScheduleLimit(),
		CanGenerateImage: policy.AllowImageGeneration,
		CanEditImage:     policy.AllowImageEditing,
		CanDocumentOCR:   policy.AllowDocumentOCR,
		Owner:            policy.Owner,
		HasHistory:       found,
		Romance:          policy.Romance,
		RomanceDays:      policy.RomanceDays,
		RomanceNote:      policy.RomanceNote,
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
	for _, member := range members {
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
