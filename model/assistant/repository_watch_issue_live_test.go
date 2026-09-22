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

// 真实 GitHub 回放，默认跳过：DIANA_LIVE_GITHUB=1，DIANA_LIVE_GITHUB_TOKEN 可选（不给时只测 REST）。
// 对 PR 远多于 issue 的真实仓库检查：GraphQL 与 REST 两条路径、__none__ 与空游标都不能把旧 issue
// 当新动态推出；发布工具的查重扫描用 GraphQL 能读全 issue；PR 按分支服务端过滤能正常返回。
func TestLiveRepositoryWatchDoesNotReplayIssuesOnPullRequestHeavyRepository(t *testing.T) {
	if os.Getenv("DIANA_LIVE_GITHUB") != "1" {
		t.Skip("set DIANA_LIVE_GITHUB=1 to read a real repository")
	}
	repository := strings.TrimSpace(os.Getenv("DIANA_LIVE_GITHUB_WATCH_REPO"))
	if repository == "" {
		repository = "SuInk/Diana"
	}
	token := strings.TrimSpace(os.Getenv("DIANA_LIVE_GITHUB_TOKEN"))
	client := &http.Client{Timeout: 60 * time.Second}
	plugin := newTestRepositoryWatchPlugin(client, "https://api.github.com")
	paths := map[string]SettingValues{"rest": {repositoryWatchSettingLimit: 5}}
	if token != "" {
		paths["graphql"] = SettingValues{repositoryWatchSettingToken: token, repositoryWatchSettingLimit: 5}
		// 没 Token 时 REST 匿名额度只有 60 次/小时，带上 Token 但强制走 REST 不现实；
		// REST 路径仍用匿名请求验证。
	}
	// 假设订阅一小时检查一次：__none__ 游标只能推上次检查之后更新的 issue。
	previousCheck := time.Now().Add(-time.Hour)
	for name, settings := range paths {
		for _, cursor := range []string{repositoryWatchNoIssueCursor, ""} {
			found, next, err := plugin.fetchIssues(context.Background(), repository, cursor, previousCheck, repositoryWatchSelection{Issues: true}, settings)
			if err != nil {
				t.Fatalf("%s cursor=%q: %v", name, cursor, err)
			}
			t.Logf("%s cursor=%q → 推送 %d 条，新游标 %q", name, cursor, len(found), next)
			for _, item := range found {
				if !item.UpdatedAt.After(previousCheck.Add(-repositoryWatchCheckClockSkew)) {
					t.Errorf("%s 推送了旧 issue #%d", name, item.Number)
				}
			}
			if next == repositoryWatchNoIssueCursor || next == "" {
				t.Fatalf("%s：有 issue 的仓库不该得出 %q", name, next)
			}
		}
	}
	pulls, next, _, err := plugin.fetchPullRequests(context.Background(), repository, "main", "", time.Time{}, repositoryWatchSelection{PullRequests: true}, paths["rest"])
	if err != nil {
		t.Fatalf("pull requests: %v", err)
	}
	t.Logf("PR base=main 建基线：%d 条推送，游标 %q", len(pulls), next)
	if token == "" {
		return
	}
	tool := newDianaGitHubTool(NewRuntime(BotConfig{OwnerID: "owner"}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil),
		MessageEvent{Kind: EventKindPrivate, UserID: "owner"}, newRepositoryPublishPlugin(client, "https://api.github.com"),
		SettingValues{repositoryPublishSettingToken: token, repositoryPublishSettingAllowlist: repository, repositoryPublishSettingTimeout: 30})
	issues, apiErr := tool.listRecentIssues(context.Background(), repository)
	if apiErr != nil {
		t.Fatalf("发布工具查重扫描：%v", apiErr)
	}
	t.Logf("发布工具 GraphQL 列出 %d 个 issue（不含 PR）", len(issues))
	if len(issues) == 0 {
		t.Fatal("发布工具没有列出任何 issue")
	}
}
