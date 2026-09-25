// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeAgedFile(t *testing.T, root, rel string, modified time.Time) string {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, modified, modified); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCleanupWorkspaceFollowsAreaPolicy(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.Local)
	ago := func(d time.Duration) time.Time { return now.Add(-d) }
	day := 24 * time.Hour

	deleted := []string{
		writeAgedFile(t, root, "tmp/draft.md", ago(2*day)),
		writeAgedFile(t, root, "downloads/sub/a.png", ago(8*day)),
		writeAgedFile(t, root, ".agent-browser/screenshot-1.png", ago(8*day)),
		writeAgedFile(t, root, "outputs/old.png", ago(31*day)),
		writeAgedFile(t, root, ".trash/"+now.Add(-8*day).Format(trashTimestampLayout)+"/downloads/x.png", now),
	}
	kept := []string{
		writeAgedFile(t, root, "tmp/fresh.md", ago(12*time.Hour)),
		writeAgedFile(t, root, "downloads/b.png", ago(6*day)),
		writeAgedFile(t, root, "outputs/recent.png", ago(20*day)),
		writeAgedFile(t, root, ".trash/"+now.Add(-2*day).Format(trashTimestampLayout)+"/outputs/y.png", ago(30*day)),
		// 永远不碰的地方，再老也不删。
		writeAgedFile(t, root, "keep/bot-a/old.png", ago(400*day)),
		writeAgedFile(t, root, ".diana/extension-overrides.json", ago(400*day)),
		writeAgedFile(t, root, "skills/demo/SKILL.md", ago(400*day)),
		writeAgedFile(t, root, ".agents/skills/x/SKILL.md", ago(400*day)),
		writeAgedFile(t, root, "coding/repo/main.go", ago(400*day)),
		writeAgedFile(t, root, "coding-runtime/auth/token.json", ago(400*day)),
		writeAgedFile(t, root, "loose.txt", ago(400*day)),
		writeAgedFile(t, root, ".extension-audience.json", ago(400*day)),
	}
	if err := os.Chtimes(filepath.Join(root, "coding", "repo"), ago(400*day), ago(400*day)); err != nil {
		t.Fatal(err)
	}

	report, err := CleanupWorkspace(root, WorkspaceCleanupOptions{Now: now, CodingReferenced: func(string) bool { return false }})
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range deleted {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("该清的没清: %s", path)
		}
	}
	for _, path := range kept {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("不该清的被清了: %s", path)
		}
	}
	if report.DeletedFiles != len(deleted) || report.DeletedBytes != int64(4*len(deleted)) {
		t.Fatalf("report = %+v", report)
	}
	// 空出来的子目录收掉，分区目录本身留着。
	if _, err := os.Stat(filepath.Join(root, "downloads", "sub")); !os.IsNotExist(err) {
		t.Fatal("空的子目录没收掉")
	}
	if _, err := os.Stat(filepath.Join(root, "downloads")); err != nil {
		t.Fatal("分区目录被删了")
	}
	if len(report.LooseFiles) != 1 || report.LooseFiles[0].Path != "loose.txt" {
		t.Fatalf("散落文件报告 = %+v", report.LooseFiles)
	}
	if len(report.IdleCoding) != 1 || report.IdleCoding[0].Path != "coding/repo" {
		t.Fatalf("闲置编码工作区报告 = %+v", report.IdleCoding)
	}
	// 被配置引用的编码工作区不报。
	report, _ = CleanupWorkspace(root, WorkspaceCleanupOptions{Now: now, CodingReferenced: func(name string) bool { return name == "repo" }})
	if len(report.IdleCoding) != 0 {
		t.Fatalf("有人用的编码工作区被报了闲置: %+v", report.IdleCoding)
	}
}

