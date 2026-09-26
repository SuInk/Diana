// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"fmt"
	"sort"
	"strings"
	"unicode"
)

// maxToolSuggestions 是报错里最多列几个候选：多了模型会挨个去试。
const maxToolSuggestions = 3

// toolNameNoise 是模型编名字时爱加、但真实工具名里没有信息量的词。
var toolNameNoise = map[string]bool{"diana": true, "official": true, "tool": true, "tools": true}

// suggestToolNames 给编错的工具名找几个当前能用的相近名字。
//
// 线上模型会凭印象编名字：要的是 repository_issues，写成 github_issue、issue、
// issue_create；只说「不存在」它就接着猜，一轮能撞好几次。这里按名字里的词对
// 词匹配（issue 和 issues 算同一个），名字对不上再看描述里有没有提到，只列当前
// 视图里拿得到的工具，不会把没权限的名字漏给模型。
func suggestToolNames(registry *ToolRegistry, requested string) []string {
	query := toolNameTokens(requested)
	if registry == nil || len(query) == 0 {
		return nil
	}
	type candidate struct {
		name  string
		score int
	}
	var candidates []candidate
	for _, name := range registry.Names() {
		if name == requested || name == ToolsLoadToolName || name == ToolsExecuteToolName {
			continue
		}
		tool, ok := registry.Get(name)
		if !ok {
			continue
		}
		nameTokens := toolNameTokens(name)
		description := strings.ToLower(tool.Description())
		score := 0
		for _, q := range query {
			switch {
			case tokenMatches(q, nameTokens):
				score += 3
			case len(q) >= 4 && strings.Contains(description, q):
				score++
			}
		}
		if score > 0 {
			candidates = append(candidates, candidate{name, score})
		}
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].score != candidates[j].score {
			return candidates[i].score > candidates[j].score
		}
		return candidates[i].name < candidates[j].name
	})
	names := make([]string, 0, maxToolSuggestions)
	for _, c := range candidates {
		if len(names) == maxToolSuggestions {
			break
		}
		names = append(names, c.name)
	}
	return names
}

func toolNameTokens(name string) []string {
	fields := strings.FieldsFunc(strings.ToLower(name), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	tokens := fields[:0]
	for _, field := range fields {
		if len(field) >= 2 && !toolNameNoise[field] {
			tokens = append(tokens, field)
		}
	}
	return tokens
}

// tokenMatches 认同一个词根：完全相同，或者较短的一方至少 4 个字母且是另一方的前缀
// （issue/issues、capability/capabilities 前段），太短的前缀误伤太多。
func tokenMatches(q string, tokens []string) bool {
	for _, t := range tokens {
		if q == t {
			return true
		}
		short, long := q, t
		if len(short) > len(long) {
			short, long = long, short
		}
		if len(short) >= 4 && strings.HasPrefix(long, short) {
			return true
		}
	}
	return false
}

// toolSuggestionHint 把候选拼成报错里的一句话，没有候选时返回空串。
func toolSuggestionHint(registry *ToolRegistry, requested string) string {
	names := suggestToolNames(registry, requested)
	if len(names) == 0 {
		return ""
	}
	quoted := make([]string, len(names))
	for i, name := range names {
		quoted[i] = fmt.Sprintf("%q", name)
	}
	return "；相近的可用工具：" + strings.Join(quoted, "、")
}
