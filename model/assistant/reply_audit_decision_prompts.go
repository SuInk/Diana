// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"strings"

	"github.com/SuInk/diana/model/llm"
)

// 判断表的文字也能在界面上改。默认值直接从 replyAuditDecisionSpec 摆出的那张全量表里
// 取，判据仍只写一份：每题的说明一段，是非题再加「判是」「判否」两段，单选题的选项
// 描述合成一段，一行一个「值：描述」。
//
// 题目本身（键、题型、档位分值、回填路径）不开放：那些是解析器的契约，改了答案就
// 回填不进 JSON。档位锚点和题目标题也不开放，它们短，且和分值一一对应，改错一档
// 整题的刻度就歪了。

const auditDecisionPromptPrefix = "audit.decision."

type auditDecisionPrompts struct {
	instructions *PromptSpec
	trueCriteria *PromptSpec
	falseCrit    *PromptSpec
	options      *PromptSpec
}

var auditDecisionPromptsByKey = registerAuditDecisionPrompts()

func registerAuditDecisionPrompts() map[string]auditDecisionPrompts {
	full := replyAuditDecisionSpec(replyAuditNeed{Quality: true, AccountSafety: true, Loop: true, Density: &replyDensity{}, Closing: true, Fatigue: true})
	byKey := make(map[string]auditDecisionPrompts, len(full.Questions))
	for _, q := range full.Questions {
		base := auditDecisionPromptPrefix + q.Key
		title := "审核判断 · " + q.Label
		prompts := auditDecisionPrompts{instructions: registerPrompt(PromptSpec{
			Key: base, Group: PromptGroupAudit, Title: title,
			Usage:   "发送前审核绑的是只做判断的模型时，这道题的作答说明。绑对话模型时不用这张表，改这里没有效果。",
			Default: q.Instructions,
		})}
		if q.Kind == llm.DecisionNoul {
			prompts.trueCriteria = registerPrompt(PromptSpec{
				Key: base + ".true", Group: PromptGroupAudit, Title: title + " · 什么算「是」",
				Usage: "什么情况下这道是非题答「是」。", Default: q.TrueCriteria,
			})
			prompts.falseCrit = registerPrompt(PromptSpec{
				Key: base + ".false", Group: PromptGroupAudit, Title: title + " · 什么算「否」",
				Usage: "什么情况下这道是非题答「否」。", Default: q.FalseCriteria,
			})
		}
		if q.Kind == llm.DecisionChoice {
			lines := make([]string, 0, len(q.Options))
			for _, option := range q.Options {
				lines = append(lines, option.Value+"："+option.Description)
			}
			prompts.options = registerPrompt(PromptSpec{
				Key: base + ".options", Group: PromptGroupAudit, Title: title + " · 各选项含义",
				Usage:   "一行一个「值：描述」。冒号前的值不能改，只改描述；删掉的行、写错的值沿用内置描述。",
				Default: strings.Join(lines, "\n"),
			})
		}
		byKey[q.Key] = prompts
	}
	return byKey
}

// replyAuditDecisionSpecForConfig 按这台机器人的覆盖改写判断表里的文字。
func replyAuditDecisionSpecForConfig(need replyAuditNeed, cfg BotConfig) *llm.DecisionSpec {
	spec := replyAuditDecisionSpec(need)
	if len(cfg.PromptOverrides) == 0 {
		return spec
	}
	for index := range spec.Questions {
		q := &spec.Questions[index]
		prompts, ok := auditDecisionPromptsByKey[q.Key]
		if !ok {
			continue
		}
		q.Instructions = cfg.prompt(prompts.instructions)
		if prompts.trueCriteria != nil {
			q.TrueCriteria = cfg.prompt(prompts.trueCriteria)
			q.FalseCriteria = cfg.prompt(prompts.falseCrit)
		}
		if prompts.options != nil {
			q.Options = applyAuditOptionDescriptions(q.Options, cfg.prompt(prompts.options))
		}
	}
	return spec
}

func applyAuditOptionDescriptions(options []llm.DecisionOption, text string) []llm.DecisionOption {
	described := map[string]string{}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(line), "-•"))
		value, description, found := strings.Cut(line, "：")
		if !found {
			value, description, found = strings.Cut(line, ":")
		}
		if found && strings.TrimSpace(description) != "" {
			described[strings.TrimSpace(value)] = strings.TrimSpace(description)
		}
	}
	result := make([]llm.DecisionOption, len(options))
	for index, option := range options {
		if description, ok := described[option.Value]; ok {
			option.Description = description
		}
		result[index] = option
	}
	return result
}
