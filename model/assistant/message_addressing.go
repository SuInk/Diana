package assistant

import (
	"encoding/json"
	"strings"
)

// Target is self/other/unknown, independent of whether the bot may join in.
type MessageMention struct {
	UserID   string `json:"user_id,omitempty"`
	Username string `json:"username,omitempty"`
	Target   string `json:"target"`
}

type messageAddressing struct {
	ReplyTarget    string           `json:"reply_target"`
	ReplyMessageID string           `json:"reply_message_id,omitempty"`
	ReplyUserID    string           `json:"reply_user_id,omitempty"`
	ReplySender    string           `json:"reply_sender,omitempty"`
	Mentions       []MessageMention `json:"mentions"`
	MentionsSelf   bool             `json:"mentions_self"`
	MentionsOther  bool             `json:"mentions_other"`
}

func addressingForEvent(event MessageEvent, cfg BotConfig) messageAddressing {
	self := firstNonEmpty(strings.TrimSpace(event.SelfID), strings.TrimSpace(cfg.BotAccount))
	classify := func(id string) string {
		if strings.TrimSpace(id) == "" || self == "" {
			return "unknown"
		}
		if strings.TrimSpace(id) == self {
			return "self"
		}
		return "other"
	}
	out := messageAddressing{ReplyTarget: "none", Mentions: []MessageMention{}}
	for _, segment := range event.Segments {
		if segment.Type == "reply" {
			out.ReplyTarget = "unknown"
			out.ReplyMessageID = strings.TrimSpace(segment.Data["id"])
		}
	}
	if q := event.Quoted; q != nil && !q.Semantic {
		out.ReplyTarget = classify(q.UserID)
		out.ReplyMessageID = q.MessageID
		out.ReplyUserID = q.UserID
		out.ReplySender = q.SenderName
	}
	for _, mention := range event.MentionTargets {
		if mention.UserID != "" {
			mention.Target = classify(mention.UserID)
		}
		out.Mentions = append(out.Mentions, mention)
	}
	for _, segment := range event.Segments {
		if segment.Type != "at" {
			continue
		}
		id := firstNonEmpty(segment.Data["qq"], segment.Data["user_id"])
		target := classify(id)
		if id == "all" {
			target = "all"
		}
		out.Mentions = append(out.Mentions, MessageMention{UserID: id, Target: target})
	}
	for _, m := range out.Mentions {
		out.MentionsSelf = out.MentionsSelf || m.Target == "self"
		out.MentionsOther = out.MentionsOther || m.Target == "other"
	}
	return out
}

const messageAddressingRule = "addressing 是平台引用与提及关系。reply_target=self 表示回复你，other 表示回复其他人，none 表示没有直接引用，unknown 表示无法确认；mentions 的 target 同理。不要把未知当成自己，也不要仅凭正文含 @ 或‘你’断定在喊你。允许旁观接话不代表你是原对话对象；参与别人的对话时，不要冒充被引用者或被提及者。引用别人但另有明确提及自己时，结合用户完整请求判断。"

func addressingPrompt(event MessageEvent, cfg BotConfig) string {
	body, _ := json.Marshal(addressingForEvent(event, cfg))
	return "\n【当前消息的对话对象】\n" + string(body) + "\n" + messageAddressingRule
}
