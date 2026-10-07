// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"
)

func TestBrowserOperationAccessOnlyGrantsOwnPrivateSession(t *testing.T) {
	t.Setenv("APP_DB_PATH", filepath.Join(t.TempDir(), "diana.db"))
	runtime := &Runtime{}
	runtime.SetBrowserBox(screenshotTakeoverBrowser{})
	policy := BrowserOperationAccess{Mode: "whitelist", AllowedUsers: []string{"member"}, AllowedHosts: []string{"example.com", "login.example.com"}}
	for _, tc := range []struct {
		name                          string
		event                         MessageEvent
		owner, operations, screenshot bool
	}{
		{"member-private", MessageEvent{Kind: EventKindPrivate, UserID: "member"}, false, true, true},
		{"member-group", MessageEvent{Kind: EventKindGroup, UserID: "member", GroupID: "g"}, false, false, false},
		{"ambiguous-event", MessageEvent{UserID: "member"}, false, false, false},
		{"private-with-group", MessageEvent{Kind: EventKindPrivate, UserID: "member", GroupID: "g"}, false, false, false},
		{"outsider", MessageEvent{Kind: EventKindPrivate, UserID: "other"}, false, false, false},
		{"similar-id", MessageEvent{Kind: EventKindPrivate, UserID: "member2"}, false, false, false},
		{"owner", MessageEvent{Kind: EventKindGroup, UserID: "owner", GroupID: "other"}, true, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := BotConfig{AgentMode: AgentModeStandard, AgentBrowserOperationAccess: policy, AgentBrowserScreenshotAccess: BrowserScreenshotAccess{Mode: "whitelist", AllowedUsers: []string{"member"}, AllowedHosts: []string{"example.com"}, AllowedGroups: []string{"g"}}}.WithDefaults()
			registry, err := runtime.newAgentRegistry(context.Background(), cfg, tc.event, RelationshipPolicy{Owner: tc.owner})
			if err != nil {
				t.Fatal(err)
			}
			defer registry.Close()
			for _, name := range []string{"browser_open", "browser_text", "browser_click", "browser_type"} {
				if _, got := registry.Get(name); got != tc.operations {
					t.Fatalf("%s visible=%v", name, got)
				}
			}
			if _, got := registry.Get("browser_screenshot"); got != tc.screenshot {
				t.Fatalf("screenshot visible=%v", got)
			}
			if _, got := registry.Get("browser_tabs"); got != tc.operations {
				t.Fatalf("personal tabs visible=%v", got)
			}
			if !tc.owner {
				for _, name := range []string{"read_file", "write_file", "run_command", "browser_ext_tabs", "diana_config"} {
					if _, got := registry.Get(name); got {
						t.Fatalf("operation access granted %s", name)
					}
				}
			}
		})
	}
	runtime.userBrowsers.Stop()
}

func TestBrowserOperationAndScreenshotModesRemainIndependent(t *testing.T) {
	t.Setenv("APP_DB_PATH", filepath.Join(t.TempDir(), "diana.db"))
	runtime := &Runtime{}
	event := MessageEvent{Kind: EventKindPrivate, UserID: "member"}
	for _, operations := range []string{"", "all", "disabled", "whitelist"} {
		for _, screenshots := range []string{"owner_only", "disabled", "whitelist"} {
			cfg := BotConfig{AgentBrowserOperationAccess: BrowserOperationAccess{Mode: operations, AllowedUsers: []string{"member"}, AllowedHosts: []string{"example.com"}}, AgentBrowserScreenshotAccess: BrowserScreenshotAccess{Mode: screenshots, AllowedUsers: []string{"member"}, AllowedHosts: []string{"example.com"}}}.WithDefaults()
			registry, err := runtime.newAgentRegistry(context.Background(), cfg, event, RelationshipPolicy{})
			if err != nil {
				t.Fatal(err)
			}
			_, open := registry.Get("browser_open")
			_, capture := registry.Get("browser_screenshot")
			if open != (operations == "whitelist") || capture != (open && screenshots == "whitelist") {
				t.Fatalf("mode %s/%s open=%v capture=%v", operations, screenshots, open, capture)
			}
			_ = registry.Close()
		}
	}
	registry, err := runtime.newAgentRegistry(context.Background(), BotConfig{AgentBrowserOperationAccess: BrowserOperationAccess{Mode: "disabled"}}.WithDefaults(), MessageEvent{}, RelationshipPolicy{Owner: true})
	if err != nil {
		t.Fatal(err)
	}
	defer registry.Close()
	if _, ok := registry.Get("browser_open"); ok {
		t.Fatal("disabled operation mode still allowed owner")
	}
	if _, ok := registry.Get("browser_screenshot"); !ok {
		t.Fatal("disabling operations also disabled owner screenshot")
	}
}

func TestBrowserOperationPolicyPersistsAndUnrelatedSavesPreserveIt(t *testing.T) {
	cfg := BotConfig{AgentBrowserOperationAccess: BrowserOperationAccess{Mode: "whitelist", AllowedUsers: []string{" member ", "member"}, AllowedHosts: []string{" EXAMPLE.COM ", "example.com"}}}.WithDefaults()
	body, err := json.Marshal(PayloadFromConfig(cfg))
	if err != nil {
		t.Fatal(err)
	}
	var payload ConfigPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatal(err)
	}
	if got := ConfigFromPayload(payload, BotConfig{}).AgentBrowserOperationAccess; !reflect.DeepEqual(got, cfg.AgentBrowserOperationAccess) {
		t.Fatalf("round trip=%#v", got)
	}
	if got := ConfigFromPayload(ConfigPayload{SystemPrompt: "edited"}, cfg).AgentBrowserOperationAccess; !reflect.DeepEqual(got, cfg.AgentBrowserOperationAccess) {
		t.Fatal("unrelated save reset operation policy")
	}
	for _, hosts := range [][]string{nil, {"*.example.com"}, {"https://example.com"}} {
		policy := cfg.AgentBrowserOperationAccess
		policy.AllowedHosts = hosts
		if policy.AllowsEvent(false, MessageEvent{Kind: EventKindPrivate, UserID: "member"}) {
			t.Fatalf("invalid hosts granted access: %v", hosts)
		}
	}
}
