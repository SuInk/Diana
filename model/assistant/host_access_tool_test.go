package assistant

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHostAccessReadsOutsideWorkspaceAndRejectsNonOwner(t *testing.T) {
	t.Setenv("APP_DB_PATH", filepath.Join(t.TempDir(), "diana.db"))
	dir := t.TempDir()
	path := filepath.Join(dir, "report.txt")
	if err := os.WriteFile(path, []byte("outside workspace"), 0600); err != nil {
		t.Fatal(err)
	}
	r := hostAccessTestRuntime(t)
	event := identityCheckEvent("100001", "owner", "")
	tool := &hostAccessTool{runtime: r, event: event}
	for _, op := range []string{"read", "list", "usage", "disk"} {
		target := path
		if op != "read" {
			target = dir
		}
		out, err := tool.Run(context.Background(), map[string]any{"operation": op, "path": target})
		if err != nil {
			t.Fatalf("%s: %v", op, err)
		}
		if !json.Valid([]byte(out)) {
			t.Fatalf("invalid output: %s", out)
		}
		if op == "read" && !strings.Contains(out, "outside workspace") {
			t.Fatal("missing contents")
		}
	}
	tool.event.UserID = "300003"
	if _, err := tool.Run(context.Background(), map[string]any{"operation": "read", "path": path}); err == nil {
		t.Fatal("member read host")
	}
	if (RelationshipPolicy{}).allowedAgentToolNames()[hostAccessToolName] {
		t.Fatal("host tool exposed to members")
	}
}
func TestHostAccessOwnerExecutesWithoutApproval(t *testing.T) {
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("Unix fixture")
	}
	root := t.TempDir()
	marker := filepath.Join(root, "created-by-owner")
	r := hostAccessTestRuntime(t)
	tool := &hostAccessTool{runtime: r, event: identityCheckEvent("100001", "owner", "")}
	out, err := tool.Run(context.Background(), map[string]any{"operation": "exec", "command": "/bin/sh", "args": []string{"-c", `touch "$1"`, "probe", marker}, "cwd": root})
	if err != nil || strings.Contains(out, "approval_required") {
		t.Fatalf("execution failed: %s %v", out, err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("owner command did not run")
	}
}
func TestHostAccessHonorsSafeModeAtExecution(t *testing.T) {
	r := NewRuntime(BotConfig{OwnerID: "owner", AgentMode: AgentModeSafe}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	tool := &hostAccessTool{runtime: r, event: MessageEvent{UserID: "owner"}}
	if _, err := tool.Run(context.Background(), map[string]any{"operation": "read", "path": filepath.Join(t.TempDir(), "missing")}); err == nil || !strings.Contains(err.Error(), "安全模式") {
		t.Fatalf("safe mode not enforced: %v", err)
	}
}

func hostAccessTestRuntime(t *testing.T) *Runtime {
	t.Helper()
	return NewRuntime(BotConfig{OwnerID: "100001", BotAccount: "200002", Platform: PlatformOneBotV11}, nilChannel{}, NewDefaultPluginManager(), nil, nil, nil, nil)
}

func TestHostAccessLegacySwitchDoesNotDisableOwner(t *testing.T) {
	var cfg BotConfig
	if err := json.Unmarshal([]byte(`{"owner_id":"100001","agent_host_access_enabled":false}`), &cfg); err != nil {
		t.Fatal(err)
	}
	r := NewRuntime(cfg, nilChannel{}, NewDefaultPluginManager(), nil, nil, nil, nil)
	event := MessageEvent{UserID: "100001"}
	registry, err := r.newAgentRegistry(context.Background(), r.ProfileConfig(""), event, RelationshipPolicy{Owner: true})
	if err != nil {
		t.Fatal(err)
	}
	defer registry.Close()
	loaded, ok := registry.Get(hostAccessToolName)
	if !ok {
		t.Fatal("default owner host tool missing")
	}
	dir := t.TempDir()
	if _, err := loaded.Run(context.Background(), map[string]any{"operation": "list", "path": dir}); err != nil {
		t.Fatal(err)
	}
	cfg = r.ProfileConfig("")
	cfg.AgentMode = AgentModeSafe
	r.SetProfiles(ProfileSet{Profiles: []BotConfig{cfg}})
	if _, err := loaded.Run(context.Background(), map[string]any{"operation": "list", "path": dir}); err == nil {
		t.Fatal("loaded tool bypassed safe mode")
	}
	safe, err := r.newAgentRegistry(context.Background(), r.ProfileConfig(""), event, RelationshipPolicy{Owner: true})
	if err != nil {
		t.Fatal(err)
	}
	defer safe.Close()
	if _, ok := safe.Get(hostAccessToolName); ok {
		t.Fatal("safe mode exposed host tool")
	}
	body, err := json.Marshal(PayloadFromConfig(cfg))
	if err != nil || strings.Contains(string(body), "agent_host_access_enabled") {
		t.Fatal("obsolete switch still serialized")
	}
}

func TestHostAccessRejectsNonOwnerExecution(t *testing.T) {
	tool := &hostAccessTool{runtime: hostAccessTestRuntime(t), event: MessageEvent{UserID: "member"}}
	if _, err := tool.Run(context.Background(), map[string]any{"operation": "exec", "command": "/bin/sh", "args": []string{"-c", "true"}, "cwd": t.TempDir()}); err == nil {
		t.Fatal("non-owner executed command")
	}
}

func TestHostAccessAdminExecutesWithoutApproval(t *testing.T) {
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("Unix fixture")
	}
	root := t.TempDir()
	marker := filepath.Join(root, "admin-result")
	tool := &hostAccessTool{runtime: hostAccessTestRuntime(t), event: MessageEvent{UserID: "webui-admin"}}
	ctx := context.WithValue(context.Background(), adminChatRunKey{}, true)
	out, err := tool.Run(ctx, map[string]any{"operation": "exec", "command": "/bin/sh", "args": []string{"-c", `printf admin > "$1"`, "probe", marker}, "cwd": root})
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(marker)
	if err != nil || string(body) != "admin" || strings.Contains(out, "approval_required") {
		t.Fatalf("execution=%s err=%v", out, err)
	}
}

func TestHostAccessRechecksOwnerAfterConfigurationChange(t *testing.T) {
	r := hostAccessTestRuntime(t)
	tool := &hostAccessTool{runtime: r, event: identityCheckEvent("100001", "owner", "")}
	cfg := r.ProfileConfig("")
	cfg.OwnerID = "new-owner"
	r.SetProfiles(ProfileSet{Profiles: []BotConfig{cfg}})
	if _, err := tool.Run(context.Background(), map[string]any{"operation": "list", "path": t.TempDir()}); err == nil {
		t.Fatal("former owner retained host access")
	}
}
