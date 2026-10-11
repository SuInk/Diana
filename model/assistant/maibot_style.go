// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"strings"

	"github.com/SuInk/diana/internal/maibotreply"
)

const maiBotCleanVocabulary = "不说粗口、谐音粗口或辱骂，包括用‘草’‘卧槽’‘我靠’‘tmd’表达惊讶；不模仿历史中的脏话。正常词语、代码和客观释义不受影响，不解释这条规则。"

const maiBotTrailingPeriodPreference = "日常短回复的最后一句通常不加中文或英文句号，问号、感叹号和省略号按语义自然使用。技术说明、多句长回答、代码、网址、文件路径和引用原文的标点照常保留，不要机械删除句号。"

const maiBotNoParentheticalLaugh = "不要使用‘（笑’‘（笑）’‘(笑’之类括号笑声或文字旁白，也不要照抄聊天记录里的这种旧口癖；觉得好笑时直接用自然措辞表达。"

func maiBotStyle(cfg BotConfig) string {
	if style := strings.TrimSpace(cfg.MaiBotReplyStyle); style != "" {
		return style
	}
	return maibotreply.DefaultReplyStyle
}

// maiBotStylePrompt borrows only MaiBot's wording guidance. Reply target,
// participation, facts, tools, identity and delivery remain Diana's job.
func maiBotStylePrompt(event MessageEvent, cfg BotConfig) string {
	attention := maibotreply.PrivateAttention
	if event.Kind == EventKindGroup {
		lines := strings.Split(maibotreply.GroupAttention, "\n")
		if len(lines) >= 3 {
			// Exclude the upstream frequency decision: Diana's participation score
			// has already decided whether this turn should reply.
			attention = strings.Join(lines[1:3], "\n")
		}
	}
	return joinPromptSections(maiBotStyle(cfg), attention, maiBotCleanVocabulary, maiBotNoParentheticalLaugh, maiBotTrailingPeriodPreference)
}

func maiBotStyleClosingAnchor(cfg BotConfig) string {
	return "最后措辞遵循这一轮的 MaiBot 表达风格：" + maiBotStyle(cfg) + "\n" + maiBotNoParentheticalLaugh + "\n" + maiBotTrailingPeriodPreference
}
