// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"strings"
	"testing"
)

// 锚点语义:首次出现记下消息 ID 且后续不覆盖;更新推送按编号找到当初那条;
// opened 是首次宣布,不引用自己。
func TestRepositoryWatchAnchorSemantics(t *testing.T) {
	target := "onebot|p1||group|20005"
	change := repositoryWatchChange{
		PullRequests: []repositoryWatchPullRequest{{Number: 12, Status: "opened"}},
		Issues:       []repositoryWatchIssue{{Number: 7, Status: "opened"}},
	}
	anchors := appendRepositoryWatchAnchors(nil, repositoryWatchAnchorEntries(target, change, "msg-100"))
	if repositoryWatchAnchorLookup(anchors, repositoryWatchAnchorKey(target, "pr", 12)) != "msg-100" {
		t.Fatalf("首次宣布应记锚点:%+v", anchors)
	}
	// opened 那一轮不引用任何东西。
	if id := repositoryWatchAnchorReplyID(nil, target, change); id != "" {
		t.Fatalf("首次宣布不该引用:%q", id)
	}

	// 合并推送引用当初宣布的那条。
	merged := repositoryWatchChange{PullRequests: []repositoryWatchPullRequest{{Number: 12, Status: "merged"}}}
	if id := repositoryWatchAnchorReplyID(anchors, target, merged); id != "msg-100" {
		t.Fatalf("合并推送应引用首次宣布的消息:%q", id)
	}
	// 更新不覆盖锚点:即使又发了一条,锚点仍指向最初那条。
	anchors = appendRepositoryWatchAnchors(anchors, repositoryWatchAnchorEntries(target, merged, "msg-200"))
	if repositoryWatchAnchorLookup(anchors, repositoryWatchAnchorKey(target, "pr", 12)) != "msg-100" {
		t.Fatalf("锚点不该被后续消息覆盖:%+v", anchors)
	}
	// 别的目标查不到这个锚点。
	if id := repositoryWatchAnchorReplyID(anchors, "other-target", merged); id != "" {
		t.Fatalf("锚点按投递目标隔离:%q", id)
	}

	// 编码往返。
	decoded := decodeRepositoryWatchAnchors(encodeRepositoryWatchAnchors(anchors))
	if repositoryWatchAnchorLookup(decoded, repositoryWatchAnchorKey(target, "pr", 12)) != "msg-100" {
		t.Fatal("编码往返丢数据")
	}

	// 上限:超出后最老的被挤掉。
	many := map[string]string{}
	for index := 0; index < repositoryWatchAnchorLimit+10; index++ {
		many[repositoryWatchAnchorKey(target, "pr", 1000+index)] = "m"
	}
	capped := appendRepositoryWatchAnchors(nil, many)
	if len(capped) != repositoryWatchAnchorLimit {
		t.Fatalf("锚点条目应封顶 %d,实际 %d", repositoryWatchAnchorLimit, len(capped))
	}
}

// 端到端:PR 合并的推送必须带着引用元数据发出,引用的是宣布 PR 的那条消息。
func TestRepositoryWatchMergeNotificationQuotesAnnouncement(t *testing.T) {
	channel := &recordingChannel{}
	runtime := NewRuntime(BotConfig{}, channel, NewPluginManager(), nil, &stubReminderStore{}, nil, nil)
	item := Reminder{
		ID: "watch-1", Kind: ReminderKindRepositoryWatch, Platform: "onebot",
		GroupID: "20005", OwnerID: "10001", Repository: "SuInk/Diana",
		NotificationEnabled: true, WatchPullRequests: true, IntervalSeconds: 600,
	}
	_ = runtime.reminders.SaveReminders([]Reminder{item})

	opened := repositoryWatchChange{
		Repository:   "SuInk/Diana",
		PullRequests: []repositoryWatchPullRequest{{Number: 12, Title: "新功能", Status: "opened"}},
	}
	if err := runtime.sendRepositoryWatchChange(context.Background(), item, "新增 PR #12", &opened); err != nil {
		t.Fatal(err)
	}
	stored := runtime.reminders.Reminders()[0]
	if !strings.Contains(stored.WatchAnchorsJSON, "pr:12") {
		t.Fatalf("锚点没有持久化:%q", stored.WatchAnchorsJSON)
	}

	merged := repositoryWatchChange{
		Repository:   "SuInk/Diana",
		PullRequests: []repositoryWatchPullRequest{{Number: 12, Title: "新功能", Status: "merged"}},
	}
	if err := runtime.sendRepositoryWatchChange(context.Background(), stored, "PR #12 已合并", &merged); err != nil {
		t.Fatal(err)
	}
	sent := channel.sentSnapshot()
	if len(sent) != 2 {
		t.Fatalf("应发出两条通知,实际 %d", len(sent))
	}
	if sent[1].ReplyMessageID != "42" {
		t.Fatalf("合并推送应引用宣布消息(id 42):%#v", sent[1])
	}
	if strings.Contains(sent[1].Text, replyMarkerPrefix) {
		t.Fatalf("标记应被解析掉,不能留在正文里:%q", sent[1].Text)
	}
}
