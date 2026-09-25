// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"strings"
	"time"

	"github.com/google/uuid"
)

// 戳一戳进会话历史。以前被戳只写事件日志、戳人只写操作日志，会话历史里什么都没
// 留下：下一轮模型不知道刚被戳过、也不知道自己刚戳过人，被问「你刚才戳我干嘛」
// 只能装傻，戳回去之后还可能再戳一遍。
//
// 戳一戳没有正文，历史里存成一条带 pokeHistorySubType 标记的群聊/私聊事件，正文是
// 中性的「[戳一戳] 戳了戳 某某」，事件页、历史检索这些按 PlainText 读的地方直接能用。
// 给模型看时由 pokeHistoryPromptText 单独渲染成旁白：机器人自己戳的那条也不当成
// assistant 发言，否则模型会照着历史里那行字把「[戳一戳]」当文字发出去。
const pokeHistorySubType = "poke"

// isPokeHistoryEvent 判断一条历史是不是戳一戳。通知本身（EventKindNotice）不算，
// 只有 rememberPoke 翻好的那种才算。
func isPokeHistoryEvent(event MessageEvent) bool {
	return event.SubType == pokeHistorySubType && (event.Kind == EventKindGroup || event.Kind == EventKindPrivate)
}

// rememberReceivedPoke 把收到的戳一戳通知记进会话历史。群友之间互戳也记：群里
// 大家看得见，是正在发生的事。机器人自己戳人时部分实现会回显一条通知，那条已经在
// 发出时记过了，这里跳过。
func (r *Runtime) rememberReceivedPoke(event MessageEvent) {
	pokerID := strings.TrimSpace(event.UserID)
	targetID := strings.TrimSpace(event.TargetID)
	if pokerID == "" || targetID == "" {
		return
	}
	cfg := r.effectiveConfigForEvent(event)
	for _, selfID := range []string{strings.TrimSpace(event.SelfID), strings.TrimSpace(cfg.BotAccount)} {
		if selfID != "" && pokerID == selfID {
			return
		}
	}
	history := event
	history.Kind = EventKindPrivate
	history.MessageType = "private"
	if strings.TrimSpace(event.GroupID) != "" {
		history.Kind = EventKindGroup
		history.MessageType = "group"
	}
	names := r.eventDisplayNameResolver(history)
	history.SenderName = pokeDisplayName(names, pokerID)
	r.remember(r.pokeHistoryEvent(history, targetID, r.pokeTargetName(history, names, targetID)))
}

// rememberSentPoke 把机器人发出的戳一戳记进会话历史。source 是触发这次戳的对话事件。
func (r *Runtime) rememberSentPoke(source MessageEvent, targetID string) {
	cfg := r.effectiveConfigForEvent(source)
	selfID := firstNonEmpty(strings.TrimSpace(source.SelfID), strings.TrimSpace(cfg.BotAccount), "bot")
	history := source
	history.Kind = EventKindPrivate
	history.MessageType = "private"
	if strings.TrimSpace(source.GroupID) != "" {
		history.Kind = EventKindGroup
		history.MessageType = "group"
		// 群里的历史按发言人记；私聊的会话键跟着对方走，UserID 得留着对方的。
		history.UserID = selfID
	}
	history.SelfID = selfID
	history.SenderName = firstNonEmpty(strings.TrimSpace(cfg.Name), "Diana")
	history.Outbound = true
	history.Time = time.Now().Unix()
	names := r.eventDisplayNameResolver(source)
	targetName := r.pokeTargetName(source, names, targetID)
	if targetID == strings.TrimSpace(source.UserID) {
		targetName = firstNonEmpty(strings.TrimSpace(source.SenderName), targetName)
	} else if source.Quoted != nil && targetID == strings.TrimSpace(source.Quoted.UserID) {
		targetName = firstNonEmpty(strings.TrimSpace(source.Quoted.SenderName), targetName)
	}
	r.remember(r.pokeHistoryEvent(history, targetID, targetName))
}

