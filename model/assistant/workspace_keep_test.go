// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SuInk/diana/model/agent"
)

// 主人说「存下来」时传 keep=true：落进本机器人的长期区，索引记下说明、来源和是谁存的。
func TestSaveToWorkspaceKeepRecordsIndex(t *testing.T) {
	t.Setenv("APP_DB_PATH", filepath.Join(t.TempDir(), "diana.db"))
	data := workspaceTestJPEG(t, 12, 9)
	id, err := agent.StoreMCPMedia(agent.MCPMedia{Kind: agent.MCPMediaImage, MIMEType: "image/jpeg", Data: data})
	if err != nil {
		t.Fatal(err)
	}
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	tool := newDianaSaveToWorkspaceTool(runtime, MessageEvent{Kind: EventKindPrivate, UserID: "owner", ProfileID: "bot-a"})
	tool.now = func() time.Time { return time.Date(2026, 9, 26, 15, 30, 12, 0, time.UTC) }
	saved := runSaveToWorkspace(t, tool, map[string]any{"source": "mcp", "media_id": id, "keep": true, "description": "头像草稿", "path": "avatars/"})
	if saved.Path != "keep/bot-a/avatars/image-20260926-153012.jpg" || saved.Area == "" {
		t.Fatalf("keep=true 没存进长期区: %+v", saved)
	}
	entries, err := agent.LoadKeepIndex(AgentWorkspaceDir(), "bot-a")
	if err != nil || len(entries) != 1 {
		t.Fatalf("索引 = %+v %v", entries, err)
	}
	if entries[0].Description != "头像草稿" || entries[0].SavedBy != "owner" || entries[0].Path != saved.Path {
		t.Fatalf("索引条目 = %+v", entries[0])
	}
	// 不传 keep 照旧进会自动清理的目录。
	plain := runSaveToWorkspace(t, tool, map[string]any{"source": "mcp", "media_id": id})
	if !strings.HasPrefix(plain.Path, agent.WorkspaceOutputsDir+"/") || plain.Area != "" {
		t.Fatalf("默认保存跑进了长期区: %+v", plain)
	}
	// path 写在 keep/ 下也按长期区处理。
	explicit := runSaveToWorkspace(t, tool, map[string]any{"source": "mcp", "media_id": id, "path": "keep/logo.jpg", "description": "logo"})
	if explicit.Path != "keep/bot-a/logo.jpg" {
		t.Fatalf("keep/ 路径没落到本机器人名下: %+v", explicit)
	}
}

func writeKeepIndexForPrompt(t *testing.T, count int) {
	t.Helper()
	root := AgentWorkspaceDir()
	base := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	for i := range count {
		name := fmt.Sprintf("keep/bot-a/file-%02d.txt", i)
		if _, err := agent.WriteWorkspaceBytes(agent.Config{WorkDir: root}, name, []byte("x"), agent.WorkspaceWriteOptions{Keep: &agent.KeepMeta{
			BotID: "bot-a", Description: fmt.Sprintf("第 %d 份", i), Now: base.Add(time.Duration(i) * time.Hour),
		}}); err != nil {
			t.Fatal(err)
		}
	}
}

