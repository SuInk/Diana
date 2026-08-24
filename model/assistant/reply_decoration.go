// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"strconv"
	"strings"
	"time"
)

// ReplyDecorationMode 描述群聊回复的装饰件（引用原消息、@ 发送者）由谁决定。
// 布尔开关只能表达「每条都带」或「一条都不带」，两种极端都不像真人：真人只在
// 需要指向具体某条消息或需要点名时才引用和 @。auto 把这个判断交给模型自己。
type ReplyDecorationMode string

const (
	// ReplyDecorationOn 每条群聊回复都带上该装饰件。
	ReplyDecorationOn ReplyDecorationMode = "on"
	// ReplyDecorationOff 永不带该装饰件。
	ReplyDecorationOff ReplyDecorationMode = "off"
	// ReplyDecorationAuto 运行时不自动添加，由模型在正文里自行写出引用标记或 @。
	ReplyDecorationAuto ReplyDecorationMode = "auto"
)

// normalizeReplyDecorationMode 归一化装饰件模式，没写就按 on 处理。
func normalizeReplyDecorationMode(mode ReplyDecorationMode) ReplyDecorationMode {
	switch ReplyDecorationMode(strings.ToLower(strings.TrimSpace(string(mode)))) {
	case ReplyDecorationOff:
		return ReplyDecorationOff
	case ReplyDecorationAuto:
		return ReplyDecorationAuto
	default:
		return ReplyDecorationOn
	}
}

// replyReferenceMode 返回本次生效的引用模式，未归一化的配置也能安全读取。
func replyReferenceMode(cfg BotConfig) ReplyDecorationMode {
	return normalizeReplyDecorationMode(cfg.ReplyReferenceMode)
}

// mentionUserMode 返回本次生效的 @ 模式，未归一化的配置也能安全读取。
func mentionUserMode(cfg BotConfig) ReplyDecorationMode {
	return normalizeReplyDecorationMode(cfg.MentionUserMode)
}

// replyDecorationPrompt 只在 auto 模式下告诉模型怎么自己带引用和 @。返回值包含
// 当前消息 ID，逐条消息都不同，因此和实时时钟一样只能作为尾部独立 system 消息注入，
// 不能拼进人设提示词——否则那段最长的前缀每条消息都会失效一次。
// pendingEarlierMessageWindow 限定「连发」的判定窗口。隔了几分钟的两条消息
// 是两个话题,不该被绑在一轮里点名承接。
const pendingEarlierMessageWindow = 3 * time.Minute

// pendingEarlierMessage 找出发送者紧挨着当前消息之前、机器人还没有回复过的
// 那条消息。追发合并(superseded_follow_up)后,合并回复只有一条,视觉上没有
// 任何锚点指向前一条——发的人会觉得前一条被跳过了。找到这条消息后提示模型
// 承接并引用它。中间隔着机器人发言或别人发言就说明不是连发,不算。
func pendingEarlierMessage(history []MessageEvent, event MessageEvent) (MessageEvent, bool) {
	currentID := strings.TrimSpace(event.MessageID)
	for index := len(history) - 1; index >= 0; index-- {
		item := history[index]
		if strings.TrimSpace(item.MessageID) == currentID && currentID != "" {
			continue
		}
		if item.crossGroupContext {
			continue
		}
		// 紧挨着的上一条不是同一个人的入站消息,就没有「连发未回」这回事。
		if item.Outbound || strings.TrimSpace(item.UserID) != strings.TrimSpace(event.UserID) {
			return MessageEvent{}, false
		}
		if event.Time > 0 && item.Time > 0 && event.Time-item.Time > int64(pendingEarlierMessageWindow/time.Second) {
			return MessageEvent{}, false
		}
		if strings.TrimSpace(historyPlainText(item)) == "" {
			return MessageEvent{}, false
		}
		return item, true
	}
	return MessageEvent{}, false
}

