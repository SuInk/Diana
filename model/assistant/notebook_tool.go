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

const dianaNotebookToolName = "diana.notebook"

const (
	defaultNotebookListLimit = 20
	maximumNotebookListLimit = 50
)

// dianaNotebookTool 是模型维护笔记本的入口：查、记、改、删、恢复。
//
// 笔记本默认跟随机器人，群聊私聊共用一本，所有条目都写进这台机器人的全局本。
// 只有管理员关掉「跟随机器人」、改成按会话隔离时，才有「global 作用域只有主人能写、
// 其余留给当前会话」这条分界。删除对普通成员限于自己教过的条目——否则谁都能把
// 别人立的规矩抹掉，而笔记本是共用的。
type dianaNotebookTool struct {
	runtime      *Runtime
	event        MessageEvent
	relationship RelationshipPolicy
}

type dianaNotebookResult struct {
	OK      bool   `json:"ok"`
	Action  string `json:"action"`
	Message string `json:"message,omitempty"`
	Scope   string `json:"scope,omitempty"`
	// Entry 是单条操作的结果，Items 是列表和检索的结果。
	Entry *NotebookEntry  `json:"entry,omitempty"`
	Items []NotebookEntry `json:"items,omitempty"`
	// ReplyGuidance 和 diana.relationship 同理：怎么把结果说出来的约束放在返回值里，
	// 只在真调用了才付 token，而且正好在要用它的那一刻送到。
	ReplyGuidance string `json:"reply_guidance,omitempty"`
}

const notebookReplyGuidance = "用自然的中文回答，不要把条目按字段抄成清单。" +
	"记住或改完一个词时说一句就够，不要复述整条条目，也不要报版本号和作用域，除非用户问。" +
	"检索没命中时直接说不知道这个说法，不要编释义。"

func newDianaNotebookTool(runtime *Runtime, event MessageEvent, relationship RelationshipPolicy) *dianaNotebookTool {
	return &dianaNotebookTool{runtime: runtime, event: event, relationship: relationship}
}

func (t *dianaNotebookTool) Name() string {
	return dianaNotebookToolName
}

func (t *dianaNotebookTool) Description() string {
	return `维护 Diana 自己的笔记本：把需要长期记住、而且必须准确的事写下来——群里的梗和黑话（term）、群规和约定（fact）、某人的偏好和忌口（preference）、发生过的事（event）、答应了还没做的事（todo）、某个人是谁（person）。` +
		`get 查单条（带修订记录），list 列出当前作用域的笔记，upsert 新建或更新，delete 作废一条已经不成立的，restore 恢复删错的。` +
		`笔记是长期维护的：发现旧内容过时、不准或变了，用 upsert 更新同一条并在 note 里写清这次改了什么，不要另建一条重复的。` +
		`只记「记错了要能改」的事；随口聊到的东西会被自动记忆收走，不必写进这里。`
}

func (t *dianaNotebookTool) InputSchema() map[string]any {
	return toolObjectSchema([]string{"operation"}, map[string]any{
		"operation": toolEnumParam("要执行的操作：get 查条目；list 列出条目；upsert 新建或更新；delete 作废；restore 恢复被作废的条目。",
			"get", "list", "upsert", "delete", "restore"),
		"kind": toolEnumParam("upsert 可选，默认 term：term 梗/黑话/缩写/外号；fact 群规、约定、谁负责什么；preference 喜好与忌口；event 发生过的事；todo 答应了还没做的事；person 某个人是谁。",
			notebookKindValues()...),
		"term":            toolStringParam("标题。term 类型填那个词本身；其余类型填一句概括，例如「群规：十点后不刷屏」。get、upsert、delete、restore 必填。"),
		"meaning":         toolStringParam("upsert 必填：这条笔记的正文，一两句话说清楚。term 类型写清是褒是贬、谁在用。条目已存在而你不是主人、也不是当初记它的人时，这段会作为「补充说法」并存，不会覆盖原释义。"),
		"aliases":         toolStringArrayParam("可选：触发词，出现在对话里就会想起这条笔记。term 类型填这个词的其它写法；其余类型填这条笔记该被什么话题勾起来——标题不会原样出现在聊天里，没有触发词的笔记基本命不中。"),
		"example":         toolStringParam("可选：一句能体现用法或场景的例子。"),
		"note":            toolStringParam("upsert 和 delete 可选：这次改动或作废的原因，会记进修订记录。"),
		"global":          toolBoolParam("可选：笔记本默认跟随机器人、所有会话共用，不用传。只有笔记本被设成按会话隔离时才有意义，且仅主人可用：写进跨会话生效的全局笔记本。"),
		"query":           toolStringParam("list 可选：只列出包含该关键词的笔记。"),
		"kinds":           toolStringArrayParam("list 可选：只列出这些类型。"),
		"limit":           toolIntParam("list 返回条数，默认 "+itoa(defaultNotebookListLimit)+"。", 1, maximumNotebookListLimit),
		"include_deleted": toolBoolParam("list 和 get 可选：把已作废的笔记也带出来，用于确认删过什么。"),
	})
}

