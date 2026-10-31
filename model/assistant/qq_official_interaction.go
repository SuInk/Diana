package assistant

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
)

// qqOfficialInteraction 是 INTERACTION_CREATE 的 d：回调按钮被点后推送。
type qqOfficialInteraction struct {
	ID                string `json:"id"`
	ChatType          int    `json:"chat_type"`
	Timestamp         string `json:"timestamp"`
	GroupOpenID       string `json:"group_openid"`
	GroupMemberOpenID string `json:"group_member_openid"`
	UserOpenID        string `json:"user_openid"`
	Data              struct {
		Resolved struct {
			ButtonData string `json:"button_data"`
			ButtonID   string `json:"button_id"`
		} `json:"resolved"`
	} `json:"data"`
}

const (
	qqInteractionChatGroup = 1
	qqInteractionChatC2C   = 2
)

// handleInteraction 应答按钮回调，再把按钮数据当成用户发来的一句话交给运行时。
// 回复走被动回复，event_id 用网关报文外层的完整 id。
func (c *QQOfficialChannel) handleInteraction(ctx context.Context, payload qqGatewayPayload) {
	var interaction qqOfficialInteraction
	if err := json.Unmarshal(payload.Data, &interaction); err != nil || interaction.ID == "" {
		return
	}
	// 不应答的话客户端会一直转圈并提示操作失败。
	c.ackInteraction(ctx, interaction.ID)
	event, ok := qqOfficialEventFromInteraction(payload.ID, interaction, c.Status().SelfID)
	if !ok {
		return
	}
	c.mu.RLock()
	handler := c.handler
	c.mu.RUnlock()
	if handler != nil {
		_ = handler(ctx, event)
	}
}

func (c *QQOfficialChannel) ackInteraction(ctx context.Context, id string) {
	auth, err := c.authHeader(ctx)
	if err != nil {
		return
	}
	c.mu.RLock()
	client := c.client
	c.mu.RUnlock()
	_, err = platformJSONRequest(ctx, client, http.MethodPut, c.apiBase()+"/interactions/"+url.PathEscape(id),
		map[string]string{"Authorization": auth}, map[string]any{"code": 0})
	c.noteTokenRejected(err)
}

// qqOfficialEventFromInteraction 把按钮回调映射成统一事件。MessageID 用外层事件 ID，
// 出站据此认出要填 event_id 而不是 msg_id。
func qqOfficialEventFromInteraction(eventID string, interaction qqOfficialInteraction, selfID string) (MessageEvent, bool) {
	eventID = strings.TrimSpace(eventID)
	text := strings.TrimSpace(interaction.Data.Resolved.ButtonData)
	if !strings.HasPrefix(eventID, qqInteractionEventPrefix) || text == "" {
		return MessageEvent{}, false
	}
	event := MessageEvent{
		Platform:   PlatformQQOfficial,
		Time:       platformEventTime(interaction.Timestamp),
		SelfID:     selfID,
		MessageID:  eventID,
		RawMessage: text,
		Segments:   platformTextSegments(text, ""),
		ToMe:       true,
	}
	switch interaction.ChatType {
	case qqInteractionChatGroup:
		event.PlatformScope = "qq_group"
		event.Kind = EventKindGroup
		event.MessageType = "group"
		event.GroupID = interaction.GroupOpenID
		event.UserID = interaction.GroupMemberOpenID
	case qqInteractionChatC2C:
		event.Kind = EventKindPrivate
		event.MessageType = "private"
		event.UserID = interaction.UserOpenID
	default:
		return MessageEvent{}, false
	}
	if event.UserID == "" || (event.Kind == EventKindGroup && event.GroupID == "") {
		return MessageEvent{}, false
	}
	return event, true
}