// 清单按保存时间从新到旧最多列 20 条，超出的只报个数；索引不变时逐字不变，
// 不让尾部前面的缓存作废。已经不在磁盘上的条目不列。
func TestKeepIndexPromptIsBoundedAndStable(t *testing.T) {
	t.Setenv("APP_DB_PATH", filepath.Join(t.TempDir(), "diana.db"))
	cfg := BotConfig{ID: "bot-a"}
	if prompt := keepIndexPrompt(cfg, "bot-a"); prompt != "" {
		t.Fatalf("空长期区也注入了: %q", prompt)
	}
	writeKeepIndexForPrompt(t, 23)
	first := keepIndexPrompt(cfg, "bot-a")
	if first != keepIndexPrompt(cfg, "bot-a") {
		t.Fatal("同一份索引两次渲染不一样")
	}
	if !strings.Contains(first, "keep/bot-a/file-22.txt  第 22 份") || strings.Contains(first, "file-02.txt") {
		t.Fatalf("没按新到旧取前 20 条: %s", first)
	}
	if !strings.Contains(first, "现有 23 个文件") || !strings.Contains(first, "另有 3 个较早的") {
		t.Fatalf("没报总数和剩余数: %s", first)
	}
	if err := os.Remove(filepath.Join(AgentWorkspaceDir(), "keep", "bot-a", "file-22.txt")); err != nil {
		t.Fatal(err)
	}
	if prompt := keepIndexPrompt(cfg, "bot-a"); strings.Contains(prompt, "file-22.txt") || !strings.Contains(prompt, "现有 22 个文件") {
		t.Fatalf("磁盘上没了的条目还在清单里: %s", prompt)
	}
	// 别的机器人看不到这份清单。
	if prompt := keepIndexPrompt(BotConfig{ID: "bot-b"}, "bot-b"); prompt != "" {
		t.Fatalf("bot-b 看到了 bot-a 的清单: %s", prompt)
	}
}

// 清单只进主人的尾部，不进头部，普通成员拿不到。
func TestKeepIndexPromptOnlyInOwnerTail(t *testing.T) {
	t.Setenv("APP_DB_PATH", filepath.Join(t.TempDir(), "diana.db"))
	writeKeepIndexForPrompt(t, 2)
	runtime := NewRuntime(BotConfig{ID: "bot-a"}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	event := MessageEvent{Kind: EventKindPrivate, UserID: "owner", ProfileID: "bot-a"}
	head, tail := runtime.systemPromptPartsWithRelationshipAndAgentTools(event, nil, false, RelationshipPolicy{Owner: true}, true, nil)
	if strings.Contains(head, "keep/bot-a/") || !strings.Contains(tail, "keep/bot-a/file-01.txt  第 1 份") {
		t.Fatalf("清单位置不对:\nhead=%q\ntail=%q", head, tail)
	}
	_, memberTail := runtime.systemPromptPartsWithRelationshipAndAgentTools(event, nil, false, RelationshipPolicy{}, true, nil)
	if strings.Contains(memberTail, "keep/bot-a/") {
		t.Fatalf("普通成员看到了长期区清单: %s", memberTail)
	}
}

func TestSweepDianaTempDirsRemovesOnlyOldOwnedEntries(t *testing.T) {
	temp := t.TempDir()
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	old := now.Add(-48 * time.Hour)
	mk := func(name string, modified time.Time, dir bool) string {
		path := filepath.Join(temp, name)
		if dir {
			if err := os.MkdirAll(path, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(path, "frame.png"), []byte("12345"), 0o600); err != nil {
				t.Fatal(err)
			}
		} else if err := os.WriteFile(path, []byte("12345"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, modified, modified); err != nil {
			t.Fatal(err)
		}
		return path
	}
	oldImage := mk("diana-agent-image-123", old, true)
	oldPDF := mk("diana-pdf-9.pdf", old, false)
	fresh := mk("diana-agent-image-456", now.Add(-time.Hour), true)
	foreign := mk("other-app-123", old, true)
	unknown := mk("diana-something-else", old, true)
	result, err := sweepDianaTempDirs(temp, now)
	if err != nil {
		t.Fatal(err)
	}
	if result.Deleted != 2 || result.Bytes != 10 {
		t.Fatalf("result = %+v", result)
	}
	for _, path := range []string{oldImage, oldPDF} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("没清掉 %s", path)
		}
	}
	for _, path := range []string{fresh, foreign, unknown} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("误删了 %s", path)
		}
	}
}

// workspaceStateTestPath 返回工作目录里 .diana/ 下的一份运行时状态文件路径，目录先建好。
func workspaceStateTestPath(t *testing.T, workDir, name string) string {
	t.Helper()
	dir := agent.DianaStateDir(workDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dir, name)
}
