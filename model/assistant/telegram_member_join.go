// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"log"
	"strconv"
	"strings"
	"time"
)

// telegramJoinDedupeWindow 内同一个人进同一个群只报一次入群。
//
// 机器人是管理员时，一次入群会同时到两份：带 new_chat_members 的服务消息和
// chat_member 更新。欢迎本身有 claimWelcomeNotice 兜着，但入群事件触发器没有，
// 不在接入端合并的话同一个新人会触发两遍。
const telegramJoinDedupeWindow = 10 * time.Minute

// telegramNewMemberEvents 把服务消息里的每个新成员各映射成一条 group_increase。
//
// 一次拉多个人进群时 new_chat_members 里有好几个人，只取第一个的话其余的人收不到欢迎。
// 被拉进来的其它机器人不算新成员：欢迎一个机器人只会让群里多一句没人接的话；
// 机器人自己入群由运行时的自发消息过滤挡掉。
func telegramNewMemberEvents(msg *telegramMessage, selfID string) []MessageEvent {
	if msg == nil || msg.Chat == nil || len(msg.NewChatMembers) == 0 || msg.Chat.Type == "private" || msg.Chat.Type == "channel" {
		return nil
	}
	operatorID := ""
	if msg.From != nil {
		operatorID = strconv.FormatInt(msg.From.ID, 10)
	}
	events := make([]MessageEvent, 0, len(msg.NewChatMembers))
	for i := range msg.NewChatMembers {
		joined := msg.NewChatMembers[i]
		if joined.ID <= 0 || joined.IsBot {
			continue
		}
		events = append(events, telegramMemberJoinEvent(msg.Chat, joined, operatorID, selfID, msg.Date, strconv.FormatInt(msg.MessageID, 10)))
	}
	return events
}

// telegramChatMemberJoinEvent 从 chat_member 更新里认出入群：之前不在群里、现在在。
//
// 大群和开了「隐藏入群消息」的群不发 new_chat_members 服务消息，只有机器人是管理员时
// 收得到的 chat_member 更新能看见有人进来。受限成员之间的权限变化（两边都在群里）不算。
func telegramChatMemberJoinEvent(update *telegramMemberUpdate, selfID string) (MessageEvent, bool) {
	if update == nil || update.Chat.ID == 0 || (update.Chat.Type != "group" && update.Chat.Type != "supergroup") {
		return MessageEvent{}, false
	}
	joined := update.New.User
	if joined.ID <= 0 || joined.IsBot || update.Old.present() || !update.New.present() {
		return MessageEvent{}, false
	}
	operatorID := ""
	if update.From.ID > 0 && update.From.ID != joined.ID {
		operatorID = strconv.FormatInt(update.From.ID, 10)
	}
	at := update.Date
	if at <= 0 {
		at = time.Now().Unix()
	}
	return telegramMemberJoinEvent(&update.Chat, joined, operatorID, selfID, at, ""), true
}

func telegramMemberJoinEvent(chat *telegramChat, joined telegramUser, operatorID, selfID string, at int64, messageID string) MessageEvent {
	userID := strconv.FormatInt(joined.ID, 10)
	// 和 OneBot 的 group_increase 一样：自己进来是 approve，被别人拉进来是 invite。
	data := map[string]string{"notice_type": "group_increase", "sub_type": "approve"}
	if operatorID != "" && operatorID != userID {
		data["sub_type"] = "invite"
		data["operator_id"] = operatorID
	} else {
		operatorID = ""
	}
	return MessageEvent{
		Platform:    PlatformTelegram,
		Kind:        EventKindNotice,
		SubType:     "group_increase",
		Time:        at,
		SelfID:      selfID,
		MessageID:   messageID,
		MessageType: "notice",
		GroupID:     strconv.FormatInt(chat.ID, 10),
		GroupName:   strings.TrimSpace(chat.Title),
		UserID:      userID,
		OperatorID:  operatorID,
		SenderName:  telegramDisplayName(&joined),
		Segments:    []MessageSegment{{Type: "notice", Data: data}},
	}
}

// claimMemberJoin 占用这次入群，窗口内另一条通道报来的同一次入群返回 false。
func (c *TelegramChannel) claimMemberJoin(event MessageEvent, now time.Time) bool {
	key := event.GroupID + "|" + event.UserID
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.recentJoins == nil {
		c.recentJoins = map[string]time.Time{}
	}
	if last, ok := c.recentJoins[key]; ok && now.Sub(last) < telegramJoinDedupeWindow {
		return false
	}
	for existing, at := range c.recentJoins {
		if now.Sub(at) > telegramJoinDedupeWindow {
			delete(c.recentJoins, existing)
		}
	}
	c.recentJoins[key] = now
	return true
}

// handleMemberJoins 把入群逐条交给上层，同一次入群从两条通道来的只交一次。
func (c *TelegramChannel) handleMemberJoins(ctx context.Context, updateID int64, events []MessageEvent) {
	c.mu.RLock()
	handler := c.handler
	c.mu.RUnlock()
	if handler == nil {
		return
	}
	now := time.Now()
	for _, event := range events {
		if !c.claimMemberJoin(event, now) {
			continue
		}
		if err := handler(ctx, event); err != nil {
			log.Printf("telegram: handle member join failed: update_id=%d chat_id=%s user_id=%s err=%v", updateID, event.GroupID, event.UserID, err)
		}
	}
}
