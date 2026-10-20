package assistant

import (
	"strings"
)

// Target is self/other/unknown, independent of whether the bot may join in.
type MessageMention struct {
	UserID   string `json:"user_id,omitempty"`
	Username string `json:"username,omitempty"`
	// UsernameKnown distinguishes a missing observation from an explicitly absent username.
	UsernameKnown bool   `json:"username_known,omitempty"`
	Target        string `json:"target"`
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
		if strings.TrimSpace(id) == "all" {
			return "all"
		}
		if strings.TrimSpace(id) == "" || self == "" {
			return "unknown"
		}
		if strings.TrimSpace(id) == self {
			return "self"
		}
		return "other"
	}
	out := messageAddressing{ReplyTarget: "none", Mentions: []MessageMention{}}
	mentionIndexes := make(map[string]int)
	addMention := func(mention MessageMention) {
		mention.UserID = strings.TrimSpace(mention.UserID)
		mention.Username = strings.TrimSpace(mention.Username)
		key := "id:" + mention.UserID
		if mention.UserID == "" {
			key = "username:" + strings.ToLower(mention.Username)
		}
		if index, exists := mentionIndexes[key]; exists {
			previous := &out.Mentions[index]
			if previous.Username == "" {
				previous.Username = mention.Username
			}
			previous.UsernameKnown = previous.UsernameKnown || mention.UsernameKnown
			return
		}
		mentionIndexes[key] = len(out.Mentions)
		out.Mentions = append(out.Mentions, mention)
	}
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
		addMention(mention)
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
		addMention(MessageMention{UserID: id, Target: target})
	}
	for _, m := range out.Mentions {
		out.MentionsSelf = out.MentionsSelf || m.Target == "self"
		out.MentionsOther = out.MentionsOther || m.Target == "other"
	}
	return out
}

const messageAddressingRule = "平台关系 self=你、other=别人、unknown=未知；未知不等于自己，旁观接话不代表自己是原对话对象。"

func addressingPrompt(event MessageEvent, cfg BotConfig) string {
	addressing := addressingForEvent(event, cfg)
	var facts []string
	if addressing.ReplyTarget != "none" {
		fact := "引用=" + addressing.ReplyTarget
		if addressing.ReplyUserID != "" {
			fact += "(" + addressing.ReplyUserID + ")"
		}
		facts = append(facts, fact)
	}
	for _, mention := range addressing.Mentions {
		fact := "提及=" + mention.Target
		if mention.UserID != "" {
			fact += "(" + mention.UserID + ")"
		} else if mention.Username != "" {
			fact += "(@" + mention.Username + ")"
		}
		facts = append(facts, fact)
	}
	if len(facts) == 0 {
		return ""
	}
	return "【平台关系】" + strings.Join(facts, "；")
}
