// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"crypto/sha256"
	"fmt"
	"strconv"
	"strings"
)

// telegramJoinRequest 是 Bot API 的 ChatJoinRequest：开了「审核入群」的群里有人申请加入。
// 只推给有 can_invite_users 权限的管理员机器人。
type telegramJoinRequest struct {
	Chat       telegramChat `json:"chat"`
	From       telegramUser `json:"from"`
	UserChatID int64        `json:"user_chat_id"`
	Date       int64        `json:"date"`
	Bio        string       `json:"bio"`
}

// telegramJoinRequestFlagPrefix 标出请求记录里的 Telegram 句柄。OneBot 的 flag 是接入端
// 签发的不透明令牌；Telegram 审批只认 chat_id + user_id，就把这两个拼进同一个字段，
// 审批时再拆开，请求存储和工具不用为 Telegram 另开一套。末尾的申请时间不参与审批，
// 只为让同一个人被拒后再次申请时落成新记录：存储按 flag 唯一去重。
const telegramJoinRequestFlagPrefix = "telegram:"

func telegramJoinRequestFlag(chatID, userID string, date int64) string {
	return telegramJoinRequestFlagPrefix + chatID + ":" + userID + ":" + strconv.FormatInt(date, 10)
}

func parseTelegramJoinRequestFlag(flag string) (chatID, userID string, ok bool) {
	rest, found := strings.CutPrefix(strings.TrimSpace(flag), telegramJoinRequestFlagPrefix)
	if !found {
		return "", "", false
	}
	// chat_id 是负数，user_id 是正数，都不会含冒号。
	parts := strings.Split(rest, ":")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	return parts[0], parts[1], true
}

// telegramJoinRequestEvent 把入群申请翻成和 OneBot 请求同一形状的事件，走同一条持久化
// 和审批链路。
func telegramJoinRequestEvent(request *telegramJoinRequest, selfID string) (MessageEvent, bool) {
	if request == nil || request.Chat.ID == 0 || request.From.ID == 0 {
		return MessageEvent{}, false
	}
	chatID := strconv.FormatInt(request.Chat.ID, 10)
	userID := strconv.FormatInt(request.From.ID, 10)
	// 同一个人撤回申请再提交会得到新的 date，按 date 区分成两条请求；Telegram 重推
	// 同一个 update 时 date 不变，去重仍然成立。
	flag := telegramJoinRequestFlag(chatID, userID, request.Date)
	fingerprint := sha256.Sum256([]byte(strings.Join([]string{selfID, "group", "add", userID, chatID, strconv.FormatInt(request.Date, 10)}, "\x00")))
	comment := strings.TrimSpace(request.Bio)
	data := map[string]string{"request_type": "group", "sub_type": "add", "user_id": userID, "group_id": chatID}
	if comment != "" {
		data["comment"] = comment
	}
	return MessageEvent{
		Platform:    PlatformTelegram,
		Kind:        EventKindRequest,
		SubType:     "add",
		Time:        request.Date,
		SelfID:      selfID,
		UserID:      userID,
		GroupID:     chatID,
		MessageID:   fmt.Sprintf("request-%x", fingerprint[:12]),
		MessageType: "group",
		RawMessage:  oneBotRequestSummary("group", "add"),
		SenderName:  telegramDisplayName(&request.From),
		Segments:    []MessageSegment{{Type: "request", Data: data}},
		oneBotRequest: &OneBotRequestEvent{
			RequestType: "group",
			SubType:     "add",
			UserID:      userID,
			GroupID:     chatID,
			Comment:     comment,
			Flag:        flag,
		},
	}, true
}
