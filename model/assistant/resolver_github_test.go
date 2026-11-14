// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParseGitHubURL(t *testing.T) {
	cases := []struct {
		raw  string
		want githubTarget
		ok   bool
	}{
		{"https://github.com/acme/widget", githubTarget{Kind: githubTargetRepo, Owner: "acme", Repo: "widget"}, true},
		{"https://github.com/acme/widget.git", githubTarget{Kind: githubTargetRepo, Owner: "acme", Repo: "widget"}, true},
		{"https://github.com/acme/widget/tree/main/docs", githubTarget{Kind: githubTargetRepo, Owner: "acme", Repo: "widget"}, true},
		{"https://github.com/acme/widget/issues/12", githubTarget{Kind: githubTargetIssue, Owner: "acme", Repo: "widget", Number: 12}, true},
		{"https://github.com/acme/widget/pull/34/files", githubTarget{Kind: githubTargetPull, Owner: "acme", Repo: "widget", Number: 34}, true},
		{"https://github.com/acme/widget/issues", githubTarget{Kind: githubTargetRepo, Owner: "acme", Repo: "widget"}, true},
		{"https://github.com/acme", githubTarget{}, false},
		{"https://github.com/settings/profile", githubTarget{}, false},
		{"https://github.com/topics/golang", githubTarget{}, false},
		{"https://example.com/acme/widget", githubTarget{}, false},
	}
	for _, tc := range cases {
		got, ok := parseGitHubURL(tc.raw)
		if ok != tc.ok || got != tc.want {
			t.Errorf("parseGitHubURL(%q) = %+v, %v; want %+v, %v", tc.raw, got, ok, tc.want, tc.ok)
		}
	}
}

func TestGitHubResourceKey(t *testing.T) {
	repo, _ := parseGitHubURL("https://github.com/Acme/Widget")
	issue, _ := parseGitHubURL("https://github.com/acme/widget/issues/7")
	pull, _ := parseGitHubURL("https://github.com/acme/widget/pull/7")
	if repo.resourceKey() != "github:acme/widget" || issue.resourceKey() != "github:acme/widget#issue-7" || pull.resourceKey() != "github:acme/widget#pull-7" {
		t.Fatalf("keys = %q %q %q", repo.resourceKey(), issue.resourceKey(), pull.resourceKey())
	}
	if got := resolverResourceKeyFromURL("github", "https://github.com/acme/widget/pull/7"); got != pull.resourceKey() {
		t.Fatalf("resolverResourceKeyFromURL = %q", got)
	}
}

// 排版照搬 ErisPulse-GitHubParser 的文字模板。
func TestFormatGitHubRepo(t *testing.T) {
	var repo githubRepoInfo
	payload := `{"full_name":"acme/widget","html_url":"https://github.com/acme/widget","homepage":"https://widget.example","description":"A small widget toolkit","language":"Go","stargazers_count":105200,"forks_count":1234,"subscribers_count":56,"open_issues_count":5,"archived":true,"pushed_at":"2026-09-30T08:00:00Z","topics":["cli","toolkit"],"license":{"spdx_id":"MIT","name":"MIT License"}}`
	if err := json.Unmarshal([]byte(payload), &repo); err != nil {
		t.Fatal(err)
	}
	want := "acme/widget\n" +
		"A small widget toolkit\n" +
		"★ Stars 10.5w · ⑂ Forks 1.2k · Watchers 56 · Issues 5\n" +
		"语言: Go | 许可证: MIT | 主页: https://widget.example | 最近推送: 2026-09-30\n" +
		"cli, toolkit\n" +
		"https://github.com/acme/widget"
	if got := formatGitHubRepo(repo); got != want {
		t.Fatalf("repo text =\n%s\nwant\n%s", got, want)
	}

	empty := formatGitHubRepo(githubRepoInfo{FullName: "a/b"})
	if empty != "a/b\n暂无描述\n★ Stars 0 · ⑂ Forks 0 · Watchers 0 · Issues 0\n语言: 未指定 | 许可证: 无 | 最近推送: 未知" {
		t.Fatalf("empty repo text = %q", empty)
	}
}

