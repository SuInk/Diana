// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func repositoryWatchChatToolFixture(t *testing.T, event MessageEvent, owner bool) (*dianaRepositoryWatchTool, *stubReminderStore) {
	t.Helper()
	store := &stubReminderStore{items: []Reminder{
		{
			ID: "watch-demo", Kind: ReminderKindRepositoryWatch, OwnerID: "webui:bot",
			Repository: "acme/demo", IntervalSeconds: 60, TriggerAt: time.Now().Add(time.Minute),
			WatchCommits: true, WatchPullRequests: true, WatchIssues: true,
		},
		{
			ID: "watch-secret", Kind: ReminderKindRepositoryWatch, OwnerID: "webui:bot",
			Repository: "acme/private", IntervalSeconds: 60, TriggerAt: time.Now().Add(time.Minute),
			WatchCommits: true,
		},
	}}
	runtime := NewRuntime(BotConfig{OwnerID: "owner"}, nilChannel{}, NewDefaultPluginManager(), nil, store, nil, nil)
	settings := SettingValues{
		repositoryPublishSettingManagerUsers:  "manager = acme/demo",
		repositoryPublishSettingManagerGroups: "group-1 = acme/demo",
	}
	managed := repositoryWatchManagedRepositories(event, settings)
	return newDianaRepositoryWatchTool(runtime, event, owner, managed, SettingValues{}), store
}

func runRepositoryWatchChatTool(t *testing.T, tool *dianaRepositoryWatchTool, input map[string]any) dianaRepositoryWatchResult {
	t.Helper()
	raw, err := tool.Run(context.Background(), input)
	if err != nil {
		t.Fatalf("Run(%v) error = %v", input, err)
	}
	var result dianaRepositoryWatchResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatalf("Unmarshal(%s) error = %v", raw, err)
	}
	return result
}

// TestRepositoryWatchChatToolLetsManagersEditTheirRepository 管理人员能在群里改自己
// 管的那个仓库的订阅——订阅本身是 WebUI 建的，属主不是他，按仓库权限判断才对。
func TestRepositoryWatchChatToolLetsManagersEditTheirRepository(t *testing.T) {
	event := MessageEvent{Kind: EventKindGroup, GroupID: "group-1", UserID: "manager"}
	tool, store := repositoryWatchChatToolFixture(t, event, false)

	listed := runRepositoryWatchChatTool(t, tool, map[string]any{"operation": "list"})
	if len(listed.Items) != 1 || listed.Items[0].ID != "watch-demo" {
		t.Fatalf("list = %#v，只该看到自己管的仓库", listed.Items)
	}

	updated := runRepositoryWatchChatTool(t, tool, map[string]any{
		"operation": "update", "id": "watch-demo",
		"watch":               []any{"pull_requests"},
		"pull_request_events": []any{"opened", "merged"},
	})
	if !updated.OK || updated.Watch == nil {
		t.Fatalf("update = %#v", updated)
	}
	if len(updated.Watch.Watch) != 1 || updated.Watch.Watch[0] != "pull_requests" {
		t.Fatalf("watch types = %v", updated.Watch.Watch)
	}
	if strings.Join(updated.Watch.PullRequestEvents, ",") != "opened,merged" {
		t.Fatalf("pull request events = %v", updated.Watch.PullRequestEvents)
	}
	for _, item := range store.items {
		if item.ID == "watch-demo" && (!item.WatchPullRequests || item.WatchCommits || len(item.WatchPullRequestEvents) != 2) {
			t.Fatalf("落库的订阅没改对: %#v", item)
		}
	}
}

// TestRepositoryWatchChatToolHidesRepositoriesTheCallerCannotManage 没授权的仓库
// 既列不出来也动不了，而且「没有」和「不给动」要回同一句话——否则可以拿 ID 试探
// 出别人配了哪些仓库。
func TestRepositoryWatchChatToolHidesRepositoriesTheCallerCannotManage(t *testing.T) {
	event := MessageEvent{Kind: EventKindGroup, GroupID: "group-1", UserID: "manager"}
	tool, _ := repositoryWatchChatToolFixture(t, event, false)

	_, err := tool.Run(context.Background(), map[string]any{"operation": "update", "id": "watch-secret", "watch": []any{"commits"}})
	if err == nil {
		t.Fatal("管理人员改了自己没授权的仓库")
	}
	_, missing := tool.Run(context.Background(), map[string]any{"operation": "update", "id": "watch-nonexistent", "watch": []any{"commits"}})
	// 两句话只该差在回显的 ID 上：措辞一样，就没法拿 ID 试出别人配了哪些仓库。
	if missing == nil || strings.ReplaceAll(missing.Error(), "watch-nonexistent", "X") != strings.ReplaceAll(err.Error(), "watch-secret", "X") {
		t.Fatalf("没权限和不存在回了不同的话：%v / %v", err, missing)
	}

	_, err = tool.Run(context.Background(), map[string]any{"operation": "create", "repository": "acme/private"})
	if err == nil {
		t.Fatal("管理人员给自己没授权的仓库建了订阅")
	}
}

// TestRepositoryWatchChatToolRefusesStrangers 既不是主人也不在任何管理人员名单里的
// 人，这个工具一开始就不该发给他；真被调到也要拒。
func TestRepositoryWatchChatToolRefusesStrangers(t *testing.T) {
	event := MessageEvent{Kind: EventKindGroup, GroupID: "group-9", UserID: "stranger"}
	tool, _ := repositoryWatchChatToolFixture(t, event, false)
	if _, err := tool.Run(context.Background(), map[string]any{"operation": "list"}); err == nil {
		t.Fatal("路人也能管仓库订阅")
	}
}

// TestRepositoryWatchChatToolOwnerSeesEverything 主人不受仓库授权名单限制。
func TestRepositoryWatchChatToolOwnerSeesEverything(t *testing.T) {
	event := MessageEvent{Kind: EventKindPrivate, UserID: "owner"}
	tool, _ := repositoryWatchChatToolFixture(t, event, true)
	listed := runRepositoryWatchChatTool(t, tool, map[string]any{"operation": "list"})
	if len(listed.Items) != 2 {
		t.Fatalf("主人只看到 %d 个订阅", len(listed.Items))
	}
	cancelled := runRepositoryWatchChatTool(t, tool, map[string]any{"operation": "cancel", "id": "watch-secret"})
	if !cancelled.OK || cancelled.Watch == nil || cancelled.Watch.Status != "cancelled" {
		t.Fatalf("cancel = %#v", cancelled)
	}
}

// TestRepositoryWatchEventKindsFromToolClearsBackToAll 传空数组是「改回全部」，
// 不是「一条都不要」；完全不传才是「别动」。
func TestRepositoryWatchEventKindsFromToolClearsBackToAll(t *testing.T) {
	current := Reminder{Repository: "acme/demo", WatchPullRequestEvents: []string{"opened"}}
	update, err := repositoryWatchUpdateFromTool(map[string]any{"pull_request_events": []any{}}, current)
	if err != nil {
		t.Fatal(err)
	}
	if update.WatchPullRequestEvents == nil || len(update.WatchPullRequestEvents) != 0 {
		t.Fatalf("清空没有传下去: %#v", update.WatchPullRequestEvents)
	}
	untouched, err := repositoryWatchUpdateFromTool(map[string]any{}, current)
	if err != nil {
		t.Fatal(err)
	}
	if untouched.WatchPullRequestEvents != nil {
		t.Fatalf("没提的字段被当成了修改: %#v", untouched.WatchPullRequestEvents)
	}
}
