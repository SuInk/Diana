// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func repositoryWatchMaskFixture() repositoryWatchChange {
	at := time.Date(2026, 8, 18, 16, 15, 3, 0, time.UTC)
	return repositoryWatchChange{
		Repository: "acme/secret-lab",
		Branch:     "main",
		Commits:    []repositoryWatchCommit{{SHA: "ee5a54bdd5712ab", Title: "bump version", Author: "alice", PushedAt: at, URL: "https://github.com/acme/secret-lab/commit/ee5a54bdd5712ab"}},
		PullRequests: []repositoryWatchPullRequest{{
			Number: 85, Title: "新增群友回复风格", Author: "bob", Status: "merged",
			BaseBranch: "main", HeadBranch: "feature/hello", OccurredAt: at,
			URL: "https://github.com/acme/secret-lab/pull/85",
		}},
		Issues: []repositoryWatchIssue{{
			Number: 128, Title: "修复通知格式", Author: "carol", Status: "opened", CreatedAt: at,
			URL: "https://github.com/acme/secret-lab/issues/128",
		}},
		Releases: []repositoryWatchRelease{{Tag: "v0.8.46", PublishedAt: at, URL: "https://github.com/acme/secret-lab/releases/tag/v0.8.46"}},
		Stars: &repositoryWatchStarChange{
			Previous: 10, Current: 12, Delta: 2,
			AddedUsers: []repositoryWatchStargazer{{Login: "dave", StarredAt: at}, {Login: "eve", StarredAt: at}},
			URL:        "https://github.com/acme/secret-lab",
		},
		Description: "内部实验项目",
	}
}

// 私有仓库的推送：仓库路径、账号名、链接都不出现，标题、编号、分支、时间照常；
// 跟评参考资料开头提醒模型别说出来。
func TestRepositoryWatchMasksPrivateRepositoryPush(t *testing.T) {
	runtime := &Runtime{}
	item := Reminder{Kind: ReminderKindRepositoryWatch, Repository: "acme/secret-lab", RepositoryPrivate: boolPointer(true)}
	message, reference := runtime.renderRepositoryWatchDelivery(item, repositoryWatchMaskFixture(), SettingValues{})

	for _, leaked := range []string{"acme", "secret-lab", "alice", "bob", "carol", "dave", "eve", "github.com", "作者："} {
		if strings.Contains(message, leaked) {
			t.Fatalf("masked push leaks %q:\n%s", leaked, message)
		}
	}
	for _, kept := range []string{"GitHub 动态：私有仓库", "Commit ee5a54b", "bump version", "PR #85（已合并）", "main ← feature/hello", "Issue #128（新建）", "Release v0.8.46", "Star +2（10 → 12）"} {
		if !strings.Contains(message, kept) {
			t.Fatalf("masked push lost %q:\n%s", kept, message)
		}
	}
	if !strings.HasPrefix(reference, "隐私要求：这个仓库在群里称为「私有仓库」") {
		t.Fatalf("reference should start with the privacy note:\n%s", reference)
	}
	if strings.Contains(reference, "github.com") {
		t.Fatalf("masked reference leaks links:\n%s", reference)
	}
}

// 设了显示名就用显示名；公开仓库自动模式不打码；总是打码的公开仓库也打码；不打码的私有仓库照常显示。
func TestRepositoryWatchMaskModes(t *testing.T) {
	runtime := &Runtime{}
	for _, tc := range []struct {
		name       string
		item       Reminder
		wantHeader string
		wantLinks  bool
	}{
		{"private with alias", Reminder{Repository: "acme/secret-lab", RepositoryPrivate: boolPointer(true), RepositoryDisplayName: "实验室项目"}, "GitHub 动态：实验室项目", false},
		{"public auto", Reminder{Repository: "acme/secret-lab", RepositoryPrivate: boolPointer(false)}, "GitHub 动态：acme/secret-lab", true},
		{"unknown visibility counts as private", Reminder{Repository: "acme/secret-lab"}, "GitHub 动态：私有仓库", false},
		{"public always", Reminder{Repository: "acme/secret-lab", RepositoryPrivate: boolPointer(false), RepositoryMask: repositoryWatchMaskAlways}, "GitHub 动态：私有仓库", false},
		{"private never", Reminder{Repository: "acme/secret-lab", RepositoryPrivate: boolPointer(true), RepositoryMask: repositoryWatchMaskNever}, "GitHub 动态：acme/secret-lab", true},
	} {
		message, reference := runtime.renderRepositoryWatchDelivery(tc.item, repositoryWatchMaskFixture(), SettingValues{})
		if !strings.HasPrefix(message, tc.wantHeader) {
			t.Fatalf("%s: header = %q", tc.name, strings.SplitN(message, "\n", 2)[0])
		}
		if got := strings.Contains(message, "https://github.com/acme/secret-lab/pull/85"); got != tc.wantLinks {
			t.Fatalf("%s: links=%v\n%s", tc.name, got, message)
		}
		if got := strings.Contains(message, "@dave"); got != tc.wantLinks {
			t.Fatalf("%s: star logins=%v\n%s", tc.name, got, message)
		}
		if got := strings.HasPrefix(reference, "隐私要求"); got == tc.wantLinks {
			t.Fatalf("%s: privacy note=%v", tc.name, got)
		}
	}
}

