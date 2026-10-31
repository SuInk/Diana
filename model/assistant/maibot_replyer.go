// Copyright (c) 2025-now SuInk.
// Diana integration for the separately licensed internal/maibotreply port.
package assistant

import (
	"context"
	"log"
	"math"
	"strings"
	"unicode"

	"github.com/SuInk/diana/internal/maibotreply"
	"github.com/SuInk/diana/model/llm"
)

func maiBotAttention(event MessageEvent) string {
	attention := maibotreply.Attention(event.Kind == EventKindGroup, "")
	if NormalizePlatformID(event.Platform) != PlatformOneBotV11 {
		attention = strings.ReplaceAll(attention, "qq群", "群")
	}
	return attention
}

func (r *Runtime) maiBotTarget(cfg BotConfig, event MessageEvent) *maibotreply.Target {
	target := &maibotreply.Target{
		ID:      event.MessageID,
		Sender:  firstNonEmpty(event.SenderName, event.UserID, "当前成员"),
		Content: r.cleanInput(event, event.RawMessage),
		BotName: firstNonEmpty(cfg.Name, "Diana"),
		Self:    event.UserID != "" && event.UserID == firstNonEmpty(cfg.BotAccount, event.SelfID),
	}
	if event.Quoted != nil && strings.TrimSpace(event.Quoted.MessageID) != "" {
		target.QuoteIDs = []string{event.Quoted.MessageID}
	}
	return target
}

func (r *Runtime) maiBotReplyerMessages(cfg BotConfig, event MessageEvent, source []llm.Message, reference string) []llm.Message {
	var history []llm.Message
	var plain []maibotreply.Message
	for _, message := range source {
		if message.Role != llm.RoleUser && message.Role != llm.RoleAssistant {
			continue
		}
		if message.AtomicText || len(message.ToolCalls) != 0 || message.ToolCallID != "" || message.ToolName != "" {
			continue
		}
		switch message.Priority {
		case llm.MessagePriorityHistory, llm.MessagePriorityRecentHistory, llm.MessagePriorityCurrent:
		default:
			continue
		}
		chat := llm.Message{
			Role:         message.Role,
			Content:      message.Content,
			Parts:        append([]llm.ContentPart(nil), message.Parts...),
			Priority:     message.Priority,
			ContextGroup: message.ContextGroup,
		}
		if chat.Role == llm.RoleAssistant {
			chat.Content = maibotreply.GuidedReply(chat.Content)
		}
		history = append(history, chat)
		plain = append(plain, maibotreply.Message{Role: string(chat.Role), Content: chat.Content})
	}
	if len(history) == 0 {
		chat := llm.Message{Role: llm.RoleUser, Content: event.RawMessage, Priority: llm.MessagePriorityCurrent}
		history = append(history, chat)
		plain = append(plain, maibotreply.Message{Role: "user", Content: chat.Content})
	}

	identity := joinPromptSections(cfg.Soul.Render(), cfg.personaPrompt())
	composed := maibotreply.Compose(maibotreply.Input{
		Identity:          maibotreply.PersonalityPrompt(firstNonEmpty(cfg.Name, "Diana"), cfg.GroupTriggers, identity),
		Style:             maiBotStyle(cfg),
		Attention:         maiBotAttention(event),
		OutputInstruction: joinPromptSections(maiBotReferenceBoundary, maiBotReferenceLayout, maiBotReplyerOutput),
		History:           plain,
		Target:            r.maiBotTarget(cfg, event),
		Time:              r.clock(),
		Reference:         reference,
		TemporaryStyle:    r.groupStylePrompt(event, cfg),
	})
	// Keep the unchanged default in its original prompt positions so merely
	// exposing it in WebUI does not alter reply behavior. Once the owner edits
	// the complete tone, that custom text replaces all four built-in lines.
	if strings.TrimSpace(cfg.MaiBotReplyStyle) == "" {
		composed[0].Content = joinPromptSections(composed[0].Content, maiBotCleanVocabulary, maiBotNoParentheticalLaugh, maiBotTrailingPeriodPreference)
	}
	out := make([]llm.Message, 0, len(composed))
	for index, message := range composed {
		if index > 0 && index <= len(history) {
			out = append(out, history[index-1])
			continue
		}
		priority := llm.MessagePriorityCurrent
		if message.Role == "system" {
			priority = llm.MessagePrioritySystem
		}
		out = append(out, llm.Message{Role: llm.Role(message.Role), Content: message.Content, Priority: priority})
	}
	return out
}

