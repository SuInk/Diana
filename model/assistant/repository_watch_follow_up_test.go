// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/llm"
)

func repositoryWatchFollowUpRuntime(replies ...string) (*Runtime, *recordingChannel, *sequenceLLMProvider) {
	channel := &recordingChannel{}
	provider := &sequenceLLMProvider{replies: replies}
	runtime := NewRuntime(BotConfig{BotAccount: "42"}, channel, NewDefaultPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
		return provider, nil
	})
	return runtime, channel, provider
}

func TestRepositoryWatchFollowUpSeesTargetConversationHistory(t *testing.T) {
	// 跟评的门槛写在「和会话里正在聊的事对得上」。不把该会话的历史交给模型，
	// 这个条件永远无法成立，功能等于关着。
	runtime, _, provider := repositoryWatchFollowUpRuntime("SKIP")
	target := MessageEvent{Kind: EventKindGroup, GroupID: "g1", UserID: "u1", MessageID: "m1", RawMessage: "谁知道那个转发的 bug 修好没"}
	target.Segments = []MessageSegment{{Type: "text", Data: map[string]string{"text": target.RawMessage}}}
	runtime.remember(target)

	runtime.followUpComment(context.Background(), followUpKindRepositoryWatch, target, "【仓库动态】SuInk/Diana 合并了 #120")

	requests := provider.requestsSnapshot()
	if len(requests) == 0 {
		t.Fatal("跟评没有到达模型")
	}
	var sawHistory bool
	for _, msg := range requests[len(requests)-1].Messages {
		if strings.Contains(msg.Content, "谁知道那个转发的 bug 修好没") {
			sawHistory = true
		}
	}
	if !sawHistory {
		t.Fatal("跟评提示词里没有目标会话的历史")
	}
}

func TestRepositoryWatchFollowUpJudgesEachTargetSeparately(t *testing.T) {
	// 订阅可能建在一个会话而推送到另外几个会话，每个目标要用自己的上下文各判一次。
	runtime, channel, provider := repositoryWatchFollowUpRuntime("SKIP", "这不就是刚才说的那个")
	item := Reminder{
		UserID: "u1",
		NotificationTargetsJSON: encodeReminderDeliveryTargets([]ReminderDeliveryTarget{
			{GroupID: "g1"},
			{GroupID: "g2"},
		}),
	}

	runtime.maybeSendRepositoryWatchFollowUp(context.Background(), item, "【仓库动态】SuInk/Diana 合并了 #120", "")

	if got := len(provider.requestsSnapshot()); got != 2 {
		t.Fatalf("每个通知目标都应各判定一次，实际请求 %d 次", got)
	}
	sent := channel.sentSnapshot()
	if len(sent) != 1 {
		t.Fatalf("只有第二个目标该开口，实际发出 %#v", sent)
	}
	if sent[0].GroupID != "g2" || sent[0].Text != "这不就是刚才说的那个" {
		t.Fatalf("跟评发错了目标或内容：%#v", sent[0])
	}
}

func TestRepositoryWatchFollowUpUsesNaturalChatSegmentation(t *testing.T) {
	withFastSendTiming(t)
	reply := "这次看起来是在把桌面端自动更新整套串起来喵，包含各平台的发布产物、更新提示和安装流程，Linux 还补了专门的应用逻辑喵\n" +
		"范围不小，合并前最好重点过一遍更新包校验、版本匹配和失败回退这些边界喵"
	runtime, channel, _ := repositoryWatchFollowUpRuntime(reply)
	item := Reminder{Kind: ReminderKindRepositoryWatch, GroupID: "g1", UserID: "u1"}

	runtime.maybeSendRepositoryWatchFollowUp(context.Background(), item, "GitHub 动态：SuInk/Diana", "")

	sent := channel.sentSnapshot()
	if len(sent) != 2 {
		t.Fatalf("两段仓库跟评没有自然分条：%#v", sent)
	}
	if got := []string{sent[0].Text, sent[1].Text}; !reflect.DeepEqual(got, strings.Split(reply, "\n")) {
		t.Fatalf("分条内容不对：%#v", got)
	}
	if sent[0].MentionUserID != "u1" || sent[1].MentionUserID != "" {
		t.Fatalf("订阅者应只在第一条被 @：%#v", sent)
	}
	if sent[0].ReplyMessageID != "" || sent[1].ReplyMessageID != "" {
		t.Fatalf("仓库跟评不应引用旧消息：%#v", sent)
	}
}

func TestRepositoryWatchFollowUpStaysQuietOnSkip(t *testing.T) {
	runtime, channel, provider := repositoryWatchFollowUpRuntime("SKIP")

	comment := runtime.followUpComment(context.Background(), followUpKindRepositoryWatch, MessageEvent{Kind: EventKindGroup, GroupID: "g1"}, "【仓库动态】SuInk/Diana 合并了 #120")

	if comment != "" {
		t.Fatalf("模型回 SKIP 时不该产出跟评：%q", comment)
	}
	if len(provider.requestsSnapshot()) != 1 {
		t.Fatal("跟评应当判定一次")
	}
	if got := channel.sentSnapshot(); len(got) != 0 {
		t.Fatalf("模型回 SKIP 时不该发言：%#v", got)
	}
}

func TestRepositoryWatchFollowUpPromptCarriesTheNotification(t *testing.T) {
	runtime, _, provider := repositoryWatchFollowUpRuntime("SKIP")
	notification := "【仓库动态】SuInk/Diana 合并了 #120"

	runtime.followUpComment(context.Background(), followUpKindRepositoryWatch, MessageEvent{Kind: EventKindGroup, GroupID: "g1"}, notification)

	requests := provider.requestsSnapshot()
	if len(requests) == 0 {
		t.Fatal("跟评没有到达模型")
	}
	last := requests[len(requests)-1].Messages
	final := last[len(last)-1]
	if final.Role != llm.RoleUser || !strings.Contains(final.Content, notification) {
		t.Fatalf("跟评提示词里缺少通知正文：%#v", final)
	}
}
