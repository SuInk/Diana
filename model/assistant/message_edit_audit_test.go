// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"strings"
	"testing"
)

func editedGroupMessage(userID, messageID, text string) MessageEvent {
	return MessageEvent{
		Kind: EventKindGroup, Platform: PlatformTelegram, SelfID: "42", GroupID: "-100", GroupName: "测试群",
		UserID: userID, SenderName: "Alice", MessageID: messageID, MessageType: "group", RawMessage: text,
		Segments: []MessageSegment{{Type: "text", Data: map[string]string{"text": text}}},
	}
}

func TestMessageEditAuditNotifiesOwnerWithBeforeAndAfter(t *testing.T) {
	runtime, channel := telegramGovernanceRuntime(t, &GroupGovernance{MessageEditAuditEnabled: true}, "70009", nil)
	runtime.remember(editedGroupMessage("55501", "301", "明天下午三点开会"))

	edited := editedGroupMessage("55501", "301", "明天下午四点开会")
	edited.Edited, edited.EditTime = true, 1_800_000_100
	if err := runtime.HandleEvent(context.Background(), edited); err != nil {
		t.Fatal(err)
	}
	var notices []OutgoingMessage
	for _, msg := range channel.sentSnapshot() {
		if msg.UserID == "70009" && msg.GroupID == "" {
			notices = append(notices, msg)
		}
	}
	if len(notices) != 1 {
		t.Fatalf("应私聊主人一次，实际 %#v", channel.sentSnapshot())
	}
	for _, want := range []string{"群消息被编辑", "测试群（-100）", "Alice（55501）", "改前：明天下午三点开会", "改后：明天下午四点开会"} {
		if !strings.Contains(notices[0].Text, want) {
			t.Fatalf("通知 %q 缺少 %q", notices[0].Text, want)
		}
	}

	// Telegram 重推同一次编辑：内容和已知版本相同，不再通知。
	if err := runtime.HandleEvent(context.Background(), edited); err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, msg := range channel.sentSnapshot() {
		if msg.UserID == "70009" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("重复的编辑不应再通知，共通知 %d 次", count)
	}
}

func TestMessageEditAuditSkipsDisabledGroupAndOwnerEdits(t *testing.T) {
	off, offChannel := telegramGovernanceRuntime(t, &GroupGovernance{}, "70009", nil)
	off.remember(editedGroupMessage("55501", "302", "原文"))
	edited := editedGroupMessage("55501", "302", "改过")
	edited.Edited = true
	_ = off.HandleEvent(context.Background(), edited)
	if len(offChannel.sentSnapshot()) != 0 {
		t.Fatalf("没开审计不应通知：%#v", offChannel.sentSnapshot())
	}

	on, onChannel := telegramGovernanceRuntime(t, &GroupGovernance{MessageEditAuditEnabled: true}, "70009", nil)
	on.remember(editedGroupMessage("70009", "303", "原文"))
	ownerEdit := editedGroupMessage("70009", "303", "改过")
	ownerEdit.Edited = true
	_ = on.HandleEvent(context.Background(), ownerEdit)
	if len(onChannel.sentSnapshot()) != 0 {
		t.Fatalf("主人自己改的不应通知：%#v", onChannel.sentSnapshot())
	}
}

// 原消息机器人没见过（比如当时还没进群），照样通知，写明不知道原文。
func TestMessageEditAuditWithoutOriginal(t *testing.T) {
	runtime, channel := telegramGovernanceRuntime(t, &GroupGovernance{MessageEditAuditEnabled: true}, "70009", nil)
	edited := editedGroupMessage("55501", "304", "新内容")
	edited.Edited = true
	_ = runtime.HandleEvent(context.Background(), edited)
	sent := channel.sentSnapshot()
	if len(sent) == 0 || !strings.Contains(sent[0].Text, "不知道改前的内容") || !strings.Contains(sent[0].Text, "改后：新内容") {
		t.Fatalf("notification = %#v", sent)
	}
}
