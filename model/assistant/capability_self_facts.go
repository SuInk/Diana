// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import "strings"

// capabilityTrigger 是能力条目的命中条件：Any 里任一词出现，且 With 为空或 With
// 里任一词也出现。匹配不分大小写、按子串，中文没有词边界。
type capabilityTrigger struct {
	Any  []string
	With []string
}

// capabilitySelfFactLimit 限制一轮最多注入几条，命中再多也只带最前面的。
const capabilitySelfFactLimit = 2

// capabilitySelfFactContext 把当前消息命中的「关于自己的事实」拼成一段尾部上下文。
//
// capabilities 工具要模型自己想起来去查；模型对「自以为知道」的问题（比如 QQ 协议端
// 用哪个）不会去查，照着训练印象答。少数答错代价高的事实因此按触发词直接带上，
// 内容和 capabilities 检索到的是同一条，不会两边说法不一致。
func capabilitySelfFactContext(text string) string {
	text = strings.ToLower(text)
	if strings.TrimSpace(text) == "" {
		return ""
	}
	var facts []string
	for _, document := range coreCapabilityDocuments {
		if !document.Enabled || !capabilityTriggersMatch(document.Triggers, text) {
			continue
		}
		facts = append(facts, "- "+document.Title+"："+document.Content)
		if len(facts) == capabilitySelfFactLimit {
			break
		}
	}
	if len(facts) == 0 {
		return ""
	}
	return "【关于我自己的已知事实】下面是关于我（Diana）自身的确定信息。本轮话题涉及时以此为准，不要凭印象另说；和话题无关就忽略，也不要主动提起。\n" + strings.Join(facts, "\n")
}

func capabilityTriggersMatch(triggers []capabilityTrigger, text string) bool {
	for _, trigger := range triggers {
		if capabilityAnyKeyword(trigger.Any, text) && (len(trigger.With) == 0 || capabilityAnyKeyword(trigger.With, text)) {
			return true
		}
	}
	return false
}

func capabilityAnyKeyword(keywords []string, text string) bool {
	for _, keyword := range keywords {
		if keyword != "" && strings.Contains(text, keyword) {
			return true
		}
	}
	return false
}
