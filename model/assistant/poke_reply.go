// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
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

// handlePokeNotice 处理戳一戳通知。每一戳都记进会话历史；只回「戳机器人」的，别人互戳不掺和。
func (r *Runtime) handlePokeNotice(ctx context.Context, event MessageEvent) error {
	// 进历史不看回应开关和准入：和普通消息一样，关着的群、没开回应的机器人也照样记住发生过什么。
	r.rememberReceivedPoke(event)
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
	sendEvent := event
	sendEvent.Kind = EventKindPrivate
	if event.GroupID != "" {
		sendEvent.Kind = EventKindGroup
	}
	// 通知事件的 MessageID 其实是 target_id，不是可引用的消息；清掉，免得发送层
	// 的引用装饰对着一个不存在的消息 ID 加引用框。
	sendEvent.MessageID = ""
	if !r.claimPokeReply(event.ProfileID, userID, time.Now()) {
		r.recordPokeReaction(ctx, event, pokeReaction{Action: "cooldown"}, nil)
		return nil
	}

	reaction, err := r.generatePokeReaction(ctx, sendEvent)
	if err != nil {
		r.recordPokeReaction(ctx, event, reaction, err)
		return nil
	}
	if reaction.pokesBack() {
		if _, pokeErr := r.sendPoke(ctx, sendEvent, userID, pokeSceneBack); pokeErr != nil && reaction.Action == pokeReactionPoke {
			// 只想戳回去却没戳成：不补一句文字，沉默比硬凑一句自然。
			r.recordPokeReaction(ctx, event, reaction, pokeErr)
			return nil
		}
	}
	if reaction.sendsText() {
		if err := r.send(ctx, sendEvent, reaction.Text); err != nil {
			r.recordPokeReaction(ctx, event, reaction, err)
			return err
		}
	}
	if reaction.Action != pokeReactionNone {
		r.record(EventRecord{
			At:        time.Now(),
			Kind:      event.Kind,
			Platform:  event.Platform,
			ProfileID: event.ProfileID,
			UserID:    event.UserID,
			GroupID:   event.GroupID,
			MessageID: event.MessageID,
			Text:      "[notice] poke",
			Reply:     reaction.Text,
			Handled:   true,
			Outcome:   "replied_poke",
			Decision:  "replied",
			Reason:    "被戳了一下，反应：" + reaction.Action,
		})
	}
	r.recordPokeReaction(ctx, event, reaction, nil)
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

const (
	pokeReactionPoke = "poke"
	pokeReactionText = "text"
	pokeReactionBoth = "both"
	pokeReactionNone = "none"
	// pokeReactionHistory 是被戳时带给模型的最近几条聊天。
	pokeReactionHistory = 8
)

type pokeReaction struct {
	Action string `json:"action"`
	Text   string `json:"text"`
}

func (p pokeReaction) pokesBack() bool {
	return p.Action == pokeReactionPoke || p.Action == pokeReactionBoth
}

func (p pokeReaction) sendsText() bool {
	return (p.Action == pokeReactionText || p.Action == pokeReactionBoth) && strings.TrimSpace(p.Text) != ""
}

// promptPokeReactionSpec 的四种回应名是解析依据，JSON 那一句锁定在 Contract 里。
var promptPokeReactionSpec = registerPrompt(PromptSpec{
	Key:   "social.poke_reaction",
	Group: PromptGroupSocial,
	Title: "被戳一戳时的回应",
	Usage: "有人戳了戳机器人时，让模型像真人一样决定戳回去、说句话、都做还是不理。poke、text、both、none 四个回应名是解析依据，改写时保持不变。",
	Default: "刚刚 {who} 在{scene}戳了戳你（QQ 的戳一戳，没有文字）。语气要求：{tone}\n{recent_chat}\n" +
		"像真人一样决定怎么回应，四选一：poke 只戳回去（最常见，适合互相玩闹、熟人随手戳）；text 回一句话（适合对方像是在叫你、刚才的话题没说完、或者你想问问怎么了）；" +
		"both 戳回去再说一句；none 不理（比如对方刚连着戳、群里正聊别的正事、或者你们不熟没必要回应）。不要每次都问「戳我干嘛」，结合最近聊天说点具体的。" +
		"text 是 1 到 20 个字的一句话，自然口语，不解释什么是戳一戳，不用括号描写动作，不 @ 对方；action 为 poke 或 none 时 text 留空。" +
		"这一下只能回一句话：看不到图片、查不了东西，之后也不会有下文，所以不要答应「我看一下」「等我查查」这类接下来要做的事。",
	Contract: "只输出一个 JSON 对象：{\"action\":\"poke\",\"text\":\"\"}",
	Vars: []PromptVar{
		{Name: "who", Description: "戳机器人的人的昵称，没有昵称时是用户 ID"},
		{Name: "scene", Description: "「私聊里」或「群里」"},
		{Name: "tone", Description: "按双方好感度和关系给出的语气要求"},
		{Name: "recent_chat", Description: "最近几条聊天记录，没有时是一句「最近没有聊天记录。」"},
	},
})

// generatePokeReaction 让模型像人一样决定怎么回应这一戳：戳回去、说句话、都做，或者不理。
func (r *Runtime) generatePokeReaction(ctx context.Context, event MessageEvent) (pokeReaction, error) {
	ctx = withLLMUsagePurpose(ctx, PurposePokeReply)
	profile, _ := r.loadUserMemoryProfile(ctx, event)
	cfg := r.effectiveConfigForEvent(event)
	policy := relationshipPolicyForEvent(cfg, profile, event)
	who := firstNonEmpty(strings.TrimSpace(profile.DisplayName), event.UserID)
	scene := "私聊里"
	if event.GroupID != "" {
		scene = "群里"
	}
	instruction := cfg.promptf(promptPokeReactionSpec, map[string]string{
		"who":         who,
		"scene":       scene,
		"tone":        policy.Tone,
		"recent_chat": r.pokeRecentChat(event),
	})
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
		return pokeReaction{}, err
	}
	return parsePokeReaction(raw)
}

