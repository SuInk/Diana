package assistant

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestHostAccessApprovalIsExactScopedExpiringAndSingleUse(t *testing.T) {
	var s hostApprovalStore
	now := time.Now()
	ok, code, err := s.authorize("bot/owner/chat", `{"args":["a",""]}`, "", now)
	if err != nil || ok || code == "" {
		t.Fatal("initial request not pending")
	}
	for _, tt := range []struct{ scope, input, text string }{
		{"other/owner/chat", `{"args":["a",""]}`, "批准主机操作 " + code},
		{"bot/owner/other", `{"args":["a",""]}`, "批准主机操作 " + code},
		{"bot/owner/chat", `{"args":["a"]}`, "批准主机操作 " + code},
		{"bot/owner/chat", `{"args":["a",""]}`, "引用：批准主机操作 " + code},
	} {
		if ok, _, _ := s.authorize(tt.scope, tt.input, tt.text, now); ok {
			t.Fatalf("accepted %#v", tt)
		}
	}
	var accepted atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if ok, _, _ := s.authorize("bot/owner/chat", `{"args":["a",""]}`, "批准主机操作 "+code, now); ok {
				accepted.Add(1)
			}
		}()
	}
	wg.Wait()
	if accepted.Load() != 1 {
		t.Fatalf("consumed %d times", accepted.Load())
	}
	_, fresh, _ := s.authorize("expiry", "input", "", now)
	if ok, next, _ := s.authorize("expiry", "input", "批准主机操作 "+fresh, now.Add(6*time.Minute)); ok || next == fresh {
		t.Fatal("expired approval accepted")
	}
	var restarted hostApprovalStore
	if ok, _, _ := restarted.authorize("bot/owner/chat", `{"args":["a",""]}`, "批准主机操作 "+code, now); ok {
		t.Fatal("approval survived restart")
	}
}
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
func TestHostAccessUnknownCommandRequiresApprovalBeforeExecution(t *testing.T) {
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("Unix fixture")
	}
	root := t.TempDir()
	marker := filepath.Join(root, "must-not-exist")
	r := hostAccessTestRuntime(t)
	tool := &hostAccessTool{runtime: r, event: identityCheckEvent("100001", "owner", "")}
	out, err := tool.Run(context.Background(), map[string]any{"operation": "exec", "command": "/bin/sh", "args": []string{"-c", `touch "$1"`, "probe", marker}, "cwd": root})
	if err != nil || !strings.Contains(out, "approval_required") {
		t.Fatalf("missing approval: %s %v", out, err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("command ran without approval")
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
	return NewRuntime(BotConfig{OwnerID: "100001", BotAccount: "200002", Platform: PlatformOneBotV11, AgentHostAccessEnabled: true}, nilChannel{}, NewDefaultPluginManager(), nil, nil, nil, nil)
}

func TestHostAccessDefaultsDisabledAndConfigRoundTrip(t *testing.T) {
	if DefaultBotConfig().AgentHostAccessEnabled || (BotConfig{}).WithDefaults().AgentHostAccessEnabled {
		t.Fatal("enabled by default")
	}
	for _, enabled := range []bool{false, true} {
		cfg := BotConfig{AgentHostAccessEnabled: enabled}.WithDefaults()
		restored := ConfigFromPayload(PayloadFromConfig(cfg), BotConfig{AgentHostAccessEnabled: !enabled})
		if restored.AgentHostAccessEnabled != enabled {
			t.Fatal("lost switch state")
		}
	}
	r := identityCheckRuntime(t)
	tool := &hostAccessTool{runtime: r, event: identityCheckEvent("100001", "owner", "")}
	for _, op := range []string{"read", "list", "usage", "disk", "exec"} {
		if _, err := tool.Run(context.Background(), map[string]any{"operation": op, "path": t.TempDir()}); err == nil {
			t.Fatalf("disabled operation %s ran", op)
		}
	}
}

func TestHostAccessSwitchRevokesLoadedToolsAndPendingApprovals(t *testing.T) {
	r := hostAccessTestRuntime(t)
	event := identityCheckEvent("100001", "owner", "")
	cfg := r.ProfileConfig("")
	registry, err := r.newAgentRegistry(context.Background(), cfg, event, RelationshipPolicy{Owner: true})
	if err != nil {
		t.Fatal(err)
	}
	defer registry.Close()
	loaded, ok := registry.Get(hostAccessToolName)
	if !ok {
		t.Fatal("enabled owner tool not registered")
	}
	_, code, err := r.hostApprovals.authorize("scope", "input", "", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	cfg.AgentHostAccessEnabled = false
	r.SetProfiles(ProfileSet{Profiles: []BotConfig{cfg}})
	if _, err = loaded.Run(context.Background(), map[string]any{"operation": "list", "path": t.TempDir()}); err == nil {
		t.Fatal("already-loaded tool bypassed disabled switch")
	}
	disabled, err := r.newAgentRegistry(context.Background(), r.ProfileConfig(""), event, RelationshipPolicy{Owner: true})
	if err != nil {
		t.Fatal(err)
	}
	defer disabled.Close()
	if _, ok = disabled.Get(hostAccessToolName); ok {
		t.Fatal("disabled tool registered")
	}
	cfg.AgentHostAccessEnabled = true
	r.SetProfiles(ProfileSet{Profiles: []BotConfig{cfg}})
	if approved, _, _ := r.hostApprovals.authorize("scope", "input", "批准主机操作 "+code, time.Now()); approved {
		t.Fatal("old approval survived configuration change")
	}
}
