// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/llm"
)

func subscriptionPushHistoryItem(t *testing.T, history []MessageEvent, text string) MessageEvent {
	t.Helper()
	for _, item := range history {
		if strings.Contains(historyPlainText(item), text) {
			return item
		}
	}
	t.Fatalf("历史里没有 %q：%#v", text, history)
	return MessageEvent{}
}

func TestRepositoryWatchPushRecordedAsSubscriptionPushNotSpeech(t *testing.T) {
	// 线上 1081572710：模型建完 Issue 后在回复里自己拼了一张「GitHub 动态：…」，
	// 二十几秒后真正的推送再来一张。根因是推送卡片进历史后和机器人发言没区别。
	runtime, _, _ := repositoryWatchFollowUpRuntime()
	card := "GitHub 动态：SuInk/Diana\nIssue #788（新建） feat(proxy) 支持代理\nhttps://github.com/SuInk/Diana/issues/788"
	item := Reminder{ID: "w1", Kind: ReminderKindRepositoryWatch, Repository: "SuInk/Diana", GroupID: "g1", UserID: "u1", NotificationEnabled: true}
	if err := runtime.sendRepositoryWatch(context.Background(), item, card); err != nil {
		t.Fatal(err)
	}
	current := MessageEvent{Kind: EventKindGroup, GroupID: "g1", UserID: "u2", SelfID: "42", MessageID: "m9", Time: 1700000000}
	push := subscriptionPushHistoryItem(t, runtime.contextHistory(current), "Issue #788")
	if push.PushKind != subscriptionPushRepositoryWatch || !push.Outbound {
		t.Fatalf("推送进历史没带标记：%#v", push)
	}
	payload, err := json.Marshal(push)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(payload), `"push_kind":"repository_watch"`) {
		t.Fatalf("标记没进持久化 payload：%s", payload)
	}

	cfg := runtime.effectiveConfigForEvent(current)
	messages := runtime.renderPromptHistoryEvent(context.Background(), current, push, cfg, false)
	if len(messages) != 1 || messages[0].Role != llm.RoleUser || !strings.Contains(messages[0].Content, "[仓库订阅推送，系统自动发出，不是你说的话") || !strings.Contains(messages[0].Content, "Issue #788") {
		t.Fatalf("推送应渲染成旁白而不是 assistant 发言：%#v", messages)
	}
	again := runtime.renderPromptHistoryEvent(context.Background(), current, push, cfg, false)
	if again[0].Content != messages[0].Content {
		t.Fatal("同一条推送两次渲染结果不同，会破坏前缀缓存")
	}

	// 普通发言走同一条出站链路，但没有推送标记，渲染保持 assistant 发言。
	// 测试通道每次回同一个 message_id，换个群免得和上面那条互相顶替。
	other := MessageEvent{Kind: EventKindGroup, GroupID: "g2", UserID: "u2", SelfID: "42", MessageID: "m10", Time: 1700000000}
	if _, err := runtime.sendWithMessageIDs(context.Background(), other, "普通回复一句"); err != nil {
		t.Fatal(err)
	}
	reply := subscriptionPushHistoryItem(t, runtime.contextHistory(other), "普通回复一句")
	if reply.PushKind != "" {
		t.Fatalf("普通回复不该带推送标记：%#v", reply)
	}
	messages = runtime.renderPromptHistoryEvent(context.Background(), current, reply, cfg, false)
	if len(messages) != 1 || messages[0].Role != llm.RoleAssistant || messages[0].Content != "普通回复一句" {
		t.Fatalf("普通回复渲染不该变：%#v", messages)
	}
}

func TestSubscriptionPushMarkerSurvivesPlatformEcho(t *testing.T) {
	// 平台回显自己发的消息时只知道是机器人发的，不知道是推送；同一 MessageID
	// 的回显不能把标记冲掉。
	runtime, _, _ := repositoryWatchFollowUpRuntime()
	pushed := MessageEvent{Kind: EventKindGroup, GroupID: "g1", UserID: "42", MessageID: "900", Outbound: true, PushKind: subscriptionPushRSSWatch,
		Segments: []MessageSegment{{Type: "text", Data: map[string]string{"text": "RSS 更新：新文章"}}}}
	runtime.remember(pushed)
	echo := pushed
	echo.PushKind, echo.Outbound = "", false
	runtime.remember(echo)

	history := runtime.contextHistory(MessageEvent{Kind: EventKindGroup, GroupID: "g1", UserID: "u1"})
	item := subscriptionPushHistoryItem(t, history, "RSS 更新")
	if item.PushKind != subscriptionPushRSSWatch {
		t.Fatalf("回显冲掉了推送标记：%#v", item)
	}
}

func TestFollowUpHistoryRendersSubscriptionPushAsNotice(t *testing.T) {
	runtime, _, provider := repositoryWatchFollowUpRuntime("SKIP")
	pushed := MessageEvent{Kind: EventKindGroup, GroupID: "g1", UserID: "42", MessageID: "901", Outbound: true, PushKind: subscriptionPushRepositoryWatch, Time: 1700000000,
		Segments: []MessageSegment{{Type: "text", Data: map[string]string{"text": "GitHub 动态：SuInk/Diana 合并了 #120"}}}}
	runtime.remember(pushed)

	runtime.followUpComment(context.Background(), followUpKindRepositoryWatch, MessageEvent{Kind: EventKindGroup, GroupID: "g1"}, "GitHub 动态：SuInk/Diana 合并了 #120")

	requests := provider.requestsSnapshot()
	if len(requests) == 0 {
		t.Fatal("跟评没有到达模型")
	}
	for _, msg := range requests[len(requests)-1].Messages {
		if msg.Role == llm.RoleAssistant && strings.Contains(msg.Content, "GitHub 动态") {
			t.Fatalf("推送卡片仍被当成 assistant 发言：%#v", msg)
		}
	}
}

func TestGitHubToolLandedWriteTellsModelNotToPostFeedCard(t *testing.T) {
	tool := &dianaGitHubTool{}
	created, err := tool.finish(context.Background(), repositoryIssueResult{OK: true, Operation: "create", Outcome: "created", Message: "GitHub 已创建 Issue。"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(created, repositoryIssueLandedReplyHint) {
		t.Fatalf("落地的写入应提醒模型别自己拼推送卡片：%s", created)
	}
	for _, result := range []repositoryIssueResult{
		{OK: true, Operation: "create", Outcome: "draft_pending", Message: "草稿"},
		{OK: true, Operation: "get", Outcome: "found", Message: "读"},
		{OK: true, Operation: "cancel_draft", Outcome: "cancelled", Message: "取消"},
		(repositoryIssueResult{Operation: "comment"}).fail("permission_denied", "没权限"),
	} {
		body, err := tool.finish(context.Background(), result)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(body, "reply_hint") {
			t.Fatalf("没落地的操作不该带提示：%s", body)
		}
	}
}
