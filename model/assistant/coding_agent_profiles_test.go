// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCodingProfilesSelectAndIsolateCredentials(t *testing.T) {
	t.Setenv("DIANA_TEST_CODEX_KEY", "codex-test-key")
	profiles := []codingAgentProfile{
		{ID: "code", Backend: codingBackendCodex, Model: "code-model", APIKeyEnv: "DIANA_TEST_CODEX_KEY", ApprovalMode: codingApprovalModeOff, Default: true},
		{ID: "review", Backend: codingBackendClaude, Model: "review-model", ApprovalMode: codingApprovalModeDangerous},
	}
	settings := codingSettings(map[string]any{codingAgentSettingProfiles: profiles, codingAgentSettingAPIKey: "legacy-test-key", codingAgentSettingWorkspaces: "demo=" + t.TempDir(), codingAgentSettingConcurrency: 3})
	cfg, err := codingAgentConfigFor(settings, "")
	if err != nil || cfg.Agent != "code" || cfg.Backend != codingBackendCodex || cfg.APIKey != "codex-test-key" || cfg.Concurrency != 3 || len(cfg.Workspaces) != 1 {
		t.Fatalf("default selection failed: %v", err)
	}
	cfg, err = codingAgentConfigFor(settings, "REVIEW")
	if err != nil || cfg.Model != "review-model" || cfg.APIKey != "" || cfg.Agent != "review" {
		t.Fatal("named profile inherited legacy credentials or wrong model")
	}
	cfg, err = codingAgentConfigFor(settings, "default")
	if err != nil || cfg.APIKey != "legacy-test-key" || cfg.Backend != codingBackendClaude {
		t.Fatal("legacy settings no longer work")
	}
	if _, err = codingAgentConfigFor(settings, "missing"); err == nil {
		t.Fatal("unknown agent silently fell back")
	}
	profiles[0].APIKeyEnv = "DIANA_MISSING_TEST_KEY"
	t.Setenv("DIANA_MISSING_TEST_KEY", "")
	if _, err = codingAgentConfigFor(settings, "code"); err == nil {
		t.Fatal("missing credential silently fell back")
	}
	// Listing configurations must work without credentials and must not expose them.
	tool := &dianaCodingTool{settings: settings}
	output, err := tool.agents()
	if err != nil || !strings.Contains(output, `"name":"code"`) || strings.Contains(output, "test-key") || strings.Contains(output, "DIANA_") {
		t.Fatalf("unsafe agent listing: %v", err)
	}
}

func TestCodingProfilesSettingsRoundTripAndValidation(t *testing.T) {
	valid := `[ {"id":"code","backend":"codex","approval_mode":"off","default":true}, {"id":"review","backend":"claude","approval_mode":"dangerous"} ]`
	var raw any
	if err := json.Unmarshal([]byte(valid), &raw); err != nil {
		t.Fatal(err)
	}
	specs := NewCodingAgentPlugin().Manifest().Settings
	normalized, err := normalizePluginSettings(specs, map[string]any{codingAgentSettingProfiles: raw})
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(normalized)
	if err != nil {
		t.Fatal(err)
	}
	var persisted map[string]any
	if err := json.Unmarshal(data, &persisted); err != nil {
		t.Fatal(err)
	}
	got := sanitizePluginSettings(specs, persisted)
	profiles, err := codingProfiles(SettingValues(got))
	if err != nil || len(profiles) != 2 || !profiles[0].Default {
		t.Fatalf("profiles lost after save/load: %v", err)
	}
	for _, invalid := range []string{
		`null`, `{}`, `[{}]`,
		`[{"id":"default","backend":"claude","approval_mode":"off"}]`,
		`[{"id":"a","backend":"claude","approval_mode":"off"},{"id":"A","backend":"claude","approval_mode":"off"}]`,
		`[{"id":"a","backend":"claude","approval_mode":"off","default":true},{"id":"b","backend":"claude","approval_mode":"off","default":true}]`,
		`[{"id":"a","backend":"codex","approval_mode":"dangerous"}]`,
		`[{"id":"a","backend":"claude","approval_mode":"off","api_key":"secret"}]`,
		`[{"id":"a","backend":"claude","approval_mode":"off","api_key_env":"bad-name"}]`,
		`[{"id":"a","backend":"custom","approval_mode":"off"}]`,
	} {
		if err := json.Unmarshal([]byte(invalid), &raw); err != nil {
			t.Fatal(err)
		}
		if _, err := normalizePluginSettings(specs, map[string]any{codingAgentSettingProfiles: raw}); err == nil {
			t.Fatalf("accepted invalid profiles: %s", invalid)
		}
	}
	if profiles, err := codingProfiles(SettingValues{}); err != nil || len(profiles) != 0 {
		t.Fatal("old settings require migration")
	}
}