func TestFormatGitHubIssueAndPull(t *testing.T) {
	var issue githubIssueInfo
	if err := json.Unmarshal([]byte(`{"html_url":"https://github.com/acme/widget/issues/9","title":"Crash on empty input","state":"closed","state_reason":"not_planned","body":"steps","comments":3,"user":{"login":"alice"},"labels":[{"name":"bug"},{"name":"help wanted"}],"created_at":"2026-08-01T00:00:00Z"}`), &issue); err != nil {
		t.Fatal(err)
	}
	comments := []githubComment{
		{User: githubUser{Login: "bob"}, Body: "能复现\n\n附日志"},
		{User: githubUser{Login: "carol"}, Body: strings.Repeat("长", 200)},
	}
	target := githubTarget{Kind: githubTargetIssue, Owner: "acme", Repo: "widget", Number: 9}
	want := "acme/widget #9 [已关闭] Crash on empty input\n" +
		"作者: alice · 评论: 3\n" +
		"bug, help wanted\n" +
		"  bob: 能复现 附日志\n" +
		"  carol: " + strings.Repeat("长", 119) + "…\n" +
		"https://github.com/acme/widget/issues/9"
	if got := formatGitHubIssue(target, issue, comments); got != want {
		t.Fatalf("issue text =\n%s\nwant\n%s", got, want)
	}
	if got := formatGitHubIssue(target, issue, nil); strings.Contains(got, "steps") {
		t.Fatalf("issue body should stay out of the chat text: %q", got)
	}

	var pull githubPullInfo
	if err := json.Unmarshal([]byte(`{"html_url":"https://github.com/acme/widget/pull/10","title":"Fix crash","state":"closed","merged":true,"commits":2,"comments":1,"user":{"login":"bob"},"additions":10,"deletions":2,"changed_files":3,"head":{"ref":"fix-crash"},"base":{"ref":"main"},"created_at":"2026-09-01T00:00:00Z"}`), &pull); err != nil {
		t.Fatal(err)
	}
	want = "acme/widget #10 [已合并] Fix crash\n" +
		"作者: bob · 评论: 1 · 提交: 2 · +10/-2 · 3 文件\n" +
		"https://github.com/acme/widget/pull/10"
	if got := formatGitHubPull(githubTarget{Kind: githubTargetPull, Owner: "acme", Repo: "widget", Number: 10}, pull, nil); got != want {
		t.Fatalf("pull text =\n%s\nwant\n%s", got, want)
	}
}

func TestGitHubCountAndTruncate(t *testing.T) {
	for value, want := range map[int]string{999: "999", 1000: "1.0k", 12345: "1.2w", 105200: "10.5w"} {
		if got := githubCount(value); got != want {
			t.Errorf("githubCount(%d) = %q, want %q", value, got, want)
		}
	}
	if got := githubTruncate("一二三四五", 5); got != "一二三四五" {
		t.Errorf("short text truncated: %q", got)
	}
	if got := githubTruncate("一二三四五六", 5); got != "一二三四…" {
		t.Errorf("long text = %q", got)
	}
}

func TestLooksLikeShellPage(t *testing.T) {
	for _, title := range []string{"小黑盒 - 玩家高能聚集地", "Sina Visitor System", "  sina visitor system "} {
		if !looksLikeShellPage(title) {
			t.Errorf("%q should be a shell page", title)
		}
	}
	for _, title := range []string{"", "某个帖子标题 - 小黑盒", "Sina Visitor System 说明"} {
		if looksLikeShellPage(title) {
			t.Errorf("%q should not be a shell page", title)
		}
	}
}

func TestResolverPlatformsIncludeHeyboxAndGitHub(t *testing.T) {
	if key, label := platformKeyAndLabel("www.xiaoheihe.cn"); key != "heybox" || label != "小黑盒" {
		t.Fatalf("heybox = %q %q", key, label)
	}
	if !platformSupportsMedia("heybox") || !platformSupportsMedia("weibo") || !platformParsesNatively("github") || platformSupportsMedia("github") {
		t.Fatal("heybox/weibo should download media and github should be text-only")
	}
	labels := map[string]string{}
	for _, option := range resolverPlatformOptions() {
		labels[option.Value] = option.Label
	}
	if labels["github"] != "GitHub（解析正文）" || labels["heybox"] != "小黑盒（可下载媒体）" {
		t.Fatalf("labels = %#v", labels)
	}
}
