// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"strings"
	"time"
)

// 意图识别会误判：线上群主一直在跟群里另一个机器人一问一答，一句没点名的「你回复
// 短一点」被当成在叫本机器人。叫醒之后，回复这边看到的是「当前需要回复的消息」和
// 「把连发的几条一起接住」，silent 的适用范围写得再宽也压不过，生产回放 5 次全部
// 把别人的问题接过来答了。
//
// 谁在跟谁来回说话，运行时从引用和 @ 里数得出来，那就把这个事实直接写给模型：
// 当前这句没点本人，而发送者最近一直在跟另一个人（或另一个机器人）往来时，提醒它先
// 判断这句是给谁的。加上这条后同一批回放 5 次全部 silent，理由都指向那个对象。

const (
	// elsewherePartnerWindow 是「最近」的长度。再往前的往来不代表眼下这句在跟谁说。
	elsewherePartnerWindow   = 10 * time.Minute
	elsewherePartnerLookback = 20
	// elsewherePartnerMinExchanges 至少要这么多条往来才算「一直在跟对方说」，
	// 一条孤立的引用不足以说明这句不是给本人的。
	elsewherePartnerMinExchanges = 2
)

// elsewherePartnerPrompt 在当前消息没点本人、发送者最近一直在跟别人往来时返回提醒。
func elsewherePartnerPrompt(cfg BotConfig, event MessageEvent, history []MessageEvent) string {
	partner := elsewherePartner(cfg, event, history)
	if partner == "" {
		return ""
	}
	return "【这句可能不是对你说的】当前消息没有点你的名、没有 @ 你，也没有引用你；发送者最近几条一直在跟 " + partner + " 来回说话。" +
		"先判断这句是不是对你说的：如果是说给 " + partner + " 的，里面的「你」指的是对方，问题和要求都不是给你的，最后 send 填 silent=true 不出声，不要接过来替人答。" +
		"确实是在跟你说话时照常回复。"
}

// elsewherePartner 返回当前发送者最近的对话对象的称呼；对象是本人、看不出来或当前消息点了本人时返回空。
func elsewherePartner(cfg BotConfig, event MessageEvent, history []MessageEvent) string {
	if event.Kind != EventKindGroup {
		return ""
	}
	senderID := strings.TrimSpace(event.UserID)
	selfID := firstNonEmpty(strings.TrimSpace(event.SelfID), strings.TrimSpace(cfg.BotAccount))
	if senderID == "" || selfID == "" {
		return ""
	}
	// 引用或 @ 了谁已经写在「当前消息的对话对象」里，这里只管什么都没点的那一种。
	addressing := addressingForEvent(event, cfg)
	if addressing.ReplyTarget != "none" || len(addressing.Mentions) > 0 || len(matchedGroupAliases(event, cfg, event.RawMessage)) > 0 {
		return ""
	}
	currentID := strings.TrimSpace(event.MessageID)
	counts := map[string]int{}
	names := map[string]string{}
	latest := ""
	scanned := 0
	for index := len(history) - 1; index >= 0 && scanned < elsewherePartnerLookback; index-- {
		item := history[index]
		if item.crossGroupContext || isPokeHistoryEvent(item) {
			continue
		}
		if currentID != "" && strings.TrimSpace(item.MessageID) == currentID {
			continue
		}
		if event.Time > 0 && item.Time > 0 && event.Time-item.Time > int64(elsewherePartnerWindow/time.Second) {
			break
		}
		scanned++
		itemUser := strings.TrimSpace(item.UserID)
		// 本人比那段往来说话更晚，这句多半是接着本人说的。本人的跟进大多不引用也不 @，
		// 只认「冲着发送者说」会漏掉：回放里群友接着本人刚抛的问题回答，被当成在跟别人说。
		if item.Outbound || itemUser == selfID {
			if latest == "" {
				return ""
			}
			continue
		}
		partnerID, partnerName := "", ""
		if itemUser == senderID {
			for _, id := range addressedUserIDs(item, history) {
				if id == selfID {
					if latest == "" {
						return ""
					}
					continue
				}
				if id != senderID && partnerID == "" {
					partnerID = id
				}
			}
		} else if proactiveReplyBotMessageAddressesUser(item, history, senderID) {
			partnerID, partnerName = itemUser, strings.TrimSpace(item.SenderName)
		}
		if partnerID == "" {
			continue
		}
		if latest == "" {
			latest = partnerID
		}
		counts[partnerID]++
		if names[partnerID] == "" && partnerName != "" {
			names[partnerID] = partnerName
		}
	}
	if latest == "" || counts[latest] < elsewherePartnerMinExchanges {
		return ""
	}
	if names[latest] == "" {
		names[latest] = senderNameInHistory(history, latest)
	}
	return firstNonEmpty(names[latest], "另一位群友")
}

// addressedUserIDs 列出一条消息引用或 @ 的账号。
func addressedUserIDs(message MessageEvent, history []MessageEvent) []string {
	var ids []string
	if q := message.Quoted; q != nil && strings.TrimSpace(q.UserID) != "" {
		ids = append(ids, strings.TrimSpace(q.UserID))
	}
	for _, mention := range message.MentionTargets {
		if id := strings.TrimSpace(mention.UserID); id != "" {
			ids = append(ids, id)
		}
	}
	for _, segment := range message.Segments {
		switch segment.Type {
		case "at":
			if id := strings.TrimSpace(firstNonEmpty(segment.Data["qq"], segment.Data["user_id"])); id != "" && id != "all" {
				ids = append(ids, id)
			}
		case "reply":
			replied := strings.TrimSpace(segment.Data["id"])
			for _, candidate := range history {
				if replied != "" && strings.TrimSpace(candidate.MessageID) == replied {
					ids = append(ids, strings.TrimSpace(candidate.UserID))
					break
				}
			}
		}
	}
	return ids
}

func senderNameInHistory(history []MessageEvent, userID string) string {
	for index := len(history) - 1; index >= 0; index-- {
		if strings.TrimSpace(history[index].UserID) == userID {
			if name := strings.TrimSpace(history[index].SenderName); name != "" {
				return name
			}
		}
	}
	return ""
}
