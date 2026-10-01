// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import "strings"

// 不学怪话：机器人学群友说话有两条路——风格笔记（group_style.go，后台读群聊写一段
// 「这个群怎么说话」）和「学群友的腔调」（group_length_norm.go，每轮让模型照着上下文
// 里群友的原话学用词和接梗）。两条路都是原样照学：群里有人满嘴脏话、开黄腔、引战、
// 起哄让它认爹，学到的就是这些。
//
// 过滤不动群友的消息：群友照常说，历史照常带，只是告诉模型这几类不跟着学。写笔记时
// 不把它们记成「这个群的梗」；回复时，凡是注入了风格笔记或「学群友的腔调」，后面都
// 跟一段「这几类不学」。已经存下的笔记不重写，靠回复时这一段兜住。
//
// 内置四类之外，主人可以自己加规则（一行一条），机器人一份、群里一份，合并后追加在
// 内置那段后面。想改内置的四类本身，覆盖那两段提示词即可。

const promptStyleFilter = "群友的话里这几类不学：脏话、骂人和阴阳怪气地损人；黄段子、开车和擦边；地域、性别等歧视，引战和站队；认爹认妈、「我是你爹」、发癫复读这类会让你说出不像自己的话的烂梗。群里再常见、别人怎么起哄也不跟着说，想接话就用你自己的方式接。"

var promptStyleFilterSpec = tailSpec("group_style.filter", "不学怪话", "「不学怪话」开着（默认）、这一轮注入了「这个群的说话风格」或「学群友的腔调」时，紧跟在后面：列出群友的哪几类话不跟着学。",
	promptStyleFilter)

const promptGroupStyleLearnFilter = "这个机器人不学怪话，下面几类在群里再常见也不要写进笔记，不要把它们写成这个群的梗、口头禅或调侃尺度：脏话、骂人和阴阳怪气地损人；黄段子、开车和擦边；地域、性别等歧视，引战和站队；认爹认妈、「我是你爹」、发癫复读这类让人说出不像自己的话的烂梗。"

var promptGroupStyleLearnFilterSpec = registerPrompt(PromptSpec{
	Key:     "memory.group_style.learn_filter",
	Group:   PromptGroupMemory,
	Title:   "风格学习：不记怪话",
	Usage:   "「不学怪话」开着（默认）时接在「风格学习：总结这个群怎么说话」后面，让写出的风格笔记不收脏话、擦边、引战和崩人设的烂梗。",
	Default: promptGroupStyleLearnFilter,
})

const (
	// styleFilterMaxRules 和 styleFilterRuleMaxRunes 给自定义规则设上限：每条都跟着
	// 回复尾部走，写成长文就成了第二份人设。
	styleFilterMaxRules     = 20
	styleFilterRuleMaxRunes = 100
)

const promptStyleFilterCustom = "另外这几条也不学：\n{rules}"

var promptStyleFilterCustomSpec = tailSpec("group_style.filter_custom", "不学怪话：自定义规则", "主人在机器人或群里写了「不学怪话」的自定义规则时，接在「不学怪话」后面。",
	promptStyleFilterCustom,
	PromptVar{Name: "rules", Description: "机器人和本群的自定义规则合并去重后，每条一行、以「- 」开头"})

const promptGroupStyleLearnFilterCustom = "另外这几条也不要写进笔记：\n{rules}"

var promptGroupStyleLearnFilterCustomSpec = registerPrompt(PromptSpec{
	Key:     "memory.group_style.learn_filter_custom",
	Group:   PromptGroupMemory,
	Title:   "风格学习：不记怪话的自定义规则",
	Usage:   "主人写了「不学怪话」的自定义规则时，接在「风格学习：不记怪话」后面。",
	Default: promptGroupStyleLearnFilterCustom,
	Vars:    []PromptVar{{Name: "rules", Description: "机器人和本群的自定义规则合并去重后，每条一行、以「- 」开头"}},
})

// styleFilterRules 把自定义规则整理成一条一项：去掉空行和列表符号，去重，按上限截。
func styleFilterRules(raw string) []string {
	var rules []string
	seen := map[string]bool{}
	for _, line := range strings.Split(raw, "\n") {
		rule := strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(line), "-*•·"))
		if runes := []rune(rule); len(runes) > styleFilterRuleMaxRunes {
			rule = string(runes[:styleFilterRuleMaxRunes])
		}
		if rule == "" || seen[rule] {
			continue
		}
		seen[rule] = true
		rules = append(rules, rule)
		if len(rules) == styleFilterMaxRules {
			break
		}
	}
	return rules
}

// mergeStyleFilterRules 合并机器人和本群的自定义规则：群里的是追加，不是替换。
func mergeStyleFilterRules(bot, group string) string {
	if strings.TrimSpace(group) == "" {
		return bot
	}
	return strings.Join(styleFilterRules(bot+"\n"+group), "\n")
}

// styleFilterCustomRules 给出自定义规则那一段；没有规则时返回空串。
func styleFilterCustomRules(cfg BotConfig, spec *PromptSpec) string {
	rules := styleFilterRules(cfg.StyleFilterRules)
	if len(rules) == 0 {
		return ""
	}
	return cfg.promptf(spec, map[string]string{"rules": "- " + strings.Join(rules, "\n- ")})
}

// styleFilterLearnPrompt 是写风格笔记时接在学习提示词后面的部分；过滤关着时为空。
func styleFilterLearnPrompt(cfg BotConfig) string {
	if !styleFilterEnabled(cfg) {
		return ""
	}
	return joinPromptSections(cfg.prompt(promptGroupStyleLearnFilterSpec), styleFilterCustomRules(cfg, promptGroupStyleLearnFilterCustomSpec))
}

func styleFilterEnabled(cfg BotConfig) bool {
	return boolValue(cfg.StyleFilterEnabled, true)
}

// styleFilterPrompt 在这一轮注入了任何「学群友」的段落（sections 里有非空的）时给出
// 「这几类不学」；过滤关着或者这一轮没在学群友时返回空串。
func styleFilterPrompt(cfg BotConfig, sections ...string) string {
	if !styleFilterEnabled(cfg) {
		return ""
	}
	for _, section := range sections {
		if section != "" {
			return joinPromptSections(cfg.prompt(promptStyleFilterSpec), styleFilterCustomRules(cfg, promptStyleFilterCustomSpec))
		}
	}
	return ""
}
