// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/agent"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func bundleManifestMap() map[string]any {
	return map[string]any{
		"id": "alice.bundle", "name": "Upstream account bundle", "version": "1.0.0", "description": "Query accounts", "permissions": []string{"process:execute"}, "entry": "SKILL.md", "skills": []string{"skills"},
		"settings": []any{map[string]any{"key": "token", "label": "Token", "type": "string", "default": "", "secret": true}},
		"scripts":  map[string]any{"query": map[string]any{"command": "sh", "entry": "scripts/query.sh", "env": map[string]string{"TOKEN": "${settings.token}"}}},
	}
}

func TestRepoPluginBundleManifestValidationAndDistribution(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"traversal", func(m map[string]any) { m["skills"] = []string{"../skills"} }},
		{"absolute", func(m map[string]any) { m["skills"] = []string{"/skills"} }},
		{"backslash", func(m map[string]any) { m["skills"] = []string{`skills\..\private`} }},
		{"no permission", func(m map[string]any) { m["permissions"] = []string{"message:read"} }},
		{"unknown setting", func(m map[string]any) {
			m["scripts"] = map[string]any{"query": map[string]any{"command": "node", "entry": "scripts/a.js", "env": map[string]string{"TOKEN": "${settings.unknown}"}}}
		}},
		{"invalid working directory", func(m map[string]any) {
			m["scripts"] = map[string]any{"query": map[string]any{"command": "node", "entry": "scripts/a.js", "working_directory": "../"}}
		}},
		{"ambient env", func(m map[string]any) {
			m["permissions"] = []string{"agent:tool", "process:execute"}
			m["mcp_servers"] = map[string]any{"demo": map[string]any{"command": "node", "inherit_env": true}}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := bundleManifestMap()
			tc.mutate(raw)
			body, _ := json.Marshal(raw)
			m, err := decodeRepoPluginManifest(body)
			if err == nil {
				err = m.validate()
			}
			if !errors.Is(err, ErrRepoPluginFormat) {
				t.Fatalf("err=%v", err)
			}
		})
	}
	body, _ := json.Marshal(bundleManifestMap())
	m, err := decodeRepoPluginManifest(body)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.validate(); err != nil {
		t.Fatal(err)
	}
	if files := strings.Join(m.normalizedFiles(), ","); !strings.Contains(files, "skills/") || !strings.Contains(files, "scripts/query.sh") {
		t.Fatalf("files=%s", files)
	}
	server := repoPluginTestServer(t, bundleManifestMap(), map[string]string{"SKILL.md": "---\nname: bundle\ndescription: Root\n---\n", "skills/accounts/SKILL.md": "---\nname: accounts\ndescription: Query\n---\n", "skills/accounts/references/api.md": "API reference", "scripts/query.sh": "printf query"}, "HEAD")
	defer server.Close()
	installer := NewRepoPluginInstaller(t.TempDir(), server.Client())
	installer.ArchiveBase = server.URL
	preview, err := installer.Preview(context.Background(), "https://github.com/alice/bundle")
	if err != nil {
		t.Fatal(err)
	}
	plugin, source, err := installer.Install(context.Background(), "https://github.com/alice/bundle", preview.Commit)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(plugin.dir, "skills/accounts/references/api.md")); err != nil {
		t.Fatal(err)
	}
	recovered, err := LoadRepoPlugin(installer.DataDir, source)
	if err != nil || recovered.bundle == nil {
		t.Fatalf("recovered=%v err=%v", recovered, err)
	}
}

