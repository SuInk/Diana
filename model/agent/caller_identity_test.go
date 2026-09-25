// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

var testCaller = CallerIdentity{
	Platform:  "qq",
	BotID:     "10001",
	UserID:    "10002",
	GroupID:   "20001",
	MessageID: "-4242",
	ChatType:  "group",
	IsOwner:   true,
}

// 回显 tools/call 收到的 _meta，测试据此判断身份有没有带过去。
func newMetaEchoMCPServer() *mcpsdk.Server {
	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "meta", Version: "0.0.1"}, nil)
	server.AddTool(&mcpsdk.Tool{
		Name:        "whoami",
		Description: "Echo request metadata.",
		InputSchema: json.RawMessage(`{"type":"object"}`),
	}, func(_ context.Context, request *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
		raw, err := json.Marshal(request.Params.Meta)
		if err != nil {
			return nil, err
		}
		return &mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: string(raw)}}}, nil
	})
	return server
}

func TestMCPCallerIdentityOnlySentToOptedInServer(t *testing.T) {
	t.Setenv("DIANA_ALLOW_PRIVATE_HTTP_FETCHES", "true")
	server := newMetaEchoMCPServer()
	handler := mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server { return server }, &mcpsdk.StreamableHTTPOptions{
		Stateless:    true,
		JSONResponse: true,
	})
	httpServer := httptest.NewServer(handler)
	defer httpServer.Close()

	dir := t.TempDir()
	configPath := filepath.Join(dir, ".mcp.json")
	body := fmt.Sprintf(`{"mcpServers":{"opted":{"url":%q,"expose_caller_identity":true},"plain":{"url":%q}}}`, httpServer.URL, httpServer.URL)
	if err := os.WriteFile(configPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	registry, err := NewMCPRegistry(context.Background(), Config{WorkDir: dir, MCPConfigPath: configPath, MCPStartupTimeoutMS: 5000})
	if err != nil {
		t.Fatal(err)
	}
	defer closeMCPClosers(registry.Closers)
	tools := map[string]Tool{}
	for _, tool := range registry.Tools {
		tools[tool.Name()] = tool
	}
	opted, plain := tools["mcp__opted__whoami"], tools["mcp__plain__whoami"]
	if opted == nil || plain == nil {
		t.Fatalf("tools = %#v", registry.Tools)
	}

	ctx := WithCallerIdentity(context.Background(), testCaller)
	got, err := opted.Run(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	// SDK 自己也往 _meta 里放协议版本等键，只看我们那一项。
	var meta map[string]json.RawMessage
	var caller map[string]any
	if err := json.Unmarshal([]byte(got), &meta); err != nil {
		t.Fatalf("meta = %q: %v", got, err)
	}
	if err := json.Unmarshal(meta[mcpCallerMetaKey], &caller); err != nil {
		t.Fatalf("meta = %q: %v", got, err)
	}
	want := map[string]any{"platform": "qq", "bot_id": "10001", "user_id": "10002", "group_id": "20001", "message_id": "-4242", "chat_type": "group", "is_owner": true}
	for key, value := range want {
		if caller[key] != value {
			t.Fatalf("caller[%s] = %#v, want %#v (meta %s)", key, caller[key], value, got)
		}
	}

	// 没开透传的服务一个字都不该收到。
	if got, err := plain.Run(ctx, nil); err != nil || strings.Contains(got, "10002") {
		t.Fatalf("plain server got %q err=%v", got, err)
	}
	// 没有触发消息的运行（定时任务）不带 _meta，也不编一个空身份。
	if got, err := opted.Run(context.Background(), nil); err != nil || strings.Contains(got, mcpCallerMetaKey) {
		t.Fatalf("no-caller run got %q err=%v", got, err)
	}
}

func TestRunCommandReceivesCallerEnvironment(t *testing.T) {
	// 宿主进程里同名的变量不能漏进子进程，冒充本次调用者。
	t.Setenv("DIANA_CALLER_USER_ID", "forged")
	t.Setenv("DIANA_CALLER_GROUP_ID", "forged-group")
	tool := &RunCommandTool{
		root:        t.TempDir(),
		allowlist:   commandAllowlistSet([]string{"env"}),
		timeout:     10 * time.Second,
		maxBytes:    DefaultMaxToolOutputChars,
		sandboxMode: CommandSandboxOff,
	}

	private := testCaller
	private.ChatType, private.GroupID, private.IsOwner = "private", "", false
	out, err := tool.Run(WithCallerIdentity(context.Background(), private), map[string]any{"command": "env"})
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{"DIANA_CALLER_USER_ID=10002", "DIANA_CALLER_CHAT_TYPE=private", "DIANA_CALLER_IS_OWNER=0", "DIANA_CALLER_PLATFORM=qq"} {
		if !strings.Contains(out, line) {
			t.Fatalf("missing %s in:\n%s", line, out)
		}
	}
	if strings.Contains(out, "forged") || strings.Contains(out, "DIANA_CALLER_GROUP_ID") {
		t.Fatalf("host or empty caller variables leaked:\n%s", out)
	}

	out, err = tool.Run(context.Background(), map[string]any{"command": "env"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "DIANA_CALLER_") {
		t.Fatalf("run without caller still set caller variables:\n%s", out)
	}
}

// 对话里的安装工具打不开身份透传：模型交来的 true 不认；覆盖同一去处时保留主人
// 在界面上开的设置，换了地址就关掉，身份不能跟着去新地方。
func TestAgentInstallCannotEnableCallerIdentity(t *testing.T) {
	cfg := Config{WorkDir: t.TempDir(), ExtensionManagement: true}
	disabled := false
	writeMCPServers(t, cfg, map[string]mcpServerConfig{
		"opted": {URL: "https://mcp.example.com/mcp", ExposeCallerIdentity: true, Enabled: &disabled},
	})
	registry, err := NewAgentToolRegistry(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer registry.Close()
	install, ok := registry.Get("mcp_install")
	if !ok {
		t.Fatal("mcp_install 不在")
	}
	stored := func(name string) mcpServerConfig {
		servers, _ := loadMCPServers(resolveMCPConfigPath(cfg.WithDefaults()))
		return servers[name]
	}
	run := func(input map[string]any) {
		t.Helper()
		input["enabled"] = false
		server, err := mcpServerConfigFromInput(input)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := install.(*MCPInstallTool).manager.installMCP(context.Background(), input["name"].(string), server, true); err != nil {
			t.Fatal(err)
		}
	}

	run(map[string]any{"name": "fresh", "url": "https://other.example.com/mcp", "expose_caller_identity": true})
	if stored("fresh").ExposeCallerIdentity {
		t.Fatal("模型装的新服务不该开身份透传")
	}
	run(map[string]any{"name": "opted", "url": "https://mcp.example.com/mcp", "tool_timeout_sec": 30})
	if !stored("opted").ExposeCallerIdentity {
		t.Fatal("同一地址只改超时，不该关掉主人开的透传")
	}
	run(map[string]any{"name": "opted", "url": "https://attacker.example.net/mcp", "expose_caller_identity": true})
	if stored("opted").ExposeCallerIdentity {
		t.Fatal("换了地址，身份透传必须关掉")
	}
}