// composeMaiBotReply gives MaiBot's replyer a content-complete Diana draft as
// reply_reference. Diana still owns tools, facts and participation; this call
// owns only the final conversational wording and has no tools.
func (r *Runtime) composeMaiBotReply(ctx context.Context, cfg BotConfig, event MessageEvent, messages []llm.Message, draft string) string {
	body, intent := consumeReplyControlIntent(draft)
	replyID, body, quoted := extractOutgoingReplyMarker(body)
	mentionIDs := mentionedIDsInText(body)
	body = strings.TrimSpace(dianaMentionMarkerPattern.ReplaceAllString(body, ""))
	if body == "" {
		return draft
	}

	// 改写提示词不认识 Diana 的分条和换行标记，原样塞进去它只会当成一串字面量，
	// 草稿分好的几条消息和清单行就被揉成一段。换成真实的空行和换行再给它看。
	draftParts := strings.Split(body, notificationSplitMarker)
	reference := strings.ReplaceAll(body, notificationSplitMarker, "\n\n")
	reference = restoreExplicitReplyLines(reference)
	request := r.maiBotReplyerMessages(cfg, event, messages, reference)
	callCtx := withLLMUsagePurpose(ctx, PurposeReplyTone)
	rendered, err := r.runLLMProviderForGroup(callCtx, llm.GroupChat, func(client LLMProvider) (string, error) {
		response, err := client.Generate(callCtx, llm.GenerateRequest{Messages: request})
		if err != nil {
			return "", err
		}
		return response.Text, nil
	})
	if err != nil || strings.TrimSpace(rendered) == "" {
		if err != nil {
			log.Printf("diana maibot replyer failed, keeping content draft: %v", err)
		}
		return draft
	}

	// Delivery decisions belong to Diana's content pass. The replyer may change
	// wording only, so discard any control markers it invents before restoring
	// the original draft's reply target, mentions and delivery mode.
	rendered, _ = consumeReplyControlIntent(rendered)
	_, rendered, _ = extractOutgoingReplyMarker(rendered)
	rendered = strings.TrimSpace(dianaMentionMarkerPattern.ReplaceAllString(rendered, ""))
	if rendered == "" {
		return draft
	}
	rendered = maiBotReplyLines(rendered, cfg, event, intent, draftParts)
	if len(mentionIDs) > 0 {
		markers := make([]string, 0, len(mentionIDs))
		for _, id := range mentionIDs {
			markers = append(markers, mentionMarkerFor(id))
		}
		rendered = strings.Join(markers, " ") + " " + rendered
	}
	if quoted {
		rendered = replyMarkerFor(replyID) + rendered
	}
	return restoreReplyControlIntent(rendered, intent)
}

// maiBotReplyLines 把改写稿里的真实换行转成 <diana-br/>。改写提示词沿用 MaiBot
// 原文，不认识 Diana 的换行标记，分点总结会直接写真实换行；后面的版式整理把真实
// 换行当随手折行压成空格，列表就挤成一行。保留换行时（本轮要求优先，其次是这条
// 消息的设置，最后是机器人配置，默认保留）才转换；收拢模式照旧交给后面压平。
// 代码块里的行原样保留：缩进和空行是代码的一部分，去掉了复制出去就跑不了。
//
// 分几条是内容那一轮定的，改写只换措辞。草稿分成了几条（draftParts 多于一条）时，
// 改写稿按空行切成段落，每段对回草稿里最像的那一条，对到的条目变了才另起一条。
// 不能直接把空行当分界：改写模型常在清单每一项之间都空一行，那样一份三行的清单
// 会被拆成三个气泡。草稿只有一条时空行照旧丢掉，不凭空多拆。
func maiBotReplyLines(rendered string, cfg BotConfig, event MessageEvent, intent replyControlIntent, draftParts []string) string {
	mode := intent.LineBreakMode
	if mode == "" {
		mode = event.replyLineBreakMode
	}
	if mode == "" && !boolValue(cfg.ReplyPreserveLineBreaks, true) {
		mode = replyLinesCompact
	}
	if mode == replyLinesCompact {
		return rendered
	}
	rendered = strings.ReplaceAll(strings.ReplaceAll(rendered, "\r\n", "\n"), "\r", "\n")
	var paragraphs [][]string
	inFence, pendingBreak := false, true
	add := func(line string) {
		if pendingBreak {
			paragraphs = append(paragraphs, nil)
			pendingBreak = false
		}
		paragraphs[len(paragraphs)-1] = append(paragraphs[len(paragraphs)-1], line)
	}
	for _, line := range strings.Split(rendered, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			inFence = !inFence
			add(trimmed)
			continue
		}
		if inFence {
			add(strings.TrimRight(line, " \t"))
			continue
		}
		if trimmed == "" {
			pendingBreak = true
			continue
		}
		add(trimmed)
	}
	parts := alignReplyParagraphs(paragraphs, draftParts)
	var out strings.Builder
	for index, paragraph := range paragraphs {
		if index > 0 {
			if parts[index] != parts[index-1] {
				out.WriteString(notificationSplitMarker)
			} else {
				out.WriteString(notificationLineMarker)
			}
		}
		out.WriteString(strings.Join(paragraph, notificationLineMarker))
	}
	return out.String()
}

// alignReplyParagraphs 给每个段落找它对应草稿里的第几条：按字的二元组算余弦相似度
// 取最像的那条（除以两边规模，长条目不会单凭字多压过短条目），只许往后对不许往回，
// 和哪条都不沾边的段落跟着上一段。
func alignReplyParagraphs(paragraphs [][]string, draftParts []string) []int {
	parts := make([]int, len(paragraphs))
	if len(draftParts) < 2 {
		return parts
	}
	drafts := make([]map[string]bool, len(draftParts))
	for index, part := range draftParts {
		drafts[index] = textBigrams(part)
	}
	current := 0
	for index, paragraph := range paragraphs {
		grams := textBigrams(strings.Join(paragraph, "\n"))
		best, bestScore := current, 0.0
		for candidate := current; candidate < len(drafts); candidate++ {
			shared := 0
			for gram := range grams {
				if drafts[candidate][gram] {
					shared++
				}
			}
			if shared == 0 {
				continue
			}
			score := float64(shared) / math.Sqrt(float64(len(grams)*len(drafts[candidate])))
			if score > bestScore {
				best, bestScore = candidate, score
			}
		}
		current = best
		parts[index] = current
	}
	return parts
}

func textBigrams(text string) map[string]bool {
	var runes []rune
	for _, r := range strings.ToLower(text) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			runes = append(runes, r)
		}
	}
	grams := make(map[string]bool, len(runes))
	for index := 1; index < len(runes); index++ {
		grams[string(runes[index-1:index+1])] = true
	}
	return grams
}
