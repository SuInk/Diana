// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"strings"
	"time"

	"github.com/SuInk/diana/model/agent"
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
	ResidentBlockPersona     = "persona"
	ResidentBlockPromptRules = "prompt_rules"
	ResidentBlockWorldBook   = "world_book"
	ResidentBlockSelfNotes   = "self_notes"
	ResidentBlockSessionNote = "session_thread"
	ResidentBlockAgentPrompt = "agent_protocol"
	ResidentBlockAgentTools  = "agent_tools"
	ResidentBlockSkills      = "skills"
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

const residentContextNote = "只列每轮都注入、与当前消息无关的内容。检索记忆、笔记本命中、世界书的触发式设定、命中触发词的 Skill 正文、跨群召回按当前消息命中才进；常驻核心记忆按发言者取，也不在这里。"

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
	// 规则那块是 head 去掉 SOUL.md 之后剩下的部分：同一段文字不能在两块里各算
	// 一次，否则合计会把它算两遍。
	rules := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(head), persona))

	snapshot.appendBlock(ResidentContextBlock{
		Key: ResidentBlockPersona, Label: "SOUL.md", Content: persona,
		Note: "排在系统提示词最前面，只有人能改；群可以整份覆盖。",
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
	if cfg.AgentEnabled {
		r.appendAgentFootprintBlocks(&snapshot, profileID, groupID)
	}
	return snapshot
}

// agentFootprint 是一轮 Agent 常驻开销的摘录。存算好的文本和数字而不是 Runner：
// Runner 跑完就关，注册表里的 MCP 会话也跟着释放。
type agentFootprint struct {
	systemPrompt  string
	toolNames     []string
	toolTokens    int64
	skillsCatalog string
	at            time.Time
}

// agentFootprintAnyGroup 是「这台机器人最近一轮，不管哪个会话」的键尾。
const agentFootprintAnyGroup = "\x00*"

func agentFootprintKey(profileID, groupID string) string {
	return strings.TrimSpace(profileID) + "\x00" + strings.TrimSpace(groupID)
}

// rememberAgentFootprint 记下这一轮的常驻开销。和档位目录一样只能取真实跑过的
// 一轮：工具按平台、权限、群开关逐个挂上去，不跑一轮算不出来。
func (r *Runtime) rememberAgentFootprint(event MessageEvent, runner *agent.Runner) {
	if r == nil || runner == nil {
		return
	}
	footprint := runner.ResidentFootprint()
	names := make([]string, 0, len(footprint.Tools))
	for _, tool := range footprint.Tools {
		names = append(names, tool.Name)
	}
	entry := agentFootprint{
		systemPrompt:  footprint.SystemPrompt,
		toolNames:     names,
		toolTokens:    llm.EstimateToolDefinitionsTokens(footprint.Tools),
		skillsCatalog: footprint.SkillsCatalog,
		at:            time.Now(),
	}
	groupID := ""
	if event.Kind == EventKindGroup {
		groupID = event.GroupID
	}
	r.agentResidencyMu.Lock()
	if r.agentFootprints == nil {
		r.agentFootprints = map[string]agentFootprint{}
	}
	r.agentFootprints[agentFootprintKey(event.ProfileID, groupID)] = entry
	r.agentFootprints[strings.TrimSpace(event.ProfileID)+agentFootprintAnyGroup] = entry
	r.agentResidencyMu.Unlock()
}

// appendAgentFootprintBlocks 把工具、MCP、Skill 这几块常驻开销补进快照。这几块才是
// 常驻档位改动的对象，也往往是底价里最大的一截。
func (r *Runtime) appendAgentFootprintBlocks(snapshot *ResidentContextSnapshot, profileID, groupID string) {
	r.agentResidencyMu.RLock()
	footprint, exact := r.agentFootprints[agentFootprintKey(profileID, groupID)]
	if !exact {
		footprint = r.agentFootprints[profileID+agentFootprintAnyGroup]
	}
	r.agentResidencyMu.RUnlock()
	if footprint.at.IsZero() {
		snapshot.appendBlock(ResidentContextBlock{
			Key: ResidentBlockAgentTools, Label: "工具、MCP 与 Skill",
			Note: "启动后还没有跑过一轮回复，这几块要等第一条消息之后才算得出来。",
		})
		return
	}
	source := "取自这个会话最近一轮回复（" + footprint.at.Format("01-02 15:04") + "）"
	if !exact {
		source = "这个会话还没回复过，取自这台机器人最近一轮别处的回复（" + footprint.at.Format("01-02 15:04") + "）；群开关和发言者权限不同，工具会有出入"
	}
	snapshot.appendBlock(ResidentContextBlock{
		Key: ResidentBlockAgentPrompt, Label: "Agent 协议与按需工具目录", Content: footprint.systemPrompt,
		Note: "按需工具只进这份目录（名字加一句用途），要用时先 tools_load。" + source + "。",
	})
	snapshot.appendBlock(ResidentContextBlock{
		Key: ResidentBlockAgentTools, Label: "常驻工具定义", Content: strings.Join(footprint.toolNames, "\n"),
		Tokens: footprint.toolTokens,
		Note:   "这些工具每一步都带完整 schema，数字按 schema 估算，正文只列名字。从机器人配置「上下文」的常驻名单里拿掉，就会挪进上面的目录。",
	})
	snapshot.appendBlock(ResidentContextBlock{
		Key: ResidentBlockSkills, Label: "Skill 目录与常驻正文", Content: footprint.skillsCatalog,
		Note: "只含配成常驻的 Skill 正文；声明了触发词的要命中才带，不在底价里。",
	})
}

func (s *ResidentContextSnapshot) appendBlock(block ResidentContextBlock) {
	block.Content = strings.TrimSpace(block.Content)
	// 工具定义的 token 按 schema 算，正文只放名字，由调用方给数。
	if block.Tokens == 0 {
		block.Tokens = llm.EstimateTextTokens(block.Content)
	}
	s.TotalTokens += block.Tokens
	s.Blocks = append(s.Blocks, block)
}
