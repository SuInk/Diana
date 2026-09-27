// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestLocalRealACPAgentSmoke 用真实的 ACP 代理跑一轮，默认跳过。它会调用代理配置的
// 模型服务、消耗额度，只在开发机上显式运行：
//
//	DIANA_RUN_REAL_ACP_SMOKE=gemini DIANA_REAL_ACP_ARGS=--acp go test ./model/assistant -run '^TestLocalRealACPAgentSmoke$' -v -count=1 -timeout 5m
//
// DIANA_RUN_REAL_ACP_SMOKE 是代理命令（可以是绝对路径），DIANA_REAL_ACP_ARGS 是它的
// 参数，空格分隔。用例在临时 Git 工作区放一个内容固定的文件，让代理读出来回答，
// 不改文件、不向真实聊天发消息。
func TestLocalRealACPAgentSmoke(t *testing.T) {
	command := strings.TrimSpace(os.Getenv("DIANA_RUN_REAL_ACP_SMOKE"))
	if command == "" {
		t.Skip("设置 DIANA_RUN_REAL_ACP_SMOKE=<代理命令> 才运行真实 ACP 冒烟")
	}
	dir := t.TempDir()
	if err := exec.Command("git", "init", "-q", dir).Run(); err != nil {
		t.Fatalf("git init: %v", err)
	}
	const token = "diana-acp-smoke-7f3a"
	if err := os.WriteFile(filepath.Join(dir, "smoke.txt"), []byte(token+"\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	var buffer bytes.Buffer
	code := runCodingACPSession(ctx, codingACPSpec{
		JobID:       "code-acp-smoke",
		Command:     command,
		Args:        strings.Fields(os.Getenv("DIANA_REAL_ACP_ARGS")),
		Dir:         dir,
		Instruction: "读取当前目录下 smoke.txt 的内容，只回答文件里的那一行原文。不要修改、创建或删除任何文件。",
	}, &codingACPLog{w: &buffer})
	logPath := filepath.Join(t.TempDir(), "job.log")
	if err := os.WriteFile(logPath, buffer.Bytes(), 0o600); err != nil {
		t.Fatalf("write log: %v", err)
	}
	snapshot := parseCodingLog(logPath)
	t.Logf("exit=%d session=%q\n进度：\n%s\n结果：%s", code, snapshot.SessionID, strings.Join(snapshot.Tail, "\n"), snapshot.Result)
	if code != 0 || !snapshot.Done || snapshot.IsError {
		t.Fatalf("没有正常完成：exit=%d done=%v error=%v\n%s", code, snapshot.Done, snapshot.IsError, buffer.String())
	}
	if !strings.Contains(snapshot.Result, token) {
		t.Fatalf("结论里没有文件内容：%q", snapshot.Result)
	}
}
