// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const dianaGlossaryToolName = "diana.glossary"

const (
	defaultGlossaryListLimit = 20
	maximumGlossaryListLimit = 50
)

// dianaGlossaryTool 是模型维护词典的入口：查、记、改、删、恢复。
//
// 写权限的分界只有一条：global 作用域（跨所有会话生效）只有主人能写，其余留给
// 当前会话。群里的梗是这个群的事，不该由一个人替所有群定义。删除对普通成员限于
// 自己教过的词条——否则谁都能把别人立的规矩抹掉，而词典是共用的。
type dianaGlossaryTool struct {
	runtime      *Runtime
	event        MessageEvent
	relationship RelationshipPolicy
}

type dianaGlossaryResult struct {
	OK      bool   `json:"ok"`
	Action  string `json:"action"`
	Message string `json:"message,omitempty"`
	Scope   string `json:"scope,omitempty"`
	// Entry 是单条操作的结果，Items 是列表和检索的结果。
	Entry *GlossaryEntry  `json:"entry,omitempty"`
	Items []GlossaryEntry `json:"items,omitempty"`
	// ReplyGuidance 和 diana.relationship 同理：怎么把结果说出来的约束放在返回值里，
	// 只在真调用了才付 token，而且正好在要用它的那一刻送到。
	ReplyGuidance string `json:"reply_guidance,omitempty"`
}

const glossaryReplyGuidance = "用自然的中文回答，不要把词条按字段抄成清单。" +
	"记住或改完一个词时说一句就够，不要复述整条词条，也不要报版本号和作用域，除非用户问。" +
	"检索没命中时直接说不知道这个说法，不要编释义。"

func newDianaGlossaryTool(runtime *Runtime, event MessageEvent, relationship RelationshipPolicy) *dianaGlossaryTool {
	return &dianaGlossaryTool{runtime: runtime, event: event, relationship: relationship}
}

func (t *dianaGlossaryTool) Name() string {
	return dianaGlossaryToolName
}

func (t *dianaGlossaryTool) Description() string {
	return `维护 Diana 自己的词典：记住群里的梗、黑话、缩写、内部称呼和外号，并在它们含义变了之后改过来。` +
		`get 查单个词条（带修订记录），list 列出当前作用域的词条，upsert 新建或更新释义，delete 作废一条已经不成立的词条，restore 恢复删错的词条。` +
		`词典是长期维护的：发现旧释义过时、不准或用法变了，用 upsert 更新并在 note 里写清这次改了什么，不要另建一条同义词条。`
}

func (t *dianaGlossaryTool) InputSchema() map[string]any {
	return toolObjectSchema([]string{"operation"}, map[string]any{
		"operation": toolEnumParam("要执行的操作：get 查词条；list 列出词条；upsert 新建或更新；delete 作废；restore 恢复被作废的词条。",
			"get", "list", "upsert", "delete", "restore"),
		"term":            toolStringParam("词条本身，例如一个梗、缩写或外号。get、upsert、delete、restore 必填。"),
		"meaning":         toolStringParam("upsert 必填：这个词在当前语境里的意思，一两句话说清楚，写清是褒是贬、谁在用。"),
		"aliases":         toolStringArrayParam("可选：同一个意思的其它写法或缩写，用于命中匹配。"),
		"example":         toolStringParam("可选：一句能体现用法的例子。"),
		"note":            toolStringParam("upsert 和 delete 可选：这次改动或作废的原因，会记进修订记录。"),
		"global":          toolBoolParam("可选，仅主人可用：写进跨会话生效的全局词典。默认只在当前会话生效。"),
		"query":           toolStringParam("list 可选：只列出包含该关键词的词条。"),
		"limit":           toolIntParam("list 返回条数，默认 "+itoa(defaultGlossaryListLimit)+"。", 1, maximumGlossaryListLimit),
		"include_deleted": toolBoolParam("list 和 get 可选：把已作废的词条也带出来，用于确认删过什么。"),
	})
}

func (t *dianaGlossaryTool) Run(ctx context.Context, input map[string]any) (string, error) {
	if t == nil || t.runtime == nil {
		return "", fmt.Errorf("diana glossary: runtime is not configured")
	}
	store := t.runtime.glossaryStore()
	if store == nil {
		return "", fmt.Errorf("词典功能需要持久化存储，当前部署没有启用")
	}
	operation := strings.ToLower(strings.TrimSpace(configToolString(input, "operation")))
	if operation == "" {
		operation = "get"
	}
	switch operation {
	case "get", "lookup":
		return t.get(ctx, store, input)
	case "list", "search":
		return t.list(ctx, store, input)
	case "upsert", "set", "update", "add":
		return t.upsert(ctx, store, input)
	case "delete", "remove", "forget":
		return t.delete(ctx, store, input)
	case "restore", "undelete":
		return t.restore(ctx, store, input)
	default:
		return "", fmt.Errorf("不支持的操作: %s", operation)
	}
}

