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

func TestCodingSetupSecretMergeRedactionAndClear(t *testing.T) {
	useTempCodingWorkspace(t)
	manager := NewPluginManager(NewCodingAgentPlugin())
	profiles := []codingAgentProfile{{ID: "one", Backend: codingBackendCodex, ApprovalMode: codingApprovalModeOff}, {ID: "two", Backend: codingBackendClaude, ApprovalMode: codingApprovalModeOff}}
	state, err := manager.UpdateSettings(codingAgentPluginID, map[string]any{codingAgentSettingProfiles: profiles, codingAgentSettingKeys: `{"one":"secret-one","two":"secret-two"}`})
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(state.Redacted())
	if strings.Contains(string(data), "secret-one") || strings.Contains(string(data), "secret-two") {
		t.Fatal("secret returned")
	}
	_, err = manager.UpdateSettings(codingAgentPluginID, map[string]any{codingAgentSettingProfiles: profiles, codingAgentSettingKeys: `{"one":"rotated"}`})
	if err != nil {
		t.Fatal(err)
	}
	_, settings, _ := manager.PluginForConfiguration(codingAgentPluginID, "")
	if codingSavedKey(settings, "one") != "rotated" || codingSavedKey(settings, "two") != "secret-two" {
		t.Fatal("credential merge lost another profile")
	}
	cfg, err := codingAgentConfigFor(settings, "one")
	if err != nil || cfg.APIKey != "rotated" {
		t.Fatal("saved key not selected")
	}
	if !strings.Contains(strings.Join(codingJobEnv(cfg), "\n"), "CODEX_API_KEY=rotated") {
		t.Fatal("Codex exec key missing")
	}
	_, err = manager.UpdateSettings(codingAgentPluginID, map[string]any{codingAgentSettingProfiles: profiles, codingAgentSettingKeys: `{"one":null}`})
	if err != nil {
		t.Fatal(err)
	}
	_, settings, _ = manager.PluginForConfiguration(codingAgentPluginID, "")
	if codingSavedKey(settings, "one") != "" || codingSavedKey(settings, "two") == "" {
		t.Fatal("credential clear affected another profile")
	}
}

func TestCodingSetupUsesSavedConfigAndDoesNotLeakCLIError(t *testing.T) {
	useTempCodingWorkspace(t)
	cli := fakeCodingCLI(t, `echo '{"type":"item.completed","item":{"type":"agent_message","text":"DIANA_CONNECTION_OK"}}'
echo '{"type":"turn.completed"}'`)
	settings := codingSettings(map[string]any{codingAgentSettingProfiles: []codingAgentProfile{{ID: "test", Backend: codingBackendCodex, Command: cli, ApprovalMode: codingApprovalModeOff}}})
	status, err := CodingAgentSetup(context.Background(), settings, "test", "status")
	if err != nil || !status.Installed {
		t.Fatalf("status %v %v", status, err)
	}
	status, err = CodingAgentSetup(context.Background(), settings, "test", "test")
	if err != nil || !strings.Contains(status.Message, "连接成功") {
		t.Fatalf("test %v %v", status, err)
	}
	bad := fakeCodingCLI(t, `echo 'secret-do-not-return' >&2; exit 1`)
	settings[codingAgentSettingProfiles] = []codingAgentProfile{{ID: "test", Backend: codingBackendCodex, Command: bad, ApprovalMode: codingApprovalModeOff}}
	_, err = CodingAgentSetup(context.Background(), settings, "test", "test")
	if err == nil || strings.Contains(err.Error(), "secret-do-not-return") {
		t.Fatal("unsafe test error")
	}
}

