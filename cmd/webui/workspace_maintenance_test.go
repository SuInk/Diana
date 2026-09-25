// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SuInk/diana/model/applog"
	"github.com/SuInk/diana/model/assistant"
)

// TestMain 把 Agent 工作目录和系统临时目录都指到这次测试自己的目录。
//
// 存储维护一启动就清工作目录、扫系统临时目录里 Diana 的残留。工作目录没设
// APP_DB_PATH 时落在用户缓存目录（本机真实实例在用的那个），临时目录是整台机器
// 共用的：不兜住的话，跑一遍测试就会删掉开发机上别的 Diana 留下的东西。
func TestMain(m *testing.M) {
	root, err := os.MkdirTemp("", "diana-cmd-webui-test-*")
	if err != nil {
		panic(err)
	}
	temp := filepath.Join(root, "tmp")
	if err := os.MkdirAll(temp, 0o700); err != nil {
		panic(err)
	}
	_ = os.Setenv("APP_DB_PATH", filepath.Join(root, "data", "diana.db"))
	_ = os.Setenv("TMPDIR", temp)
	code := m.Run()
	_ = os.RemoveAll(root)
	os.Exit(code)
}

type recordingAppLog struct {
	entries []applog.Entry
}

func (r *recordingAppLog) AppendLog(_ context.Context, entry applog.Entry) error {
	r.entries = append(r.entries, entry)
	return nil
}

// 维护任务清掉过期的下载、扫掉临时目录残留，并把结果记进应用日志；散落在根下的
// 文件只报告不删。
func TestWorkspaceMaintenanceCleansAndLogs(t *testing.T) {
	workspace := assistant.AgentWorkspaceDir()
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.Local)
	old := now.Add(-10 * 24 * time.Hour)
	write := func(path string, modified time.Time) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("data"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, modified, modified); err != nil {
			t.Fatal(err)
		}
	}
	stale := filepath.Join(workspace, "downloads", "stale.png")
	loose := filepath.Join(workspace, "notes.md")
	write(stale, old)
	write(loose, old)
	leftover := filepath.Join(os.TempDir(), "diana-agent-image-leftover")
	write(filepath.Join(leftover, "frame.png"), old)
	if err := os.Chtimes(leftover, old, old); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(workspace) })

	logs := &recordingAppLog{}
	runWorkspaceMaintenance(context.Background(), logs, now, nil)
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("过期下载没清掉: %v", err)
	}
	if _, err := os.Stat(loose); err != nil {
		t.Fatalf("根下散落的文件被删了: %v", err)
	}
	if _, err := os.Stat(leftover); !os.IsNotExist(err) {
		t.Fatalf("临时目录残留没清掉: %v", err)
	}
	if len(logs.entries) != 1 || logs.entries[0].Action != "workspace_cleanup" {
		t.Fatalf("应用日志 = %+v", logs.entries)
	}
	if message := logs.entries[0].Message; !strings.Contains(message, "删除 1 个过期文件") || !strings.Contains(message, "散落 1 个文件") {
		t.Fatalf("日志没说清清理了什么: %s", message)
	}

	// 什么都没删的那几天不往应用日志里写。
	logs.entries = nil
	runWorkspaceMaintenance(context.Background(), logs, now, nil)
	if len(logs.entries) != 0 {
		t.Fatalf("没删东西也记了日志: %+v", logs.entries)
	}
}