// pokeHistoryEvent 在 base 的会话身份上拼出戳一戳历史，base 的发言人就是戳的人。
func (r *Runtime) pokeHistoryEvent(base MessageEvent, targetID, targetName string) MessageEvent {
	text := "[戳一戳] 戳了戳 " + formatPromptIdentity(targetName, targetID)
	event := MessageEvent{
		Platform:         base.Platform,
		ProfileID:        base.ProfileID,
		ContextNamespace: base.ContextNamespace,
		Kind:             base.Kind,
		SubType:          pokeHistorySubType,
		Time:             base.Time,
		SelfID:           base.SelfID,
		UserID:           base.UserID,
		TargetID:         targetID,
		GroupID:          base.GroupID,
		MessageID:        "local-poke-" + uuid.NewString(),
		MessageType:      base.MessageType,
		RawMessage:       text,
		Segments:         []MessageSegment{{Type: "text", Data: map[string]string{"text": text}}},
		SenderName:       base.SenderName,
		Outbound:         base.Outbound,
	}
	if event.Time <= 0 {
		event.Time = time.Now().Unix()
	}
	if event.Kind == EventKindPrivate {
		event.GroupID = ""
	}
	return event
}

// pokeTargetName 找被戳的人的名字：机器人用人设名，其他人翻近期历史。
func (r *Runtime) pokeTargetName(event MessageEvent, names AtMentionNameResolver, targetID string) string {
	cfg := r.effectiveConfigForEvent(event)
	for _, selfID := range []string{strings.TrimSpace(event.SelfID), strings.TrimSpace(cfg.BotAccount)} {
		if selfID != "" && targetID == selfID {
			return firstNonEmpty(strings.TrimSpace(cfg.Name), "Diana")
		}
	}
	return pokeDisplayName(names, targetID)
}

func pokeDisplayName(names AtMentionNameResolver, userID string) string {
	if names == nil {
		return ""
	}
	return strings.TrimSpace(names(userID))
}

// pokeHistoryPromptText 把戳一戳历史渲染成给模型看的一行历史旁白。
func pokeHistoryPromptText(event MessageEvent, botIDs ...string) string {
	return historyLinePrefix(event) + "[戳一戳，没有文字] " + pokeHistorySentence(event, botIDs...)
}

// pokeHistorySentence 是「谁戳了戳谁」这一句，机器人一方写成「你」。
func pokeHistorySentence(event MessageEvent, botIDs ...string) string {
	isBot := func(id string) bool {
		id = strings.TrimSpace(id)
		if id == "" {
			return false
		}
		for _, botID := range botIDs {
			if strings.TrimSpace(botID) == id {
				return true
			}
		}
		return false
	}
	poker := promptSenderIdentity(event)
	if event.Outbound || isBot(event.UserID) {
		poker = "你"
	}
	target := strings.TrimSpace(event.TargetID)
	if isBot(target) {
		target = "你"
	} else if target == "" {
		target = "对方"
	} else if name := pokeTargetNameFromText(event); name != "" {
		target = name
	}
	return poker + " 戳了戳 " + target
}

// pokeTargetNameFromText 从存下的正文里取回被戳者的称呼，免得渲染时再查一遍名字。
func pokeTargetNameFromText(event MessageEvent) string {
	text := strings.TrimSpace(PlainText(event.Segments))
	const prefix = "[戳一戳] 戳了戳 "
	if !strings.HasPrefix(text, prefix) {
		return ""
	}
	return neutralizeIdentityMarkers(strings.TrimSpace(strings.TrimPrefix(text, prefix)))
}

// pokeLeadsToBotMessage 判断机器人这句话是不是在回应和 userID 之间的戳一戳：机器人
// 戳了他，或者这句话紧跟在他戳机器人之后。被戳后回的那句话不带 @ 也不带引用，光看
// 这句话本身认不出它在回谁，路由就会把对方接着说的「看到哪了」当成群友闲聊。
func pokeLeadsToBotMessage(message MessageEvent, history []MessageEvent, userID string) bool {
	if isPokeHistoryEvent(message) {
		return message.Outbound && strings.TrimSpace(message.TargetID) == userID
	}
	messageID := strings.TrimSpace(message.MessageID)
	if messageID == "" {
		return false
	}
	index := -1
	for i := range history {
		if strings.TrimSpace(history[i].MessageID) == messageID {
			index = i
			break
		}
	}
	botID := strings.TrimSpace(message.UserID)
	for i := index - 1; i >= 0; i-- {
		item := history[i]
		if message.Time > 0 && item.Time > 0 && message.Time-item.Time > int64(pokeReplyTimeout/time.Second)*2 {
			return false
		}
		if isPokeHistoryEvent(item) {
			if item.Outbound {
				if strings.TrimSpace(item.TargetID) == userID {
					return true
				}
				continue
			}
			return strings.TrimSpace(item.UserID) == userID && strings.TrimSpace(item.TargetID) == botID
		}
		// 中间隔着别的话，这句就不是在回那一戳了。
		return false
	}
	return false
}