func installedBundleFixture(t *testing.T, mutate ...func(map[string]any)) (*Runtime, *RepoPlugin, BotConfig, MessageEvent) {
	t.Helper()
	t.Setenv("APP_DB_PATH", filepath.Join(t.TempDir(), "test.sqlite"))
	root := t.TempDir()
	for path, body := range map[string]string{"SKILL.md": "---\nname: bundle\ndescription: Root\n---\n", "skills/accounts/SKILL.md": "---\nname: accounts\ndescription: Query upstream accounts\n---\nQuery accounts using the official script.", "scripts/query.sh": "printf 'result=%s' \"$TOKEN\""} {
		full := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	raw := bundleManifestMap()
	for _, change := range mutate {
		change(raw)
	}
	body, _ := json.Marshal(raw)
	if err := os.WriteFile(filepath.Join(root, RepoPluginManifestFile), body, 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := decodeRepoPluginManifest(body)
	if err != nil {
		t.Fatal(err)
	}
	p := NewRepoPlugin(root, RepoPluginSource{URL: "https://github.com/alice/bundle"}, m.pluginManifest())
	plugins := NewPluginManager(p)
	if _, err := plugins.Install(p.manifest.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := plugins.UpdateSettings(p.manifest.ID, map[string]any{"token": "test-token$literal"}); err != nil {
		t.Fatal(err)
	}
	r := &Runtime{plugins: plugins}
	cfg := DefaultBotConfig()
	cfg.ID = "bot"
	cfg.AgentMCPConfigPath = filepath.Join(t.TempDir(), "missing.json")
	cfg.AgentCommandAllowlist = []string{"sh"}
	cfg.AgentCommandSandbox = agent.CommandSandboxOff
	event := MessageEvent{Kind: EventKindPrivate, ProfileID: "bot", Platform: cfg.Platform, UserID: "owner"}
	t.Cleanup(r.closeAgentRegistryCache)
	return r, p, cfg, event
}

func TestRepoPluginBundleOwnerScopeSecretsAndLifecycle(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	r, p, cfg, event := installedBundleFixture(t)
	registry, err := r.newAgentRegistry(context.Background(), cfg, event, RelationshipPolicy{Owner: true})
	if err != nil {
		t.Fatal(err)
	}
	defer registry.Close()
	if !strings.Contains(agent.RenderSkillsCatalog(registry.Skills(), 8000), "alice.bundle:accounts") {
		t.Fatalf("skills=%v", registry.Skills())
	}
	catalog, ok := registry.Get("list_capabilities")
	if !ok {
		t.Fatal("catalog missing")
	}
	listed, err := catalog.Run(context.Background(), nil)
	if err != nil || !strings.Contains(listed, "alice.bundle:accounts") || strings.Contains(listed, "test-token") {
		t.Fatalf("catalog=%s err=%v", listed, err)
	}
	tool, ok := registry.Get(agent.PluginToolPrefix(p.manifest.ID) + "__run")
	if !ok {
		t.Fatal("script tool missing")
	}
	output, err := tool.Run(context.Background(), map[string]any{"script": "query"})
	if err != nil || !strings.Contains(output, "[REDACTED]") || strings.Contains(output, "test-token") {
		t.Fatalf("output=%q err=%v", output, err)
	}
	member, err := r.newAgentRegistry(context.Background(), cfg, event, RelationshipPolicy{})
	if err != nil {
		t.Fatal(err)
	}
	defer member.Close()
	if _, ok := member.Get(tool.Name()); ok {
		t.Fatal("non-owner received bundle execution")
	}
	for _, skill := range member.Skills() {
		if strings.HasPrefix(skill.Name, "alice.bundle:") {
			t.Fatal("non-owner received bundle skill")
		}
	}
	if _, err := r.plugins.SetEnabledForProfile(p.manifest.ID, "bot", false); err != nil {
		t.Fatal(err)
	}
	if _, err := tool.Run(context.Background(), map[string]any{"script": "query"}); err == nil {
		t.Fatal("old tool survived disable")
	}
	if len(p.bundleSessions) != 0 {
		t.Fatal("bundle sessions retained after disable")
	}
	if _, err := r.plugins.SetEnabledForProfile(p.manifest.ID, "bot", true); err != nil {
		t.Fatal(err)
	}
	next, err := r.newAgentRegistry(context.Background(), cfg, event, RelationshipPolicy{Owner: true})
	if err != nil {
		t.Fatal(err)
	}
	defer next.Close()
	newTool, ok := next.Get(tool.Name())
	if !ok {
		t.Fatal("tool missing after reenable")
	}
	if _, err := r.plugins.UpdateSettings(p.manifest.ID, map[string]any{"token": "replacement"}); err != nil {
		t.Fatal(err)
	}
	if _, err := newTool.Run(context.Background(), map[string]any{"script": "query"}); err == nil {
		t.Fatal("old credential snapshot survived settings update")
	}
	if _, err := r.plugins.Uninstall(p.manifest.ID); err != nil {
		t.Fatal(err)
	}
	if len(p.bundleSessions) != 0 {
		t.Fatal("sessions retained after uninstall")
	}
}

func TestRepoPluginBundleSessionReuseAndErrorRedaction(t *testing.T) {
	r, p, cfg, event := installedBundleFixture(t)
	value, settings, _ := r.pluginWithSettingsForEvent(p.manifest.ID, event)
	if value != p {
		t.Fatal("plugin lookup failed")
	}
	agentCfg := r.agentRegistryConfig(cfg, event, true)
	first, _, err := p.bundleRuntime(context.Background(), "bot\x00", agentCfg, settings)
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := p.bundleRuntime(context.Background(), "bot\x00", agentCfg, settings)
	if err != nil || second != first {
		t.Fatal("session not reused")
	}
	secret := "quoted\"token\\line\n"
	encoded, _ := json.Marshal(secret)
	redacted := redactBundleText(secret+" "+string(encoded), []string{secret})
	if strings.Contains(redacted, "token") || strings.Contains(redacted, "line") {
		t.Fatalf("redacted=%q", redacted)
	}
	p.Close()
	if first.active.Load() {
		t.Fatal("session remained active after close")
	}
}

func TestRepoPluginBundleMCPReuseCredentialsAndShutdown(t *testing.T) {
	t.Setenv("DIANA_ALLOW_PRIVATE_HTTP_FETCHES", "true")
	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "bundle-test", Version: "1"}, nil)
	server.AddTool(&mcpsdk.Tool{Name: "echo", InputSchema: json.RawMessage(`{"type":"object"}`)}, func(context.Context, *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
		return &mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: "MCP works"}}}, nil
	})
	transport := mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server { return server }, nil)
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Header.Get("Authorization") != "Bearer test-token$literal" {
			http.Error(w, "unexpected credentials", http.StatusUnauthorized)
			return
		}
		transport.ServeHTTP(w, req)
	}))
	defer httpServer.Close()
	r, p, cfg, event := installedBundleFixture(t, func(m map[string]any) {
		m["permissions"] = []string{"process:execute", "network:http", "agent:tool"}
		m["mcp_servers"] = map[string]any{"remote": map[string]any{"url": httpServer.URL, "headers": map[string]string{"Authorization": "Bearer ${settings.token}"}, "enabled_tools": []string{"echo"}, "required": true}}
	})
	first, err := r.newAgentRegistry(context.Background(), cfg, event, RelationshipPolicy{Owner: true})
	if err != nil {
		t.Fatal(err)
	}
	var name string
	for _, candidate := range first.Names() {
		if strings.HasPrefix(candidate, "mcp__plugin__") {
			name = candidate
		}
	}
	if name == "" {
		t.Fatal("plugin MCP tool missing")
	}
	first.Close()
	second, err := r.newAgentRegistry(context.Background(), cfg, event, RelationshipPolicy{Owner: true})
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	tool, ok := second.Get(name)
	if !ok {
		t.Fatal("MCP did not survive closing request view")
	}
	output, err := tool.Run(context.Background(), nil)
	if err != nil || output != "MCP works" {
		t.Fatalf("output=%q err=%v", output, err)
	}
	if _, err := r.plugins.SetEnabledForProfile(p.manifest.ID, "bot", false); err != nil {
		t.Fatal(err)
	}
	if _, err := tool.Run(context.Background(), nil); err == nil {
		t.Fatal("MCP survived plugin disable")
	}
	if len(p.bundleSessions) != 0 {
		t.Fatal("MCP connections retained after disable")
	}
}

