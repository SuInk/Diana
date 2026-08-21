// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/SuInk/diana/model/applog"
)

const recentTextReferenceWindow = 10 * time.Minute

var (
	recentTextVersionPattern = regexp.MustCompile(`(?i)v?\d+(?:\.\d+)+`)
	recentTextTokenPattern   = regexp.MustCompile(`[\p{L}\p{N}]+(?:[._-][\p{L}\p{N}]+)*`)
)

type recentTextReference struct {
	Shorthand       string   `json:"shorthand"`
	Canonical       string   `json:"canonical,omitempty"`
	SourceMessageID string   `json:"source_message_id,omitempty"`
	Method          string   `json:"method"`
	Confidence      float64  `json:"confidence"`
	Candidates      []string `json:"candidates,omitempty"`
}

type recentTextReferenceCandidate struct {
	Canonical       string
	Normalized      string
	SourceMessageID string
	Method          string
	Score           int
}

func (r *Runtime) enrichRecentTextReference(ctx context.Context, event MessageEvent, text string, history []MessageEvent) MessageEvent {
	reference := resolveRecentTextReference(event, text, history, r.effectiveConfigForEvent(event).BotAccount)
	if reference == nil {
		return event
	}
	event.recentTextReference = reference
	r.recordRecentTextReference(ctx, event, reference)
	return event
}

func resolveRecentTextReference(event MessageEvent, text string, history []MessageEvent, botAccount string) *recentTextReference {
	keys := recentTextReferenceKeys(text)
	if len(keys) == 0 {
		return nil
	}
	for _, key := range keys {
		if event.Quoted != nil {
			quoted := recentTextCandidatesFromSource(quotedPlainText(event.Quoted), key, recentTextReferenceCandidate{
				SourceMessageID: strings.TrimSpace(event.Quoted.MessageID),
				Method:          "explicit_quote",
				Score:           1000,
			})
			if reference := chooseRecentTextReference(key, quoted); reference != nil {
				return reference
			}
		}

		candidates := recentTextReferenceHistoryCandidates(event, history, botAccount, key)
		if reference := chooseRecentTextReference(key, candidates); reference != nil {
			return reference
		}
	}
	return nil
}

func recentTextReferenceKeys(text string) []string {
	matches := recentTextVersionPattern.FindAllString(strings.TrimSpace(text), -1)
	keys := make([]string, 0, len(matches))
	seen := map[string]bool{}
	for _, match := range matches {
		key := normalizeRecentTextVersion(match)
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		keys = append(keys, key)
	}
	return keys
}

func normalizeRecentTextVersion(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.ReplaceAll(value, " ", "")
	value = strings.TrimPrefix(value, "v")
	return value
}

func recentTextReferenceHistoryCandidates(event MessageEvent, history []MessageEvent, botAccount, key string) []recentTextReferenceCandidate {
	sameSenderIDs := map[string]bool{}
	for _, item := range history {
		if sameConversationSender(item, event) {
			if id := strings.TrimSpace(item.MessageID); id != "" {
				sameSenderIDs[id] = true
			}
		}
	}
	candidates := make([]recentTextReferenceCandidate, 0)
	for index := len(history) - 1; index >= 0; index-- {
		item := history[index]
		if item.MessageID == event.MessageID || !recentTextReferenceWithinWindow(item.Time, event.Time) {
			continue
		}
		recency := index
		if assistantHistoryEvent(item, botAccount) && recentAssistantReplyTargetsSender(item, event.UserID, sameSenderIDs) {
			candidates = append(candidates, recentTextCandidatesFromSource(historyPlainText(item), key, recentTextReferenceCandidate{
				SourceMessageID: strings.TrimSpace(item.MessageID), Method: "assistant_reply", Score: 800 + recency,
			})...)
			continue
		}
		if sameConversationSender(item, event) {
			if reply := strings.TrimSpace(item.botReply); reply != "" && !semanticErrorWrapperText(reply) {
				candidates = append(candidates, recentTextCandidatesFromSource(reply, key, recentTextReferenceCandidate{
					SourceMessageID: strings.TrimSpace(item.MessageID), Method: "assistant_reply", Score: 800 + recency,
				})...)
			}
			candidates = append(candidates, recentTextCandidatesFromSource(historyPlainText(item), key, recentTextReferenceCandidate{
				SourceMessageID: strings.TrimSpace(item.MessageID), Method: "same_sender", Score: 600 + recency,
			})...)
			continue
		}
	}
	return candidates
}

func sameConversationSender(candidate, current MessageEvent) bool {
	return strings.TrimSpace(candidate.UserID) != "" && strings.TrimSpace(candidate.UserID) == strings.TrimSpace(current.UserID)
}

func recentTextReferenceWithinWindow(candidateTime, currentTime int64) bool {
	if candidateTime <= 0 || currentTime <= 0 {
		return true
	}
	age := currentTime - candidateTime
	return age >= 0 && age <= int64(recentTextReferenceWindow/time.Second)
}

func recentAssistantReplyTargetsSender(item MessageEvent, userID string, sameSenderIDs map[string]bool) bool {
	userID = strings.TrimSpace(userID)
	if item.Kind == EventKindPrivate && strings.TrimSpace(item.UserID) == userID {
		return true
	}
	if item.Quoted != nil && strings.TrimSpace(item.Quoted.UserID) == userID {
		return true
	}
	for _, messageID := range replyReferenceIDs(item.Segments) {
		if sameSenderIDs[strings.TrimSpace(messageID)] {
			return true
		}
	}
	return false
}

