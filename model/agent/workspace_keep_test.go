// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newKeepTestRegistry(t *testing.T, botID, actor string) (*ToolRegistry, string) {
	t.Helper()
	workDir := t.TempDir()
	registry, err := NewDefaultToolRegistry(Config{WorkDir: workDir, FileWriteEnabled: true, WorkspaceBotID: botID, WorkspaceActorID: actor}.WithDefaults())
	if err != nil {
		t.Fatal(err)
	}
	tool, _ := registry.Get(ManageFilesToolName)
	tool.(*ManageFilesTool).now = func() time.Time { return time.Date(2026, 9, 26, 15, 30, 12, 0, time.Local) }
	root, _ := filepath.Abs(workDir)
	return registry, root
}

func keepIndexByPath(t *testing.T, root, botID string) map[string]KeepEntry {
	t.Helper()
	entries, err := LoadKeepIndex(root, botID)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]KeepEntry{}
	for _, entry := range entries {
		out[entry.Path] = entry
	}
	return out
}

// 挪进 keep/ 的东西落到本机器人的长期区，索引记下说明和是谁让存的；在长期区里改名
// 说明跟着走；删除进回收站，索引同步去掉。
func TestManageFilesKeepMoveRenameDeleteKeepsIndex(t *testing.T) {
	registry, root := newKeepTestRegistry(t, "bot-a", "owner-1")
	tool, _ := registry.Get(ManageFilesToolName)
	manage := tool.(*ManageFilesTool)
	writeManageTestFile(t, root, "downloads/poster.png", testJPEG(t, 10, 10))

	result := runManageFiles(t, manage, map[string]any{"action": "move", "path": "downloads/poster.png", "to": "keep/poster.png", "description": "群活动海报 9 月版"})
	if result["to"] != "keep/bot-a/poster.png" {
		t.Fatalf("keep/ 没落到本机器人的长期区: %v", result)
	}
	index := keepIndexByPath(t, root, "bot-a")
	entry, ok := index["keep/bot-a/poster.png"]
	if !ok || entry.Description != "群活动海报 9 月版" || entry.SavedBy != "owner-1" || entry.MIME != "image/jpeg" || entry.Size == 0 {
		t.Fatalf("索引没记对: %+v", index)
	}

	runManageFiles(t, manage, map[string]any{"action": "move", "path": "keep/bot-a/poster.png", "to": "keep/bot-a/events/poster-sep.png"})
	index = keepIndexByPath(t, root, "bot-a")
	if _, stale := index["keep/bot-a/poster.png"]; stale {
		t.Fatalf("改名后旧路径还在索引里: %+v", index)
	}
	if renamed := index["keep/bot-a/events/poster-sep.png"]; renamed.Description != "群活动海报 9 月版" || renamed.SavedBy != "owner-1" {
		t.Fatalf("改名丢了说明: %+v", index)
	}

	deleted := runManageFiles(t, manage, map[string]any{"action": "delete", "path": "keep/bot-a/events/poster-sep.png"})
	if !strings.HasPrefix(deleted["trash_path"].(string), WorkspaceTrashDir+"/") {
		t.Fatalf("删除没进回收站: %v", deleted)
	}
	if index := keepIndexByPath(t, root, "bot-a"); len(index) != 0 {
		t.Fatalf("删除后索引没清: %+v", index)
	}

	// 挪出长期区同样把条目去掉。
	writeManageTestFile(t, root, "outputs/a.txt", []byte("hello"))
	runManageFiles(t, manage, map[string]any{"action": "move", "path": "outputs/a.txt", "to": "keep/", "description": "说明"})
	runManageFiles(t, manage, map[string]any{"action": "move", "path": "keep/bot-a/a.txt", "to": "outputs/a.txt"})
	if index := keepIndexByPath(t, root, "bot-a"); len(index) != 0 {
		t.Fatalf("挪出长期区后索引没清: %+v", index)
	}
}

// 别的机器人的长期区只能读：不许挪走、删掉或改写，往那里放东西会落回自己名下。
func TestManageFilesKeepRefusesOtherBotsArea(t *testing.T) {
	registry, root := newKeepTestRegistry(t, "bot-a", "owner-1")
	tool, _ := registry.Get(ManageFilesToolName)
	manage := tool.(*ManageFilesTool)
	writeManageTestFile(t, root, "keep/bot-b/secret.txt", []byte("b"))
	writeManageTestFile(t, root, "downloads/x.txt", []byte("x"))
	for _, input := range []map[string]any{
		{"action": "delete", "path": "keep/bot-b/secret.txt"},
		{"action": "move", "path": "keep/bot-b/secret.txt", "to": "downloads/"},
		{"action": "delete", "path": "keep"},
		{"action": "delete", "path": "keep/bot-a"},
	} {
		if out, err := manage.Run(context.Background(), input); err == nil {
			t.Fatalf("%v 应该被拒: %s", input, out)
		}
	}
	edit, _ := registry.Get("edit_file")
	if _, err := edit.Run(context.Background(), map[string]any{"path": "keep/bot-b/secret.txt", "edits": []any{map[string]any{"old_text": "b", "new_text": "c"}}}); err == nil {
		t.Fatal("改写了别的机器人的长期文件")
	}
	result := runManageFiles(t, manage, map[string]any{"action": "move", "path": "downloads/x.txt", "to": "keep/bot-b/x.txt"})
	if result["to"] != "keep/bot-a/bot-b/x.txt" {
		t.Fatalf("写进别人长期区的目标没收回自己名下: %v", result)
	}
	// 读照样可以。
	if _, err := manage.Run(context.Background(), map[string]any{"action": "stat", "path": "keep/bot-b/secret.txt"}); err != nil {
		t.Fatalf("别的机器人的长期文件读不了: %v", err)
	}
}

