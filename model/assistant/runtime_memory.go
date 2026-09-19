// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/SuInk/diana/model/applog"
	"github.com/SuInk/diana/model/llm"
)

// SetUserMemoryStore 注入持久用户画像存储，用于记住所有人的长期偏好和好感度。
func (r *Runtime) SetUserMemoryStore(store UserMemoryStore) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.userMemory = store
}

// SetStructuredMemoryStore injects the durable extraction queue and layered
// long-term memory view. Relationship profiles remain in UserMemoryStore.
func (r *Runtime) SetStructuredMemoryStore(store StructuredMemoryStore) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.structuredMemory = store
}

// SetNotebookStore 注入笔记本存储。没有它时笔记本整体静默失效：自动命中查不到、
// notebook 明确报错，回复本身不受影响。
func (r *Runtime) SetNotebookStore(store NotebookStore) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.notebook = store
}

// claimSourceRecallEnabled 决定是否把结论引用的来源留到之后几轮供追问使用。
func (r *Runtime) claimSourceRecallEnabled(event MessageEvent) bool {
	settings, enabled := r.webSearchPluginSettings(event)
	if !enabled {
		return false
	}
	return settings.Bool(webSearchSettingSourceRecall, true)
}

func (r *Runtime) withUserFacingPersona(event MessageEvent, messages []llm.Message) []llm.Message {
	cfg := r.effectiveConfigForEvent(event)
	// 语气锚点和风格描述一起注入，让这条旁路的说话方式与主回复链路保持一致。
	voice := personaVoiceFrom(cfg.SelfReference, cfg.SentenceEnders)
	actionsEnabled := boolValue(cfg.ActionDescriptionEnabled, false)
	// 时段语气这条旁路也要带上：漏了的话同一台机器人两条链路在深夜的语气不一样。
	// 心情同理——主链路蔫着、旁路却活蹦乱跳，一台机器人像两个人。
	persona := strings.TrimSpace(cfg.SystemPrompt + "\n" + replyPresentationPrompt(!chatSplitLimitsForEvent(cfg, event).SingleMessage, voice) + "\n" + replyLineBreakPrompt(cfg) + "\n" + actionDescriptionPrompt(actionsEnabled) + "\n" + dayPartToneForConfig(cfg, r.clock()) + "\n" + r.moodToneForConfig(cfg, event.ProfileID) + "\n" + personaClosingAnchor() + "\n" + actionDescriptionClosingAnchor(actionsEnabled))
	if persona == "" {
		return messages
	}
	for _, message := range messages {
		content := strings.TrimSpace(message.Content)
		if message.Role == llm.RoleSystem && (content == persona || strings.HasPrefix(content, persona+"\n")) {
			return messages
		}
	}
	result := make([]llm.Message, 0, len(messages)+1)
	result = append(result, llm.Message{Role: llm.RoleSystem, Content: persona, Priority: llm.MessagePrioritySystem})
	return append(result, messages...)
}

func recallForwardTextFallback(messages []OutgoingMessage) []OutgoingMessage {
	out := make([]OutgoingMessage, 0, len(messages))
	for _, msg := range messages {
		text := strings.TrimSpace(msg.Text)
		if text == "" {
			text = strings.TrimSpace(PlainText(msg.Segments))
		}
		if text == "" && (len(msg.ImageURLs) > 0 || hasReplyCandidateImage(msg.Segments)) {
			text = "图片转发失败，原图未包含在本条文本回退中。"
		}
		if text == "" && len(msg.VideoURLs) > 0 {
			text = "[视频]"
		}
		if text == "" {
			text = "[无法转发的消息]"
		}
		out = append(out, OutgoingMessage{
			Text:        text,
			ForwardName: msg.ForwardName,
			ForwardUIN:  msg.ForwardUIN,
			ForwardTime: msg.ForwardTime,
		})
	}
	return out
}

func recallReplyShouldAutoDelete(cfg BotConfig, responses []PluginResponse) bool {
	cfg = cfg.WithDefaults()
	if cfg.RecallReplyAutoDeleteEnabled == nil || !*cfg.RecallReplyAutoDeleteEnabled {
		return false
	}
	for _, response := range responses {
		if response.RecallDisclosure {
			return true
		}
	}
	return false
}

func recallReplyAutoDeleteDelay(cfg BotConfig) time.Duration {
	seconds := cfg.WithDefaults().RecallReplyTTLSeconds
	return time.Duration(seconds) * time.Second
}

