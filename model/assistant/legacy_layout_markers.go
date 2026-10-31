package assistant

import (
	"context"
	"regexp"
	"strings"

	"github.com/SuInk/diana/model/llm"
)

// Diana 的标记统一写成 <diana-…/> 标签，照 QQ 官方机器人 <qqbot-at-user id="…" />
// 的路子：diana 打头，后面接用途，单标签自闭合。以前是方括号写法
// （[diana-at:ID]、[diana-msg]、[[DIANA_REPLY_SINGLE]]），旧历史、用户改过的
// 提示词和模型的老习惯里还会冒出来；写歪的标签（单引号、漏了斜杠、漏了 diana-
// 前缀写成 Koishi 原生的 <at id/>）也常见。模型的回复一进来就在这里统一扶正，
// 后面的解析只认标准写法。
//
// 方括号里写歪的 diana-at / diana-reply（多空格、换分隔符）另有
// normalizeDianaMentionVariants / normalizeDianaReplyVariants 在出站前兜底。

// Preserve code examples while consuming legacy layout controls in ordinary prose.
var legacyLayoutTokens = regexp.MustCompile("(?s)```.*?```|~~~.*?~~~|`[^`\\n]*`|\\[[ \\t]*diana[ \\t]*-[ \\t]*(?:line|msg|br)[ \\t]*\\]")

func normalizeLegacyLayoutMarkers(reply string) string {
	return legacyLayoutTokens.ReplaceAllStringFunc(reply, func(token string) string {
		if !strings.HasPrefix(token, "[") {
			return token
		}
		if strings.Contains(token, "line") {
			return notificationLineMarker
		}
		return notificationSplitMarker
	})
}

var legacyDianaDirectives = strings.NewReplacer(
	"[[DIANA_REPLY_SINGLE]]", replySingleMarker,
	"[[DIANA_REPLY_AUTO]]", replyAutoMarker,
	"[[DIANA_LINES_PRESERVE]]", replyLinesPreserveMarker,
	"[[DIANA_LINES_COMPACT]]", replyLinesCompactMarker,
	"[[DIANA_IGNORE_CURRENT_USER_30M]]", replySuppressionMarker,
	"[[DIANA_REFUSE_CURRENT]]", replyRefusalMarker,
)

var (
	legacyDianaIDMarker = regexp.MustCompile(mentionCodeSpanAlternation + `\[diana-(at|reply):([^\]\s"<>]{1,200})\]`)
	// 标签的宽松形态：引号可有可无（JSON 里还可能是转义过的 \"），斜杠和空格随意。
	looseDianaIDTag = regexp.MustCompile(mentionCodeSpanAlternation +
		`<[ \t]*(?:diana-)?(at|quote|reply)[ \t]+id[ \t]*=[ \t]*(?:\\?["'])?([^"'\\\s/<>]{1,200})(?:\\?["'])?[ \t]*/?[ \t]*>`)
	looseDianaFixedTag = regexp.MustCompile(mentionCodeSpanAlternation +
		`<[ \t]*(/?)[ \t]*(diana-(?:message|br|single|auto|lines-preserve|lines-compact|ignore-user|refuse))[ \t]*/?[ \t]*>`)
)

// normalizeDianaMarkup 把模型写的旧写法、写歪的标签扶正成标准标签。
// 代码块里的不动：那是在给人看写法。
func normalizeDianaMarkup(text string) string {
	if !strings.ContainsAny(text, "[<") {
		return text
	}
	if strings.Contains(text, "[[DIANA_") {
		text = legacyDianaDirectives.Replace(text)
	}
	text = normalizeLegacyLayoutMarkers(text)
	text = legacyDianaIDMarker.ReplaceAllStringFunc(text, func(token string) string {
		if isMentionCodeSpan(token) {
			return token
		}
		match := legacyDianaIDMarker.FindStringSubmatch(token)
		if match[1] == "at" {
			return mentionMarkerFor(match[2])
		}
		return replyMarkerFor(match[2])
	})
	text = looseDianaIDTag.ReplaceAllStringFunc(text, func(token string) string {
		if isMentionCodeSpan(token) {
			return token
		}
		match := looseDianaIDTag.FindStringSubmatch(token)
		if match[1] == "at" {
			return mentionMarkerFor(match[2])
		}
		return replyMarkerFor(match[2])
	})
	return looseDianaFixedTag.ReplaceAllStringFunc(text, func(token string) string {
		if isMentionCodeSpan(token) {
			return token
		}
		match := looseDianaFixedTag.FindStringSubmatch(token)
		// 模型把分条写成容器 <diana-message>…</diana-message> 时，闭合标签不是
		// 新的分条点，丢掉即可。
		if match[1] == "/" {
			return ""
		}
		return "<" + match[2] + "/>"
	})
}

