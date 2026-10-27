// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"encoding/json"
	"regexp"
	"strings"

	"github.com/SuInk/diana/model/llm"
)

// 有的供应商和中转网关不会把 function call 放进 tool_calls，而是把它渲染成给人看
// 的一行字塞进正文：「调用工具：agent_finalize，参数：{...}」。这既不是 Agent 的
// JSON 动作协议，也不是自然语言回复：按普通正文发出去就等于把内部协议连同收尾
// 信封一起丢给用户看。这里统一识别这种形状，收尾调用救回正文，其余一律不当正文。

// renderedToolCallHead 匹配「<渲染前缀><工具名>」这一段，只认开头：正文中间出现
// 这几个字是正常表达，只有整条回复以它开头才是被渲染出来的调用。
var renderedToolCallHead = regexp.MustCompile(`(?i)^\s*(?:调用工具|工具调用|调用函数|函数调用|使用工具|calling tool|call tool|tool call|tool_call|function call|function_call)\s*[:：]\s*([A-Za-z_][A-Za-z0-9_.\-]{0,63})`)

// renderedToolCallArgsGap 是工具名和参数 JSON 之间允许出现的东西：分隔符加一个
// 「参数」标签。除此之外还有别的文字，就说明这是在讲话，不是渲染出来的调用。
var renderedToolCallArgsGap = regexp.MustCompile(`(?i)^[\s,，、]*(?:参数|入参|参数为|arguments|argument|args|input|with)?\s*[:：]?\s*$`)

// renderedToolCall 识别被渲染成人话的工具调用。ok 为真时 name 是工具名，
// arguments 是解析出来的参数（网关截断或参数畸形时可能为 nil）。
func renderedToolCall(text string) (name string, arguments map[string]any, ok bool) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return "", nil, false
	}
	head := renderedToolCallHead.FindStringSubmatchIndex(trimmed)
	if head == nil {
		return "", nil, false
	}
	name = trimmed[head[2]:head[3]]
	rest := trimmed[head[1]:]
	start := strings.Index(rest, "{")
	if start < 0 {
		// 只渲染了工具名，没带参数：仍然是一次被渲染掉的调用。
		if strings.TrimSpace(rest) != "" && !renderedToolCallArgsGap.MatchString(rest) {
			return "", nil, false
		}
		return name, nil, true
	}
	if !renderedToolCallArgsGap.MatchString(rest[:start]) {
		return "", nil, false
	}
	end := strings.LastIndex(rest, "}")
	if end > start {
		var parsed map[string]any
		if err := json.Unmarshal([]byte(rest[start:end+1]), &parsed); err == nil {
			arguments = parsed
		}
	}
	return name, arguments, true
}

// LooksLikeRenderedToolCall 报告这段文本是不是一次被渲染成人话的工具调用。
// 出站侧拿它做最后一道拦截：这种文本永远不该出现在发给用户的回复里。
func LooksLikeRenderedToolCall(text string) bool {
	_, _, ok := renderedToolCall(text)
	return ok
}

// renderedToolCallAction 把渲染出来的调用转成内部动作。收尾调用带正文时救回正文，
// 其余情况只回报「这是一次动作」——正文绝不外泄，交给调用方走协议修复。
func renderedToolCallAction(text string) (llmAction, bool) {
	name, arguments, ok := renderedToolCall(text)
	if !ok {
		return llmAction{}, false
	}
	if name != finalizeToolName && name != legacyFinalizeToolName {
		return llmAction{}, true
	}
	return finalizeAction([]llm.ToolCall{{Name: finalizeToolName, Arguments: arguments}}, ""), true
}

// 还有一种形状是模型把收尾调用写成标签拼在正文后面。线上 10-22 swe-2 就这样把
// 「正文[agent_finalize content="…" render="" silent="false" …/>]」整串发进了群；
// 也见过只剩一对空标签的「正文[agent_finalize][/agent_finalize]」。方括号、尖括号、
// 自闭合和闭合标签都认，属性值按引号整体吃掉，免得值里的 ] 或 > 把标签截断。
// 改名前的 agent_finalize 也认：模型照老历史还会写出旧标签。
var inlineFinalizeTag = regexp.MustCompile(`(?i)[\[<]\s*/?\s*(?:` + finalizeToolName + `|` + legacyFinalizeToolName + `)\b((?:\s+[A-Za-z_][A-Za-z0-9_]*\s*=\s*(?:"[^"]*"|'[^']*'|[^\s"'\]>/]*))*)\s*/?\s*(?:>\s*\]?|\])`)

var inlineFinalizeAttr = regexp.MustCompile(`([A-Za-z_][A-Za-z0-9_]*)\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s"'\]>/]*))`)

// StripInlineFinalizeTags 去掉正文里的收尾标签，返回剩下的正文和第一个标签的属性。
// ok 为假时原文没有这种标签。
func StripInlineFinalizeTags(text string) (body string, attrs map[string]string, ok bool) {
	matches := inlineFinalizeTag.FindAllStringSubmatchIndex(text, -1)
	if len(matches) == 0 {
		return text, nil, false
	}
	var out strings.Builder
	last := 0
	for _, match := range matches {
		out.WriteString(text[last:match[0]])
		last = match[1]
		if attrs != nil || match[2] < 0 || match[2] == match[3] {
			continue
		}
		attrs = map[string]string{}
		for _, attr := range inlineFinalizeAttr.FindAllStringSubmatch(text[match[2]:match[3]], -1) {
			attrs[strings.ToLower(attr[1])] = attr[2] + attr[3] + attr[4]
		}
	}
	out.WriteString(text[last:])
	return strings.TrimSpace(out.String()), attrs, true
}

// inlineFinalizeAction 把拼着收尾标签的正文转成收尾动作。标签外有正文就用正文：
// 线上那条标签里的 content 只是正文开头的半句，外面那段才是模型真正写完的回复。
// 只有标签外什么都没有时才看标签里的 content 和 silent。
//
// 标签里包的也可能是整个信封：线上 10-27 swe-2 写出
// 「[agent_finalize]{"content":"…[diana-msg]…","silent":false,"sticker":"",…}[/agent_finalize]」，
// 剥完标签剩下的正好是 JSON，原来被当成正文，再按 [diana-msg] 拆成两条半截 JSON 发进了群。
// 这时按信封解码，判据和 finalizeEnvelopeFromText 一致，所以要带上调用方声明的字段。
func inlineFinalizeAction(text string, declared ...FinalizeField) (llmAction, bool) {
	body, attrs, ok := StripInlineFinalizeTags(text)
	if !ok {
		return llmAction{}, false
	}
	if envelope, decoded := finalizeEnvelopeFromText(body, declared...); decoded {
		return envelope, true
	}
	arguments := map[string]any{}
	for key, value := range attrs {
		arguments[key] = value
	}
	arguments["silent"] = body == "" && strings.EqualFold(strings.TrimSpace(attrs["silent"]), "true")
	if body != "" {
		arguments["content"] = body
	}
	return finalizeAction([]llm.ToolCall{{Name: finalizeToolName, Arguments: arguments}}, ""), true
}