// 分区被换成软链接时不跟进去删：指向哪里不归清理任务管。
func TestCleanupWorkspaceDoesNotFollowSymlinkedArea(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	now := time.Now()
	victim := writeAgedFile(t, outside, "important.txt", now.Add(-100*24*time.Hour))
	if err := os.Symlink(outside, filepath.Join(root, "tmp")); err != nil {
		t.Skip("不支持软链接")
	}
	if _, err := CleanupWorkspace(root, WorkspaceCleanupOptions{Now: now}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(victim); err != nil {
		t.Fatal("跟着软链接删到了工作目录外面")
	}
}

func TestEmptyWorkspaceTrash(t *testing.T) {
	root := t.TempDir()
	writeAgedFile(t, root, ".trash/20260901-010203/a.txt", time.Now())
	writeAgedFile(t, root, ".trash/20260902-010203/b/c.txt", time.Now())
	files, bytes, err := EmptyWorkspaceTrash(root)
	if err != nil || files != 2 || bytes != 8 {
		t.Fatalf("files=%d bytes=%d err=%v", files, bytes, err)
	}
	entries, _ := os.ReadDir(filepath.Join(root, WorkspaceTrashDir))
	if len(entries) != 0 {
		t.Fatalf("回收站没清空: %v", entries)
	}
}

// 工作目录页的列表按分区分组，长期区带上索引里的说明，运行时配置和 .diana/ 不出现。
func TestListWorkspaceGroupsAreasAndHidesState(t *testing.T) {
	root := t.TempDir()
	now := time.Now()
	writeAgedFile(t, root, "downloads/a.png", now)
	writeAgedFile(t, root, "notes/x.md", now)
	writeAgedFile(t, root, "loose.txt", now)
	writeAgedFile(t, root, ".diana/extension-overrides.json", now)
	if _, err := WriteWorkspaceBytes(Config{WorkDir: root}, "keep/p.txt", []byte("hello"), WorkspaceWriteOptions{Keep: &KeepMeta{BotID: "bot-a", Description: "说明"}}); err != nil {
		t.Fatal(err)
	}
	listing, err := ListWorkspace(root, WorkspaceCleanupOptions{Now: now}, 0)
	if err != nil {
		t.Fatal(err)
	}
	areas := map[string]WorkspaceArea{}
	for _, area := range listing.Areas {
		areas[area.Key+":"+area.BotID] = area
		for _, entry := range area.Entries {
			if filepath.Base(filepath.Dir(entry.Path)) == DianaStateDirName {
				t.Fatalf("列出了运行时状态: %s", entry.Path)
			}
		}
	}
	keep := areas["keep:bot-a"]
	if len(keep.Entries) != 1 || keep.Entries[0].Description != "说明" || keep.QuotaBytes != KeepQuotaBytes {
		t.Fatalf("keep = %+v", keep)
	}
	if downloads := areas["downloads:"]; downloads.Files != 1 || downloads.Retention != "7 天后自动清理" {
		t.Fatalf("downloads = %+v", downloads)
	}
	if other := areas["other:"]; other.Files != 1 || other.Entries[0].Path != "notes/x.md" {
		t.Fatalf("other = %+v", other)
	}
	if len(listing.Loose) != 1 || listing.Loose[0].Path != "loose.txt" {
		t.Fatalf("loose = %+v", listing.Loose)
	}
}

func TestOpenWorkspaceFileRefusesUnsafePaths(t *testing.T) {
	root := t.TempDir()
	writeAgedFile(t, root, "downloads/a.txt", time.Now())
	writeAgedFile(t, root, ".diana/extension-overrides.json", time.Now())
	writeAgedFile(t, root, ".mcp.json", time.Now())
	cfg := Config{WorkDir: root}
	file, _, clean, err := OpenWorkspaceFile(cfg, "downloads/a.txt")
	if err != nil || clean != "downloads/a.txt" {
		t.Fatalf("普通文件打不开: %v", err)
	}
	file.Close()
	outside := t.TempDir()
	writeAgedFile(t, outside, "secret.txt", time.Now())
	_ = os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(root, "link.txt"))
	for _, rel := range []string{"../etc/passwd", "/etc/passwd", ".diana/extension-overrides.json", ".mcp.json", "link.txt", "downloads", "."} {
		if file, _, _, err := OpenWorkspaceFile(cfg, rel); err == nil {
			file.Close()
			t.Fatalf("%s 被打开了", rel)
		}
	}
}
