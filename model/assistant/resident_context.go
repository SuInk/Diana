// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"strings"

	"github.com/SuInk/diana/model/llm"
)

// 常驻上下文快照：把「每轮都会注入、不看当前消息内容」的那几块原文摆出来。
//
// 预算分配图（ContextBudgetBreakdownForGroup）答的是「窗口怎么切的」，调试记录里的
// layers 答的是「这一轮各层装了多少、丢了什么」，两者都只有数字。真要回答「它每轮
// 到底被灌了些什么」，得把原文摆出来——这正是改人设、查串味、算 token 账时要看的。
//
// 按发言者或当前消息变化的那几层不在这里：检索记忆按相关性召回、常驻核心记忆按
// 发言者取（见 memory_context.go 里 coreCurrentMemory 的判定）、笔记本和世界书的
// 触发式设定要命中关键词。它们不是「常驻」，摆进来只会让人以为每轮都在付这笔钱。
const (
	ResidentBlockSoul        = "soul"
	ResidentBlockPersona     = "persona"
	ResidentBlockPromptRules = "prompt_rules"
	ResidentBlockWorldBook   = "world_book"
	ResidentBlockSelfNotes   = "self_notes"
	ResidentBlockSessionNote = "session_thread"
)

// ResidentContextBlock 是快照里的一块。
type ResidentContextBlock struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	// Tokens 是这块正文的估算 token。估法和编排请求时用的是同一个函数，数字对得上。
	Tokens int64 `json:"tokens"`
	// Budget 是这块所在层的 token 配额；0 表示这块不单独占一层配额（人设和固定
	// 规则属于系统提示词，跟着请求预算走，不在分层配额里）。
	Budget  int64  `json:"budget,omitempty"`
	Content string `json:"content,omitempty"`
	// Note 说明这块为什么是空的、或者它的数字要怎么读。
	Note string `json:"note,omitempty"`
}

// ResidentContextSnapshot 是一台机器人（可选带群）的常驻上下文。
type ResidentContextSnapshot struct {
	ProfileID     string                 `json:"profile_id,omitempty"`
	GroupID       string                 `json:"group_id,omitempty"`
	ContextWindow int64                  `json:"context_window"`
	Blocks        []ResidentContextBlock `json:"blocks"`
	// TotalTokens 是各块合计，也就是「什么都没说的时候，这台机器人每轮的底价」。
	TotalTokens int64 `json:"total_tokens"`
	// Note 说明哪些层故意没收进来。
	Note string `json:"note,omitempty"`
}

const residentContextNote = "只列每轮都注入、与当前消息无关的内容。检索记忆、笔记本命中、世界书的触发式设定、跨群召回按当前消息命中才进；常驻核心记忆按发言者取，也不在这里。"

// ResidentContextForGroup 组装常驻上下文快照。groupID 留空时按私聊场景取，
// 会话便签那块会因为没有具体会话而为空。
func (r *Runtime) ResidentContextForGroup(ctx context.Context, profileID, groupID string) ResidentContextSnapshot {
	profileID = strings.TrimSpace(profileID)
	groupID = strings.TrimSpace(groupID)
	event := MessageEvent{Kind: EventKindPrivate, ProfileID: profileID}
	if groupID != "" {
		event.Kind = EventKindGroup
		event.GroupID = groupID
	}
	cfg := r.effectiveConfigForEvent(event)
	window := r.promptContextWindowTokens(event, cfg)
	snapshot := ResidentContextSnapshot{ProfileID: profileID, GroupID: groupID, ContextWindow: window, Note: residentContextNote}

	persona := strings.TrimSpace(cfg.SystemPrompt)
	// head 里除人设之外的部分就是那几千字固定规则。registry 传 nil 表示「按全部
	// 工具都注册」算，所以这里是上限：实际注入哪几条随当轮注册的工具增减。
	head, _ := r.systemPromptPartsWithRelationshipAndAgentTools(event, nil, false, RelationshipPolicy{}, cfg.AgentEnabled, nil)
	// 规则那块是 head 去掉品格和人设之后剩下的部分：同一段文字不能在两块里各算
	// 一次，否则合计会把它算两遍。
	rules := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(head), cfg.Soul.Render()))
	rules = strings.TrimSpace(strings.TrimPrefix(rules, persona))

	snapshot.appendBlock(ResidentContextBlock{
		Key: ResidentBlockSoul, Label: "品格（soul）", Content: cfg.Soul.Render(),
		Note: "身份、价值、硬边界，排在系统提示词最前面。只有人能改，分群覆盖动不了它。",
	})
	snapshot.appendBlock(ResidentContextBlock{
		Key: ResidentBlockPersona, Label: "人设正文", Content: persona,
		Note: "系统提示词稳定头部的第一行，只有人能改（WebUI 或 soul.md）。",
	})
	snapshot.appendBlock(ResidentContextBlock{
		Key: ResidentBlockPromptRules, Label: "固定提示词规则", Content: rules,
		Note: "按「全部工具都注册」计算，是上限；实际注入哪几条随本轮注册的工具增减。随发言者变化的那段（权限、昵称、语气锚点）在请求尾部，不在这里。",
	})
	snapshot.appendBlock(ResidentContextBlock{
		Key: ResidentBlockWorldBook, Label: "世界书常驻设定", Content: r.worldBookContext(ctx, event, ""),
		Budget: worldBookContextTokenBudget,
		Note:   "只含标了「常驻」的节点；按关键词触发的设定要命中才进。",
	})
	selfNotes, _ := r.selfNoteContext(ctx, event)
	snapshot.appendBlock(ResidentContextBlock{
		Key: ResidentBlockSelfNotes, Label: "自述", Content: selfNotes, Budget: selfNoteBudget(window),
		Note: "机器人自己写的自我认知，默认关闭。",
	})
	snapshot.appendBlock(ResidentContextBlock{
		Key: ResidentBlockSessionNote, Label: "会话便签", Content: r.sessionThreadNote(ctx, event), Budget: sessionThreadBudget(window),
		Note: "这个会话「聊到哪一步」的便签，由后台随对话滚动更新。",
	})
	return snapshot
}

func (s *ResidentContextSnapshot) appendBlock(block ResidentContextBlock) {
	block.Content = strings.TrimSpace(block.Content)
	block.Tokens = llm.EstimateTextTokens(block.Content)
	s.TotalTokens += block.Tokens
	s.Blocks = append(s.Blocks, block)
}
