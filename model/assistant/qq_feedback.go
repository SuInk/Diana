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

	"github.com/google/uuid"

	"github.com/SuInk/diana/model/applog"
	"github.com/SuInk/diana/model/llm"
)

// QQ 官方机器人单聊回复的赞踩反馈。对方点赞、点踩或取消时平台推来一条互动事件
// （INTERACTION_CREATE，data.type = 13），以前只打一行日志，点了等于没点。
// Diana 自己不往回复下挂赞踩栏；事件来自以前发出的消息上的赞踩栏，或客户端自带的反馈入口。
//
// 现在每次反馈做三件事：
//   - 记一条平台通知进事件页，写明是哪条回复、点了什么；
//   - 在这段私聊历史里留一条旁白，下一轮模型知道上一条回复被赞了还是被踩了；
//   - 点赞或点踩时旁路问一次模型要不要回一句（和戳一戳一样的小提示词），取消不回。
//
// 群聊的反馈不处理。
//
// 平台文档没有写这类事件的字段取值，所以解析放得很宽：checked 为 0 当取消，
// feedback_opt 按包含关系认赞和踩，认不出的原样记下、不回应。
const (
	qqFeedbackSubType     = "qq_feedback"
	qqFeedbackSegmentType = "qq_feedback"
	qqFeedbackHistoryID   = "local-feedback-"
	qqFeedbackHistoryHead = "[回复反馈] "

	qqFeedbackLike    = "like"
	qqFeedbackDislike = "dislike"
	qqFeedbackCancel  = "cancel"
	qqFeedbackUnknown = "unknown"

	// qqFeedbackReplyCooldown 是同一个人两次得到回应的最小间隔：来回点赞踩不该刷出一串话。
	qqFeedbackReplyCooldown = 60 * time.Second
	// qqFeedbackReplyMaxRunes 兜底裁剪生成结果。
	qqFeedbackReplyMaxRunes = 60
	// qqFeedbackExcerptRunes 是记录里带的被评价回复的开头长度。
	qqFeedbackExcerptRunes = 40
)

// qqFeedbackAction 把平台的 feedback_opt、checked 归成赞、踩、取消。
func qqFeedbackAction(opt string, checked int) string {
	if checked == 0 {
		return qqFeedbackCancel
	}
	opt = strings.ToLower(strings.TrimSpace(opt))
	for _, mark := range []string{"dislike", "down", "negative", "bad", "踩"} {
		if strings.Contains(opt, mark) {
			return qqFeedbackDislike
		}
	}
	for _, mark := range []string{"like", "up", "positive", "good", "赞"} {
		if strings.Contains(opt, mark) {
			return qqFeedbackLike
		}
	}
	return qqFeedbackUnknown
}

// qqOfficialFeedbackEvent 把单聊的赞踩互动映射成一条通知事件。MessageID 用外层事件 ID，
// 回应时据此走 event_id 被动回复；TargetID 是被评价的那条机器人消息。
func qqOfficialFeedbackEvent(eventID string, interaction qqOfficialInteraction, selfID string) (MessageEvent, bool) {
	eventID = strings.TrimSpace(eventID)
	userID := strings.TrimSpace(interaction.UserOpenID)
	if interaction.Data.Type != qqInteractionTypeFeedback || interaction.ChatType != qqInteractionChatC2C ||
		!strings.HasPrefix(eventID, qqInteractionEventPrefix) || userID == "" {
		return MessageEvent{}, false
	}
	resolved := interaction.Data.Resolved
	return MessageEvent{
		Platform:    PlatformQQOfficial,
		Kind:        EventKindNotice,
		SubType:     qqFeedbackSubType,
		MessageType: "private",
		Time:        platformEventTime(interaction.Timestamp),
		SelfID:      selfID,
		UserID:      userID,
		TargetID:    strings.TrimSpace(resolved.MessageID),
		MessageID:   eventID,
		Segments: []MessageSegment{{Type: qqFeedbackSegmentType, Data: map[string]string{
			"opt":     strings.TrimSpace(resolved.FeedbackOpt),
			"checked": strconv.Itoa(resolved.Checked),
		}}},
	}, true
}