func (t *dianaGlossaryTool) get(ctx context.Context, store GlossaryStore, input map[string]any) (string, error) {
	term := TruncateGlossaryText(configToolString(input, "term"), GlossaryTermMaxRunes)
	if strings.TrimSpace(term) == "" {
		return "", fmt.Errorf("要查的词条不能为空")
	}
	includeDeleted := toolInputBool(input, "include_deleted")
	for _, scope := range glossaryScopeKeys(t.event) {
		entry, found, err := store.GlossaryEntryDetail(ctx, scope, term)
		if err != nil {
			return "", err
		}
		if !found || (entry.Status == GlossaryStatusDeleted && !includeDeleted) {
			continue
		}
		return marshalDianaGlossaryResult(dianaGlossaryResult{
			OK:     true,
			Action: "retrieved",
			Scope:  entry.ScopeKey,
			Entry:  &entry,
		})
	}
	return marshalDianaGlossaryResult(dianaGlossaryResult{
		OK:      true,
		Action:  "missing",
		Message: "词典里没有「" + term + "」。如果这轮对话里有人解释了它，可以用 upsert 记下来。",
	})
}

func (t *dianaGlossaryTool) list(ctx context.Context, store GlossaryStore, input map[string]any) (string, error) {
	entries, err := store.ListGlossaryEntries(ctx, GlossaryQuery{
		ScopeKeys:      glossaryScopeKeys(t.event),
		Text:           strings.TrimSpace(configToolString(input, "query")),
		Limit:          glossaryListLimit(input),
		IncludeDeleted: toolInputBool(input, "include_deleted"),
		Now:            time.Now(),
	})
	if err != nil {
		return "", err
	}
	message := ""
	if len(entries) == 0 {
		message = "当前作用域的词典还是空的。"
	}
	return marshalDianaGlossaryResult(dianaGlossaryResult{
		OK:      true,
		Action:  "listed",
		Message: message,
		Items:   entries,
	})
}

func (t *dianaGlossaryTool) upsert(ctx context.Context, store GlossaryStore, input map[string]any) (string, error) {
	term := TruncateGlossaryText(configToolString(input, "term"), GlossaryTermMaxRunes)
	meaning := TruncateGlossaryText(configToolString(input, "meaning"), GlossaryMeaningMaxRunes)
	if strings.TrimSpace(term) == "" {
		return "", fmt.Errorf("词条不能为空")
	}
	if strings.TrimSpace(meaning) == "" {
		return "", fmt.Errorf("释义不能为空；不知道意思就先别记，别编一个")
	}
	cfg := t.runtime.effectiveConfigForEvent(t.event)
	shared := boolValue(cfg.GlossarySharedScopeEnabled, false)
	global := toolInputBool(input, "global")
	// 共用一本词典时不存在「全局是特权」这回事：所有词条本来就写进全局。
	if global && !shared && !t.relationship.Owner {
		return "", fmt.Errorf("只有主人能写全局词典；这条会记在当前会话里")
	}
	entry, created, err := store.UpsertGlossaryEntry(ctx, GlossaryUpsertRequest{
		ScopeKey:        glossaryScopeKeyForWrite(t.event, cfg, global, t.relationship.Owner),
		Term:            term,
		Aliases:         NormalizeGlossaryAliases(term, configToolStringSlice(input, "aliases")),
		Meaning:         meaning,
		Example:         TruncateGlossaryText(configToolString(input, "example"), GlossaryExampleMaxRunes),
		Note:            TruncateGlossaryText(configToolString(input, "note"), GlossaryNoteMaxRunes),
		EditorUserID:    strings.TrimSpace(t.event.UserID),
		EditorName:      strings.TrimSpace(t.event.SenderNameOrID()),
		SourceSession:   sessionKey(t.event),
		SourceMessageID: strings.TrimSpace(t.event.MessageID),
		Now:             time.Now(),
	})
	if err != nil {
		return "", err
	}
	action, message := "updated", "已更新「"+entry.Term+"」的释义，这是第 "+itoa(entry.Version)+" 版。"
	if created {
		action, message = "created", "已记住「"+entry.Term+"」。"
	}
	return marshalDianaGlossaryResult(dianaGlossaryResult{
		OK:      true,
		Action:  action,
		Message: message,
		Scope:   entry.ScopeKey,
		Entry:   &entry,
	})
}

