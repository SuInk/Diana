package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func environmentPayload(t *testing.T, tool *ExtensionsListTool, input map[string]any) map[string]any {
	t.Helper()
	body, err := tool.Run(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		t.Fatal(err)
	}
	return payload
}

func TestExecutionEnvironmentPermissionsAndRequestIsolation(t *testing.T) {
	root := t.TempDir()
	runner := &RunCommandTool{root: root, allowlist: map[string]bool{"node": true}, sandboxMode: CommandSandboxOff}
	ownerRegistry := NewToolRegistry(runner, &WriteFileTool{root: root})
	owner := NewExtensionsListTool(nil, false)
	owner.SetExecutionRegistry(ownerRegistry)
	guestRegistry := NewToolRegistry(runner, &WriteFileTool{root: root})
	guest := NewExtensionsListTool(nil, false)
	guest.SetExecutionRegistry(guestRegistry)
	guestRegistry.Retain(map[string]bool{"list_capabilities": true})
	if _, exists := environmentPayload(t, owner, map[string]any{"execution": false})["execution"]; exists {
		t.Fatal("execution:false must omit diagnostics")
	}
	ownerExecution := environmentPayload(t, owner, map[string]any{"execution": true})["execution"].(map[string]any)
	if ownerExecution["run_command"] != true || ownerExecution["write_file"] != true || ownerExecution["extract_archive"] != false {
		t.Fatal(ownerExecution)
	}
	guestExecution := environmentPayload(t, guest, map[string]any{"execution": true})["execution"].(map[string]any)
	if guestExecution["run_command"] != false || guestExecution["write_file"] != false {
		t.Fatal(guestExecution)
	}
	body, _ := json.Marshal(guestExecution)
	if strings.Contains(string(body), root) || strings.Contains(string(body), `"path"`) {
		t.Fatalf("unauthorized paths: %s", body)
	}
	if environmentPayload(t, owner, map[string]any{"execution": true})["execution"].(map[string]any)["run_command"] != true {
		t.Fatal("guest contaminated owner")
	}
}

func TestExecutionEnvironmentNewViewBindsFinalRegistry(t *testing.T) {
	base := NewToolRegistry()
	owner, err := base.NewView(Config{WorkDir: t.TempDir(), CommandAllowlist: []string{"node"}, FileWriteEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	guest, err := base.NewView(Config{WorkDir: t.TempDir(), CommandAllowlist: []string{"node"}, FileWriteEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	defer guest.Close()
	guest.Retain(map[string]bool{"list_capabilities": true})
	for _, test := range []struct {
		registry   *ToolRegistry
		authorized bool
	}{{owner, true}, {guest, false}, {owner, true}} {
		tool, ok := test.registry.Get("list_capabilities")
		if !ok {
			t.Fatal("missing list_capabilities")
		}
		payload := environmentPayload(t, tool.(*ExtensionsListTool), nil)["execution"].(map[string]any)
		for _, name := range []string{"run_command", "write_file", "extract_archive"} {
			if payload[name] != test.authorized {
				t.Fatalf("%s: %v", name, payload)
			}
		}
	}
}

func TestExecutionEnvironmentMissingAndUnauthorizedPrograms(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	// Literal plugin lookup excludes fallback paths and never executes the file.
	runner := &RunCommandTool{env: []string{}, allowlist: map[string]bool{"node": true}, sandboxMode: CommandSandboxOff}
	result := executionEnvironment(context.Background(), NewToolRegistry(runner))
	programs := result["programs"].([]map[string]any)
	if programs[0]["installed"] != false || programs[0]["authorized"] != true {
		t.Fatal(programs)
	}
	if programs[1]["authorized"] != false {
		t.Fatal(programs)
	}
	bin := filepath.Join(t.TempDir(), "node")
	marker := filepath.Join(t.TempDir(), "executed")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\ntouch '"+marker+"'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", filepath.Dir(bin))
	runner.allowlist = map[string]bool{}
	programs = executionEnvironment(context.Background(), NewToolRegistry(runner))["programs"].([]map[string]any)
	if programs[0]["installed"] != true || programs[0]["authorized"] != false || programs[0]["path"] != nil {
		t.Fatal(programs)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("installation probe executed interpreter")
	}
}

func TestExecutionEnvironmentRequiredSandbox(t *testing.T) {
	runner := &RunCommandTool{root: t.TempDir(), sandboxMode: CommandSandboxRequire}
	result := executionEnvironment(context.Background(), NewToolRegistry(runner))
	sandbox := result["sandbox"].(map[string]any)
	if sandbox["effective"] != "blocked" || sandbox["network"] != false || sandbox["reason"] == "" {
		t.Fatal(sandbox)
	}
	runner.sandboxMode = CommandSandboxAuto
	sandbox = executionEnvironment(context.Background(), NewToolRegistry(runner))["sandbox"].(map[string]any)
	if sandbox["effective"] != "unsandboxed" || sandbox["network"] != true {
		t.Fatal(sandbox)
	}
}

func TestRunCommandLocalBinLookupAndWhitelist(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	name := "diana-environment-test"
	if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\nprintf found"), 0700); err != nil {
		t.Fatal(err)
	}
	runner := &RunCommandTool{root: t.TempDir(), allowlist: map[string]bool{name: true}, timeout: time.Second, maxBytes: 100, sandboxMode: CommandSandboxOff}
	output, err := runner.Run(context.Background(), map[string]any{"command": name})
	if err != nil || !strings.Contains(output, "found") {
		t.Fatalf("%s %v", output, err)
	}
	runner.allowlist = nil
	if _, err := runner.Run(context.Background(), map[string]any{"command": name}); err == nil {
		t.Fatal("lookup bypassed whitelist")
	}
	runner.env = []string{"PATH=/fixed/plugin"}
	if _, err := runner.resolveCommand(name); err == nil {
		t.Fatal("fixed plugin lookup changed")
	}
}

func TestRunCommandRealNode(t *testing.T) {
	runner := &RunCommandTool{root: t.TempDir(), allowlist: map[string]bool{"node": true}, timeout: 5 * time.Second, maxBytes: 100, sandboxMode: CommandSandboxOff}
	if _, err := runner.resolveCommand("node"); err != nil {
		t.Skip("node is not installed")
	}
	output, err := runner.Run(context.Background(), map[string]any{"command": "node", "args": []string{"-e", "console.log('diana-node-ok')"}})
	if err != nil || !strings.Contains(output, "diana-node-ok") {
		t.Fatalf("%s %v", output, err)
	}
}