func TestCodingManagedWorkspaceAndCommandPersist(t *testing.T) {
	useTempCodingWorkspace(t)
	settings := codingSettings(map[string]any{codingAgentSettingProfiles: []codingAgentProfile{{ID: "test", Backend: codingBackendCodex, ManagedWorkspace: true, ApprovalMode: codingApprovalModeOff}}})
	cfg, err := codingAgentConfigFor(settings, "test")
	if err != nil {
		t.Fatal(err)
	}
	if err := prepareCodingRuntime(cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(cfg.Workspaces[0].Dir, ".git")); err != nil {
		t.Fatal("managed git workspace not initialized")
	}
	command := codingManagedCommand(codingBackendCodex)
	if err := os.MkdirAll(filepath.Dir(command), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(command, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	cfg, err = codingAgentConfigFor(settings, "test")
	if err != nil || cfg.Command != command {
		t.Fatal("managed binary not rediscovered")
	}
}

func TestCodingLoginExtractsOnlyPublicDeviceFields(t *testing.T) {
	state := &codingDeviceLogin{}
	_, _ = state.Write([]byte("ignored secret-token\nhttps://auth.openai.com/codex/device\nABCD-EFGHI\n"))
	result := codingLoginStatus(state)
	data, _ := json.Marshal(result)
	if result.DeviceCode != "ABCD-EFGHI" || result.LoginURL != "https://auth.openai.com/codex/device" || strings.Contains(string(data), "secret-token") {
		t.Fatal("invalid device login view")
	}
	state.done = true
	state.failed = true
	result = codingLoginStatus(state)
	if result.LoginState != "failed" || result.DeviceCode != "" {
		t.Fatal("failed login retains usable code")
	}
}

// Explicit opt-in: run this in the Docker verification image as its non-root
// runtime user. Installs the official packages using the same code as WebUI.
func TestCodingManagedInstallInContainer(t *testing.T) {
	if os.Getenv("DIANA_TEST_MANAGED_INSTALL") != "1" {
		t.Skip("manual Docker integration")
	}
	useTempCodingWorkspace(t)
	for _, backend := range []string{codingBackendCodex, codingBackendClaude} {
		settings := codingSettings(map[string]any{codingAgentSettingProfiles: []codingAgentProfile{{ID: backend, Backend: backend, ApprovalMode: codingApprovalModeOff}}})
		status, err := CodingAgentSetup(context.Background(), settings, backend, "install")
		if err != nil || !status.Installed {
			t.Fatalf("%s: %v", backend, err)
		}
		cfg, err := codingAgentConfigFor(settings, backend)
		if err != nil {
			t.Fatal(err)
		}
		output, err := exec.Command(cfg.Command, "--version").CombinedOutput()
		if err != nil {
			t.Fatalf("%s version: %v %s", backend, err, output)
		}
		t.Logf("installed %s: %s", backend, strings.TrimSpace(string(output)))
	}
}

func TestCodingDeviceLoginProcessLifecycle(t *testing.T) {
	useTempCodingWorkspace(t)
	cli := fakeCodingCLI(t, `echo 'https://auth.openai.com/codex/device'; echo 'ABCD-EFGHI'; sleep 1; exit 0`)
	settings := codingSettings(map[string]any{codingAgentSettingProfiles: []codingAgentProfile{{ID: "login", Backend: codingBackendCodex, Command: cli, ApprovalMode: codingApprovalModeOff}}})
	result, err := CodingAgentDeviceLogin(settings, "login", "login-start")
	if err != nil || result.LoginState != "pending" {
		t.Fatalf("start: %v %v", result, err)
	}
	waitForCondition(t, 5*time.Second, func() bool {
		result, err = CodingAgentDeviceLogin(settings, "login", "login-status")
		return err == nil && result.DeviceCode == "ABCD-EFGHI"
	})
	waitForCondition(t, 5*time.Second, func() bool {
		result, err = CodingAgentDeviceLogin(settings, "login", "login-status")
		return err == nil && result.LoginState == "success"
	})
	if result.LoginURL != "" || result.DeviceCode != "" {
		t.Fatal("completed login retained device code")
	}
}