func (t *dianaGlossaryTool) delete(ctx context.Context, store GlossaryStore, input map[string]any) (string, error) {
	term := TruncateGlossaryText(configToolString(input, "term"), GlossaryTermMaxRunes)
	if strings.TrimSpace(term) == "" {
		return "", fmt.Errorf("要作废的词条不能为空")
	}
	scope, entry, err := t.resolveWritableEntry(ctx, store, term)
	if err != nil {
		return "", err
	}
	if entry.Status == GlossaryStatusDeleted {
		return marshalDianaGlossaryResult(dianaGlossaryResult{
			OK:      true,
			Action:  "already_deleted",
			Message: "「" + entry.Term + "」之前已经作废过了。",
			Scope:   scope,
			Entry:   &entry,
		})
	}
	deleted, found, err := store.DeleteGlossaryEntry(ctx, scope, term,
		strings.TrimSpace(t.event.UserID), strings.TrimSpace(t.event.SenderNameOrID()),
		TruncateGlossaryText(configToolString(input, "note"), GlossaryNoteMaxRunes), time.Now())
	if err != nil {
		return "", err
	}
	if !found {
		return "", fmt.Errorf("词典里没有「%s」", term)
	}
	return marshalDianaGlossaryResult(dianaGlossaryResult{
		OK:      true,
		Action:  "deleted",
		Message: "已作废「" + deleted.Term + "」，修订记录还留着，说错了可以 restore 回来。",
		Scope:   scope,
		Entry:   &deleted,
	})
}

func (t *dianaGlossaryTool) restore(ctx context.Context, store GlossaryStore, input map[string]any) (string, error) {
	term := TruncateGlossaryText(configToolString(input, "term"), GlossaryTermMaxRunes)
	if strings.TrimSpace(term) == "" {
		return "", fmt.Errorf("要恢复的词条不能为空")
	}
	scope, _, err := t.resolveWritableEntry(ctx, store, term)
	if err != nil {
		return "", err
	}
	entry, found, err := store.RestoreGlossaryEntry(ctx, scope, term,
		strings.TrimSpace(t.event.UserID), strings.TrimSpace(t.event.SenderNameOrID()), time.Now())
	if err != nil {
		return "", err
	}
	if !found {
		return "", fmt.Errorf("词典里没有「%s」", term)
	}
	return marshalDianaGlossaryResult(dianaGlossaryResult{
		OK:      true,
		Action:  "restored",
		Message: "已恢复「" + entry.Term + "」。",
		Scope:   scope,
		Entry:   &entry,
	})
}

// resolveWritableEntry 找到词条并检查改动权限。全局词条只有主人能动；会话词条
// 主人和当初教它的人能动——词典是共用的，谁都能抹掉别人立的规矩就没法用了。
func (t *dianaGlossaryTool) resolveWritableEntry(ctx context.Context, store GlossaryStore, term string) (string, GlossaryEntry, error) {
	for _, scope := range glossaryScopeKeys(t.event) {
		entry, found, err := store.GlossaryEntryDetail(ctx, scope, term)
		if err != nil {
			return "", GlossaryEntry{}, err
		}
		if !found {
			continue
		}
		if scope == GlossaryScopeGlobal && !t.relationship.Owner {
			return "", GlossaryEntry{}, fmt.Errorf("「%s」是全局词条，只有主人能改", entry.Term)
		}
		if !t.relationship.Owner && entry.AuthorUserID != "" && entry.AuthorUserID != strings.TrimSpace(t.event.UserID) {
			return "", GlossaryEntry{}, fmt.Errorf("「%s」是别人记下的词条，只有主人或当初记它的人能改", entry.Term)
		}
		return scope, entry, nil
	}
	return "", GlossaryEntry{}, fmt.Errorf("词典里没有「%s」", term)
}

func glossaryListLimit(input map[string]any) int {
	limit, err := strconv.Atoi(strings.TrimSpace(configToolString(input, "limit")))
	if err != nil || limit <= 0 {
		return defaultGlossaryListLimit
	}
	if limit > maximumGlossaryListLimit {
		return maximumGlossaryListLimit
	}
	return limit
}

func marshalDianaGlossaryResult(result dianaGlossaryResult) (string, error) {
	if result.OK && result.ReplyGuidance == "" {
		result.ReplyGuidance = glossaryReplyGuidance
	}
	body, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return "", err
	}
	return string(body), nil
}
