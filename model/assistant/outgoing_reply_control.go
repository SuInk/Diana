package assistant

import (
	"regexp"
	"strings"
)

// Consume control syntax separately from ID validation. A failed lookup must
// not turn an internal control into user-visible text.
func consumeOutgoingReplyControl(text string) (string, string, bool) {
	rest := strings.TrimLeft(text, " \t\r\n")
	var id string
	count := 0
	for strings.HasPrefix(rest, replyMarkerPrefix) {
		end := strings.IndexByte(rest, ']')
		if end < 0 {
			break
		}
		id = rest[len(replyMarkerPrefix):end]
		rest = strings.TrimLeft(rest[end+1:], " \t\r\n")
		count++
	}
	if count == 0 {
		return "", text, false
	}
	if count != 1 {
		id = "" // Multiple targets are ambiguous; do not choose one implicitly.
	}
	return id, rest, true
}

// 引用标记和提及标记是同一家族，模型写歪它的方式也一样。生产库里 30 天只逮到
// 一条——「[ diana-reply : 1447664451 ]喵？这张图这次没送进我眼里……」，冒号两侧
// 多了空格，consumeOutgoingReplyControl 要求的是严格前缀，于是整条标记当正文发
// 进了群。比提及少见是因为引用多数时候由发送层自己加，轮不到模型写。
//
// 处理方式和提及一致：外壳和分隔符扶正，扶不正的丢掉。扶正在消费之前跑，消费
// 之后再清一次——发送层只认开头那一个，写在正文中间的引用标记没人消费，留着也
// 只会以字面量发出去。
var (
	dianaReplyVariantCore      = `[Dd]iana[-_ ]?reply[ \t]*[:：=][ \t]*(-?[0-9]{1,19})`
	dianaReplyVariantPattern   = regexp.MustCompile(mentionCodeSpanAlternation + `[\[<({【][ \t]*` + dianaReplyVariantCore + `[ \t]*[\]>)}】]`)
	dianaReplyVariantIDPattern = regexp.MustCompile(dianaReplyVariantCore)
	dianaReplyResiduePattern   = regexp.MustCompile(mentionCodeSpanAlternation +
		`[\[<({【][^\[\]<>(){}【】\n]{0,80}?[Dd]iana[-_ ]?reply[^\[\]<>(){}【】\n]{0,80}?[\]>)}】]`)
)

// normalizeDianaReplyVariants 把写歪的引用标记改回正规形态。
func normalizeDianaReplyVariants(text string) string {
	if !strings.Contains(strings.ToLower(text), "diana") {
		return text
	}
	return dianaReplyVariantPattern.ReplaceAllStringFunc(text, func(token string) string {
		if isMentionCodeSpan(token) {
			return token
		}
		match := dianaReplyVariantIDPattern.FindStringSubmatch(token)
		if match == nil {
			return token
		}
		return replyMarkerPrefix + match[1] + "]"
	})
}

// dropResidualDianaReplyMarkers 丢掉消费之后还留在正文里的引用标记。
func dropResidualDianaReplyMarkers(text string) string {
	if !strings.Contains(strings.ToLower(text), "diana") {
		return text
	}
	var builder strings.Builder
	last := 0
	for _, bounds := range dianaReplyResiduePattern.FindAllStringIndex(text, -1) {
		token := text[bounds[0]:bounds[1]]
		if isMentionCodeSpan(token) {
			continue
		}
		builder.WriteString(text[last:bounds[0]])
		last = bounds[1]
		if last < len(text) && text[last] == ' ' {
			last++
		}
	}
	if last == 0 {
		return text
	}
	builder.WriteString(text[last:])
	return strings.TrimSpace(builder.String())
}
