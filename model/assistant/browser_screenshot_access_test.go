// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/agent"
)

func TestBrowserScreenshotAccessInRequestRegistries(t *testing.T) {
	t.Setenv("APP_DB_PATH", filepath.Join(t.TempDir(), "diana.db"))
	runtime := &Runtime{}
	tests := []struct {
		name  string
		mode  string
		user  string
		owner bool
		want  bool
	}{
		{"default-owner", "", "owner", true, true},
		{"default-member", "", "member", false, false},
		{"disabled-owner", BrowserScreenshotDisabled, "owner", true, false},
		{"disabled-member", BrowserScreenshotDisabled, "member", false, false},
		{"whitelist-owner", BrowserScreenshotWhitelist, "owner", true, true},
		{"whitelist-member", BrowserScreenshotWhitelist, "member", false, false},
		{"whitelist-outsider", BrowserScreenshotWhitelist, "other", false, false},
		{"whitelist-exact-id", BrowserScreenshotWhitelist, "member2", false, false},
		{"legacy-all-member", "all", "other", false, false},
		{"legacy-all-owner", "all", "owner", true, true},
		{"unknown-mode-member", "everybody", "member", false, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := BotConfig{ID: tc.name, OwnerID: "owner", AgentBrowserScreenshotAccess: BrowserScreenshotAccess{Mode: tc.mode, AllowedUsers: []string{" member ", "member", ""}, AllowedHosts: []string{" Example.COM ", "example.com"}, AllowedGroups: []string{" group ", "group"}}}.WithDefaults()
			registry, err := runtime.newAgentRegistry(context.Background(), cfg, MessageEvent{ProfileID: cfg.ID, UserID: tc.user}, RelationshipPolicy{Owner: tc.owner})
			if err != nil {
				t.Fatal(err)
			}
			defer registry.Close()
			if _, got := registry.Get("browser_screenshot"); got != tc.want {
				t.Fatalf("screenshot visible=%v want=%v", got, tc.want)
			}
			for _, name := range []string{"browser_open", "browser_text", "browser_click", "browser_type", "read_file", "write_file", "run_command"} {
				if _, got := registry.Get(name); got && !tc.owner {
					t.Fatalf("screenshot access also granted %s", name)
				}
			}
			if tc.owner {
				if _, got := registry.Get("browser_click"); !got {
					t.Fatal("screenshot policy removed an unrelated owner tool")
				}
			}
		})
	}
}

func TestBrowserScreenshotAccessConfigRoundTrip(t *testing.T) {
	for _, mode := range []string{BrowserScreenshotDisabled, BrowserScreenshotOwnerOnly, BrowserScreenshotWhitelist} {
		cfg := BotConfig{AgentBrowserScreenshotAccess: BrowserScreenshotAccess{Mode: mode, AllowedUsers: []string{" member ", "member", ""}, AllowedHosts: []string{" Example.COM ", "example.com"}, AllowedGroups: []string{" group ", "group"}}}.WithDefaults()
		body, err := json.Marshal(PayloadFromConfig(cfg))
		if err != nil {
			t.Fatal(err)
		}
		var payload ConfigPayload
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatal(err)
		}
		restored := ConfigFromPayload(payload, BotConfig{})
		if !reflect.DeepEqual(restored.AgentBrowserScreenshotAccess, cfg.AgentBrowserScreenshotAccess) {
			t.Fatalf("%s was lost in the config round trip: %#v", mode, restored.AgentBrowserScreenshotAccess)
		}
	}
	if got := (BotConfig{}).WithDefaults().AgentBrowserScreenshotAccess.Mode; got != BrowserScreenshotOwnerOnly {
		t.Fatalf("legacy config default=%q", got)
	}
}

func TestOtherConfigEditsPreserveBrowserScreenshotAccess(t *testing.T) {
	existing := BotConfig{AgentBrowserScreenshotAccess: BrowserScreenshotAccess{Mode: BrowserScreenshotDisabled}}.WithDefaults()
	var payload ConfigPayload
	if err := json.Unmarshal([]byte(`{"system_prompt":"edited persona"}`), &payload); err != nil {
		t.Fatal(err)
	}
	saved := ConfigFromPayload(payload, existing)
	if saved.AgentBrowserScreenshotAccess.Mode != BrowserScreenshotDisabled {
		t.Fatal("saving an unrelated setting reset the screenshot policy")
	}
	payload.AgentBrowserScreenshotAccess = &BrowserScreenshotAccess{Mode: BrowserScreenshotWhitelist, AllowedUsers: []string{"member"}, AllowedHosts: []string{"example.com"}}
	saved = ConfigFromPayload(payload, existing)
	if !saved.AgentBrowserScreenshotAccess.Allows(false, "member") {
		t.Fatal("explicit screenshot policy did not override the previous value")
	}
}