func TestCodingProfilesSubmitAndFollowupStayOnOriginalAgent(t *testing.T) {
	useTempCodingWorkspace(t)
	cli := fakeCodingCLI(t, `echo '{"type":"result","subtype":"success","result":"agent-a","session_id":"session-a"}'`)
	other := fakeCodingCLI(t, `echo '{"type":"result","subtype":"success","result":"agent-b","session_id":"session-b"}'`)
	rt, settings, event := codingTestRuntime(t, other, t.TempDir())
	profiles := []codingAgentProfile{
		{ID: "a", Backend: codingBackendClaude, Command: cli, ApprovalMode: codingApprovalModeOff},
		{ID: "b", Backend: codingBackendClaude, Command: other, ApprovalMode: codingApprovalModeOff, Default: true},
	}
	settings[codingAgentSettingProfiles] = profiles
	tool := newDianaCodingTool(rt, event, settings)
	output, err := tool.Run(context.Background(), map[string]any{"operation": "submit", "agent": "a", "instruction": "test"})
	if err != nil {
		t.Fatal(err)
	}
	var result dianaCodingResult
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatal(err)
	}
	firstID := result.Job.ID
	wait := func(id string) CodingJob {
		t.Helper()
		waitForCondition(t, 10*time.Second, func() bool {
			job, err := loadCodingJob(id)
			registry := rt.codingJobs()
			registry.mu.Lock()
			running := len(registry.running)
			registry.mu.Unlock()
			return err == nil && job.finished() && running == 0
		})
		job, err := loadCodingJob(id)
		if err != nil {
			t.Fatal(err)
		}
		return job
	}
	saved := wait(firstID)
	if saved.Agent != "a" || saved.AgentFingerprint == "" || saved.Result != "agent-a" {
		t.Fatalf("wrong agent launched: %#v", saved)
	}
	record, err := os.ReadFile(codingJobRecordPath(firstID))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(record), `"agent": "a"`) {
		t.Fatal("agent not persisted")
	}
	// b is the default, but followup must still run a.
	output, err = tool.Run(context.Background(), map[string]any{"operation": "followup", "job_id": firstID, "instruction": "continue"})
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatal(err)
	}
	continued := wait(result.Job.ID)
	if continued.Agent != "a" || continued.Result != "agent-a" || continued.ResumedFrom != "session-a" {
		t.Fatalf("followup switched agent: %#v", continued)
	}
	if _, err := tool.Run(context.Background(), map[string]any{"operation": "followup", "job_id": firstID, "agent": "b", "instruction": "continue"}); err == nil {
		t.Fatal("explicit cross-agent resume allowed")
	}
	if _, err := tool.Run(context.Background(), map[string]any{"operation": "followup", "job_id": firstID, "workspace": "other", "instruction": "continue"}); err == nil {
		t.Fatal("cross-workspace resume allowed")
	}
	originalWorkspaces := settings[codingAgentSettingWorkspaces]
	settings[codingAgentSettingWorkspaces] = "demo=" + t.TempDir()
	if _, err := tool.Run(context.Background(), map[string]any{"operation": "followup", "job_id": firstID, "instruction": "continue"}); err == nil {
		t.Fatal("changed workspace reused old session")
	}
	settings[codingAgentSettingWorkspaces] = originalWorkspaces
	profiles[0].Model = "different-model"
	if _, err := tool.Run(context.Background(), map[string]any{"operation": "followup", "job_id": firstID, "instruction": "continue"}); err == nil {
		t.Fatal("changed configuration reused old session")
	}
	delete(settings, codingAgentSettingProfiles)
	if _, err := tool.Run(context.Background(), map[string]any{"operation": "followup", "job_id": firstID, "instruction": "continue"}); err == nil {
		t.Fatal("deleted agent fell back to default")
	}
	// Status still works after the original configuration is removed.
	if _, err := tool.Run(context.Background(), map[string]any{"operation": "status", "job_id": firstID}); err != nil {
		t.Fatal(err)
	}
}

func TestCodingProfileSnapshotsDoNotShareMutableProfiles(t *testing.T) {
	manager := NewPluginManager(NewCodingAgentPlugin())
	_, err := manager.UpdateSettings(codingAgentPluginID, map[string]any{codingAgentSettingProfiles: []codingAgentProfile{{ID: "code", Backend: codingBackendCodex, ApprovalMode: codingApprovalModeOff}}})
	if err != nil {
		t.Fatal(err)
	}
	state := manager.Snapshot()[codingAgentPluginID]
	profiles := state.Settings[codingAgentSettingProfiles].([]codingAgentProfile)
	profiles[0].ID = "mutated"
	fresh, _ := manager.Get(codingAgentPluginID)
	if fresh.Settings[codingAgentSettingProfiles].([]codingAgentProfile)[0].ID != "code" {
		t.Fatal("snapshot mutation changed stored configuration")
	}
}

func TestCodingCodexJSONKeepsFinalAnswerDespiteStartupNoise(t *testing.T) {
	path := filepath.Join(t.TempDir(), "codex.log")
	log := strings.Repeat("startup warning\n", 30) + `{"type":"thread.started","thread_id":"thread-1"}
{"type":"item.completed","item":{"type":"agent_message","text":"Reading the file."}}
{"type":"item.started","item":{"type":"command_execution","command":"cat probe.txt"}}
{"type":"item.completed","item":{"type":"command_execution","command":"cat probe.txt","aggregated_output":"TOKEN","exit_code":0}}
{"type":"item.completed","item":{"type":"agent_message","text":"TOKEN"}}
{"type":"turn.completed","usage":{"input_tokens":100,"output_tokens":10}}
`
	if err := os.WriteFile(path, []byte(log), 0600); err != nil {
		t.Fatal(err)
	}
	got := parseCodingLog(path)
	if got.Result != "TOKEN" || !got.Done || got.IsError || got.SessionID != "thread-1" {
		t.Fatalf("bad parsed result: %#v", got)
	}
	failed := codingJobSnapshot{}
	applyCodingLogLine(&failed, `{"type":"turn.failed","error":{"message":"authentication failed"}}`)
	if !failed.Done || !failed.IsError || failed.Result != "authentication failed" {
		t.Fatalf("bad failure: %#v", failed)
	}
}