func (r *Runtime) recordRecallReplyDelete(event MessageEvent, messageID string, delay time.Duration, deleteErr error) {
	writer := r.appLogWriter()
	if deleteErr != nil {
		log.Printf("diana recall disclosure auto-delete failed: message_id=%s: %v", messageID, deleteErr)
	}
	if writer == nil {
		return
	}
	entry := applog.Entry{
		Kind:    applog.KindOperation,
		Level:   applog.LevelInfo,
		Action:  "diana.recall_reply.auto_delete",
		Message: "撤回记录回复已自动撤回",
		Actor:   oneBotEventActor(event),
		Target:  messageID,
		Metadata: map[string]any{
			"group_id":      event.GroupID,
			"source_id":     event.MessageID,
			"delay_seconds": int64(delay.Seconds()),
		},
	}
	if deleteErr != nil {
		entry.Kind = applog.KindError
		entry.Level = applog.LevelError
		entry.Message = "撤回记录回复自动撤回失败"
		entry.Detail = deleteErr.Error()
	}
	_ = writer.AppendLog(context.Background(), entry)
}

func (r *Runtime) updateUserMemory(event MessageEvent, favorabilityDelta int) (UserMemoryProfile, bool) {
	// 从积压包里取出来当回复对象的消息，进包时已经记过这一次互动，别再数一遍。
	if event.backlogHeld && favorabilityDelta == 0 && event.userProfileLoaded {
		return event.userProfile, true
	}
	return r.writeUserMemory(event, UserMemoryUpdate{FavorabilityDelta: favorabilityDelta})
}

// applyEvaluatedRelationshipUpdate 落库一次后台评估的结果：好感度增减和这一轮
// 观察到的画像。两者一起写，因为它们出自同一次评估——分两次写就是同一条消息在
// 档案上留下两次修改。
//
// Administrative=true：评估是回复之后的后台动作，不是一次新的互动，不该再加一
// 次互动次数——那一次在回复时已经记过了。
func (r *Runtime) applyEvaluatedRelationshipUpdate(event MessageEvent, favorabilityDelta int, reason string, traits []UserPortraitTrait) (UserMemoryProfile, bool) {
	return r.writeUserMemory(event, UserMemoryUpdate{
		FavorabilityDelta:        favorabilityDelta,
		FavorabilityChangeSource: "interaction",
		FavorabilityChangeReason: strings.TrimSpace(reason),
		PortraitTraits:           traits,
		Administrative:           true,
	})
}

func (r *Runtime) writeUserMemory(event MessageEvent, update UserMemoryUpdate) (UserMemoryProfile, bool) {
	if strings.TrimSpace(event.UserID) == "" {
		return UserMemoryProfile{}, false
	}
	r.mu.RLock()
	store := r.userMemory
	r.mu.RUnlock()
	if store == nil {
		return UserMemoryProfile{}, false
	}
	cfg := r.effectiveConfigForEvent(event)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	update.OwnerID = cfg.OwnerIDForEvent(event)
	profile, err := store.UpdateUserMemory(ctx, event, update)
	if err != nil {
		log.Printf("diana user memory update failed: %v", err)
		return UserMemoryProfile{}, false
	}
	return profile, true
}

func (r *Runtime) loadUserMemoryProfile(ctx context.Context, event MessageEvent) (UserMemoryProfile, bool) {
	userID := strings.TrimSpace(event.UserID)
	if userID == "" {
		return UserMemoryProfile{}, false
	}
	r.mu.RLock()
	store := r.userMemory
	r.mu.RUnlock()
	if store == nil {
		return UserMemoryProfile{UserID: userID, DisplayName: event.SenderNameOrID()}, false
	}
	loadCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	profile, ok, err := store.GetUserMemory(loadCtx, strings.TrimSpace(event.ProfileID), userID)
	if err != nil {
		log.Printf("diana user memory load failed: %v", err)
		return UserMemoryProfile{UserID: userID, DisplayName: event.SenderNameOrID()}, false
	}
	if !ok {
		return UserMemoryProfile{UserID: userID, DisplayName: event.SenderNameOrID()}, false
	}
	if profile.DisplayName == "" {
		profile.DisplayName = event.SenderNameOrID()
	}
	return profile, true
}

