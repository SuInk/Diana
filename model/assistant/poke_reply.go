// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/SuInk/diana/model/applog"
	"github.com/SuInk/diana/model/llm"
)

// 戳一戳响应：有人在 QQ 上戳机器人（OneBot 的 notify/poke 通知），它回一句。
//
// 戳一戳没有正文，走不了正常的消息回复链路；这里用旁路生成——人设加关系语气
// 拼一条小提示词，让模型按角色回一句短的。生成失败就沉默：被戳了没反应像发呆，
// 被戳了报错像故障，前者好得多。
//
// 冷却按「机器人×戳的人」记：连着戳是常见的玩法，条条都回会刷屏，也给了
// 刷戳的人免费的注意力。冷却期内的戳直接忽略，不攒着。

const (
	// pokeReplyCooldown 是同一个人两次得到回应的最小间隔。
	pokeReplyCooldown = 90 * time.Second
	// pokeReplyTimeout 限制旁路生成的耗时：戳一戳是即时互动，十几秒后才回话
	// 的反应已经错过了那个瞬间。
	pokeReplyTimeout = 15 * time.Second
	// pokeReplyMaxRunes 兜底裁剪生成结果。提示词要求一句话，模型偶尔不听。
	pokeReplyMaxRunes = 60
)

// handlePokeNotice 处理戳一戳通知。只回「戳机器人」的；别人互戳不掺和。
func (r *Runtime) handlePokeNotice(ctx context.Context, event MessageEvent) error {
	cfg := r.effectiveConfigForEvent(event)
	if !boolValue(cfg.PokeReplyEnabled, false) {
		return nil
	}
	userID := strings.TrimSpace(event.UserID)
	targetID := strings.TrimSpace(event.TargetID)
	selfIDs := map[string]bool{}
	for _, id := range []string{strings.TrimSpace(event.SelfID), strings.TrimSpace(cfg.BotAccount)} {
		if id != "" {
			selfIDs[id] = true
		}
	}
	if userID == "" || targetID == "" || !selfIDs[targetID] || selfIDs[userID] {
		return nil
	}
	if !r.admitsNotice(cfg, event) {
		return nil
	}
	if !r.claimPokeReply(event.ProfileID, userID, time.Now()) {
		return nil
	}

	reply, err := r.generatePokeReply(ctx, event)
	if err != nil || reply == "" {
		if err != nil {
			r.recordPokeReply(ctx, event, "", err)
		}
		return nil
	}
	sendEvent := event
	sendEvent.Kind = EventKindPrivate
	if event.GroupID != "" {
		sendEvent.Kind = EventKindGroup
	}
	// 通知事件的 MessageID 其实是 target_id，不是可引用的消息；清掉，免得发送层
	// 的引用装饰对着一个不存在的消息 ID 加引用框。
	sendEvent.MessageID = ""
	if err := r.send(ctx, sendEvent, reply); err != nil {
		r.recordPokeReply(ctx, event, reply, err)
		return err
	}
	r.record(EventRecord{
		At:        time.Now(),
		Kind:      event.Kind,
		Platform:  event.Platform,
		ProfileID: event.ProfileID,
		UserID:    event.UserID,
		GroupID:   event.GroupID,
		MessageID: event.MessageID,
		Text:      "[notice] poke",
		Reply:     reply,
		Handled:   true,
		Outcome:   "replied_poke",
		Decision:  "replied",
		Reason:    "被戳了一下，回了一句",
	})
	r.recordPokeReply(ctx, event, reply, nil)
	return nil
}

// claimPokeReply 占用一次回应额度，冷却期内返回 false。
func (r *Runtime) claimPokeReply(profileID, userID string, now time.Time) bool {
	key := strings.TrimSpace(profileID) + "|" + userID
	r.pokeMu.Lock()
	defer r.pokeMu.Unlock()
	if r.pokeLastReply == nil {
		r.pokeLastReply = map[string]time.Time{}
	}
	if last, ok := r.pokeLastReply[key]; ok && now.Sub(last) < pokeReplyCooldown {
		return false
	}
	// 顺手清掉早就过期的条目，别让这张表跟着被戳的次数一直长。
	for existing, at := range r.pokeLastReply {
		if now.Sub(at) > 24*time.Hour {
			delete(r.pokeLastReply, existing)
		}
	}
	r.pokeLastReply[key] = now
	return true
}

// generatePokeReply 用人设和关系语气生成一句回应。
func (r *Runtime) generatePokeReply(ctx context.Context, event MessageEvent) (string, error) {
	ctx = withLLMUsagePurpose(ctx, "poke_reply")
	profile, _ := r.loadUserMemoryProfile(ctx, event)
	policy := RelationshipPolicyForConfig(r.effectiveConfigForEvent(event), profile, event.UserID)
	who := firstNonEmpty(strings.TrimSpace(profile.DisplayName), event.UserID)
	scene := "私聊里"
	if event.GroupID != "" {
		scene = "群里"
	}
	instruction := fmt.Sprintf(
		"刚刚 %s 在%s戳了戳你（QQ 的戳一戳，没有文字）。你们的关系等级是「%s」，语气要求：%s\n"+
			"按你的人设回一句话作为反应：1 到 20 个字，自然、口语化，可以是招呼、疑问、调侃或抱怨，符合当前关系的亲疏。"+
			"不要解释什么是戳一戳，不要用括号描写动作，不要 @ 对方，只输出要发的那一句话。",
		who, scene, policy.Name, policy.Tone)
	messages := r.withUserFacingPersona(event, []llm.Message{{Role: llm.RoleUser, Content: instruction}})
	callCtx, cancel := context.WithTimeout(ctx, pokeReplyTimeout)
	defer cancel()
	raw, err := r.runLLMRouterProvider(callCtx, func(client LLMProvider) (string, error) {
		resp, err := client.Generate(callCtx, llm.GenerateRequest{Messages: messages})
		if err != nil {
			return "", err
		}
		return resp.Text, nil
	})
	if err != nil {
		return "", err
	}
	reply := strings.TrimSpace(raw)
	if index := strings.IndexByte(reply, '\n'); index > 0 {
		reply = strings.TrimSpace(reply[:index])
	}
	return truncateRunesPlain(reply, pokeReplyMaxRunes), nil
}

func (r *Runtime) recordPokeReply(ctx context.Context, event MessageEvent, reply string, err error) {
	writer := r.appLogWriter()
	if writer == nil {
		return
	}
	entry := applog.Entry{
		Kind:    applog.KindOperation,
		Level:   applog.LevelInfo,
		Action:  "diana.poke_reply",
		Message: "戳一戳已回应",
		Actor:   oneBotEventActor(event),
		Target:  event.MessageID,
		Metadata: map[string]any{
			"group_id": event.GroupID,
			"user_id":  event.UserID,
			"reply":    truncateRunesFromStart(reply, 120),
		},
	}
	if err != nil {
		entry.Kind = applog.KindError
		entry.Level = applog.LevelError
		entry.Message = "戳一戳回应失败，本次保持沉默"
		entry.Detail = err.Error()
	}
	_ = writer.AppendLog(ctx, entry)
}
