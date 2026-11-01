// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"log"
	"strings"
	"time"
	"unicode/utf8"
)

const notebookLearningRule = `
16. 同一次门控还要提取群内说法的笔记候选 notebook_candidates，不要把词义关系只压缩成人物偏好。当前发言者亲自解释某个群内简称、黑话或外号，或明确确认别人对自己用词的解释时，结合 current/current_batch 与 recent_messages 提取“词→含义”；解释可以是展开拼写、答复前文疑问，不要求固定措辞。confirmation_message_id 必须指向 current/current_batch 中当前发言者亲自解释或确认的消息；evidence_message_ids 引用输入里真实存在的用词和解释消息 ID。仅有机器人解释、搜索结果或你自己的推测而无人确认，不得记为已确认；普通词汇、仅提到产品全名但没有别称关系的不收。只收已有明确依据的释义，不需要等出现多次，也不要求网上有同样叫法。写入范围由程序按笔记本共享设置决定，不覆盖已有条目或恢复已作废条目。最多3条，没有就返回空数组。已经放进 notebook_candidates 的词不要重复放进 unknown_terms；未知含义的词继续走 unknown_terms。`

type notebookLearningCandidate struct {
	Term                  string   `json:"term"`
	Meaning               string   `json:"meaning"`
	EvidenceMessageIDs    []string `json:"evidence_message_ids"`
	ConfirmationMessageID string   `json:"confirmation_message_id"`
}

func notebookLearningSchema() map[string]any {
	return toolItemsParam("由当前发言者明确解释或确认的群内说法；没有则空数组", 3,
		[]string{"term", "meaning", "evidence_message_ids", "confirmation_message_id"}, map[string]any{
			"term":                    toolStringParam("原文出现的2到8字群内说法"),
			"meaning":                 toolStringParam("已确认的释义，不超过120字，不写推测"),
			"evidence_message_ids":    toolStringArrayParam("输入中的用词、解释消息ID，最多6个"),
			"confirmation_message_id": toolStringParam("current/current_batch 中本人解释或确认的消息ID"),
		})
}

// The model supplies semantics; provenance, scope and overwrite protection are enforced in code.
func (r *Runtime) learnNotebookDefinitions(ctx context.Context, event MessageEvent, payload memoryGatePayload, raw string, speakerAI *memorySpeakerAI) map[string]bool {
	confirmed := make(map[string]bool)
	store := r.notebookStore()
	cfg := r.effectiveConfigForEvent(event)
	if store == nil || !slangLearningActive(cfg, event) || strings.TrimSpace(event.ProfileID) == "" || event.Outbound ||
		(speakerAI != nil && speakerAI.Likelihood >= 0.9) || r.userLikelyAI(event.ProfileID, event.UserID) {
		return confirmed
	}
	scope, err := notebookScopeKeyForWrite(event, cfg, false, false)
	if err != nil {
		return confirmed
	}
	raw = strings.TrimSpace(stripJSONCodeFence(raw))
	start := strings.Index(raw, "{")
	if start < 0 {
		return confirmed
	}
	var envelope struct {
		Candidates []notebookLearningCandidate `json:"notebook_candidates"`
	}
	if json.NewDecoder(strings.NewReader(raw[start:])).Decode(&envelope) != nil {
		return confirmed
	}
	current := append([]memoryGateEvent(nil), payload.CurrentBatch...)
	if payload.Current != nil {
		current = append(current, *payload.Current)
	}
	all := make(map[string]memoryGateEvent)
	confirmations := make(map[string]memoryGateEvent)
	for _, item := range append(append([]memoryGateEvent(nil), payload.RecentMessages...), current...) {
		if item.MessageID != "" && item.GroupID == event.GroupID {
			all[item.MessageID] = item
		}
	}
	for _, item := range current {
		if item.MessageID != "" && item.GroupID == event.GroupID && item.UserID == event.UserID && !item.FromBot {
			confirmations[item.MessageID] = item
		}
	}
	for i, candidate := range envelope.Candidates {
		if i >= 3 {
			break
		}
		term := normalizeSlangTerm(candidate.Term)
		meaning := strings.TrimSpace(candidate.Meaning)
		confirmation, ok := confirmations[candidate.ConfirmationMessageID]
		if !ok || term == "" || meaning == "" || utf8.RuneCountInString(meaning) > 120 || len(candidate.EvidenceMessageIDs) == 0 || len(candidate.EvidenceMessageIDs) > 6 {
			continue
		}
		valid, used := true, false
		for _, id := range candidate.EvidenceMessageIDs {
			item, exists := all[id]
			if !exists {
				valid = false
				break
			}
			if !item.FromBot && slangTermInText(term, item.Text) {
				used = true
			}
		}
		if !valid || !used {
			continue
		}
		key := NormalizeNotebookTitle(term)
		if confirmed[key] {
			continue
		}
		confirmed[key] = true
		_, _, err := store.UpsertNotebookEntry(ctx, NotebookUpsertRequest{
			ScopeKey: scope, Kind: NotebookKindTerm, Term: term, Meaning: meaning,
			CreateOnly: true, GuardScopes: notebookScopeKeys(event),
			EditorUserID: SlangAutoEditorID, EditorName: slangAutoEditorName,
			SourceSession: sessionKey(event), SourceMessageID: confirmation.MessageID,
			Note: TruncateNotebookText("自动学习：群友明确解释；解释者="+confirmation.UserID+"；确认消息="+confirmation.MessageID+"；依据="+strings.Join(candidate.EvidenceMessageIDs, ","), NotebookNoteMaxRunes),
			Now:  time.Now(),
		})
		if err != nil {
			log.Printf("diana notebook learning failed: %v", err)
		}
	}
	return confirmed
}
