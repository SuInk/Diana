// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// QQ 官方机器人单聊的「对方正在输入」（msg_type 6 + input_notify）。要带被动回复的
// msg_id 和 msg_seq，平台没说占不占这条消息的回复次数（单聊一条消息 60 分钟内只能回
// 4～5 次，各页说法不一），所以默认关，开了也只在每条入站消息上发一次，最长显示 60 秒，
// 正式回复发出后状态自然被替换。
const (
	qqMsgTypeInputNotify = 6
	qqInputNotifyTyping  = 1
	qqInputNotifySeconds = 60
	qqInputNotifyTTL     = 10 * time.Minute
)

// SendChatAction 发「正在输入」。群聊、频道和没有被动回复 ID 的消息直接跳过。
func (c *QQOfficialChannel) SendChatAction(ctx context.Context, msg OutgoingMessage, _ string) error {
	target, isGroup := platformChatTarget(msg)
	replyID := strings.TrimSpace(msg.PassiveReplyMessageID)
	if isGroup || target == "" || replyID == "" || !c.claimInputNotify(replyID) {
		return nil
	}
	auth, err := c.authHeader(ctx)
	if err != nil {
		return err
	}
	body := map[string]any{
		"msg_type":     qqMsgTypeInputNotify,
		"input_notify": map[string]any{"input_type": qqInputNotifyTyping, "input_second": qqInputNotifySeconds},
		"msg_seq":      c.nextPassiveSeq(replyID),
	}
	if strings.HasPrefix(replyID, qqInteractionEventPrefix) {
		body["event_id"] = replyID
	} else {
		body["msg_id"] = replyID
	}
	c.mu.RLock()
	client := c.client
	c.mu.RUnlock()
	_, err = platformJSONRequest(ctx, client, http.MethodPost, c.apiBase()+"/v2/users/"+target+"/messages",
		map[string]string{"Authorization": auth}, body)
	if err != nil {
		c.noteTokenRejected(err)
		return fmt.Errorf("qq: 输入状态发送失败: %w", err)
	}
	return nil
}

// claimInputNotify 让一条入站消息只发一次输入状态：分条回复中间会重新点亮输入状态，
// 每次都发就白占回复次数。
func (c *QQOfficialChannel) claimInputNotify(replyID string) bool {
	c.inputNotifyMu.Lock()
	defer c.inputNotifyMu.Unlock()
	now := time.Now()
	for id, at := range c.inputNotified {
		if now.Sub(at) > qqInputNotifyTTL {
			delete(c.inputNotified, id)
		}
	}
	if _, done := c.inputNotified[replyID]; done {
		return false
	}
	if c.inputNotified == nil {
		c.inputNotified = map[string]time.Time{}
	}
	c.inputNotified[replyID] = now
	return true
}