// qqFeedbackFromEvent 取出通知里的反馈动作和平台原始取值。
func qqFeedbackFromEvent(event MessageEvent) (action, opt string, ok bool) {
	if event.Kind != EventKindNotice || event.SubType != qqFeedbackSubType {
		return "", "", false
	}
	for _, segment := range event.Segments {
		if segment.Type != qqFeedbackSegmentType {
			continue
		}
		checked, _ := strconv.Atoi(segment.Data["checked"])
		return qqFeedbackAction(segment.Data["opt"], checked), segment.Data["opt"], true
	}
	return "", "", false
}

// qqFeedbackPhrase 是「点了踩」这半句，excerpt 是被评价回复的开头，查不到时为空。
func qqFeedbackPhrase(action, opt, excerpt string) string {
	target := "回复"
	if excerpt != "" {
		target = "回复「" + excerpt + "」"
	}
	switch action {
	case qqFeedbackLike:
		return "给" + target + "点了赞"
	case qqFeedbackDislike:
		return "给" + target + "点了踩"
	case qqFeedbackCancel:
		return "取消了对" + target + "的赞踩"
	default:
		return "给" + target + "点了反馈（" + firstNonEmpty(strings.TrimSpace(opt), "未知") + "）"
	}
}

func isQQFeedbackHistoryEvent(event MessageEvent) bool {
	return event.SubType == qqFeedbackSubType && event.Kind == EventKindPrivate
}

// qqFeedbackHistoryPromptText 给模型看的那一行：标明是按钮反馈，不是对方说的话。
func qqFeedbackHistoryPromptText(event MessageEvent) string {
	text := strings.TrimSpace(strings.TrimPrefix(historyPlainText(event), strings.TrimSpace(qqFeedbackHistoryHead)))
	return historyLinePrefix(event) + "[回复反馈，不是对方说的话] 对方" + strings.Replace(text, "回复", "你的回复", 1)
}

// handleQQFeedbackNotice 处理一次赞踩反馈：记录、进历史，再决定回不回一句。
func (r *Runtime) handleQQFeedbackNotice(ctx context.Context, event MessageEvent) error {
	action, opt, ok := qqFeedbackFromEvent(event)
	if !ok || strings.TrimSpace(event.UserID) == "" || strings.TrimSpace(event.GroupID) != "" {
		return nil
	}
	cfg := r.effectiveConfigForEvent(event)
	chatEvent := event
	chatEvent.Kind = EventKindPrivate
	chatEvent.MessageType = "private"
	chatEvent.Segments = nil

	excerpt := r.qqFeedbackExcerpt(ctx, chatEvent)
	phrase := qqFeedbackPhrase(action, opt, excerpt)
	notice := event
	notice.RawMessage = "[通知] 对方" + phrase
	r.recordNoticeEvent(notice)
	r.rememberQQFeedback(chatEvent, phrase)

	if action != qqFeedbackLike && action != qqFeedbackDislike {
		r.recordQQFeedback(ctx, event, action, opt, "recorded", "", nil)
		return nil
	}
	if !r.admitsNotice(cfg, event) {
		r.recordQQFeedback(ctx, event, action, opt, "not_admitted", "", nil)
		return nil
	}
	if !r.claimQQFeedbackReply(event.ProfileID, event.UserID, time.Now()) {
		r.recordQQFeedback(ctx, event, action, opt, "cooldown", "", nil)
		return nil
	}
	reply, err := r.generateQQFeedbackReply(ctx, chatEvent, action, excerpt)
	if err != nil {
		// 生成失败就不说话：反馈已经记下了，没反应比报错好。
		r.recordQQFeedback(ctx, event, action, opt, "failed", "", err)
		return nil
	}
	if reply == "" {
		r.recordQQFeedback(ctx, event, action, opt, "silent", "", nil)
		return nil
	}
	// chatEvent.MessageID 是互动事件 ID，发送层据此按 event_id 被动回复。
	if err := r.send(ctx, chatEvent, reply); err != nil {
		r.recordQQFeedback(ctx, event, action, opt, "failed", reply, err)
		return err
	}
	r.record(EventRecord{
		At:        time.Now(),
		Kind:      event.Kind,
		Platform:  event.Platform,
		ProfileID: event.ProfileID,
		UserID:    event.UserID,
		MessageID: event.MessageID,
		Text:      notice.RawMessage,
		Reply:     reply,
		Handled:   true,
		Outcome:   "replied_qq_feedback",
		Decision:  "replied",
		Reason:    "对方" + phrase + "，回了一句",
	})
	r.recordQQFeedback(ctx, event, action, opt, "replied", reply, nil)
	return nil
}