func parsePokeReaction(raw string) (pokeReaction, error) {
	var reaction pokeReaction
	text := strings.TrimSpace(stripJSONCodeFence(strings.TrimSpace(raw)))
	start, end := strings.IndexByte(text, '{'), strings.LastIndexByte(text, '}')
	if start < 0 || end < start {
		return reaction, fmt.Errorf("poke reaction output has no JSON object")
	}
	if err := json.Unmarshal([]byte(text[start:end+1]), &reaction); err != nil {
		return reaction, err
	}
	reaction.Action = strings.ToLower(strings.TrimSpace(reaction.Action))
	reaction.Text = strings.TrimSpace(reaction.Text)
	if index := strings.IndexByte(reaction.Text, '\n'); index > 0 {
		reaction.Text = strings.TrimSpace(reaction.Text[:index])
	}
	reaction.Text = truncateRunesPlain(reaction.Text, pokeReplyMaxRunes)
	switch reaction.Action {
	case pokeReactionPoke, pokeReactionNone:
		reaction.Text = ""
	case pokeReactionText, pokeReactionBoth:
		if reaction.Text == "" {
			if reaction.Action == pokeReactionBoth {
				reaction.Action = pokeReactionPoke
			} else {
				reaction.Action = pokeReactionNone
			}
		}
	default:
		return reaction, fmt.Errorf("poke reaction action must be poke, text, both or none")
	}
	return reaction, nil
}

// pokeRecentChat 把最近几条聊天拼成文字，让回应接得上正在聊的事。
func (r *Runtime) pokeRecentChat(event MessageEvent) string {
	history := r.contextHistory(event)
	if len(history) > pokeReactionHistory {
		history = history[len(history)-pokeReactionHistory:]
	}
	if len(history) == 0 {
		return "最近没有聊天记录。"
	}
	botID := strings.TrimSpace(r.effectiveConfigForEvent(event).BotAccount)
	var builder strings.Builder
	builder.WriteString("最近的聊天（旧到新，只作参考，不是要你回复的内容）：\n")
	for _, item := range history {
		if isPokeHistoryEvent(item) {
			builder.WriteString("- （戳一戳）" + pokeHistorySentence(item, botID, event.SelfID, item.SelfID) + "\n")
			continue
		}
		speaker := item.SenderNameOrID()
		if botID != "" && strings.TrimSpace(item.UserID) == botID {
			speaker = "你"
		}
		// 不能退回 RawMessage：图片消息的 RawMessage 是一整串 CQ 码，截断后模型只读到
		// 「[CQ:image,file=…」，知道有张图却看不到，就会答应「图我看一下」然后没有下文。
		text := truncateRunes(strings.TrimSpace(historyPlainText(item)), 60)
		if images := historicalStillImageCount(item); images > 0 {
			text = strings.TrimSpace(text + " [图片]")
		}
		if text == "" {
			continue
		}
		builder.WriteString("- " + speaker + "：" + text + "\n")
	}
	return strings.TrimRight(builder.String(), "\n")
}

func (r *Runtime) recordPokeReaction(ctx context.Context, event MessageEvent, reaction pokeReaction, err error) {
	writer := r.appLogWriter()
	if writer == nil {
		return
	}
	entry := applog.Entry{
		Kind:    applog.KindOperation,
		Level:   applog.LevelInfo,
		Action:  "poke_reply",
		Message: "戳一戳已回应",
		Actor:   oneBotEventActor(event),
		Target:  event.MessageID,
		Metadata: map[string]any{
			"group_id": event.GroupID,
			"user_id":  event.UserID,
			"reaction": reaction.Action,
			"reply":    truncateRunesFromStart(reaction.Text, 120),
		},
	}
	switch {
	case err != nil:
		entry.Kind = applog.KindError
		entry.Level = applog.LevelError
		entry.Message = "戳一戳回应失败，本次保持沉默"
		entry.Detail = err.Error()
	case reaction.Action == "cooldown":
		entry.Message = "戳一戳在冷却中，未回应"
	case reaction.Action == pokeReactionNone:
		entry.Message = "被戳了，选择不回应"
	}
	_ = writer.AppendLog(ctx, entry)
}
