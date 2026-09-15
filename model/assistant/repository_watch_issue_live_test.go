// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// 真实 GitHub 回放，默认跳过：DIANA_LIVE_GITHUB=1，可选 DIANA_LIVE_GITHUB_TOKEN。
// 对 PR 远多于 issue 的真实仓库检查：__none__ 和空游标都不能把旧 issue 当新动态推出。
func TestLiveRepositoryWatchDoesNotReplayIssuesOnPullRequestHeavyRepository(t *testing.T) {
	if os.Getenv("DIANA_LIVE_GITHUB") != "1" {
		t.Skip("set DIANA_LIVE_GITHUB=1 to read a real repository")
	}
	repository := strings.TrimSpace(os.Getenv("DIANA_LIVE_GITHUB_WATCH_REPO"))
	if repository == "" {
		repository = "SuInk/Diana"
	}
	plugin := newRepositoryWatchPlugin(&http.Client{Timeout: 60 * time.Second}, "https://api.github.com")
	settings := SettingValues{repositoryWatchSettingToken: strings.TrimSpace(os.Getenv("DIANA_LIVE_GITHUB_TOKEN")), repositoryWatchSettingLimit: 5}
	for _, cursor := range []string{repositoryWatchNoIssueCursor, ""} {
		found, next, err := plugin.fetchIssues(context.Background(), repository, cursor, repositoryWatchSelection{Issues: true}, settings)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("cursor=%q → 推送 %d 条，新游标 %q", cursor, len(found), next)
		for _, item := range found {
			t.Logf("  #%d %s 更新于 %s", item.Number, item.Title, item.UpdatedAt.Format(time.RFC3339))
			if item.UpdatedAt.Before(time.Now().Add(-repositoryWatchNoneCursorWindow)) {
				t.Errorf("推送了旧 issue #%d", item.Number)
			}
		}
		if next == repositoryWatchNoIssueCursor || next == "" {
			t.Fatalf("有 issue 的仓库不该得出 %q", next)
		}
	}
}