func (t *dianaNotebookTool) Run(ctx context.Context, input map[string]any) (string, error) {
	if t == nil || t.runtime == nil {
		return "", fmt.Errorf("diana notebook: runtime is not configured")
	}
	store := t.runtime.notebookStore()
	if store == nil {
		return "", fmt.Errorf("笔记本功能需要持久化存储，当前部署没有启用")
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

func (t *dianaNotebookTool) get(ctx context.Context, store NotebookStore, input map[string]any) (string, error) {
	term := TruncateNotebookText(configToolString(input, "term"), NotebookTitleMaxRunes)
	if strings.TrimSpace(term) == "" {
		return "", fmt.Errorf("要查的条目不能为空")
	}
	includeDeleted := toolInputBool(input, "include_deleted")
	for _, scope := range notebookScopeKeys(t.event) {
		entry, found, err := store.NotebookEntryDetail(ctx, scope, term)
		if err != nil {
			return "", err
		}
		if !found || (entry.Status == NotebookStatusDeleted && !includeDeleted) {
			continue
		}
		return marshalDianaNotebookResult(dianaNotebookResult{
			OK:     true,
			Action: "retrieved",
			Scope:  entry.ScopeKey,
			Entry:  &entry,
		})
	}
	return marshalDianaNotebookResult(dianaNotebookResult{
		OK:      true,
		Action:  "missing",
		Message: "笔记本里没有「" + term + "」。如果这轮对话里有人解释了它，可以用 upsert 记下来。",
	})
}

func (t *dianaNotebookTool) list(ctx context.Context, store NotebookStore, input map[string]any) (string, error) {
	entries, err := store.ListNotebookEntries(ctx, NotebookQuery{
		ScopeKeys:      notebookScopeKeys(t.event),
		Kinds:          notebookKindFilter(configToolStringSlice(input, "kinds")),
		Text:           strings.TrimSpace(configToolString(input, "query")),
		Limit:          notebookListLimit(input),
		IncludeDeleted: toolInputBool(input, "include_deleted"),
		Now:            time.Now(),
	})
	if err != nil {
		return "", err
	}
	message := ""
	if len(entries) == 0 {
		message = "当前作用域的笔记本还是空的。"
	}
	return marshalDianaNotebookResult(dianaNotebookResult{
		OK:      true,
		Action:  "listed",
		Message: message,
		Items:   entries,
	})
}

func (t *dianaNotebookTool) upsert(ctx context.Context, store NotebookStore, input map[string]any) (string, error) {
	// 标题上限按类型走：词条仍然卡在一个词的长度，其余类型要放得下一句概括。
	kind := NormalizeNotebookKind(configToolString(input, "kind"))
	term := TruncateNotebookText(configToolString(input, "term"), kind.TitleLimit())
	meaning := TruncateNotebookText(configToolString(input, "meaning"), NotebookContentMaxRunes)
	if strings.TrimSpace(term) == "" {
		return "", fmt.Errorf("标题不能为空")
	}
	if strings.TrimSpace(meaning) == "" {
		return "", fmt.Errorf("正文不能为空；不知道内容就先别记，别编一个")
	}
	cfg := t.runtime.effectiveConfigForEvent(t.event)
	shared := boolValue(cfg.NotebookSharedScopeEnabled, true)
	global := toolInputBool(input, "global")
	// 共用一本笔记时不存在「全局是特权」这回事：所有条目本来就写进全局。
	if global && !shared && !t.relationship.Owner {
		return "", fmt.Errorf("只有主人能写全局笔记；这条会记在当前会话里")
	}
	now := time.Now()
	editorUserID := strings.TrimSpace(t.event.UserID)
	editorName := strings.TrimSpace(t.event.SenderNameOrID())
	aliases := configToolStringSlice(input, "aliases")
	example := TruncateNotebookText(configToolString(input, "example"), NotebookExampleMaxRunes)
	note := TruncateNotebookText(configToolString(input, "note"), NotebookNoteMaxRunes)

	// 先找有没有这一条：已存在就原地修订，不管它当初记在哪个作用域。否则「跟随机器人」
	// 之后修订一条老的会话条目会在全局本另起一条，读取时旧条目仍然优先，改了等于没改。
	existingScope, existing, found, err := t.findActiveEntry(ctx, store, term)
	if err != nil {
		return "", err
	}
	settles := t.relationship.Owner || !found || existing.AuthorUserID == "" || existing.AuthorUserID == editorUserID
	if found && !settles {
		// 不是主人也不是当初记它的人：不覆盖，作为补充说法并存（见 mergeNotebookSupplement）。
		merged := mergeNotebookSupplement(existing.Meaning, editorName, meaning, now)
		if merged == existing.Meaning && len(mergeNotebookAliases(existing.Term, existing.Aliases, aliases)) == len(existing.Aliases) {
			return marshalDianaNotebookResult(dianaNotebookResult{
				OK:      true,
				Action:  "unchanged",
				Message: "「" + existing.Term + "」已经有同样的说法，没有改动。",
				Scope:   existingScope,
				Entry:   &existing,
			})
		}
		entry, _, err := store.UpsertNotebookEntry(ctx, NotebookUpsertRequest{
			ScopeKey:        existingScope,
			Kind:            existing.Kind,
			Term:            existing.Term,
			Aliases:         mergeNotebookAliases(existing.Term, existing.Aliases, aliases),
			Meaning:         merged,
			Example:         firstNonEmpty(strings.TrimSpace(existing.Example), example),
			Note:            TruncateNotebookText(joinPromptSections("补充说法，未覆盖原释义", note), NotebookNoteMaxRunes),
			EditorUserID:    editorUserID,
			EditorName:      editorName,
			SourceSession:   sessionKey(t.event),
			SourceMessageID: strings.TrimSpace(t.event.MessageID),
			Now:             now,
		})
		if err != nil {
			return "", err
		}
		author := strings.TrimSpace(existing.AuthorName)
		if author == "" {
			author = "别人"
		}
		return marshalDianaNotebookResult(dianaNotebookResult{
			OK:      true,
			Action:  "supplemented",
			Message: "「" + entry.Term + "」已有释义（" + author + " 记的），这次的说法记为补充说法并存，没有覆盖原释义。回复时两种说法都可以提，说清各是谁的用法；主人或当初记它的人确认后才会改成主释义。",
			Scope:   entry.ScopeKey,
			Entry:   &entry,
		})
	}

	scope := existingScope
	if !found {
		scope, err = notebookScopeKeyForWrite(t.event, cfg, global, t.relationship.Owner)
		if err != nil {
			return "", err
		}
	}
	entry, created, err := store.UpsertNotebookEntry(ctx, NotebookUpsertRequest{
		ScopeKey:        scope,
		Kind:            kind,
		Term:            term,
		Aliases:         NormalizeNotebookAliases(term, aliases),
		Meaning:         meaning,
		Example:         example,
		Note:            note,
		EditorUserID:    editorUserID,
		EditorName:      editorName,
		SourceSession:   sessionKey(t.event),
		SourceMessageID: strings.TrimSpace(t.event.MessageID),
		Now:             now,
	})
	if err != nil {
		return "", err
	}
	action, message := "updated", "已更新「"+entry.Term+"」的释义，这是第 "+itoa(entry.Version)+" 版。"
	if created {
		action, message = "created", "已记住「"+entry.Term+"」。"
	}
	return marshalDianaNotebookResult(dianaNotebookResult{
		OK:      true,
		Action:  action,
		Message: message,
		Scope:   entry.ScopeKey,
		Entry:   &entry,
	})
}

// findActiveEntry 按读取顺序找这个标题现有的活跃条目。
func (t *dianaNotebookTool) findActiveEntry(ctx context.Context, store NotebookStore, term string) (string, NotebookEntry, bool, error) {
	for _, scope := range notebookScopeKeys(t.event) {
		entry, found, err := store.NotebookEntryDetail(ctx, scope, term)
		if err != nil {
			return "", NotebookEntry{}, false, err
		}
		if found && entry.Status != NotebookStatusDeleted {
			return scope, entry, true, nil
		}
	}
	return "", NotebookEntry{}, false, nil
}

func (t *dianaNotebookTool) delete(ctx context.Context, store NotebookStore, input map[string]any) (string, error) {
	term := TruncateNotebookText(configToolString(input, "term"), NotebookTitleMaxRunes)
	if strings.TrimSpace(term) == "" {
		return "", fmt.Errorf("要作废的条目不能为空")
	}
	scope, entry, err := t.resolveWritableEntry(ctx, store, term)
	if err != nil {
		return "", err
	}
	if entry.Status == NotebookStatusDeleted {
		return marshalDianaNotebookResult(dianaNotebookResult{
			OK:      true,
			Action:  "already_deleted",
			Message: "「" + entry.Term + "」之前已经作废过了。",
			Scope:   scope,
			Entry:   &entry,
		})
	}
	deleted, found, err := store.DeleteNotebookEntry(ctx, scope, term,
		strings.TrimSpace(t.event.UserID), strings.TrimSpace(t.event.SenderNameOrID()),
		TruncateNotebookText(configToolString(input, "note"), NotebookNoteMaxRunes), time.Now())
	if err != nil {
		return "", err
	}
	if !found {
		return "", fmt.Errorf("笔记本里没有「%s」", term)
	}
	return marshalDianaNotebookResult(dianaNotebookResult{
		OK:      true,
		Action:  "deleted",
		Message: "已作废「" + deleted.Term + "」，修订记录还留着，说错了可以 restore 回来。",
		Scope:   scope,
		Entry:   &deleted,
	})
}

func (t *dianaNotebookTool) restore(ctx context.Context, store NotebookStore, input map[string]any) (string, error) {
	term := TruncateNotebookText(configToolString(input, "term"), NotebookTitleMaxRunes)
	if strings.TrimSpace(term) == "" {
		return "", fmt.Errorf("要恢复的条目不能为空")
	}
	scope, _, err := t.resolveWritableEntry(ctx, store, term)
	if err != nil {
		return "", err
	}
	entry, found, err := store.RestoreNotebookEntry(ctx, scope, term,
		strings.TrimSpace(t.event.UserID), strings.TrimSpace(t.event.SenderNameOrID()), time.Now())
	if err != nil {
		return "", err
	}
	if !found {
		return "", fmt.Errorf("笔记本里没有「%s」", term)
	}
	return marshalDianaNotebookResult(dianaNotebookResult{
		OK:      true,
		Action:  "restored",
		Message: "已恢复「" + entry.Term + "」。",
		Scope:   scope,
		Entry:   &entry,
	})
}

// resolveWritableEntry 找到条目并检查改动权限。主人和当初教它的人能动——笔记本
// 是共用的，谁都能抹掉别人立的规矩就没法用了。按会话隔离时全局条目只有主人能动；
// 笔记本跟随机器人时所有条目本来就在全局本里，没有这层特权。
func (t *dianaNotebookTool) resolveWritableEntry(ctx context.Context, store NotebookStore, term string) (string, NotebookEntry, error) {
	shared := boolValue(t.runtime.effectiveConfigForEvent(t.event).NotebookSharedScopeEnabled, true)
	for _, scope := range notebookScopeKeys(t.event) {
		entry, found, err := store.NotebookEntryDetail(ctx, scope, term)
		if err != nil {
			return "", NotebookEntry{}, err
		}
		if !found {
			continue
		}
		if scope == NotebookScopeGlobal && !shared && !t.relationship.Owner {
			return "", NotebookEntry{}, fmt.Errorf("「%s」是全局条目，只有主人能改", entry.Term)
		}
		if !t.relationship.Owner && entry.AuthorUserID != "" && entry.AuthorUserID != strings.TrimSpace(t.event.UserID) {
			return "", NotebookEntry{}, fmt.Errorf("「%s」是别人记下的条目，只有主人或当初记它的人能改", entry.Term)
		}
		return scope, entry, nil
	}
	return "", NotebookEntry{}, fmt.Errorf("笔记本里没有「%s」", term)
}

func notebookListLimit(input map[string]any) int {
	limit, err := strconv.Atoi(strings.TrimSpace(configToolString(input, "limit")))
	if err != nil || limit <= 0 {
		return defaultNotebookListLimit
	}
	if limit > maximumNotebookListLimit {
		return maximumNotebookListLimit
	}
	return limit
}

func marshalDianaNotebookResult(result dianaNotebookResult) (string, error) {
	if result.OK && result.ReplyGuidance == "" {
		result.ReplyGuidance = notebookReplyGuidance
	}
	body, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return "", err
	}
	return string(body), nil
}

// notebookKindValues 是工具 schema 里 kind 的取值，顺序跟着展示顺序走。
func notebookKindValues() []string {
	kinds := NotebookKinds()
	values := make([]string, 0, len(kinds))
	for _, kind := range kinds {
		values = append(values, string(kind))
	}
	return values
}

// notebookKindFilter 把模型给的类型名转成筛选条件，未知的直接丢掉——
// 写错一个类型名不该让整次 list 变成空结果。
func notebookKindFilter(raw []string) []NotebookKind {
	kinds := make([]NotebookKind, 0, len(raw))
	for _, value := range raw {
		kind := NotebookKind(strings.ToLower(strings.TrimSpace(value)))
		if kind.Valid() {
			kinds = append(kinds, kind)
		}
	}
	return kinds
}