func replyDecorationPrompt(cfg BotConfig, event MessageEvent, history []MessageEvent) string {
	if event.Kind != EventKindGroup {
		return ""
	}
	var builder strings.Builder
	if earlier, ok := pendingEarlierMessage(history, event); ok {
		preview := []rune(strings.TrimSpace(historyPlainText(earlier)))
		if len(preview) > 40 {
			preview = append(preview[:40], '…')
		}
		builder.WriteString("发送者刚连发了多条消息,上一条「" + string(preview) + "」你还没有回复。这一轮把它们一起接住:先明确回应那一条,再回应当前这条,别让对方觉得前一条被跳过。")
	}
	if replyReferenceMode(cfg) == ReplyDecorationAuto {
		if messageID := strings.TrimSpace(event.MessageID); validOutgoingReplyMessageID(messageID) {
			appendPromptSection(&builder, "本次是否引用原消息由你自己决定：话题跳转、隔了几轮才回应、或群里同时有多个话题时，在回复最开头写 "+
				replyMarkerPrefix+messageID+"] 来指向当前这条消息；正常一问一答、连续对话时不要引用。整段标记必须写在最开头，正文里不要出现。")
		}
	}
	if mentionUserMode(cfg) == ReplyDecorationAuto {
		if userID := strings.TrimSpace(event.UserID); userID != "" {
			appendPromptSection(&builder, mentionDecorationRule(userID, otherSpeakersBefore(history, event)))
		}
	}
	return strings.TrimSpace(builder.String())
}

// 「该不该 @」原先整条交给模型判断：提示词说「多人同时说话需要点名时才写 @」,
// 而模型看不出群有多热闹——它拿到的是一段已经排好序的历史,谁在跟谁说话、隔了
// 多久,都得从文本里猜。猜不准就一律不 @,实际表现是该点名的时候也不点。
//
// 插话人数是运行时算得出来的,那就别让模型猜:这里把它数出来写进提示词,规则挂在
// 这个数上。判断仍然由模型做（写不写 @ 是语气问题）,但它依据的是事实而不是印象。
const (
	// mentionCrowdWindow 是「刚才这段时间」的长度。再往前就不影响当下这句话
	// 该不该点名了。
	mentionCrowdWindow = 5 * time.Minute
	// mentionCrowdLookback 限制回溯条数,免得在刷屏群里把整段历史都数一遍。
	mentionCrowdLookback = 20
)

// otherSpeakersBefore 数出当前消息之前那一小段里,除发送者和机器人之外还有几个
// 不同的人说过话。跨群带进来的上下文不算——那不是这个群里的插话。
func otherSpeakersBefore(history []MessageEvent, event MessageEvent) int {
	senderID := strings.TrimSpace(event.UserID)
	currentID := strings.TrimSpace(event.MessageID)
	speakers := make(map[string]struct{})
	scanned := 0
	for index := len(history) - 1; index >= 0 && scanned < mentionCrowdLookback; index-- {
		item := history[index]
		if item.crossGroupContext {
			continue
		}
		if currentID != "" && strings.TrimSpace(item.MessageID) == currentID {
			continue
		}
		if event.Time > 0 && item.Time > 0 && event.Time-item.Time > int64(mentionCrowdWindow/time.Second) {
			break
		}
		scanned++
		if item.Outbound {
			continue
		}
		userID := strings.TrimSpace(item.UserID)
		if userID == "" || userID == senderID {
			continue
		}
		speakers[userID] = struct{}{}
	}
	return len(speakers)
}

// mentionDecorationRule 按插话人数给出这一轮的 @ 规则。
func mentionDecorationRule(userID string, otherSpeakers int) string {
	if otherSpeakers == 0 {
		return "本次是否 @ 发送者由你自己决定：刚才这段时间里群里只有 TA 在说话,你们是一对一在接话,不用 @；" +
			"只有隔了很久才回、对方可能已经走开时才写 @" + userID + "。"
	}
	return "本次是否 @ 发送者由你自己决定：刚才这段时间里群里除了 TA 还有 " + strconv.Itoa(otherSpeakers) +
		" 个人在说话,这种时候在回复最开头写 @" + userID + " 点名,对方才知道这句是回 TA 的；" +
		"除非你上一条刚回过 TA、这句是紧接着的补充,那就不用重复 @。"
}
