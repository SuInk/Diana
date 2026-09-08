package assistant

import (
	"context"
	"strings"

	"github.com/SuInk/diana/model/llm"
	emojinames "github.com/SuInk/diana/resources/unicode"
)

const emojiSemanticsGroup = "emoji_semantics"
const emojiSemanticsLimit = 32

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
	seen := map[string]bool{}
	var names []emojinames.Name
	collect := func(text string) {
		found, err := emojinames.Find(text, emojiSemanticsLimit)
		if err != nil {
			return
		}
		for _, name := range found {
			if !seen[name.Emoji] && len(names) < emojiSemanticsLimit {
				seen[name.Emoji] = true
				names = append(names, name)
			}
		}
	}
	// Prefer recent input. Do not treat model output or persona examples as user emoji.
	for i := len(req.Messages) - 1; i >= 0 && len(names) < emojiSemanticsLimit; i-- {
		message := req.Messages[i]
		if message.Role != llm.RoleUser && message.Role != llm.RoleTool {
			continue
		}
		collect(message.Content)
		for _, part := range message.Parts {
			if part.Type == llm.ContentPartText {
				collect(part.Text)
			}
		}
	}
	if len(names) == 0 {
		stale := false
		for _, message := range req.Messages {
			stale = stale || message.ContextGroup == emojiSemanticsGroup
		}
		if !stale {
			return req
		}
	}
	// Copy before appending: Agent loops may reuse the original request backing array.
	messages := make([]llm.Message, 0, len(req.Messages)+1)
	for _, message := range req.Messages {
		if message.ContextGroup != emojiSemanticsGroup {
			messages = append(messages, message)
		}
	}
	if len(names) > 0 {
		var note strings.Builder
		note.WriteString("【本轮表情释义】以下是输入中实际出现的 Unicode emoji 标准名称，仅供理解，不是用户原话，也不代表用户的情绪或行为。不要照抄释义；结合语境理解，不确定就不要编造具体含义，不必逐个回应表情。\n")
		for _, name := range names {
			note.WriteString(name.Emoji + "：")
			if name.Chinese != "" {
				note.WriteString(name.Chinese + " / ")
			}
			note.WriteString(name.English + "\n")
		}
		messages = append(messages, llm.Message{Role: llm.RoleSystem, Content: note.String(), ContextGroup: emojiSemanticsGroup, AtomicText: true})
	}
	req.Messages = messages
	return req
}