type screenshotTakeoverBrowser struct{}

func (screenshotTakeoverBrowser) Endpoint(context.Context) (string, error) {
	return "", errors.New("用户正在人工接管内置浏览器")
}
func (screenshotTakeoverBrowser) BrowserFor(string) agent.BuiltinBrowserBridge {
	return screenshotTakeoverBrowser{}
}

func TestAuthorizedBrowserScreenshotStillHonorsTakeover(t *testing.T) {
	t.Setenv("APP_DB_PATH", filepath.Join(t.TempDir(), "diana.db"))
	runtime := &Runtime{}
	runtime.SetBrowserBox(screenshotTakeoverBrowser{})
	cfg := BotConfig{AgentMode: AgentModeStandard, AgentBrowserScreenshotAccess: BrowserScreenshotAccess{Mode: BrowserScreenshotWhitelist, AllowedUsers: []string{"member"}, AllowedHosts: []string{"example.com"}}}.WithDefaults()
	registry, err := runtime.newAgentRegistry(context.Background(), cfg, MessageEvent{UserID: "owner"}, RelationshipPolicy{Owner: true})
	if err != nil {
		t.Fatal(err)
	}
	defer registry.Close()
	tool, ok := registry.Get("browser_screenshot")
	if !ok {
		t.Fatal("authorized screenshot tool is missing")
	}
	if _, err := tool.Run(context.Background(), map[string]any{"url": "https://example.com"}); err == nil || !strings.Contains(err.Error(), "接管") {
		t.Fatalf("screenshot did not honor takeover: %v", err)
	}
}

func TestBrowserScreenshotAccessDoesNotChangeSharedExtensionScope(t *testing.T) {
	runtime := &Runtime{}
	cfg := BotConfig{}.WithDefaults()
	owner := runtime.agentRegistryConfig(cfg, MessageEvent{UserID: "owner"}, true).ExtensionScope()
	cfg.AgentBrowserScreenshotAccess = BrowserScreenshotAccess{Mode: BrowserScreenshotWhitelist, AllowedUsers: []string{"member"}, AllowedHosts: []string{"example.com"}}
	member := runtime.agentRegistryConfig(cfg, MessageEvent{UserID: "member"}, false)
	member.ExtensionManagement = true
	if !reflect.DeepEqual(owner, member.ExtensionScope()) {
		t.Fatal("screenshot policy would start a second shared extension registry")
	}
}

func TestBrowserScreenshotAccessRequiresExplicitGroupAndHostScopes(t *testing.T) {
	t.Setenv("APP_DB_PATH", filepath.Join(t.TempDir(), "diana.db"))
	runtime := &Runtime{}
	access := BrowserScreenshotAccess{Mode: BrowserScreenshotWhitelist, AllowedUsers: []string{"member"}, AllowedHosts: []string{"example.com"}, AllowedGroups: []string{"group"}}
	for _, tc := range []struct {
		name  string
		event MessageEvent
		owner bool
		want  bool
	}{
		{"private", MessageEvent{Kind: EventKindPrivate, UserID: "member"}, false, false},
		{"authorized-group", MessageEvent{Kind: EventKindGroup, UserID: "member", GroupID: "group"}, false, false},
		{"other-group", MessageEvent{Kind: EventKindGroup, UserID: "member", GroupID: "other"}, false, false},
		{"unknown-group", MessageEvent{Kind: EventKindGroup, UserID: "member"}, false, false},
		{"other-member", MessageEvent{Kind: EventKindGroup, UserID: "other", GroupID: "group"}, false, false},
		{"owner-outside-scopes", MessageEvent{Kind: EventKindGroup, UserID: "owner", GroupID: "other"}, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := BotConfig{AgentBrowserScreenshotAccess: access}.WithDefaults()
			registry, err := runtime.newAgentRegistry(context.Background(), cfg, tc.event, RelationshipPolicy{Owner: tc.owner})
			if err != nil {
				t.Fatal(err)
			}
			defer registry.Close()
			if _, got := registry.Get("browser_screenshot"); got != tc.want {
				t.Fatalf("access=%v want=%v", got, tc.want)
			}
		})
	}
	access.AllowedGroups = nil
	if access.AllowsEvent(false, MessageEvent{Kind: EventKindGroup, UserID: "member", GroupID: "group"}) {
		t.Fatal("empty group scope opened all groups")
	}
	access.AllowedHosts = nil
	if access.Allows(false, "member") {
		t.Fatal("empty host scope opened all sites")
	}
	access.AllowedHosts = []string{"*.example.com"}
	if access.Allows(false, "member") {
		t.Fatal("wildcard host opened screenshot access")
	}
}
