// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"encoding/json"
	"strings"
)

// proactiveReplyRouteSystemPrompt 是接话判断那一步的系统提示词。运行时和界面预览走
// 同一个函数，预览看到的就是实际发出去的那一份。
func proactiveReplyRouteSystemPrompt(cfg BotConfig, chatIn chatInSettings) string {
	return proactiveReplyRouterPromptForChatIn(cfg.prompt(promptLegacyRouterSpec), cfg.ProactiveReplyExtraCriteria, chatIn, boolValue(cfg.SocialReplyEnabled, false), cfg)
}

// ParticipationPromptPreview 是接话评分这一步发给模型的内容，给界面预览用。
//
// 系统提示词和用户消息开头的任务说明都是按这台机器人的配置现拼的；用户消息里的上下文
// 是一段示例群聊，实际发送时换成当前消息和最近的群聊记录，图片随消息附带。
type ParticipationPromptPreview struct {
	System string `json:"system"`
	User   string `json:"user"`
	// Retry 是评分解析失败、重问一次时插在最前面的那条系统消息。
	Retry string `json:"retry"`
	// Decision 是绑了只做判断的模型时它收到的题目：同一套判据按题目摆，不读上面两条。
	Decision []ParticipationDecisionPreview `json:"decision"`
}

type ParticipationDecisionPreview struct {
	Label         string   `json:"label"`
	Instructions  string   `json:"instructions"`
	TrueCriteria  string   `json:"true_criteria,omitempty"`
	FalseCriteria string   `json:"false_criteria,omitempty"`
	Levels        []string `json:"levels,omitempty"`
}

// PreviewParticipationPrompt 按配置拼出接话评分发给模型的内容。配置可以是还没保存的那一份。
func PreviewParticipationPrompt(cfg BotConfig) (ParticipationPromptPreview, error) {
	chatIn := cfg.chatInSettings()
	payload, err := json.Marshal(participationPreviewPayload(cfg))
	if err != nil {
		return ParticipationPromptPreview{}, err
	}
	preview := ParticipationPromptPreview{
		System: proactiveReplyRouteSystemPrompt(cfg, chatIn),
		User:   strings.TrimSpace(cfg.prompt(promptParticipationRouteInstructionSpec) + string(payload)),
		Retry:  cfg.prompt(promptParticipationRetrySpec),
	}
	for _, question := range participationDecisionSpec(cfg.PromptOverrides).Questions {
		preview.Decision = append(preview.Decision, ParticipationDecisionPreview{
			Label:         question.Label,
			Instructions:  question.Instructions,
			TrueCriteria:  question.TrueCriteria,
			FalseCriteria: question.FalseCriteria,
			Levels:        question.Levels,
		})
	}
	return preview, nil
}

// participationPreviewPayload 是预览里的示例上下文：机器人上一句话之后，有人接着往下问。
// 字段和运行时的 proactiveReplyPayload 是同一个结构，只是内容是编的。
func participationPreviewPayload(cfg BotConfig) proactiveReplyPayload {
	age := func(seconds int64) *int64 { return &seconds }
	none := func() messageAddressing { return messageAddressing{ReplyTarget: "none", Mentions: []MessageMention{}} }
	botName := firstNonEmpty(strings.TrimSpace(cfg.Name), "机器人")
	lastBot := proactiveReplyHistoryItem{Addressing: none(), Sender: botName, Text: "上次说的那个展好像延期了", IsBot: true, AgeSeconds: age(60)}
	after := 1
	return proactiveReplyPayload{
		Addressing:           none(),
		CurrentText:          "有人知道改到哪天了吗",
		CurrentSender:        "小林",
		BotAccount:           firstNonEmpty(strings.TrimSpace(cfg.BotAccount), "10000"),
		BotAliases:           append([]string(nil), cfg.GroupTriggers...),
		ContextGapSeconds:    age(25),
		LastBotMessage:       &lastBot,
		MessagesAfterLastBot: &after,
		RecentMessages: []proactiveReplyHistoryItem{
			{Addressing: none(), Sender: "阿杰", Text: "啊？那我票白买了", AgeSeconds: age(25)},
			lastBot,
			{Addressing: none(), Sender: "小林", Text: "周末有人去看展吗", AgeSeconds: age(95)},
		},
		Candidates: []proactiveReplyCandidatePayload{{Addressing: none(), MessageID: "preview", Sender: "小林", AgeSeconds: age(0), IsCurrent: true}},
		AvailableReplyTools: []string{
			"web_search：始终注册的实时联网搜索；Provider 不可用时会返回明确配置或上游错误",
		},
	}
}
