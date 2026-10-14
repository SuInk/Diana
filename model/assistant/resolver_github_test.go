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

func TestFormatGitHubRepo(t *testing.T) {
	var repo githubRepoInfo
	payload := `{"full_name":"acme/widget","description":"A small widget toolkit","language":"Go","stargazers_count":1200,"forks_count":34,"open_issues_count":5,"archived":true,"pushed_at":"2026-09-30T08:00:00Z","topics":["cli","toolkit"],"license":{"spdx_id":"MIT","name":"MIT License"}}`
	if err := json.Unmarshal([]byte(payload), &repo); err != nil {
		t.Fatal(err)
	}
	got := formatGitHubRepo(repo)
	for _, want := range []string{"[GitHub] acme/widget（已归档）", "A small widget toolkit", "★ 1200", "Fork 34", "Go", "MIT", "话题：cli、toolkit", "最近推送：2026-09-30"} {
		if !strings.Contains(got, want) {
			t.Errorf("repo text missing %q:\n%s", want, got)
		}
	}
}

func TestFormatGitHubIssueAndPull(t *testing.T) {
	var issue githubIssueInfo
	if err := json.Unmarshal([]byte(`{"title":"Crash on empty input","state":"closed","state_reason":"not_planned","body":"steps:\n\n1. run it\n2. boom","comments":3,"user":{"login":"alice"},"labels":[{"name":"bug"},{"name":"help wanted"}],"created_at":"2026-08-01T00:00:00Z"}`), &issue); err != nil {
		t.Fatal(err)
	}
	target := githubTarget{Kind: githubTargetIssue, Owner: "acme", Repo: "widget", Number: 9}
	got := formatGitHubIssue(target, issue)
	for _, want := range []string{"[GitHub Issue] acme/widget#9 Crash on empty input", "已关闭（不计划处理）", "作者 alice", "3 条评论", "标签：bug、help wanted", "steps: 1. run it 2. boom"} {
		if !strings.Contains(got, want) {
			t.Errorf("issue text missing %q:\n%s", want, got)
		}
	}

	var pull githubPullInfo
	if err := json.Unmarshal([]byte(`{"title":"Fix crash","state":"closed","merged":true,"comments":1,"user":{"login":"bob"},"additions":10,"deletions":2,"changed_files":3,"head":{"ref":"fix-crash"},"base":{"ref":"main"},"created_at":"2026-09-01T00:00:00Z"}`), &pull); err != nil {
		t.Fatal(err)
	}
	got = formatGitHubPull(githubTarget{Kind: githubTargetPull, Owner: "acme", Repo: "widget", Number: 10}, pull)
	for _, want := range []string{"[GitHub PR] acme/widget#10 Fix crash", "已合并", "作者 bob", "fix-crash → main · 3 个文件 +10 −2"} {
		if !strings.Contains(got, want) {
			t.Errorf("pull text missing %q:\n%s", want, got)
		}
	}
}

func TestGitHubBodyIsTruncated(t *testing.T) {
	issue := githubIssueInfo{Title: "t", State: "open", Body: strings.Repeat("长", githubBodyMaxRunes*2)}
	got := formatGitHubIssue(githubTarget{Kind: githubTargetIssue, Owner: "a", Repo: "b", Number: 1}, issue)
	if n := len([]rune(got)); n > githubBodyMaxRunes+200 {
		t.Fatalf("text not truncated: %d runes", n)
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