func TestRepoPluginBundleSafeModeReadsWithoutStartingProcesses(t *testing.T) {
	r, plugin, cfg, event := installedBundleFixture(t, func(manifest map[string]any) {
		manifest["permissions"] = []string{"process:execute", "agent:tool"}
		manifest["mcp_servers"] = map[string]any{"blocked": map[string]any{"command": "diana-no-such-program", "required": true}}
	})
	cfg.AgentMode = AgentModeSafe
	registry, err := r.newAgentRegistry(context.Background(), cfg, event, RelationshipPolicy{Owner: true})
	if err != nil {
		t.Fatalf("safe mode attempted to start a required MCP: %v", err)
	}
	defer registry.Close()
	prefix := agent.PluginToolPrefix(plugin.manifest.ID)
	if _, ok := registry.Get(prefix + "__run"); ok {
		t.Fatal("safe mode exposed a script entry point")
	}
	reader, ok := registry.Get(prefix + "__read")
	if !ok {
		t.Fatal("safe mode lost resource reading")
	}
	result, err := reader.Run(context.Background(), map[string]any{"path": "skills/accounts/SKILL.md"})
	if err != nil || !strings.Contains(result, "Query upstream accounts") {
		t.Fatalf("resource read failed: %s %v", result, err)
	}
	plugin.bundleMu.Lock()
	started := len(plugin.bundleSessions)
	plugin.bundleMu.Unlock()
	if started != 0 {
		t.Fatalf("safe mode started %d plugin sessions", started)
	}
}
