// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SuInk/diana/model/netguard"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestLockedBufferPreservesCompleteStderr(t *testing.T) {
	writer := &lockedBuffer{}
	prefix := strings.Repeat("stderr-segment-", 3000)
	if _, err := writer.Write([]byte(prefix)); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write([]byte("tail-marker")); err != nil {
		t.Fatal(err)
	}
	if got := writer.String(); got != prefix+"tail-marker" {
		t.Fatalf("stderr length = %d, want %d", len(got), len(prefix)+len("tail-marker"))
	}
}

func TestLockedBufferCapsStderrAtTail(t *testing.T) {
	writer := &lockedBuffer{}
	prefix := strings.Repeat("x", maxMCPStderrBytes)
	if _, err := writer.Write([]byte(prefix)); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write([]byte("tail-marker")); err != nil {
		t.Fatal(err)
	}
	got := writer.String()
	if len(got) != maxMCPStderrBytes || !strings.HasSuffix(got, "tail-marker") {
		t.Fatalf("stderr length = %d suffix preserved = %v", len(got), strings.HasSuffix(got, "tail-marker"))
	}
}

func TestSelfInstalledMCPUsesExplicitEnvironmentOnly(t *testing.T) {
	t.Setenv("DIANA_MCP_UNRELATED_SECRET", "do-not-inherit")
	t.Setenv("DIANA_MCP_REQUESTED_SECRET", "requested-value")
	server, err := mcpServerConfigFromInput(map[string]any{
		"command":  "demo",
		"required": true,
		"env": map[string]any{
			"EXPLICIT_TOKEN": "${DIANA_MCP_REQUESTED_SECRET}",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if server.InheritEnv == nil || *server.InheritEnv {
		t.Fatalf("self-installed server InheritEnv = %#v, want false", server.InheritEnv)
	}
	if server.Required {
		t.Fatal("self-installed MCP server must remain optional")
	}
	environment := mergedCommandEnvironment(server.Env, server.inheritEnvironment())
	joined := strings.Join(environment, "\n")
	if strings.Contains(joined, "DIANA_MCP_UNRELATED_SECRET=") {
		t.Fatalf("unrelated process secret leaked into MCP environment: %s", joined)
	}
	if !strings.Contains(joined, "EXPLICIT_TOKEN=requested-value") {
		t.Fatalf("explicit MCP environment value was not expanded: %s", joined)
	}
}

// TestMCPRegistryCallsStdioTool 验证 MCP stdio server 能被启动、列工具并调用。
func TestMCPRegistryCallsStdioTool(t *testing.T) {
	if os.Getenv("DIANA_AGENT_MCP_TEST_SERVER") == "1" {
		runMCPTestServer()
		return
	}
	dir := t.TempDir()
	configPath := filepath.Join(dir, ".mcp.json")
	body := fmt.Sprintf(`{
  "mcpServers": {
    "demo": {
      "command": %q,
      "args": ["-test.run=TestMCPRegistryCallsStdioTool"],
      "env": {"DIANA_AGENT_MCP_TEST_SERVER": "1"}
    }
  }
}`, os.Args[0])
	if err := os.WriteFile(configPath, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	registry, err := NewMCPRegistry(context.Background(), Config{WorkDir: dir, MCPConfigPath: configPath, MCPStartupTimeoutMS: 3000, MCPToolTimeoutMS: 3000})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		for _, closer := range registry.Closers {
			_ = closer.Close()
		}
	}()
	if len(registry.Tools) != 1 {
		t.Fatalf("tools = %#v", registry.Tools)
	}
	if registry.Tools[0].Name() != "mcp__demo__echo" {
		t.Fatalf("tool name = %q", registry.Tools[0].Name())
	}
	got, err := registry.Tools[0].Run(context.Background(), map[string]any{"text": "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if got != "echo: hello" {
		t.Fatalf("got = %q", got)
	}
}

func TestMCPToolSurvivesRequestViewClose(t *testing.T) {
	if os.Getenv("DIANA_AGENT_MCP_TEST_SERVER") == "1" {
		runMCPTestServer()
		return
	}
	dir := t.TempDir()
	configPath := filepath.Join(dir, ".mcp.json")
	body := fmt.Sprintf(`{"mcpServers":{"demo":{"command":%q,"args":["-test.run=TestMCPToolSurvivesRequestViewClose"],"env":{"DIANA_AGENT_MCP_TEST_SERVER":"1"}}}}`, os.Args[0])
	if err := os.WriteFile(configPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := Config{WorkDir: dir, MCPConfigPath: configPath, MCPStartupTimeoutMS: 3000, MCPToolTimeoutMS: 3000}
	base, err := NewAgentToolRegistry(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()
	first, err := base.NewView(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := first.Get("mcp__demo__echo"); !ok {
		t.Fatal("first request view is missing MCP tool")
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := base.NewView(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	echo, ok := second.Get("mcp__demo__echo")
	if !ok {
		t.Fatal("closing first request view closed shared MCP tool")
	}
	got, err := echo.Run(context.Background(), map[string]any{"text": "still-live"})
	if err != nil || got != "echo: still-live" {
		t.Fatalf("echo output=%q err=%v", got, err)
	}
}

func TestMCPInstallToolPersistsAndRefreshesRegistry(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{
		WorkDir:             dir,
		ExtensionManagement: true,
		MCPStartupTimeoutMS: 5000,
		MCPToolTimeoutMS:    3000,
	}
	registry, err := NewAgentToolRegistry(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	install, ok := registry.Get("mcp.install")
	if !ok {
		t.Fatal("mcp.install is missing")
	}
	registry.Retain(map[string]bool{"mcp.install": true})
	output, err := install.Run(context.Background(), map[string]any{
		"name":    "dynamic",
		"command": os.Args[0],
		"args":    []any{"-test.run=TestMCPRegistryCallsStdioTool"},
		"env":     map[string]any{"DIANA_AGENT_MCP_TEST_SERVER": "1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output, "mcp__dynamic__echo") {
		t.Fatalf("install output = %s", output)
	}
	echo, ok := registry.Get("mcp__dynamic__echo")
	if !ok {
		t.Fatal("installed MCP tool was not registered")
	}
	got, err := echo.Run(context.Background(), map[string]any{"text": "now"})
	if err != nil || got != "echo: now" {
		t.Fatalf("echo output=%q err=%v", got, err)
	}
	if err := registry.Close(); err != nil {
		t.Fatal(err)
	}

	reloaded, err := NewAgentToolRegistry(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := reloaded.Get("mcp__dynamic__echo"); !ok {
		t.Fatal("persisted MCP tool was not restored")
	}
	uninstall, _ := reloaded.Get("mcp.uninstall")
	if _, err := uninstall.Run(context.Background(), map[string]any{"name": "dynamic"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := reloaded.Get("mcp__dynamic__echo"); ok {
		t.Fatal("uninstalled MCP tool remains registered")
	}
	if err := reloaded.Close(); err != nil {
		t.Fatal(err)
	}
	servers, err := loadMCPServers(resolveMCPConfigPath(cfg.WithDefaults()))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := servers["dynamic"]; ok {
		t.Fatal("uninstalled MCP config remains on disk")
	}
}

func TestSaveMCPServersUsesPrivatePermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".mcp.json")
	if err := saveMCPServers(path, map[string]mcpServerConfig{
		"demo": {Command: "demo"},
	}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("MCP config permissions = %04o, want 0600", got)
	}
}

func TestMCPRegistryCallsStreamableHTTPTool(t *testing.T) {
	t.Setenv("DIANA_ALLOW_PRIVATE_HTTP_FETCHES", "true")
	server := newEchoMCPServer()
	handler := mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server { return server }, &mcpsdk.StreamableHTTPOptions{
		Stateless:    true,
		JSONResponse: true,
	})
	httpServer := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer test-token" {
			http.Error(response, "missing authorization", http.StatusUnauthorized)
			return
		}
		handler.ServeHTTP(response, request)
	}))
	defer httpServer.Close()

	dir := t.TempDir()
	configPath := filepath.Join(dir, ".mcp.json")
	body := fmt.Sprintf(`{"mcpServers":{"remote":{"url":%q,"headers":{"Authorization":"Bearer ${MCP_TEST_TOKEN}"}}}}`, httpServer.URL)
	if err := os.WriteFile(configPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MCP_TEST_TOKEN", "test-token")
	registry, err := NewMCPRegistry(context.Background(), Config{WorkDir: dir, MCPConfigPath: configPath, MCPStartupTimeoutMS: 5000})
	if err != nil {
		t.Fatal(err)
	}
	defer closeMCPClosers(registry.Closers)
	if len(registry.Tools) != 1 || registry.Tools[0].Name() != "mcp__remote__echo" {
		t.Fatalf("remote tools = %#v", registry.Tools)
	}
	got, err := registry.Tools[0].Run(context.Background(), map[string]any{"text": "remote"})
	if err != nil || got != "echo: remote" {
		t.Fatalf("remote output=%q err=%v", got, err)
	}
}

func TestMCPHeadersAreNotForwardedAcrossOrigins(t *testing.T) {
	t.Setenv("DIANA_ALLOW_PRIVATE_HTTP_FETCHES", "true")
	forwarded := make(chan string, 1)
	target := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		forwarded <- request.Header.Get("Authorization")
		response.WriteHeader(http.StatusNoContent)
	}))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		http.Redirect(response, request, target.URL, http.StatusTemporaryRedirect)
	}))
	defer source.Close()

	origin, err := url.Parse(source.URL)
	if err != nil {
		t.Fatal(err)
	}
	client := netguard.NewPublicHTTPClient(3 * time.Second)
	client.Transport = &mcpHeaderTransport{
		base:    client.Transport,
		headers: map[string]string{"Authorization": "Bearer private-token"},
		origin:  origin,
	}
	response, err := client.Get(source.URL)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if got := <-forwarded; got != "" {
		t.Fatalf("authorization header leaked across origins: %q", got)
	}
}

func runMCPTestServer() {
	server := newEchoMCPServer()
	_ = server.Run(context.Background(), &mcpsdk.StdioTransport{})
	os.Exit(0)
}

type echoMCPInput struct {
	Text string `json:"text"`
}

func newEchoMCPServer() *mcpsdk.Server {
	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "demo", Version: "0.0.1"}, nil)
	server.AddTool(&mcpsdk.Tool{
		Name:        "echo",
		Description: "Echo text.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}}}`),
	}, func(_ context.Context, request *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
		var input echoMCPInput
		if err := json.Unmarshal(request.Params.Arguments, &input); err != nil {
			return nil, err
		}
		return &mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: "echo: " + strings.TrimSpace(input.Text)}}}, nil
	})
	return server
}
