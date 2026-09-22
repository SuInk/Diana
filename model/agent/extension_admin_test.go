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
	read, _ := a.Get("read_skill")
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
	servers, err := loadMCPServers(resolveMCPConfigPath(cfg.WithDefaults()))
	if err != nil || servers["echo"].Headers["Authorization"] != "secret-token" {
		t.Fatal("blank save lost credential")
	}
	request.ClearHeaders = []string{"Authorization"}
	if _, err := AdministerExtensions(ctx, cfg, request); err != nil {
		t.Fatal(err)
	}
	servers, _ = loadMCPServers(resolveMCPConfigPath(cfg.WithDefaults()))
	if servers["echo"].Headers["Authorization"] != "" {
		t.Fatal("explicit secret clear failed")
	}
	info, _ := os.Stat(resolveMCPConfigPath(cfg.WithDefaults()))
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

func TestExtensionAdminMemberPermissionDefaultsToOwnerOnly(t *testing.T) {
	cfg := Config{WorkDir: t.TempDir(), MCPConfigPath: "mcp.json"}
	ctx := context.Background()
	config := map[string]any{"url": "https://example.com/mcp", "enabled": true}
	if _, err := AdministerExtensions(ctx, cfg, ExtensionAdminRequest{Operation: "save", Kind: "mcp", Name: "probe", Config: config}); err != nil {
		t.Fatal(err)
	}
	members := func() *bool {
		result, err := AdministerExtensions(ctx, cfg, ExtensionAdminRequest{Operation: "list", ProfileID: "bot-a"})
		if err != nil {
			t.Fatal(err)
		}
		for _, state := range result.(map[string]any)["items"].([]ExtensionState) {
			if state.ID == "mcp:probe" {
				return state.MembersEnabled
			}
		}
		t.Fatal("saved MCP missing from catalog")
		return nil
	}
	if value := members(); value == nil || *value {
		t.Fatalf("new MCP defaults to members_enabled=%v, want false", value)
	}
	if _, err := AdministerExtensions(ctx, cfg, ExtensionAdminRequest{Operation: "members", Kind: "mcp", Name: "probe", ProfileID: "bot-a", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if value := members(); value == nil || !*value {
		t.Fatalf("members_enabled = %v after opening the service", value)
	}
	values, err := LoadExtensionOverrides(cfg.WorkDir, "bot-a")
	if err != nil {
		t.Fatal(err)
	}
	if ids := MemberAllowedExtensionIDs(values); strings.Join(ids, ",") != "mcp:probe" {
		t.Fatalf("member extensions = %v", ids)
	}
	// 机器人级停用优先：服务在这台机器人上关掉后，成员开关不再生效。
	if _, err := AdministerExtensions(ctx, cfg, ExtensionAdminRequest{Operation: "enabled", Kind: "mcp", Name: "probe", ProfileID: "bot-a", Enabled: false}); err != nil {
		t.Fatal(err)
	}
	values, err = LoadExtensionOverrides(cfg.WorkDir, "bot-a")
	if err != nil {
		t.Fatal(err)
	}
	if ids := MemberAllowedExtensionIDs(values); len(ids) != 0 {
		t.Fatalf("disabled service still open to members: %v", ids)
	}
	// 另一台机器人不受影响，默认仍是仅主人。
	other, err := LoadExtensionOverrides(cfg.WorkDir, "bot-b")
	if err != nil {
		t.Fatal(err)
	}
	if ids := MemberAllowedExtensionIDs(other); len(ids) != 0 {
		t.Fatalf("member permission crossed robots: %v", ids)
	}
	if _, err := AdministerExtensions(ctx, cfg, ExtensionAdminRequest{Operation: "members", Kind: "builtin", Name: "probe", ProfileID: "bot-a", Enabled: true}); err == nil {
		t.Fatal("builtin plugin accepted a member permission it cannot enforce")
	}
	// Skill 走同一套开关，内置插件不参与。
	if _, err := AdministerExtensions(ctx, cfg, ExtensionAdminRequest{Operation: "save", Kind: "skill", Name: "guide", Content: "---\nname: guide\ndescription: Member guide\n---\nHello"}); err != nil {
		t.Fatal(err)
	}
	if _, err := AdministerExtensions(ctx, cfg, ExtensionAdminRequest{Operation: "members", Kind: "skill", Name: "guide", ProfileID: "bot-a", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	values, err = LoadExtensionOverrides(cfg.WorkDir, "bot-a")
	if err != nil {
		t.Fatal(err)
	}
	if ids := MemberAllowedExtensionIDs(values); strings.Join(ids, ",") != "skill:guide" {
		t.Fatalf("member extensions = %v", ids)
	}
}

func TestExtensionAudienceLimitsMemberAccess(t *testing.T) {
	cfg := Config{WorkDir: t.TempDir(), MCPConfigPath: "mcp.json"}
	ctx := context.Background()
	config := map[string]any{"url": "https://example.com/mcp", "enabled": true}
	for _, name := range []string{"probe", "other"} {
		if _, err := AdministerExtensions(ctx, cfg, ExtensionAdminRequest{Operation: "save", Kind: "mcp", Name: name, Config: config}); err != nil {
			t.Fatal(err)
		}
		if _, err := AdministerExtensions(ctx, cfg, ExtensionAdminRequest{Operation: "members", Kind: "mcp", Name: name, ProfileID: "bot-a", Enabled: true}); err != nil {
			t.Fatal(err)
		}
	}
	audience := ExtensionAudience{Users: []string{" 1001 ", "1002", "1001"}, Groups: []string{"g1"}}
	if _, err := AdministerExtensions(ctx, cfg, ExtensionAdminRequest{Operation: "audience", Kind: "mcp", Name: "probe", ProfileID: "bot-a", Audience: audience}); err != nil {
		t.Fatal(err)
	}
	overrides, err := LoadExtensionOverrides(cfg.WorkDir, "bot-a")
	if err != nil {
		t.Fatal(err)
	}
	audiences, err := LoadExtensionAudiences(cfg.WorkDir, "bot-a")
	if err != nil {
		t.Fatal(err)
	}
	saved := audiences["mcp:probe"]
	if strings.Join(saved.Users, ",") != "1001,1002" || strings.Join(saved.Groups, ",") != "g1" {
		t.Fatalf("audience = %#v，重复和空白没有清掉", saved)
	}
	cases := []struct {
		user, group string
		want        string
	}{
		{"1001", "g1", "mcp:other,mcp:probe"},
		{"1003", "g1", "mcp:other"},
		{"1001", "g2", "mcp:other"},
		{"1001", "", "mcp:other"},
	}
	for _, tt := range cases {
		got := MemberAllowedExtensionIDsFor(overrides, audiences, tt.user, tt.group)
		if strings.Join(got, ",") != tt.want {
			t.Fatalf("user=%q group=%q allowed=%v want=%s", tt.user, tt.group, got, tt.want)
		}
	}
	// 名单清空就是不限制，同时不在文件里留空记录。
	if _, err := AdministerExtensions(ctx, cfg, ExtensionAdminRequest{Operation: "audience", Kind: "mcp", Name: "probe", ProfileID: "bot-a"}); err != nil {
		t.Fatal(err)
	}
	audiences, err = LoadExtensionAudiences(cfg.WorkDir, "bot-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(audiences) != 0 {
		t.Fatalf("清空后仍有记录：%#v", audiences)
	}
	if got := MemberAllowedExtensionIDsFor(overrides, audiences, "1003", "g9"); strings.Join(got, ",") != "mcp:other,mcp:probe" {
		t.Fatalf("清空后仍在限制：%v", got)
	}
	// 关掉成员开关后，名单不能把权限找回来。
	if _, err := AdministerExtensions(ctx, cfg, ExtensionAdminRequest{Operation: "members", Kind: "mcp", Name: "probe", ProfileID: "bot-a", Enabled: false}); err != nil {
		t.Fatal(err)
	}
	overrides, err = LoadExtensionOverrides(cfg.WorkDir, "bot-a")
	if err != nil {
		t.Fatal(err)
	}
	if got := MemberAllowedExtensionIDsFor(overrides, audiences, "1001", "g1"); strings.Join(got, ",") != "mcp:other" {
		t.Fatalf("关掉成员开关后仍然开放：%v", got)
	}
}