func recentTextCandidatesFromSource(text, key string, base recentTextReferenceCandidate) []recentTextReferenceCandidate {
	indexes := recentTextTokenPattern.FindAllStringIndex(text, -1)
	if len(indexes) == 0 {
		return nil
	}
	tokens := make([]string, len(indexes))
	for index, bounds := range indexes {
		tokens[index] = text[bounds[0]:bounds[1]]
	}
	result := make([]recentTextReferenceCandidate, 0)
	for index, token := range tokens {
		versionBounds := recentTextVersionPattern.FindAllStringIndex(token, -1)
		for _, bounds := range versionBounds {
			if normalizeRecentTextVersion(token[bounds[0]:bounds[1]]) != key {
				continue
			}
			start := index
			attachedPrefix := strings.Trim(token[:bounds[0]], "._-")
			if strings.EqualFold(attachedPrefix, "v") {
				attachedPrefix = ""
			}
			if attachedPrefix == "" && index > 0 && recentTextEntityPrefix(tokens[index-1]) {
				start = index - 1
				if utf8.RuneCountInString(tokens[index-1]) == 1 && index > 1 && recentTextEntityPrefix(tokens[index-2]) {
					start = index - 2
				}
			}
			canonical := strings.Join(tokens[start:index+1], " ")
			if !recentTextCanonicalHasQualifier(canonical, key) {
				continue
			}
			candidate := base
			candidate.Canonical = canonical
			candidate.Normalized = normalizeRecentTextCanonical(canonical)
			result = append(result, candidate)
		}
	}
	return result
}

func recentTextEntityPrefix(token string) bool {
	for _, char := range token {
		if unicode.IsLetter(char) {
			return true
		}
	}
	return false
}

func recentTextCanonicalHasQualifier(canonical, key string) bool {
	normalized := normalizeRecentTextCanonical(canonical)
	remainder := strings.Replace(normalized, strings.ReplaceAll(key, ".", ""), "", 1)
	remainder = strings.TrimPrefix(remainder, "v")
	for _, char := range remainder {
		if unicode.IsLetter(char) {
			return true
		}
	}
	return false
}

func normalizeRecentTextCanonical(value string) string {
	var builder strings.Builder
	for _, char := range strings.ToLower(value) {
		if unicode.IsLetter(char) || unicode.IsDigit(char) {
			builder.WriteRune(char)
		}
	}
	return builder.String()
}

func chooseRecentTextReference(key string, candidates []recentTextReferenceCandidate) *recentTextReference {
	unique := map[string]recentTextReferenceCandidate{}
	for _, candidate := range candidates {
		if candidate.Normalized == "" {
			continue
		}
		if previous, ok := unique[candidate.Normalized]; !ok || candidate.Score > previous.Score {
			unique[candidate.Normalized] = candidate
		}
	}
	ordered := make([]recentTextReferenceCandidate, 0, len(unique))
	for _, candidate := range unique {
		ordered = append(ordered, candidate)
	}
	sort.SliceStable(ordered, func(left, right int) bool { return ordered[left].Score > ordered[right].Score })
	if len(ordered) == 1 {
		candidate := ordered[0]
		confidence := 0.92
		if candidate.Method == "explicit_quote" {
			confidence = 0.99
		}
		return &recentTextReference{
			Shorthand: key, Canonical: candidate.Canonical, SourceMessageID: candidate.SourceMessageID,
			Method: candidate.Method, Confidence: confidence,
		}
	}
	if len(ordered) > 1 {
		names := make([]string, 0, len(ordered))
		for _, candidate := range ordered {
			names = append(names, candidate.Canonical)
		}
		return &recentTextReference{Shorthand: key, Method: "ambiguous", Confidence: 1, Candidates: names}
	}
	return nil
}

func recentTextReferencePrompt(reference *recentTextReference) string {
	if reference == nil {
		return ""
	}
	payload, err := json.Marshal(reference)
	if err != nil {
		return ""
	}
	if reference.Method == "ambiguous" {
		return "【运行时文本指代判定】" + string(payload) + "\n该短指代对应多个仍活跃的候选，不能猜测；请简洁地列出候选并要求用户确认。"
	}
	return "【运行时已解析的文本指代】" + string(payload) + "\ncanonical 是当前消息中 shorthand 的唯一高置信度指代。直接按 canonical 理解并回答，不要再次询问它指什么。"
}

func (r *Runtime) recordRecentTextReference(ctx context.Context, event MessageEvent, reference *recentTextReference) {
	writer := r.appLogWriter()
	if writer == nil || reference == nil {
		return
	}
	hash := func(value string) string {
		sum := sha256.Sum256([]byte(value))
		return fmt.Sprintf("%x", sum[:8])
	}
	metadata := map[string]any{
		"method": reference.Method, "confidence": reference.Confidence,
		"candidate_count": len(reference.Candidates), "shorthand_hash": hash(reference.Shorthand),
	}
	if reference.Canonical != "" {
		metadata["canonical_hash"] = hash(reference.Canonical)
	}
	if reference.SourceMessageID != "" {
		metadata["source_message_hash"] = hash(reference.SourceMessageID)
	}
	logCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	_ = writer.AppendLog(logCtx, applog.Entry{
		Kind: applog.KindOperation, Level: applog.LevelInfo, Action: "chatbot.text_reference.resolved",
		Message: "当前消息文本指代已完成确定性解析", Metadata: metadata,
	})
}
