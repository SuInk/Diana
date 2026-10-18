// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import "github.com/SuInk/diana/model/llm"

// ResidentFootprint 是 Runner 每一步都随请求下发、与当前消息无关的那部分：协议提示词
// （含按需工具目录和扩展说明）、常驻工具的完整定义、Skill 名称与用途目录。
//
// 常驻档位改的就是这几块，但它们在 assistant 那一层的系统提示词之外，上下文占比
// 以前根本看不到——把一条 MCP 调成常驻，底价纹丝不动，看的人没法判断这一档值不值。
type ResidentFootprint struct {
	SystemPrompt  string
	Tools         []llm.ToolDefinition
	SkillsCatalog string
}

// ResidentFootprint 按 Run 开始时的状态算一遍：没有加载过任何按需工具，Skill 只带目录，正文按需读取。不改 Runner 自身状态。
func (r *Runner) ResidentFootprint() ResidentFootprint {
	if r == nil || r.registry == nil {
		return ResidentFootprint{}
	}
	probe := *r
	probe.loader = newDeferredToolLoader(r.registry, r.cfg.CoreTools)
	catalog := RenderSkillsCatalog(r.registry.Skills(), r.cfg.SkillsListBudget)
	return ResidentFootprint{
		SystemPrompt:  probe.systemPrompt(),
		Tools:         probe.turnDefinitions(false),
		SkillsCatalog: catalog,
	}
}