// qqFeedbackExcerpt 按消息 ID 查回被评价的那条回复，取开头一段。查不到返回空。
func (r *Runtime) qqFeedbackExcerpt(ctx context.Context, chatEvent MessageEvent) string {
	messageID := strings.TrimSpace(chatEvent.TargetID)
	if messageID == "" {
		return ""
	}
	quoted, err := r.lookupQuotedMessageWithError(ctx, chatEvent, messageID)
	if err != nil || quoted == nil {
		return ""
	}
	text := strings.TrimSpace(PlainText(quoted.Segments))
	if text == "" {
		text = strings.TrimSpace(quoted.RawMessage)
	}
	return truncateRunes(strings.Join(strings.Fields(text), " "), qqFeedbackExcerptRunes)
}

// rememberQQFeedback 在私聊历史里记一条反馈旁白。它不是谁说的话，见 isNarrationHistoryEvent。
func (r *Runtime) rememberQQFeedback(chatEvent MessageEvent, phrase string) {
	text := qqFeedbackHistoryHead + phrase
	history := MessageEvent{
		Platform:         chatEvent.Platform,
		ProfileID:        chatEvent.ProfileID,
		ContextNamespace: chatEvent.ContextNamespace,
		Kind:             EventKindPrivate,
		MessageType:      "private",
		SubType:          qqFeedbackSubType,
		Time:             chatEvent.Time,
		SelfID:           chatEvent.SelfID,
		UserID:           chatEvent.UserID,
		TargetID:         chatEvent.TargetID,
		SenderName:       chatEvent.SenderName,
		MessageID:        qqFeedbackHistoryID + uuid.NewString(),
		RawMessage:       text,
		Segments:         []MessageSegment{{Type: "text", Data: map[string]string{"text": text}}},
	}
	if history.Time <= 0 {
		history.Time = time.Now().Unix()
	}
	r.remember(history)
}

// claimQQFeedbackReply 占用一次回应额度，冷却期内返回 false。
func (r *Runtime) claimQQFeedbackReply(profileID, userID string, now time.Time) bool {
	key := strings.TrimSpace(profileID) + "|" + strings.TrimSpace(userID)
	r.qqFeedbackMu.Lock()
	defer r.qqFeedbackMu.Unlock()
	if r.qqFeedbackLastReply == nil {
		r.qqFeedbackLastReply = map[string]time.Time{}
	}
	if last, ok := r.qqFeedbackLastReply[key]; ok && now.Sub(last) < qqFeedbackReplyCooldown {
		return false
	}
	for existing, at := range r.qqFeedbackLastReply {
		if now.Sub(at) > 24*time.Hour {
			delete(r.qqFeedbackLastReply, existing)
		}
	}
	r.qqFeedbackLastReply[key] = now
	return true
}

// promptQQFeedbackReplySpec 的输出是一个只有 reply 的 JSON，留空表示不回应。
var promptQQFeedbackReplySpec = registerPrompt(PromptSpec{
	Key:   "social.qq_feedback_reply",
	Group: PromptGroupSocial,
	Title: "回复被赞踩时的回应",
	Usage: "QQ 官方机器人单聊里，对方给一条回复点了赞或点了踩时，让模型决定要不要回一句。reply 留空表示不回应。",
	Default: "刚刚 {who} 在私聊里给你的{target}点了{action}（回复下面的赞踩按钮，没有文字）。语气要求：{tone}\n{recent_chat}\n" +
		"像真人一样决定要不要回应。被踩了：通常简短接一句，结合最近聊天猜猜哪里没答好，或者直接问对方哪里不对，不要长篇道歉，也不要重新回答一遍；" +
		"被赞了：多数时候不用说话，偶尔自然地接一句也行，不要每次都说谢谢。" +
		"reply 是 1 到 30 个字的一句话，自然口语，不解释什么是赞踩，不用括号描写动作；不回应时 reply 留空。" +
		"这一下只能回一句话：查不了东西，之后也不会有下文，所以不要答应「我再查查」「等我重新看看」这类接下来要做的事。",
	Contract: "只输出一个 JSON 对象：{\"reply\":\"\"}",
	Vars: []PromptVar{
		{Name: "who", Description: "点赞踩的人的昵称，没有昵称时是用户 ID"},
		{Name: "target", Description: "被评价的回复，带开头一段原文；查不到原文时是「一条回复」"},
		{Name: "action", Description: "「赞」或「踩」"},
		{Name: "tone", Description: "按双方好感度和关系给出的语气要求"},
		{Name: "recent_chat", Description: "最近几条聊天记录，没有时是一句「最近没有聊天记录。」"},
	},
})

