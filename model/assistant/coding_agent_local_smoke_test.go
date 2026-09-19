// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLocalRealCodexProfilesSmoke(t *testing.T) {
	if os.Getenv("DIANA_RUN_REAL_CODEX_SMOKE") != "1" {
		t.Skip("manual local integration")
	}
	runLocalRealCodingProfileSmoke(t, codingBackendCodex, "DIANA_REAL_CODEX_OK_7319")
}

func TestLocalRealClaudeProfilesSmoke(t *testing.T) {
	if os.Getenv("DIANA_RUN_REAL_CLAUDE_SMOKE") != "1" {
		t.Skip("manual local integration")
	}
	runLocalRealCodingProfileSmoke(t, codingBackendClaude, "DIANA_REAL_CLAUDE_OK_8426")
}

func runLocalRealCodingProfileSmoke(t *testing.T, backend, marker string) {
	t.Helper()
	useTempCodingWorkspace(t)
	cli, err := exec.LookPath(backend)
	if err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	if out, err := exec.Command("git", "init", workspace).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	if err := os.WriteFile(filepath.Join(workspace, "probe.txt"), []byte(marker+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	rt, settings, event := codingTestRuntime(t, cli, workspace)
	// Choose a real named profile while the default profile points at a missing
	// executable. This verifies routing, not just invoking codex from the shell.
	settings[codingAgentSettingCommand] = filepath.Join(workspace, "missing-default-cli")
	settings[codingAgentSettingProfiles] = []codingAgentProfile{{ID: "local-" + backend, Backend: backend, Command: cli, ApprovalMode: codingApprovalModeOff}}
	tool := newDianaCodingTool(rt, event, settings)
	payload, err := tool.Run(context.Background(), map[string]any{"operation": "submit", "agent": "local-" + backend, "instruction": "Read only the file probe.txt in the current workspace and reply with its exact content. Do not modify any files, use network tools, or inspect other directories. Do not delegate. This is a minimal CLI integration test."})
	if err != nil {
		t.Fatal(err)
	}
	var result dianaCodingResult
	if err := json.Unmarshal([]byte(payload), &result); err != nil {
		t.Fatal(err)
	}
	id := result.Job.ID
	t.Logf("started real CLI: agent=%s job=%s", result.Job.Agent, id)
	defer func() {
		job, _ := loadCodingJob(id)
		if !job.finished() {
			_, _ = rt.cancelCodingJob(context.Background(), id)
		}
	}()
	deadline := time.Now().Add(150 * time.Second)
	var job CodingJob
	for time.Now().Before(deadline) {
		job, err = loadCodingJob(id)
		if err != nil {
			t.Fatal(err)
		}
		if job.finished() {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if !job.finished() {
		t.Fatal("real CLI did not finish within 150 seconds")
	}
	t.Logf("completed: status=%s exit=%d agent=%s result=%s", job.Status, job.ExitCode, job.Agent, job.Result)
	if job.Status != codingJobStatusSucceeded {
		t.Fatalf("real CLI failed: %s; log tail: %s", job.Error, strings.Join(codingLogTail(job, 8), "\n"))
	}
	if job.Agent != "local-"+backend || strings.TrimSpace(job.Result) != marker {
		t.Fatalf("wrong agent or missing output")
	}
	content, err := os.ReadFile(filepath.Join(workspace, "probe.txt"))
	if err != nil || string(content) != marker+"\n" {
		t.Fatal("probe changed")
	}
	status, err := tool.Run(context.Background(), map[string]any{"operation": "status", "job_id": id})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(status, marker) {
		t.Fatal("status does not expose result")
	}
}

func TestLocalRealCodexSetupConnection(t *testing.T) {
	if os.Getenv("DIANA_RUN_REAL_CODEX_SMOKE") != "1" {
		t.Skip("manual local integration")
	}
	useTempCodingWorkspace(t)
	cli, err := exec.LookPath("codex")
	if err != nil {
		t.Fatal(err)
	}
	settings := codingSettings(map[string]any{codingAgentSettingProfiles: []codingAgentProfile{{ID: "setup", Backend: codingBackendCodex, Command: cli, ApprovalMode: codingApprovalModeOff}}})
	result, err := CodingAgentSetup(context.Background(), settings, "setup", "test")
	if err != nil {
		t.Fatal(err)
	}
	t.Log(result.Message)
}
