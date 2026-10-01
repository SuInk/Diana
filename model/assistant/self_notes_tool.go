// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

const (
	defaultSelfNoteListLimit = 20
	maximumSelfNoteListLimit = 60
)

// dianaSelfNoteTool 是模型维护自述的入口。
//
// 写入不需要主人权限：自述是它自己的自我描述，不是谁的特权，按好感度锁起来这个
// 功能就等于没有。清空是主人专属——那是一次性抹掉全部修订史的操作。
type dianaSelfNoteTool struct {
	runtime      *Runtime
	event        MessageEvent
	relationship RelationshipPolicy
}

type dianaSelfNoteResult struct {
	OK      bool       `json:"ok"`
	Action  string     `json:"action"`
	Message string     `json:"message,omitempty"`
	Note    *SelfNote  `json:"note,omitempty"`
	Items   []SelfNote `json:"items,omitempty"`
	Removed int        `json:"removed,omitempty"`
	// ReplyGuidance 只在真调用了才付 token，而且正好在要用它的那一刻送到。
	ReplyGuidance string `json:"reply_guidance,omitempty"`
}

const selfNoteReplyGuidance = "记下或改完一条自述时顺口说一句就够，不要复述条目、不要报 ID 和版本号，也不要把它说成用户下的指令。" +
	"别人问你有哪些自述时可以概括着说，不必逐条念。"

func newDianaSelfNoteTool(runtime *Runtime, event MessageEvent, relationship RelationshipPolicy) *dianaSelfNoteTool {
	return &dianaSelfNoteTool{runtime: runtime, event: event, relationship: relationship}
}

func (*dianaSelfNoteTool) Name() string { return dianaSelfNoteToolName }

func (*dianaSelfNoteTool) Description() string {
	// 「别人的评价不记」「自述不能放权」的完整说法在系统提示词 promptToolSelfNote；
	// 权限那句这里仍保留一句，因为它是安全边界，工具单独挂上时也要在。
	return "维护你对自己的自述：说话习惯、偏好、常犯的毛病、自己定的做法。" +
		"用户的偏好和事实归长期记忆和笔记本。权限、主人、安全边界和系统规则写进来不会生效。" +
		"同一件事已有条目就 revise 那一条，不要另加矛盾的。"
}

func (*dianaSelfNoteTool) InputSchema() map[string]any {
	return toolObjectSchema([]string{"operation"}, map[string]any{
		"operation": toolEnumParam("revise 改写已有条目；purge 清空全部，仅主人",
			"add", "revise", "delete", "list", "purge"),
		"topic":            toolStringParam("类别标签，如「说话方式」，≤" + itoa(SelfNoteTopicMaxRunes) + " 字"),
		"content":          toolStringParam("add/revise 必填：一句话一件事，≤" + itoa(SelfNoteContentMaxRunes) + " 字"),
		"id":               toolStringParam("revise/delete 必填：条目 ID"),
		"reason":           toolStringParam("delete：不再成立的原因"),
		"include_inactive": toolBoolParam("list：包含已改写和删除的历史版本"),
		"limit":            toolIntParam("list 条数，默认 "+itoa(defaultSelfNoteListLimit), 1, maximumSelfNoteListLimit),
	})
}

func (t *dianaSelfNoteTool) Run(ctx context.Context, input map[string]any) (string, error) {
	if t == nil || t.runtime == nil {
		return "", fmt.Errorf("diana self note: runtime is not configured")
	}
	store := t.runtime.selfNoteStore()
	if store == nil {
		return "", fmt.Errorf("自述功能需要持久化存储，当前部署没有启用")
	}
	if !t.runtime.selfNoteEnabled(t.event) {
		return "", fmt.Errorf("这台机器人没有开启自述")
	}
	operation := strings.ToLower(strings.TrimSpace(configToolString(input, "operation")))
	if operation == "" {
		operation = "list"
	}
	switch operation {
	case "add", "set", "create":
		return t.write(ctx, store, input, "")
	case "revise", "update", "upsert":
		id := strings.TrimSpace(configToolString(input, "id"))
		if id == "" {
			return "", fmt.Errorf("改写自述要带上 id，先用 list 查一遍")
		}
		return t.write(ctx, store, input, id)
	case "delete", "remove", "forget":
		return t.delete(ctx, store, input)
	case "list", "get":
		return t.list(ctx, store, input)
	case "purge", "clear":
		return t.purge(ctx, store)
	default:
		return "", fmt.Errorf("不支持的操作: %s", operation)
	}
}