// generateQQFeedbackReply 让模型决定被赞、被踩后要不要回一句，返回空串表示不回。
func (r *Runtime) generateQQFeedbackReply(ctx context.Context, chatEvent MessageEvent, action, excerpt string) (string, error) {
	ctx = withLLMUsagePurpose(withLLMUsageContext(ctx, chatEvent), PurposeQQFeedbackReply)
	profile, _ := r.loadUserMemoryProfile(ctx, chatEvent)
	cfg := r.effectiveConfigForEvent(chatEvent)
	policy := relationshipPolicyForEvent(cfg, profile, chatEvent)
	target := "一条回复"
	if excerpt != "" {
		target = "回复「" + excerpt + "」"
	}
	actionName := "赞"
	if action == qqFeedbackDislike {
		actionName = "踩"
	}
	instruction := cfg.promptf(promptQQFeedbackReplySpec, map[string]string{
		"who":         firstNonEmpty(strings.TrimSpace(profile.DisplayName), chatEvent.UserID),
		"target":      target,
		"action":      actionName,
		"tone":        policy.Tone,
		"recent_chat": r.pokeRecentChat(chatEvent),
	})
	messages := r.withUserFacingPersona(chatEvent, []llm.Message{{Role: llm.RoleUser, Content: instruction}})
	raw, err := r.runLLMRouterProvider(ctx, func(client LLMProvider) (string, error) {
		resp, err := client.Generate(ctx, llm.GenerateRequest{Messages: messages})
		if err != nil {
			return "", err
		}
		return resp.Text, nil
	})
	if err != nil {
		return "", err
	}
	return parseQQFeedbackReply(raw)
}

func parseQQFeedbackReply(raw string) (string, error) {
	text := strings.TrimSpace(stripJSONCodeFence(strings.TrimSpace(raw)))
	start, end := strings.IndexByte(text, '{'), strings.LastIndexByte(text, '}')
	if start < 0 || end < start {
		return "", fmt.Errorf("qq feedback reply output has no JSON object")
	}
	var out struct {
		Reply string `json:"reply"`
	}
	if err := json.Unmarshal([]byte(text[start:end+1]), &out); err != nil {
		return "", err
	}
	reply := strings.TrimSpace(out.Reply)
	if index := strings.IndexByte(reply, '\n'); index > 0 {
		reply = strings.TrimSpace(reply[:index])
	}
	return truncateRunesPlain(reply, qqFeedbackReplyMaxRunes), nil
}

// recordQQFeedback 把一次反馈和它的结果写进操作日志。
func (r *Runtime) recordQQFeedback(ctx context.Context, event MessageEvent, action, opt, result, reply string, err error) {
	writer := r.appLogWriter()
	if writer == nil {
		return
	}
	messages := map[string]string{
		"recorded":     "收到回复反馈，已记录",
		"not_admitted": "收到回复反馈，对方不在回复范围内，未回应",
		"cooldown":     "收到回复反馈，回应在冷却中",
		"silent":       "收到回复反馈，选择不回应",
		"replied":      "收到回复反馈，已回应",
		"failed":       "回复反馈回应失败，本次保持沉默",
	}
	entry := applog.Entry{
		Kind:    applog.KindOperation,
		Level:   applog.LevelInfo,
		Action:  "qq_feedback",
		Message: messages[result],
		Actor:   oneBotEventActor(event),
		Target:  event.TargetID,
		Metadata: map[string]any{
			"user_id":      event.UserID,
			"message_id":   event.TargetID,
			"feedback":     action,
			"feedback_opt": opt,
			"result":       result,
			"reply":        truncateRunesFromStart(reply, 120),
		},
	}
	if err != nil {
		entry.Kind = applog.KindError
		entry.Level = applog.LevelError
		entry.Detail = err.Error()
	}
	_ = writer.AppendLog(ctx, entry)
}
