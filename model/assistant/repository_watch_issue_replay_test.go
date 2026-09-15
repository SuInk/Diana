// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"
)

// issueReplayServer 模拟 GitHub issues 接口：按 page 分页，每页 100 条，记下每次请求的参数。
type issueReplayServer struct {
	mu      sync.Mutex
	items   []map[string]any
	queries []map[string]string
}

func (s *issueReplayServer) handler(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.queries = append(s.queries, map[string]string{"page": r.URL.Query().Get("page"), "since": r.URL.Query().Get("since")})
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	start, end := (page-1)*100, page*100
	if start > len(s.items) {
		start = len(s.items)
	}
	if end > len(s.items) {
		end = len(s.items)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(s.items[start:end])
}

func issueReplayItem(number int, updatedAt time.Time, pull bool) map[string]any {
	item := map[string]any{
		"number": number, "title": fmt.Sprintf("item %d", number), "state": "closed",
		"created_at": updatedAt.Add(-time.Hour).Format(time.RFC3339), "updated_at": updatedAt.Format(time.RFC3339),
		"closed_at": updatedAt.Format(time.RFC3339), "html_url": fmt.Sprintf("https://github.com/acme/demo/issues/%d", number),
	}
	if pull {
		item["pull_request"] = map[string]any{"url": "https://example.com"}
	}
	return item
}

func issueReplayPlugin(t *testing.T, server *issueReplayServer, now time.Time) *RepositoryWatchPlugin {
	t.Helper()
	httpServer := httptest.NewServer(http.HandlerFunc(server.handler))
	t.Cleanup(httpServer.Close)
	plugin := newRepositoryWatchPlugin(httpServer.Client(), httpServer.URL)
	plugin.now = func() time.Time { return now }
	return plugin
}

// 线上事故：游标长期停在 __none__，某次返回里出现几条早就关闭的 issue，被当成新动态全部推出。
// 现在游标是 __none__ 时，旧记录只建基线，不推送。
func TestRepositoryWatchNoneCursorDoesNotReplayOldIssues(t *testing.T) {
	now := time.Date(2026, 9, 15, 13, 53, 36, 0, time.UTC)
	server := &issueReplayServer{items: []map[string]any{
		issueReplayItem(325, time.Date(2026, 9, 1, 8, 0, 44, 0, time.UTC), false),
		issueReplayItem(44, time.Date(2026, 8, 19, 4, 28, 21, 0, time.UTC), false),
		issueReplayItem(46, time.Date(2026, 8, 17, 7, 45, 54, 0, time.UTC), false),
	}}
	plugin := issueReplayPlugin(t, server, now)
	found, next, err := plugin.fetchIssues(context.Background(), "acme/demo", repositoryWatchNoIssueCursor, now.Add(-time.Hour), repositoryWatchSelection{Issues: true}, SettingValues{repositoryWatchSettingLimit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 0 {
		t.Fatalf("__none__ 游标把旧 issue 当成了新动态：%#v", found)
	}
	if next != "2026-09-01T08:00:44Z#325" {
		t.Fatalf("应以最新 issue 建立基线，got %q", next)
	}
}

// 检查周期由订阅自己设置，可能是几个小时。游标是 __none__ 时以上次成功检查为界：
// 周期再长，上次检查之后才出现的 issue 也要推送；上次检查之前就有的只建基线。
func TestRepositoryWatchNoneCursorUsesPreviousCheckNotFixedWindow(t *testing.T) {
	now := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	previousCheck := now.Add(-6 * time.Hour)
	fresh := issueReplayItem(15, now.Add(-5*time.Hour), false)
	fresh["state"], fresh["created_at"], fresh["closed_at"] = "open", fresh["updated_at"], nil
	server := &issueReplayServer{items: []map[string]any{
		fresh,
		issueReplayItem(14, now.Add(-7*time.Hour), false),
	}}
	plugin := issueReplayPlugin(t, server, now)
	found, _, err := plugin.fetchIssues(context.Background(), "acme/demo", repositoryWatchNoIssueCursor, previousCheck, repositoryWatchSelection{Issues: true}, SettingValues{repositoryWatchSettingLimit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 1 || found[0].Number != 15 || found[0].Status != "opened" {
		t.Fatalf("上次检查之后的新 issue 应推送、之前的不推：%#v", found)
	}

	// 不知道上次检查时间时只建基线，宁可漏一次也不重放。
	found, _, err = plugin.fetchIssues(context.Background(), "acme/demo", repositoryWatchNoIssueCursor, time.Time{}, repositoryWatchSelection{Issues: true}, SettingValues{repositoryWatchSettingLimit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 0 {
		t.Fatalf("没有上次检查时间时不该推送：%#v", found)
	}
}

// 上次成功检查时间随进度一起保存，只前进不后退；升级前的订阅没有它时退回 LastRunAt。
func TestRepositoryWatchProgressStoresLastSuccessfulCheck(t *testing.T) {
	first := time.Date(2026, 9, 16, 6, 0, 0, 0, time.UTC)
	store := &stubReminderStore{items: []Reminder{{
		ID: "watch", Kind: ReminderKindRepositoryWatch, Repository: "acme/demo", IntervalSeconds: 6 * 3600,
		WatchIssues: true, LastIssueCursor: repositoryWatchNoIssueCursor, LastRunAt: first.Add(-time.Hour),
	}}}
	runtime := &Runtime{reminders: store}
	if got := repositoryWatchPreviousCheckAt(store.Reminders()[0]); !got.Equal(first.Add(-time.Hour)) {
		t.Fatalf("旧订阅应退回 LastRunAt：%s", got)
	}
	if err := runtime.storeRepositoryWatchProgress("watch", repositoryWatchSnapshot{CheckedAt: first, IssueCursor: repositoryWatchNoIssueCursor}, "", ""); err != nil {
		t.Fatal(err)
	}
	if err := runtime.storeRepositoryWatchProgress("watch", repositoryWatchSnapshot{CheckedAt: first.Add(-time.Minute), IssueCursor: repositoryWatchNoIssueCursor}, "", ""); err != nil {
		t.Fatal(err)
	}
	item := store.Reminders()[0]
	item.LastRunAt = first.Add(3 * time.Hour) // 之后一轮失败，LastRunAt 前移，但上次成功时间不变
	if got := repositoryWatchPreviousCheckAt(item); !got.Equal(first) {
		t.Fatalf("应返回上次成功检查时间：%s", got)
	}
}

// 第一页 100 条全是 PR，issue 在第二页：没有游标时翻页找到最新 issue 建基线，不推送。
func TestRepositoryWatchFindsIssuesBehindManyPullRequests(t *testing.T) {
	now := time.Date(2026, 9, 15, 14, 0, 0, 0, time.UTC)
	server := &issueReplayServer{}
	for i := 0; i < 100; i++ {
		server.items = append(server.items, issueReplayItem(500+i, now.Add(-time.Duration(i)*time.Minute), true))
	}
	server.items = append(server.items, issueReplayItem(325, time.Date(2026, 9, 1, 8, 0, 44, 0, time.UTC), false))
	plugin := issueReplayPlugin(t, server, now)
	found, next, err := plugin.fetchIssues(context.Background(), "acme/demo", "", time.Time{}, repositoryWatchSelection{Issues: true}, SettingValues{})
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 0 || next != "2026-09-01T08:00:44Z#325" {
		t.Fatalf("found=%#v next=%q", found, next)
	}
	if len(server.queries) != 2 {
		t.Fatalf("应翻到第二页找到 issue：%#v", server.queries)
	}
}

// 翻满上限仍全是 PR：不能断定没有 issue，记下已扫描的最新时间当水位，不退回 __none__。
// 之后带着这个水位检查时会传 since。
func TestRepositoryWatchPullRequestOnlyScanSetsTimeWatermark(t *testing.T) {
	now := time.Date(2026, 9, 15, 14, 0, 0, 0, time.UTC)
	server := &issueReplayServer{}
	for i := 0; i < repositoryWatchIssueScanPages*100+50; i++ {
		server.items = append(server.items, issueReplayItem(10000-i, now.Add(-time.Duration(i)*time.Minute), true))
	}
	plugin := issueReplayPlugin(t, server, now)
	found, next, err := plugin.fetchIssues(context.Background(), "acme/demo", "", time.Time{}, repositoryWatchSelection{Issues: true}, SettingValues{})
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 0 || next != repositoryWatchPullCursor(now, 1) {
		t.Fatalf("found=%#v next=%q", found, next)
	}
	if len(server.queries) != repositoryWatchIssueScanPages {
		t.Fatalf("翻页次数=%d", len(server.queries))
	}

	server.mu.Lock()
	server.items = append([]map[string]any{issueReplayItem(7, now.Add(time.Minute), false)}, server.items[:3]...)
	server.queries = nil
	server.mu.Unlock()
	found, next, err = plugin.fetchIssues(context.Background(), "acme/demo", next, time.Time{}, repositoryWatchSelection{Issues: true, IssueEvents: nil}, SettingValues{repositoryWatchSettingLimit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 1 || found[0].Number != 7 {
		t.Fatalf("水位之后的新 issue 应被通知：%#v", found)
	}
	if server.queries[0]["since"] != now.Format(time.RFC3339) {
		t.Fatalf("带水位检查时应传 since：%#v", server.queries)
	}
}
