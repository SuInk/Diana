package updater

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// initTestRepo 建一个带两个提交的临时 git 仓库，返回仓库路径与两个提交号。
func initTestRepo(t *testing.T) (string, string, string) {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) string {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=t@t",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v (%s)", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	run("init", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-m", "first")
	first := run("rev-parse", "--short", "HEAD")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("v2"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-m", "second")
	second := run("rev-parse", "--short", "HEAD")
	return dir, first, second
}

// TestParseDescribeAndVersionLabel 验证对应功能场景。
func TestParseDescribeAndVersionLabel(t *testing.T) {
	cases := []struct {
		describe string
		tag      string
		count    int
		ok       bool
	}{
		{"v0.1.0-12-gc8e8432", "v0.1.0", 12, true},
		{"v0.1.0-0-gc8e8432", "v0.1.0", 0, true},
		{"release-2026.07-3-gabc1234", "release-2026.07", 3, true},
		{"gc8e8432", "", 0, false},
	}
	for _, tc := range cases {
		tag, count, ok := parseDescribe(tc.describe)
		if tag != tc.tag || count != tc.count || ok != tc.ok {
			t.Fatalf("parseDescribe(%q) = %q,%d,%v", tc.describe, tag, count, ok)
		}
	}
	if got := (Status{NearestTag: "v0.1.0", CommitsSinceTag: 12}).VersionLabel(); got != "v0.1.0+12" {
		t.Fatalf("VersionLabel = %q", got)
	}
	if got := (Status{NearestTag: "v0.1.0"}).VersionLabel(); got != "v0.1.0" {
		t.Fatalf("VersionLabel exact = %q", got)
	}
	if got := (Status{HeadCommit: "abc1234"}).VersionLabel(); got != "abc1234" {
		t.Fatalf("VersionLabel no-tag = %q", got)
	}
}

// TestStatusReportsNearestTag 验证对应功能场景。
func TestStatusReportsNearestTag(t *testing.T) {
	dir, _, _ := initTestRepo(t)
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v (%s)", args, err, out)
		}
	}
	run("tag", "v9.9.0", "HEAD~1")
	u, _ := NewGitUpdater(dir)
	status, err := u.Status(context.Background())
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if status.NearestTag != "v9.9.0" || status.CommitsSinceTag != 1 {
		t.Fatalf("tag info = %q +%d", status.NearestTag, status.CommitsSinceTag)
	}
	if status.VersionLabel() != "v9.9.0+1" {
		t.Fatalf("label = %q", status.VersionLabel())
	}
}

// TestRollbackMovesHeadBack 验证对应功能场景。
func TestRollbackMovesHeadBack(t *testing.T) {
	dir, first, second := initTestRepo(t)
	u, err := NewGitUpdater(dir)
	if err != nil {
		t.Fatalf("NewGitUpdater() error = %v", err)
	}
	result, err := u.Rollback(context.Background(), first)
	if err != nil {
		t.Fatalf("Rollback() error = %v", err)
	}
	if result.Status.HeadCommit != first {
		t.Fatalf("head = %s, want %s", result.Status.HeadCommit, first)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "a.txt"))
	if string(data) != "v1" {
		t.Fatalf("file content = %q after rollback", data)
	}
	_ = second
}

// TestRollbackRefusesDirtyTreeAndBadRef 验证对应功能场景。
func TestRollbackRefusesDirtyTreeAndBadRef(t *testing.T) {
	dir, first, _ := initTestRepo(t)
	u, _ := NewGitUpdater(dir)

	// 不存在的目标。
	if _, err := u.Rollback(context.Background(), "deadbeef123"); err == nil {
		t.Fatal("missing ref accepted")
	}
	// 非法字符（参数注入形态）。
	if _, err := u.Rollback(context.Background(), "--hard"); err == nil {
		t.Fatal("flag-like ref accepted")
	}
	// 脏工作区拒绝。
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("dirty"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := u.Rollback(context.Background(), first); err == nil {
		t.Fatal("dirty tree rollback accepted")
	}
}
