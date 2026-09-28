// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"log"
	"strings"
	"time"
)

// 群友叫 Diana「在这个群别再说某个词」时，这条要求对全群生效。
//
// 长期交互要求（instruction）默认只跟着提要求的人走：谁说的，Diana 回谁的时候才常驻
// 带上。这对「叫我主人」「跟我说鼠话」这类个人口味是对的，对「别说草了」就不对——
// 线上有人提了，Diana 当场答应，转头接别人的话照样「草，」开头，一晚上又说了 7 次。
// 风格笔记里写着「草 表示笑喷」，每轮都在反向提示，比偶尔检索到的一条记忆强得多。
//
// 所以记忆门控把这类要求记成 instruction.group.avoid.<词>，回复时单独查出来放在风格
// 笔记后面，谁发言都带。只收「别说什么」：它只会让 Diana 少说，不会被一个人拿来给
// 全群换腔调；要加口头禅、换语气的仍按普通 instruction 只对本人生效。
//
// 这类要求有时效：群里的梗和情绪会变，一句「别说草了」不该永远管下去。落库时最多
// 留 GroupAvoidRetentionDays 天，说了「今天别说」这类更短的期限就按说的算；过期前
// 有人再提一次，同一条的期限往后顺延。

const (
	// GroupAvoidMemoryKeyPrefix 是落库后的 key 前缀（normalizeMemoryKey 把下划线也变成点）。
	GroupAvoidMemoryKeyPrefix = "instruction.group.avoid."
	// GroupAvoidRetentionDays 是这类要求最长的有效天数，存储层按它封顶。
	GroupAvoidRetentionDays = 30
	// groupAvoidMaxItems 限制一次带进回复的条数：这一段每轮都在，不能无限长。
	groupAvoidMaxItems = 8
	// groupAvoidItemMaxRunes 限制单条要求的长度。
	groupAvoidItemMaxRunes = 80
)

// groupAvoidRequests 取这个群里群友要 Diana 别再说的那些要求。查不到或出错都当没有。
func (r *Runtime) groupAvoidRequests(event MessageEvent) []StructuredMemoryItem {
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
		Session: sessionKey(event), CurrentSessionOnly: true, Now: time.Now(),
		Kinds: []MemoryKind{MemoryKindInstruction}, KeyPrefix: GroupAvoidMemoryKeyPrefix,
		MaxCandidates: groupAvoidMaxItems,
	})
	if err != nil {
		log.Printf("diana group avoid requests load failed: %v", err)
		return nil
	}
	result := make([]StructuredMemoryItem, 0, len(items))
	for _, item := range items {
		if isGroupAvoidMemory(item) && strings.TrimSpace(item.Content) != "" {
			result = append(result, item)
		}
	}
	if len(result) > groupAvoidMaxItems {
		result = result[:groupAvoidMaxItems]
	}
	return result
}

func isGroupAvoidMemory(item StructuredMemoryItem) bool {
	return item.Kind == MemoryKindInstruction && strings.HasPrefix(item.Key, GroupAvoidMemoryKeyPrefix)
}

// groupAvoidPrompt 是回复尾部那段「这个群不想听你说的」，没有要求时返回空串。
// 它不看风格学习开没开：要求是群友当面提的，不是学出来的。
func (r *Runtime) groupAvoidPrompt(event MessageEvent, cfg BotConfig) string {
	items := r.groupAvoidRequests(event)
	if len(items) == 0 {
		return ""
	}
	lines := make([]string, 0, len(items))
	for _, item := range items {
		lines = append(lines, "- "+truncateRunes(strings.Join(strings.Fields(item.Content), " "), groupAvoidItemMaxRunes))
	}
	return cfg.promptf(promptGroupAvoidSpec, map[string]string{"items": strings.Join(lines, "\n")})
}

const promptGroupAvoid = "【这个群不想听你说的】群友明确提过，在这个群里别再这样说。不管在回谁，都照这个来；风格笔记里有、你以前的回复里说过的也一样不用：\n{items}"

var promptGroupAvoidSpec = tailSpec("group_avoid", "群友不想听你说的", "群聊里有人要机器人在这个群别再说某个词、口头禅或表情，被长期记忆记下后注入，紧跟在风格笔记后面，对全群生效。",
	promptGroupAvoid,
	PromptVar{Name: "items", Description: "这个群记下的「别再说」要求，一行一条"})
