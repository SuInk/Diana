// Copyright (c) 2025-now SuInk.
// Diana integration for the separately licensed internal/maibotreply port.
package assistant

import (
	"context"
	"log"
	"strings"

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
		OutputInstruction: joinPromptSections(maiBotReferenceBoundary, maiBotReplyerOutput),
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

	request := r.maiBotReplyerMessages(cfg, event, messages, body)
	callCtx := withLLMUsagePurpose(ctx, PurposeReply)
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
	if len(mentionIDs) > 0 {
		markers := make([]string, 0, len(mentionIDs))
		for _, id := range mentionIDs {
			markers = append(markers, mentionMarkerFor(id))
		}
		rendered = strings.Join(markers, " ") + " " + rendered
	}
	if quoted {
		rendered = replyMarkerPrefix + replyID + "]" + rendered
	}
	return restoreReplyControlIntent(rendered, intent)
}