// 没有机器人 ID 的工具表（命令行、测试）用不了长期区，说清楚原因。
func TestKeepRequiresBotID(t *testing.T) {
	registry, root := newKeepTestRegistry(t, "", "")
	tool, _ := registry.Get(ManageFilesToolName)
	writeManageTestFile(t, root, "downloads/x.txt", []byte("x"))
	_, err := tool.Run(context.Background(), map[string]any{"action": "move", "path": "downloads/x.txt", "to": "keep/x.txt"})
	if err == nil || !strings.Contains(err.Error(), "长期保存区") {
		t.Fatalf("err = %v", err)
	}
}

// 超出配额直接拒绝并说清楚，不悄悄挤掉旧文件。
func TestKeepQuotaRefusesInsteadOfEvicting(t *testing.T) {
	registry, root := newKeepTestRegistry(t, "bot-a", "owner-1")
	big := filepath.Join(root, "keep", "bot-a", "big.bin")
	if err := os.MkdirAll(filepath.Dir(big), 0o755); err != nil {
		t.Fatal(err)
	}
	file, err := os.Create(big)
	if err != nil {
		t.Fatal(err)
	}
	// 稀疏文件：按大小算配额，不真占盘。
	if err := file.Truncate(KeepQuotaBytes - 2); err != nil {
		t.Fatal(err)
	}
	file.Close()
	writeManageTestFile(t, root, "downloads/x.txt", []byte("hello"))

	tool, _ := registry.Get(ManageFilesToolName)
	_, err = tool.Run(context.Background(), map[string]any{"action": "move", "path": "downloads/x.txt", "to": "keep/x.txt"})
	if err == nil || !strings.Contains(err.Error(), "上限") {
		t.Fatalf("超配额没拒: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "downloads", "x.txt")); err != nil {
		t.Fatalf("被拒的文件不该挪走: %v", err)
	}
	if _, err := os.Stat(big); err != nil {
		t.Fatalf("旧文件被挤掉了: %v", err)
	}
	write, _ := registry.Get("write_file")
	if _, err := write.Run(context.Background(), map[string]any{"path": "keep/note.md", "content": "hello"}); err == nil || !strings.Contains(err.Error(), "上限") {
		t.Fatalf("write_file 超配额没拒: %v", err)
	}
	_, err = WriteWorkspaceBytes(Config{WorkDir: root}, "keep/pic.bin", []byte("abc"), WorkspaceWriteOptions{Keep: &KeepMeta{BotID: "bot-a"}})
	if err == nil || !strings.Contains(err.Error(), "上限") {
		t.Fatalf("WriteWorkspaceBytes 超配额没拒: %v", err)
	}
	// 在长期区里改名不多占地方，不受配额影响。
	runManageFiles(t, tool.(*ManageFilesTool), map[string]any{"action": "move", "path": "keep/bot-a/big.bin", "to": "keep/bot-a/archive/big.bin"})
}

// 二进制文件经 WriteWorkspaceBytes 存进长期区时记下来源；没带长期区信息就不许写进 keep/。
func TestWriteWorkspaceBytesRecordsKeepIndex(t *testing.T) {
	root := t.TempDir()
	cfg := Config{WorkDir: root}
	if _, err := WriteWorkspaceBytes(cfg, "keep/x.jpg", testJPEG(t, 4, 4), WorkspaceWriteOptions{}); err == nil {
		t.Fatal("没带长期区信息也写进了 keep/")
	}
	now := time.Date(2026, 9, 1, 8, 0, 0, 0, time.UTC)
	saved, err := WriteWorkspaceBytes(cfg, "keep/cat.jpg", testJPEG(t, 4, 4), WorkspaceWriteOptions{Keep: &KeepMeta{
		BotID: "bot-a", Description: "猫", SourceMessageID: "m1", SavedBy: "u1", Now: now,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if saved != "keep/bot-a/cat.jpg" {
		t.Fatalf("saved = %s", saved)
	}
	entries, err := LoadKeepIndex(root, "bot-a")
	if err != nil || len(entries) != 1 {
		t.Fatalf("entries = %+v %v", entries, err)
	}
	entry := entries[0]
	if entry.Description != "猫" || entry.SourceMessageID != "m1" || entry.SavedBy != "u1" || !entry.SavedAt.Equal(now) || entry.MIME != "image/jpeg" {
		t.Fatalf("entry = %+v", entry)
	}
	// 索引在 .diana/ 里，文件工具碰不到。
	if !WorkspaceFileProtected(cfg, ".diana/keep-index/bot-a.json") {
		t.Fatal("长期区索引没有被保护")
	}
}

func TestKeepBotDirIsPathSafe(t *testing.T) {
	for id, want := range map[string]string{"bot-a": "bot-a", "Bot_1.x": "Bot_1.x"} {
		if got := KeepBotDir(id); got != want {
			t.Fatalf("KeepBotDir(%q) = %q", id, got)
		}
	}
	for _, id := range []string{"../etc", "a/b", "..", "机器人", "a b"} {
		got := KeepBotDir(id)
		if got == "" || strings.ContainsAny(got, `/\ `) || got == "." || got == ".." || strings.HasPrefix(got, ".") {
			t.Fatalf("KeepBotDir(%q) = %q 不安全", id, got)
		}
	}
	if KeepBotDir("a/b") == KeepBotDir("a_b") {
		t.Fatal("替换过字符的 ID 和原样的 ID 撞成了同一个目录")
	}
}
