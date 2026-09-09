package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestExtensionAdminSkillAndRobotOverrides(t *testing.T) {
	cfg := Config{WorkDir: t.TempDir(), ExtensionManagement: true}
	ctx := context.Background()
	_, err := AdministerExtensions(ctx, cfg, ExtensionAdminRequest{Operation: "save", Kind: "skill", Name: "demo", Content: "---\nname: demo\ndescription: Demo skill\n---\nHello"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := AdministerExtensions(ctx, cfg, ExtensionAdminRequest{Operation: "enabled", Kind: "skill", Name: "demo", ProfileID: "a", Enabled: false}); err != nil {
		t.Fatal(err)
	}
	parent, err := NewAgentToolRegistry(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer parent.Close()
	a, err := parent.NewView(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := parent.NewView(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	values, err := LoadExtensionOverrides(cfg.WorkDir, "a")
	if err != nil {
		t.Fatal(err)
	}
	a.ApplyExtensionOverrides(values)
	if len(a.Skills()) != 0 || len(b.Skills()) != 1 {
		t.Fatal("skill enable state crossed robots")
	}
	read, _ := a.Get("skills.read")
	if _, err := read.Run(ctx, map[string]any{"name": "demo"}); err == nil {
		t.Fatal("disabled skill readable")
	}
	result, err := AdministerExtensions(ctx, cfg, ExtensionAdminRequest{Operation: "read", Kind: "skill", Name: "demo"})
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(result)
	if !strings.Contains(string(data), "Hello") {
		t.Fatal("skill content missing")
	}
	if _, err := AdministerExtensions(ctx, cfg, ExtensionAdminRequest{Operation: "delete", Kind: "skill", Name: "demo"}); err != nil {
		t.Fatal(err)
	}
	result, err = AdministerExtensions(ctx, cfg, ExtensionAdminRequest{Operation: "list"})
	if err != nil {
		t.Fatal(err)
	}
	data, _ = json.Marshal(result)
	if strings.Contains(string(data), `"name":"demo"`) {
		t.Fatal("deleted skill remains")
	}
}

func TestExtensionAdminMCPDoesNotConnectUntilTestAndRedactsSecrets(t *testing.T) {
	t.Setenv("DIANA_ALLOW_PRIVATE_HTTP_FETCHES", "true")
	server := newEchoMCPServer()
	handler := mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server { return server }, &mcpsdk.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	var calls atomic.Int64
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") != "secret-token" {
			w.WriteHeader(401)
			return
		}
		handler.ServeHTTP(w, r)
	}))
	defer httpServer.Close()
	cfg := Config{WorkDir: t.TempDir()}
	ctx := context.Background()
	request := ExtensionAdminRequest{Operation: "save", Kind: "mcp", Name: "echo", Config: map[string]any{"url": httpServer.URL, "headers": map[string]any{"Authorization": "secret-token"}}}
	if _, err := AdministerExtensions(ctx, cfg, request); err != nil {
		t.Fatal(err)
	}
	read, err := AdministerExtensions(ctx, cfg, ExtensionAdminRequest{Operation: "read", Kind: "mcp", Name: "echo"})
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(read)
	if strings.Contains(string(data), "secret-token") {
		t.Fatal("MCP credential leaked")
	}
	if calls.Load() != 0 {
		t.Fatal("read/save started MCP")
	}
	request.Operation = "test"
	request.Config = map[string]any{"url": httpServer.URL, "headers": map[string]any{"Authorization": ""}}
	result, err := AdministerExtensions(ctx, cfg, request)
	if err != nil {
		t.Fatal(err)
	}
	data, _ = json.Marshal(result)
	if !strings.Contains(string(data), "echo") {
		t.Fatal("tools not discovered")
	}
	request.Operation = "save"
	request.Replace = true
	if _, err := AdministerExtensions(ctx, cfg, request); err != nil {
		t.Fatal(err)
	}
	servers, err := loadMCPServers(filepath.Join(cfg.WorkDir, ".mcp.json"))
	if err != nil || servers["echo"].Headers["Authorization"] != "secret-token" {
		t.Fatal("blank save lost credential")
	}
	request.ClearHeaders = []string{"Authorization"}
	if _, err := AdministerExtensions(ctx, cfg, request); err != nil {
		t.Fatal(err)
	}
	servers, _ = loadMCPServers(filepath.Join(cfg.WorkDir, ".mcp.json"))
	if servers["echo"].Headers["Authorization"] != "" {
		t.Fatal("explicit secret clear failed")
	}
	info, _ := os.Stat(filepath.Join(cfg.WorkDir, ".mcp.json"))
	if info.Mode().Perm() != 0600 {
		t.Fatal("insecure permissions")
	}
}

func TestGlobalExtensionPathsDoNotFollowSelectedBot(t *testing.T) {
	root := t.TempDir()
	a, err := GlobalExtensionPaths(Config{WorkDir: root, MCPConfigPath: "first.json", SkillRoots: []string{"first"}})
	if err != nil {
		t.Fatal(err)
	}
	b, err := GlobalExtensionPaths(Config{WorkDir: root, MCPConfigPath: "second.json", SkillRoots: []string{"second"}})
	if err != nil {
		t.Fatal(err)
	}
	if a.MCPConfigPath != b.MCPConfigPath || strings.Join(a.SkillRoots, "|") != strings.Join(b.SkillRoots, "|") {
		t.Fatal("configuration switched with robot")
	}
}

func TestMCPRobotOverrideBlocksLaterToolsWithoutChangingOtherView(t *testing.T) {
	parent := NewToolRegistry(&MCPTool{serverName: "notes", modelName: "notes_first"})
	a, err := parent.NewView(Config{WorkDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := parent.NewView(Config{WorkDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	a.ApplyExtensionOverrides(map[string]bool{"mcp:notes": false})
	parent.Register(&MCPTool{serverName: "notes", modelName: "notes_later"})
	for _, name := range []string{"notes_first", "notes_later"} {
		if _, ok := a.Get(name); ok {
			t.Fatal("disabled MCP exposed a tool")
		}
		if _, ok := b.Get(name); !ok {
			t.Fatal("other bot lost shared MCP tool")
		}
	}
	if strings.Contains(strings.Join(a.Names(), " "), "notes_") {
		t.Fatal("disabled tools leaked into catalog")
	}
}

func TestExtensionAdminCannotReplaceReadonlySkill(t *testing.T) {
	root := t.TempDir()
	external := filepath.Join(root, "external")
	if err := os.MkdirAll(external, 0700); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: readonly\ndescription: External instructions\n---\nOriginal"
	if err := os.WriteFile(filepath.Join(external, "SKILL.md"), []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := Config{WorkDir: root, SkillRoots: []string{external}}
	if _, err := AdministerExtensions(context.Background(), cfg, ExtensionAdminRequest{Operation: "save", Kind: "skill", Name: "readonly", Content: content + " changed", Replace: true}); err == nil {
		t.Fatal("external skill shadowed")
	}
	data, _ := os.ReadFile(filepath.Join(external, "SKILL.md"))
	if string(data) != content {
		t.Fatal("external file changed")
	}
}
