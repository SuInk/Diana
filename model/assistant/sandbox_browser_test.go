// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"path/filepath"
	"testing"
)

func TestGroupMembersGetPublicSandboxBrowser(t *testing.T) {
	t.Setenv("APP_DB_PATH", filepath.Join(t.TempDir(), "diana.db"))
	runtime := &Runtime{}
	runtime.SetBrowserBox(screenshotTakeoverBrowser{})
	group := MessageEvent{Kind: EventKindGroup, UserID: "member", GroupID: "20001"}
	for _, tc := range []struct {
		name    string
		cfg     BotConfig
		event   MessageEvent
		owner   bool
		sandbox bool
	}{
		{"member-group", BotConfig{AgentMode: AgentModeStandard}, group, false, true},
		{"member-private", BotConfig{AgentMode: AgentModeStandard}, MessageEvent{Kind: EventKindPrivate, UserID: "member"}, false, true},
		{"switched-off", BotConfig{AgentMode: AgentModeStandard, AgentSandboxBrowserDisabled: true}, group, false, false},
		{"safe-mode", BotConfig{AgentMode: AgentModeSafe}, group, false, false},
		{"operations-disabled", BotConfig{AgentMode: AgentModeStandard, AgentBrowserOperationAccess: BrowserOperationAccess{Mode: BrowserAccessDisabled}}, group, false, false},
		{"owner", BotConfig{AgentMode: AgentModeStandard}, group, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			registry, err := runtime.newAgentRegistry(context.Background(), tc.cfg.WithDefaults(), tc.event, RelationshipPolicy{Owner: tc.owner})
			if err != nil {
				t.Fatal(err)
			}
			defer registry.Close()
			if !tc.owner {
				for _, name := range []string{"browser_open", "browser_click", "browser_type", "browser_scroll", "browser_wait", "browser_screenshot"} {
					if _, got := registry.Get(name); got != tc.sandbox {
						t.Fatalf("%s visible=%v want %v", name, got, tc.sandbox)
					}
				}
				for _, name := range []string{"browser_eval", "browser_select", "read_file", "run_command"} {
					if _, got := registry.Get(name); got {
						t.Fatalf("member got %s", name)
					}
				}
			}
			click, ok := registry.Get("browser_click")
			if !ok {
				return
			}
			_, indexed := click.(interface{ InputSchema() map[string]any }).InputSchema()["properties"].(map[string]any)["index"]
			if indexed != tc.sandbox {
				t.Fatalf("click index=%v want %v", indexed, tc.sandbox)
			}
		})
	}
	if runtime.sandboxBrowsers != nil {
		runtime.sandboxBrowsers.Stop()
	}
}