// pendingDianaMarkupTail 判断流式文本末尾是不是还没写完的标签，是的话预览
// 先停一下，免得把半截 <diana-at 发出去。
func pendingDianaMarkupTail(text string) bool {
	start := strings.LastIndex(text, "<")
	if start < 0 || strings.Contains(text[start:], ">") {
		return false
	}
	tail := strings.TrimLeft(text[start+1:], " \t/")
	for _, prefix := range []string{"diana-", "at ", "quote "} {
		if strings.HasPrefix(prefix, tail) || strings.HasPrefix(tail, prefix) {
			return true
		}
	}
	return false
}

// dianaMarkupProvider 在模型回复一进来时扶正标记，正文和工具参数都过一遍。
//
// 发出去的请求也要处理一件事：工具返回值和提示词里的 JSON 多半是 json.Marshal
// 编的，它默认把 < > & 转成 \u003c \u003e \u0026，标签就变成了
// \u003cdiana-at id=\"1\"/\u003e 交给模型，模型照着抄就成了乱码。这里统一还原，
// 结果仍是合法 JSON。
type dianaMarkupProvider struct {
	provider LLMProvider
}

func withDianaMarkupRun(run llmProviderRunFunc) llmProviderRunFunc {
	if run == nil {
		return run
	}
	return func(provider LLMProvider) (string, error) {
		return run(&dianaMarkupProvider{provider: provider})
	}
}

func (p *dianaMarkupProvider) Generate(ctx context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	response, err := p.provider.Generate(ctx, unescapeRequestHTMLEscapes(req))
	if err != nil || response == nil {
		return response, err
	}
	normalized := *response
	normalized.Text = normalizeDianaMarkup(response.Text)
	if len(response.ToolCalls) > 0 {
		normalized.ToolCalls = make([]llm.ToolCall, len(response.ToolCalls))
		for index, call := range response.ToolCalls {
			if len(call.Arguments) > 0 {
				call.Arguments, _ = normalizeDianaMarkupValue(call.Arguments).(map[string]any)
			}
			normalized.ToolCalls[index] = call
		}
	}
	return &normalized, nil
}

func normalizeDianaMarkupValue(value any) any {
	switch typed := value.(type) {
	case string:
		return normalizeDianaMarkup(typed)
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			out[key] = normalizeDianaMarkupValue(item)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for index, item := range typed {
			out[index] = normalizeDianaMarkupValue(item)
		}
		return out
	}
	return value
}

func unescapeRequestHTMLEscapes(req llm.GenerateRequest) llm.GenerateRequest {
	var messages []llm.Message
	for index, message := range req.Messages {
		content := unescapeJSONHTMLEscapes(message.Content)
		var parts []llm.ContentPart
		for partIndex, part := range message.Parts {
			if text := unescapeJSONHTMLEscapes(part.Text); text != part.Text {
				if parts == nil {
					parts = append([]llm.ContentPart(nil), message.Parts...)
				}
				parts[partIndex].Text = text
			}
		}
		if content == message.Content && parts == nil {
			continue
		}
		if messages == nil {
			messages = append([]llm.Message(nil), req.Messages...)
		}
		messages[index].Content = content
		if parts != nil {
			messages[index].Parts = parts
		}
	}
	if messages != nil {
		req.Messages = messages
	}
	return req
}

// unescapeJSONHTMLEscapes 把 \u003c \u003e \u0026 换回字面字符。成对的反斜杠
// 原样跳过：\\u003c 是转义过的反斜杠后面跟着普通文字，不是要还原的字符。
func unescapeJSONHTMLEscapes(text string) string {
	if !strings.Contains(text, `\u00`) {
		return text
	}
	var builder strings.Builder
	builder.Grow(len(text))
	for index := 0; index < len(text); index++ {
		if text[index] != '\\' || index+1 >= len(text) {
			builder.WriteByte(text[index])
			continue
		}
		if text[index+1] == 'u' && index+6 <= len(text) {
			switch strings.ToLower(text[index+2 : index+6]) {
			case "003c":
				builder.WriteByte('<')
				index += 5
				continue
			case "003e":
				builder.WriteByte('>')
				index += 5
				continue
			case "0026":
				builder.WriteByte('&')
				index += 5
				continue
			}
		}
		builder.WriteByte(text[index])
		builder.WriteByte(text[index+1])
		index++
	}
	return builder.String()
}
