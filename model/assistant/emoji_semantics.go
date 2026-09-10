package assistant

import (
	"context"
	"regexp"
	"sort"
	"strings"

	"github.com/SuInk/diana/model/llm"
	emojinames "github.com/SuInk/diana/resources/unicode"
)

const emojiSemanticsGroup = "emoji_semantics"
const emojiSemanticsLimit = 32

var emojiTextURL = regexp.MustCompile("https?://[^\\s<>\"）]+")

func withEmojiSemanticsRun(run llmProviderRunFunc) llmProviderRunFunc {
	return func(provider LLMProvider) (string, error) {
		return run(&emojiSemanticsProvider{provider: provider})
	}
}

type emojiSemanticsProvider struct{ provider LLMProvider }

func (p *emojiSemanticsProvider) Generate(ctx context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	return p.provider.Generate(ctx, requestWithEmojiSemantics(req))
}

func requestWithEmojiSemantics(req llm.GenerateRequest) llm.GenerateRequest {
	// Copy message and part slices: retries and Agent loops reuse the source request.
	messages := make([]llm.Message, 0, len(req.Messages))
	for _, message := range req.Messages {
		if message.ContextGroup == emojiSemanticsGroup {
			continue
		}
		if message.Role == llm.RoleUser || message.Role == llm.RoleTool {
			message.Content = annotateEmojiText(message.Content)
			if message.Parts != nil {
				message.Parts = append([]llm.ContentPart(nil), message.Parts...)
				for i := range message.Parts {
					if message.Parts[i].Type == llm.ContentPartText {
						message.Parts[i].Text = annotateEmojiText(message.Parts[i].Text)
					}
				}
			}
		}
		messages = append(messages, message)
	}
	req.Messages = messages
	return req
}

func annotateEmojiText(text string) string {
	names, err := emojinames.Find(text, emojiSemanticsLimit)
	if err != nil || len(names) == 0 {
		return text
	}
	// Match complete ZWJ / skin-tone sequences before their shorter components.
	sort.SliceStable(names, func(i, j int) bool { return len(names[i].Emoji) > len(names[j].Emoji) })
	replacements := make([]string, 0, len(names)*4)
	for _, name := range names {
		label := name.English
		if name.Chinese != "" {
			label = name.Chinese + " / " + label
		}
		// An inline name may occur inside a JSON tool result or embedded payload.
		label = strings.NewReplacer("\\", "", "\"", "’", "\n", " ").Replace(label)
		annotated := name.Emoji + "（表情名称：" + label + "）"
		replacements = append(replacements, annotated, annotated, name.Emoji, annotated)
	}
	replacer := strings.NewReplacer(replacements...)
	var out strings.Builder
	start := 0
	for _, span := range emojiTextURL.FindAllStringIndex(text, -1) {
		out.WriteString(replacer.Replace(text[start:span[0]]))
		out.WriteString(text[span[0]:span[1]])
		start = span[1]
	}
	out.WriteString(replacer.Replace(text[start:]))
	return out.String()
}
