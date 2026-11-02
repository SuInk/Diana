// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"
)

// messageEditAuditMaxRunes 是通知里改前、改后各自保留的字数，长消息截断，别把私聊刷成一面墙。
const messageEditAuditMaxRunes = 500

// messageRevisionBeforeEdit 找这条消息被编辑之前的样子：先看内存里的会话历史，
// 再查持久化的聊天记录。找不到时 ok 为 false。
func (r *Runtime) messageRevisionBeforeEdit(event MessageEvent) (MessageEvent, bool) {
	messageID := strings.TrimSpace(event.MessageID)
	if messageID == "" || strings.TrimSpace(event.GroupID) == "" {
		return MessageEvent{}, false
	}
	r.mu.RLock()
	for _, item := range r.history[sessionKey(event)] {
		if strings.TrimSpace(item.MessageID) == messageID && sameEditedMessageIdentity(item, event) {
			r.mu.RUnlock()
			return item, true
		}
	}
	store := r.messageStore
	r.mu.RUnlock()
	lookup, ok := store.(MessageEventLookupStore)
	if !ok {
		return MessageEvent{}, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	record, found, err := lookup.FindMessageEvent(ctx, sessionKey(event), messageID)
	if err != nil {
		log.Printf("diana edited message load failed: message_id=%s: %v", messageID, err)
		return MessageEvent{}, false
	}
	return record, found
}

// auditMessageEdit 在本群开了消息编辑审计时，把改前改后私聊发给主人。
//
// 主人自己改的不通知；内容没变的编辑（Telegram 补链接预览之类）也不通知。
// 改前找不到（机器人当时没收到原消息）照样通知，只是写明不知道原文。
func (r *Runtime) auditMessageEdit(ctx context.Context, event MessageEvent, before MessageEvent, hasBefore bool) {
	if event.Kind != EventKindGroup || !event.Edited {
		return
	}
	gov, ok := r.groupGovernance(event)
	if !ok || !gov.MessageEditAuditEnabled {
		return
	}
	cfg := r.effectiveConfigForEvent(event)
	if strings.TrimSpace(cfg.OwnerID) == "" || !r.admitsGroupScope(cfg, event) {
		return
	}
	afterText := messageEditAuditText(event)
	beforeText := ""
	if hasBefore {
		beforeText = messageEditAuditText(before)
		if beforeText == afterText {
			return
		}
	}
	record := EventRecord{
		At: r.clock(), Kind: event.Kind, Platform: event.Platform, ProfileID: event.ProfileID,
		UserID: event.UserID, GroupID: event.GroupID, MessageID: event.MessageID,
		Text: "[notice] message_edit", Handled: true, Outcome: "message_edit_audited",
		Decision: "notified", Reason: "成员编辑了消息，已私聊通知主人",
	}
	ownerID, err := r.ownerDeliveryID(event, cfg)
	if err != nil {
		log.Printf("diana message edit audit skipped: group=%s user=%s: %v", event.GroupID, event.UserID, err)
		record.Outcome, record.Decision, record.Reason = "message_edit_audit_undeliverable", "skipped", "成员编辑了消息，但通知不到主人："+err.Error()
		r.record(record)
		return
	}
	if ownerID == strings.TrimSpace(event.UserID) {
		return
	}

	groupLabel := event.GroupID
	if name := strings.TrimSpace(event.GroupName); name != "" {
		groupLabel = fmt.Sprintf("%s（%s）", name, event.GroupID)
	} else {
		infoCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		if info, err := r.getGroupInfoForEvent(infoCtx, event, event.GroupID); err == nil && strings.TrimSpace(info.GroupName) != "" {
			groupLabel = fmt.Sprintf("%s（%s）", strings.TrimSpace(info.GroupName), event.GroupID)
		}
		cancel()
	}
	member := event.UserID
	if name := strings.TrimSpace(event.SenderName); name != "" {
		member = fmt.Sprintf("%s（%s）", name, event.UserID)
	}
	if !hasBefore {
		beforeText = "（机器人没收到原消息，不知道改前的内容）"
	} else if beforeText == "" {
		beforeText = "（无文字）"
	}
	if afterText == "" {
		afterText = "（无文字）"
	}
	text := strings.Join([]string{
		"群消息被编辑", "群：" + groupLabel, "成员：" + member,
		"改前：" + truncateRunes(beforeText, messageEditAuditMaxRunes),
		"改后：" + truncateRunes(afterText, messageEditAuditMaxRunes),
	}, "\n")
	notifyEvent := MessageEvent{
		ProfileID: event.ProfileID, Platform: event.Platform, ContextNamespace: event.ContextNamespace,
		Kind: EventKindPrivate, SelfID: event.SelfID, UserID: ownerID, Time: r.clock().Unix(),
	}
	if err := r.sendNotification(ctx, notifyEvent, text); err != nil {
		log.Printf("diana message edit audit notification failed: group=%s user=%s: %v", event.GroupID, event.UserID, err)
		record.Outcome, record.Decision, record.Reason = "message_edit_audit_failed", "failed", "成员编辑了消息，私聊通知主人失败："+err.Error()
	}
	r.record(record)
}

func messageEditAuditText(event MessageEvent) string {
	text := strings.TrimSpace(PlainText(event.Segments))
	if text == "" {
		text = strings.TrimSpace(event.RawMessage)
	}
	return text
}