// 失败告警里错误原文带出的仓库路径和链接也换成称呼，大小写不同也认。
func TestMaskRepositoryWatchText(t *testing.T) {
	text := "仓库订阅 acme/secret-lab 连续 3 次检查失败：读取 ACME/Secret-Lab 仓库信息: 404 https://github.com/acme/secret-lab/pulls?page=2 Diana 会继续自动重试。"
	got := maskRepositoryWatchText(text, "acme/secret-lab", "私有仓库")
	if strings.Contains(strings.ToLower(got), "secret-lab") || strings.Contains(got, "github.com") {
		t.Fatalf("masked text still leaks: %s", got)
	}
	if !strings.HasPrefix(got, "仓库订阅 私有仓库 连续 3 次") {
		t.Fatalf("masked text = %s", got)
	}
}

func TestNormalizeRepositoryWatchMaskSettings(t *testing.T) {
	for input, want := range map[string]string{"": "", "auto": "", " ALWAYS ": "always", "never": "never"} {
		if got, err := normalizeRepositoryWatchMask(input); err != nil || got != want {
			t.Fatalf("mask %q = %q err=%v", input, got, err)
		}
	}
	if _, err := normalizeRepositoryWatchMask("sometimes"); err == nil {
		t.Fatal("unknown mask mode should be rejected")
	}
	if got, err := normalizeRepositoryWatchDisplayName("  实验室   项目 "); err != nil || got != "实验室 项目" {
		t.Fatalf("display name = %q err=%v", got, err)
	}
	if _, err := normalizeRepositoryWatchDisplayName(strings.Repeat("长", repositoryWatchDisplayNameMaxRunes+1)); err == nil {
		t.Fatal("overlong display name should be rejected")
	}
}

// 端到端：订阅还不知道仓库是否私有，有动态的那一轮查到是私有的，推送打码并把结论存回订阅；
// 之后改成公开，一天内沿用旧结论，不每轮都查。
func TestRuntimeRepositoryWatchLearnsVisibilityAndMasksPush(t *testing.T) {
	github := &repositoryWatchTestGitHub{
		private: true,
		commits: []map[string]any{
			repositoryWatchCommitPayload("new-sha", "fix delivery"),
			repositoryWatchCommitPayload("base-sha", "initial"),
		},
	}
	server := httptest.NewServer(http.HandlerFunc(github.handler))
	defer server.Close()
	plugin := newTestRepositoryWatchPlugin(server.Client(), server.URL)
	store := &stubReminderStore{items: []Reminder{{
		ID: "watch-mask", Kind: ReminderKindRepositoryWatch, OwnerID: "owner", GroupID: "12345",
		Repository: "acme/demo", RepositoryDisplayName: "演示项目", WatchCommits: true, LastCommitSHA: "base-sha",
		TriggerAt: time.Now().Add(-time.Minute), IntervalSeconds: 1800, CreatedAt: time.Now().Add(-time.Hour),
	}}}
	channel := &recordingChannel{}
	runtime := NewRuntime(BotConfig{}, channel, NewPluginManager(plugin), nil, store, nil, nil)
	runtime.fireDueReminders(context.Background())

	channel.mu.Lock()
	sent := append([]OutgoingMessage(nil), channel.sent...)
	channel.mu.Unlock()
	if len(sent) != 1 {
		t.Fatalf("sent = %#v", sent)
	}
	text := sent[0].Text
	if !strings.HasPrefix(text, "GitHub 动态：演示项目") || !strings.Contains(text, "fix delivery") {
		t.Fatalf("push = %s", text)
	}
	for _, leaked := range []string{"acme/demo", "github.com", "diana", "作者："} {
		if strings.Contains(text, leaked) {
			t.Fatalf("push leaks %q: %s", leaked, text)
		}
	}
	item := store.items[0]
	if item.RepositoryPrivate == nil || !*item.RepositoryPrivate || item.RepositoryVisibilityCheckedAt.IsZero() {
		t.Fatalf("visibility not stored: %#v", item)
	}

	// 仓库转为公开：跟评拉仓库简介时顺带读到可见性，下一条推送就不再打码（显示名照用）。
	github.mu.Lock()
	github.private = false
	github.commits = append([]map[string]any{repositoryWatchCommitPayload("newer-sha", "second fix")}, github.commits...)
	github.mu.Unlock()
	store.items[0].TriggerAt = time.Now().Add(-time.Second)
	runtime.fireDueReminders(context.Background())
	channel.mu.Lock()
	sent = append([]OutgoingMessage(nil), channel.sent...)
	channel.mu.Unlock()
	if len(sent) != 2 || !strings.Contains(sent[1].Text, "https://github.com/acme/demo/commit/newer-s") || !strings.HasPrefix(sent[1].Text, "GitHub 动态：演示项目") {
		t.Fatalf("public push = %#v", sent)
	}
	if store.items[0].RepositoryPrivate == nil || *store.items[0].RepositoryPrivate {
		t.Fatalf("visibility not refreshed: %#v", store.items[0].RepositoryPrivate)
	}
}