func (t *dianaSelfNoteTool) write(ctx context.Context, store SelfNoteStore, input map[string]any, supersedesID string) (string, error) {
	content := NormalizeSelfNoteContent(configToolString(input, "content"))
	if content == "" {
		return "", fmt.Errorf("自述正文不能为空")
	}
	note, err := store.WriteSelfNote(ctx, SelfNoteWriteRequest{
		ProfileID:       strings.TrimSpace(t.event.ProfileID),
		Topic:           NormalizeSelfNoteTopic(configToolString(input, "topic")),
		Content:         content,
		SupersedesID:    supersedesID,
		SourceSession:   sessionKey(t.event),
		SourceGroupID:   strings.TrimSpace(t.event.GroupID),
		SourceMessageID: strings.TrimSpace(t.event.MessageID),
		SourceUserID:    strings.TrimSpace(t.event.UserID),
		SourceUserName:  strings.TrimSpace(t.event.SenderName),
		Now:             t.runtime.clock(),
	})
	switch {
	case errors.Is(err, ErrSelfNoteCapacity):
		// 明确告诉模型该怎么办：满了不是「记不住」，是该去改已有的那条。
		return "", fmt.Errorf("自述已经有 %d 条，到上限了：先 list 看一遍，用 revise 改掉过时的那条，或者 delete 一条不再成立的", MaximumActiveSelfNotes)
	case err != nil:
		return "", err
	}
	action := "add"
	message := "已记下这条自述"
	if supersedesID != "" {
		action = "revise"
		message = "已改写这条自述"
	}
	return encodeSelfNoteResult(dianaSelfNoteResult{OK: true, Action: action, Message: message, Note: &note, ReplyGuidance: selfNoteReplyGuidance})
}

func (t *dianaSelfNoteTool) delete(ctx context.Context, store SelfNoteStore, input map[string]any) (string, error) {
	id := strings.TrimSpace(configToolString(input, "id"))
	if id == "" {
		return "", fmt.Errorf("要删的条目 id 不能为空，先用 list 查一遍")
	}
	note, found, err := store.DeleteSelfNote(ctx, strings.TrimSpace(t.event.ProfileID), id,
		strings.TrimSpace(t.event.UserID), strings.TrimSpace(t.event.SenderName), t.runtime.clock())
	if err != nil {
		return "", err
	}
	if !found {
		return encodeSelfNoteResult(dianaSelfNoteResult{Action: "delete", Message: "没有这条自述，可能已经删过了", ReplyGuidance: selfNoteReplyGuidance})
	}
	return encodeSelfNoteResult(dianaSelfNoteResult{OK: true, Action: "delete", Message: "已删掉这条自述", Note: &note, ReplyGuidance: selfNoteReplyGuidance})
}

func (t *dianaSelfNoteTool) list(ctx context.Context, store SelfNoteStore, input map[string]any) (string, error) {
	limit := intFromAny(input["limit"])
	if limit <= 0 {
		limit = defaultSelfNoteListLimit
	}
	if limit > maximumSelfNoteListLimit {
		limit = maximumSelfNoteListLimit
	}
	// 历史版本只给主人看：它带着「谁在场时写的」这类来源信息，属于审计视角。
	includeInactive := toolInputBool(input, "include_inactive") && t.relationship.Owner
	notes, err := store.ListSelfNotes(ctx, strings.TrimSpace(t.event.ProfileID), includeInactive, limit)
	if err != nil {
		return "", err
	}
	message := fmt.Sprintf("当前有 %d 条自述", len(notes))
	if len(notes) == 0 {
		message = "还没有写过自述"
	}
	return encodeSelfNoteResult(dianaSelfNoteResult{OK: true, Action: "list", Message: message, Items: notes, ReplyGuidance: selfNoteReplyGuidance})
}

func (t *dianaSelfNoteTool) purge(ctx context.Context, store SelfNoteStore) (string, error) {
	if !t.relationship.Owner {
		return "", fmt.Errorf("清空全部自述只有主人能做；单条不成立的用 delete")
	}
	removed, err := store.PurgeSelfNotes(ctx, strings.TrimSpace(t.event.ProfileID),
		strings.TrimSpace(t.event.UserID), strings.TrimSpace(t.event.SenderName), t.runtime.clock())
	if err != nil {
		return "", err
	}
	return encodeSelfNoteResult(dianaSelfNoteResult{OK: true, Action: "purge", Message: fmt.Sprintf("已清空 %d 条自述", removed), Removed: removed, ReplyGuidance: selfNoteReplyGuidance})
}

func encodeSelfNoteResult(result dianaSelfNoteResult) (string, error) {
	encoded, err := json.Marshal(result)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}
