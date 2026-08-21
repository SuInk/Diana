// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"strings"
	"testing"
	"time"
)

// 「PR 有更新」本身看不出改了什么，得点进去才知道；通知里要直接列出这轮新推的提交。
func TestRenderRepositoryWatchPullCommits(t *testing.T) {
	at := time.Date(2026, 8, 21, 9, 3, 6, 0, time.UTC)
	pullRequest := repositoryWatchPullRequest{
		Number: 134, Title: "修图", Status: "updated", UpdatedAt: at, OccurredAt: at,
		Commits: []repositoryWatchPullCommit{
			{SHA: "4620b0c9f1a2b3c4d5e6f7", Title: "跟评提示词改默认沉默", CommittedAt: at},
			{SHA: "eb2e6f8", Title: "不再抽掉引用原图", CommittedAt: at},
		},
	}
	rendered := renderRepositoryWatchChanges(repositoryWatchChange{PullRequests: []repositoryWatchPullRequest{pullRequest}})
	// 长 SHA 截断到 7 位，标题跟在后面。
	if !strings.Contains(rendered, "4620b0c 跟评提示词改默认沉默") {
		t.Fatalf("commit line missing or not shortened: %s", rendered)
	}
	if !strings.Contains(rendered, "eb2e6f8 不再抽掉引用原图") {
		t.Fatalf("second commit missing: %s", rendered)
	}

	// 超出上限时注明还剩多少。
	pullRequest.OmittedCommits = 4
	if rendered := renderRepositoryWatchChanges(repositoryWatchChange{PullRequests: []repositoryWatchPullRequest{pullRequest}}); !strings.Contains(rendered, "还有 4 个提交未列出") {
		t.Fatalf("omitted notice missing: %s", rendered)
	}

	// 没有新增提交时整行消失，不能留下空行或光秃秃的标签。
	pullRequest.Commits = nil
	pullRequest.OmittedCommits = 0
	rendered = renderRepositoryWatchChanges(repositoryWatchChange{PullRequests: []repositoryWatchPullRequest{pullRequest}})
	if strings.Contains(rendered, "\n\n") {
		t.Fatalf("empty commit line left a blank line: %q", rendered)
	}
	if strings.Contains(rendered, "还有") {
		t.Fatalf("unexpected omitted notice: %s", rendered)
	}
}

// 水位线时间要能从游标里解析出来；解析不出时返回零值，调用方据此不做时间过滤。
func TestRepositoryWatchPullCursorTime(t *testing.T) {
	at := time.Date(2026, 8, 21, 9, 3, 6, 0, time.UTC)
	cursor := repositoryWatchPullCursor(at, 134)
	if got := repositoryWatchPullCursorTime(cursor); !got.Equal(at) {
		t.Fatalf("cursor time = %v, want %v", got, at)
	}
	for _, bad := range []string{"", repositoryWatchNoPullCursor, "0#134", "garbage", "#134"} {
		if got := repositoryWatchPullCursorTime(bad); !got.IsZero() {
			t.Fatalf("cursor %q should yield zero time, got %v", bad, got)
		}
	}
}