func formatUserMemoryContext(profile UserMemoryProfile, policy RelationshipPolicy) string {
	if profile.UserID == "" {
		return ""
	}
	var builder strings.Builder
	displayName := strings.TrimSpace(profile.DisplayName)
	if displayName == "" {
		displayName = profile.UserID
	}
	builder.WriteString("【当前发言者长期记忆，仅用于理解语气和关系，不要直接复述】\n")
	builder.WriteString("用户：")
	builder.WriteString(displayName)
	builder.WriteString("（")
	builder.WriteString(profile.UserID)
	builder.WriteString("）\n")
	builder.WriteString("好感度：")
	builder.WriteString(strconv.Itoa(profile.Favorability))
	builder.WriteString("\n关系等级：")
	builder.WriteString(policy.Name)
	// 不再列「已授权能力」：那份清单每个等级都一样，摆在这里只会被当成本等级
	// 的特权复述出去。能力问题由 capabilities 负责。
	//
	// 语气要求和恋爱关系也不在这里重复：它们由 relationshipPermissionContext 放在
	// 紧挨生成的系统尾部，那份优先级更高、不会被预算裁掉。两处各写一遍既浪费
	// token，改了一处还会自相矛盾。
	builder.WriteString("\n互动次数：")
	builder.WriteString(strconv.Itoa(profile.MessageCount))
	if lines := FormatPortraitLines(profile.Portrait); lines != "" {
		builder.WriteString("\n人员画像（当前发言者的长期情况，只在自然相关时用上，不要主动背出来）：")
		builder.WriteString(lines)
	}
	// 这里不再列 profile.Memories。那份东西是原始发言的环形缓冲，不是长期记忆：
	// 没有模型参与，不做相关性检索，@ 和回复标记也原样留着。同群聊天时它和最近
	// 历史逐条重复，跨群带过去的也只是几句原话。真正的长期记忆由结构化记忆检索
	// 负责（见 formatStructuredMemoryContextWithTokenBudgetDetailed），这条兜底
	// 路径只保留关系核心和画像。
	return truncateRunesFromStart(builder.String(), 1800)
}

func (r *Runtime) recallHistory(event MessageEvent) []MessageEvent {
	if event.Kind != EventKindGroup || strings.TrimSpace(event.GroupID) == "" {
		return nil
	}
	r.mu.RLock()
	store := r.messageStore
	r.mu.RUnlock()
	recallStore, ok := store.(GroupRecallHistoryStore)
	if !ok {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	events, err := recallStore.ListGroupRecallEvents(ctx, event.GroupID)
	if err != nil {
		log.Printf("diana recall history load failed: %v", err)
		return nil
	}
	return events
}

func (r *Runtime) enrichRecallNotice(ctx context.Context, event MessageEvent) MessageEvent {
	if !isRecallNotice(event) || recallEventHasContent(event) || strings.TrimSpace(event.MessageID) == "" {
		return event
	}
	r.mu.RLock()
	store := r.messageStore
	r.mu.RUnlock()
	lookup, ok := store.(MessageEventLookupStore)
	var record MessageEvent
	found := false
	if ok {
		loadCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		var err error
		record, found, err = lookup.FindMessageEvent(loadCtx, sessionKey(event), event.MessageID)
		cancel()
		if err != nil {
			log.Printf("diana recalled message load failed: %v", err)
		}
	}
	if !found && r.channel != nil {
		callCtx, callCancel := context.WithTimeout(ctx, 3*time.Second)
		data, callErr := r.callOneBotAPIForEvent(callCtx, event, "get_msg", map[string]any{"message_id": oneBotMessageIDParam(event.MessageID)})
		callCancel()
		if callErr != nil {
			log.Printf("diana recalled message get_msg failed: message_id=%s: %v", event.MessageID, callErr)
		} else {
			session := HistorySession{Kind: EventKindPrivate, ID: event.UserID}
			if event.GroupID != "" {
				session = HistorySession{Kind: EventKindGroup, ID: event.GroupID}
			}
			if recovered, ok := r.historyEventFromData(session, data); ok {
				record = recovered
				found = true
				r.persistMessageEvent(recovered)
			}
		}
	}
	if !found {
		return event
	}
	event.OriginalTime = record.Time
	event.RawMessage = record.RawMessage
	event.Segments = append([]MessageSegment(nil), record.Segments...)
	event.SenderName = record.SenderName
	event.Quoted = record.Quoted
	if event.UserID == "" {
		event.UserID = record.UserID
	}
	return event
}

func isRecallNotice(event MessageEvent) bool {
	return event.Kind == EventKindNotice && (event.SubType == "group_recall" || event.SubType == "friend_recall")
}

func (r *Runtime) isBotOwnRecall(event MessageEvent) bool {
	if !isRecallNotice(event) {
		return false
	}
	botAccount := firstNonEmpty(r.profileConfig(event.ProfileID).BotAccount, event.SelfID)
	return botAccount != "" && event.UserID == botAccount && event.OperatorID == botAccount
}
