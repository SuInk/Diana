// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"log"
	"strings"
	"time"
)

// 本群约定（见 MemoryAudienceGroup）在回复里单独成段，紧跟在风格笔记后面。
//
// 它不走长期记忆那一层：那一层按相关性召回，「别说草」只有在别人的消息里刚好出现
// 「草」时才会被带上，而 Diana 说草的时候对方往往根本没提这个字。就算常驻，也排在
// 提示词前段，压不住尾部风格笔记里那句「草 表示笑喷」。

const (
	// groupRuleMaxItems 限制一次带进回复的条数：这一段每轮都在，不能无限长。
	groupRuleMaxItems = 8
	// groupRuleItemMaxRunes 限制单条约定的长度。
	groupRuleItemMaxRunes = 80
)

// groupRules 取本群还在有效期内的约定。查不到或出错都当没有。
func (r *Runtime) groupRules(event MessageEvent) []StructuredMemoryItem {
	if event.Kind != EventKindGroup || strings.TrimSpace(event.GroupID) == "" {
		return nil
	}
	r.mu.RLock()
	store := r.structuredMemory
	r.mu.RUnlock()
	if store == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	items, err := store.ListStructuredMemories(ctx, StructuredMemoryQuery{
		Session: sessionKey(event), CurrentSessionOnly: true, GroupRulesOnly: true,
		Now: time.Now(), MaxCandidates: groupRuleMaxItems,
	})
	if err != nil {
		log.Printf("diana group rules load failed: %v", err)
		return nil
	}
	rules := make([]StructuredMemoryItem, 0, len(items))
	for _, item := range items {
		if IsGroupRule(item) && strings.TrimSpace(item.Content) != "" && len(rules) < groupRuleMaxItems {
			rules = append(rules, item)
		}
	}
	return rules
}

// groupRulesPrompt 是回复尾部那段「这个群的约定」，没有约定时返回空串。
// 它不看风格学习开没开：约定是群友当面提的，不是学出来的。
func (r *Runtime) groupRulesPrompt(event MessageEvent, cfg BotConfig) string {
	rules := r.groupRules(event)
	if len(rules) == 0 {
		return ""
	}
	lines := make([]string, 0, len(rules))
	for _, rule := range rules {
		lines = append(lines, "- "+truncateRunes(strings.Join(strings.Fields(rule.Content), " "), groupRuleItemMaxRunes))
	}
	return cfg.promptf(promptGroupRulesSpec, map[string]string{"rules": strings.Join(lines, "\n")})
}

const promptGroupRules = "【这个群的约定】群友明确提过，在这个群里别再这样说。不管在回谁都照这个来，上面风格笔记里有、你以前的回复里说过的也一样不用：\n{rules}"

var promptGroupRulesSpec = tailSpec("group_rules", "这个群的约定", "群聊里有人要机器人在这个群别再说某个词、口头禅或表情，被长期记忆记成本群约定后注入，紧跟在风格笔记后面，对全群生效，默认 30 天后失效。",
	promptGroupRules,
	PromptVar{Name: "rules", Description: "本群还在有效期内的约定，一行一条"})
