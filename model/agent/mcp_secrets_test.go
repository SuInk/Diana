// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strings"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// 一个 40 位的 GitHub 风格令牌，头尾分别是 ghp_ 和 abcd，正好对上 issue 里的掩码示例。
const leakyToken = "ghp_0123456789ABCDEFGHIJKLMNOPQRSTUVabcd"

func TestMaskSecret(t *testing.T) {
	cases := map[string]string{
		"":                       "",
		"short":                  "****",
		"secret-token":           "se****en",
		leakyToken:               "ghp_****abcd",
		"Bearer " + leakyToken:   "Bearer ghp_****abcd",
		"token " + leakyToken:    "token ghp_****abcd",
		"  " + leakyToken + "  ": "ghp_****abcd",
	}
	for input, want := range cases {
		if got := maskSecret(input); got != want {
			t.Errorf("maskSecret(%q) = %q，期望 %q", input, got, want)
		}
	}
	// 带空格但不是「方案 凭据」两段式的，整串按一个值遮。
	if got := maskSecret("not a scheme value at all"); strings.Contains(got, "scheme") {
		t.Fatalf("普通带空格的值不该按认证方案拆开：%q", got)
	}
}

func TestMCPRedactorCoversEchoedCredentials(t *testing.T) {
	t.Setenv("DIANA_TEST_REFERENCED_TOKEN", "env-referenced-secret-value")
	redactor := newMCPRedactor(mcpServerConfig{
		URL: "https://mcp.example.com/mcp?key=query-secret-value&region=cn",
		Headers: map[string]string{
			"Authorization": "Bearer " + leakyToken,
			"X-From-Env":    "${DIANA_TEST_REFERENCED_TOKEN}",
		},
		Env: map[string]string{
			"GITEA_ACCESS_TOKEN": "gitea-access-token-value",
			"GITEA_HOST":         "https://git.example.com",
			"DATABASE_URL":       "postgres://diana:db-password-value@db.example.com/diana",
		},
	})
	text := strings.Join([]string{
		"Authorization: Bearer " + leakyToken,
		"raw " + leakyToken,
		"expanded env-referenced-secret-value",
		`Post "https://mcp.example.com/mcp?key=query-secret-value": dial tcp`,
		"GITEA_ACCESS_TOKEN=gitea-access-token-value",
		"db-password-value",
		"see https://git.example.com/owner/repo",
	}, "\n")
	got := redactor.text(text)
	for _, secret := range []string{leakyToken, "env-referenced-secret-value", "query-secret-value", "gitea-access-token-value", "db-password-value"} {
		if strings.Contains(got, secret) {
			t.Fatalf("%q 没有被遮住：\n%s", secret, got)
		}
	}
	if !strings.Contains(got, "ghp_****abcd") {
		t.Fatalf("遮完应当留下可辨认的掩码：\n%s", got)
	}
	// 实例地址是普通配置：遮掉它，工具结果里的仓库链接就全坏了。
	if !strings.Contains(got, "https://git.example.com/owner/repo") {
		t.Fatalf("非凭据的实例地址不该被遮：\n%s", got)
	}

	base := errors.New("connect failed: " + leakyToken)
	wrapped := redactor.error(base)
	if strings.Contains(wrapped.Error(), leakyToken) || !errors.Is(wrapped, base) {
		t.Fatalf("报错要遮住令牌且保留错误链：%v", wrapped)
	}
}

// leakyMCPHandler 是一个故意把凭据往回吐的 MCP 服务：工具描述里写着令牌，调用结果
// 回显收到的 Authorization，出错时报错文本里也带着它。真实世界里这对应调试输出、把
// 配置原样打进报错的服务。
func leakyMCPHandler(t *testing.T) *httptest.Server {
	t.Helper()
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received := r.Header.Get("Authorization")
		if received != "Bearer "+leakyToken {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "leaky", Version: "1"}, nil)
		server.AddTool(&mcpsdk.Tool{
			Name:        "whoami",
			Description: "Uses credential " + received,
			InputSchema: json.RawMessage(`{"type":"object","properties":{"fail":{"type":"boolean","description":"token ` + leakyToken + `"}}}`),
		}, func(_ context.Context, request *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
			var input struct {
				Fail bool `json:"fail"`
			}
			_ = json.Unmarshal(request.Params.Arguments, &input)
			return &mcpsdk.CallToolResult{IsError: input.Fail, Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: "authorized with " + received}}}, nil
		})
		mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server { return server }, &mcpsdk.StreamableHTTPOptions{Stateless: true, JSONResponse: true}).ServeHTTP(w, r)
	}))
	t.Cleanup(httpServer.Close)
	return httpServer
}

func writeMCPServers(t *testing.T, cfg Config, servers map[string]mcpServerConfig) {
	t.Helper()
	if err := saveMCPServers(resolveMCPConfigPath(cfg.WithDefaults()), servers); err != nil {
		t.Fatal(err)
	}
}

// 验收的第一条：Agent 能碰到的每一处（工具列表里的描述和 schema、调用结果、调用报错、
// 能力目录、连不上时的报错）都只有掩码。第二条也在这里：掩码不影响正常调用——服务端
// 只认真令牌，调得通就说明运行时发出去的是原文。
func TestAgentNeverSeesMCPTokenInOutputs(t *testing.T) {
	t.Setenv("DIANA_ALLOW_PRIVATE_HTTP_FETCHES", "true")
	upstream := leakyMCPHandler(t)
	cfg := Config{WorkDir: t.TempDir(), ExtensionManagement: true}
	writeMCPServers(t, cfg, map[string]mcpServerConfig{
		"leaky": {URL: upstream.URL, Headers: map[string]string{"Authorization": "Bearer " + leakyToken}},
		// 连不上的那条：地址查询参数里带着令牌，Go 的 HTTP 报错会把整个 URL 打出来。
		"broken": {URL: "http://127.0.0.1:1/mcp?access_token=" + leakyToken},
	})
	registry, err := NewAgentToolRegistry(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer registry.Close()

	var seen []string
	tool, ok := registry.Get(mcpModelToolName("leaky", "whoami"))
	if !ok {
		t.Fatalf("没注册上 leaky 的工具：%v", registry.Names())
	}
	schema, _ := json.Marshal(tool.(*MCPTool).InputSchema())
	seen = append(seen, tool.Description(), string(schema))
	output, err := tool.Run(context.Background(), map[string]any{})
	if err != nil {
		t.Fatalf("带真令牌的调用应当成功：%v", err)
	}
	if !strings.Contains(output, "authorized with Bearer ghp_****abcd") {
		t.Fatalf("回显的令牌应当换成掩码：%q", output)
	}
	seen = append(seen, output)
	failed, err := tool.Run(context.Background(), map[string]any{"fail": true})
	if err == nil {
		t.Fatal("服务端报错应当透传成工具报错")
	}
	seen = append(seen, failed, err.Error())

	list, ok := registry.Get("list_capabilities")
	if !ok {
		t.Fatal("list_capabilities 不在")
	}
	catalog, err := list.Run(context.Background(), map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	seen = append(seen, catalog)
	if !strings.Contains(catalog, `"masked": "Bearer ghp_****abcd"`) || !strings.Contains(catalog, `"configured": true`) {
		t.Fatalf("能力目录应当给出掩码和「已配置」：%s", catalog)
	}
	if !strings.Contains(catalog, `"name": "broken"`) || !strings.Contains(catalog, `"error"`) {
		t.Fatalf("连不上的那条应当在目录里带着报错：%s", catalog)
	}

	for _, text := range seen {
		if strings.Contains(text, leakyToken) {
			t.Fatalf("令牌原文进了模型可见的文本：%s", text)
		}
	}
}

// 验收的第二条的另一半：WebUI 的「测试连接」交回的是掩码，也得用真令牌去连。
func TestAdminTestConnectionWithMaskUsesStoredToken(t *testing.T) {
	t.Setenv("DIANA_ALLOW_PRIVATE_HTTP_FETCHES", "true")
	upstream := leakyMCPHandler(t)
	cfg := Config{WorkDir: t.TempDir()}
	ctx := context.Background()
	if _, err := AdministerExtensions(ctx, cfg, ExtensionAdminRequest{Operation: "save", Kind: "mcp", Name: "leaky", Config: map[string]any{"url": upstream.URL, "headers": map[string]any{"Authorization": "Bearer " + leakyToken}}}); err != nil {
		t.Fatal(err)
	}
	read, err := AdministerExtensions(ctx, cfg, ExtensionAdminRequest{Operation: "read", Kind: "mcp", Name: "leaky"})
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(read)
	if strings.Contains(string(body), leakyToken) {
		t.Fatalf("read 不能带出原文：%s", body)
	}
	config := read.(map[string]any)["config"].(map[string]any)
	headers := config["headers"].(map[string]string)
	if headers["Authorization"] != "Bearer ghp_****abcd" {
		t.Fatalf("read 应当给掩码，旧界面照样拿得到键名：%#v", headers)
	}

	// 界面把读到的配置原样交回来：掩码就是「保持原值」。
	echoed := map[string]any{"url": upstream.URL, "headers": map[string]any{"Authorization": headers["Authorization"]}}
	result, err := AdministerExtensions(ctx, cfg, ExtensionAdminRequest{Operation: "test", Kind: "mcp", Name: "leaky", Config: echoed})
	if err != nil {
		t.Fatalf("交回掩码测试连接应当用已保存的令牌：%v", err)
	}
	if connected, _ := result.(map[string]any)["connected"].(bool); !connected {
		t.Fatalf("测试连接没连上：%#v", result)
	}
	if _, err := AdministerExtensions(ctx, cfg, ExtensionAdminRequest{Operation: "save", Kind: "mcp", Name: "leaky", Replace: true, Config: echoed}); err != nil {
		t.Fatal(err)
	}
	servers, _ := loadMCPServers(resolveMCPConfigPath(cfg.WithDefaults()))
	if servers["leaky"].Headers["Authorization"] != "Bearer "+leakyToken {
		t.Fatalf("交回掩码保存把令牌覆盖了：%q", servers["leaky"].Headers["Authorization"])
	}

	// 对不上的掩码不能当令牌存下去。
	wrong := map[string]any{"url": upstream.URL, "headers": map[string]any{"Authorization": "Bearer ghp_****zzzz"}}
	if _, err := AdministerExtensions(ctx, cfg, ExtensionAdminRequest{Operation: "save", Kind: "mcp", Name: "leaky", Replace: true, Config: wrong}); err == nil {
		t.Fatal("对不上的掩码应当拒绝保存")
	}

	// 主人要看原文走 reveal。
	revealed, err := AdministerExtensions(ctx, cfg, ExtensionAdminRequest{Operation: "reveal", Kind: "mcp", Name: "leaky"})
	if err != nil {
		t.Fatal(err)
	}
	if got := revealed.(map[string]any)["headers"].(map[string]string)["Authorization"]; got != "Bearer "+leakyToken {
		t.Fatalf("reveal 应当给主人原文：%q", got)
	}
}

func TestAdminPresetReadMasksAndRevealsSecrets(t *testing.T) {
	cfg := Config{WorkDir: t.TempDir()}
	ctx := context.Background()
	writeMCPServers(t, cfg, map[string]mcpServerConfig{
		"gitea": {Command: "gitea-mcp", Args: []string{"-t", "stdio"}, Env: map[string]string{"GITEA_HOST": "https://git.example.com", "GITEA_ACCESS_TOKEN": leakyToken}, Preset: "gitea", PresetTransport: "stdio"},
	})
	read, err := AdministerExtensions(ctx, cfg, ExtensionAdminRequest{Operation: "read", Kind: "mcp", Name: "gitea"})
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(read)
	if strings.Contains(string(body), leakyToken) {
		t.Fatalf("预设 read 不能带出原文：%s", body)
	}
	detail := read.(map[string]any)
	if masks := detail["preset_secret_masks"].(map[string]string); masks["token"] != "ghp_****abcd" {
		t.Fatalf("预设表单要拿到令牌掩码：%#v", masks)
	}
	if values := detail["preset_values"].(map[string]string); values["token"] != "" || values["host"] != "https://git.example.com" {
		t.Fatalf("非机密字段照旧回填，令牌不回填：%#v", values)
	}
	revealed, err := AdministerExtensions(ctx, cfg, ExtensionAdminRequest{Operation: "reveal", Kind: "mcp", Name: "gitea"})
	if err != nil {
		t.Fatal(err)
	}
	if got := revealed.(map[string]any)["preset_secrets"].(map[string]string)["token"]; got != leakyToken {
		t.Fatalf("reveal 应当给出预设令牌原文：%q", got)
	}
}

// Agent 改配置时只见过掩码。交回掩码是「保留原令牌」，但只能在去处不变时成立：
// 换了地址或命令还把原文填回去，就等于替模型把令牌送出去。
func TestAgentInstallKeepsMaskedTokenOnlyForSameTarget(t *testing.T) {
	cfg := Config{WorkDir: t.TempDir(), ExtensionManagement: true}
	disabled := false
	writeMCPServers(t, cfg, map[string]mcpServerConfig{
		"remote": {URL: "https://mcp.example.com/mcp", Headers: map[string]string{"Authorization": "Bearer " + leakyToken}, Enabled: &disabled},
		"local":  {Command: "gitea-mcp", Args: []string{"-t", "stdio"}, Env: map[string]string{"GITEA_ACCESS_TOKEN": leakyToken}, Enabled: &disabled},
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
	manager := install.(*MCPInstallTool).manager
	stored := func(name string) mcpServerConfig {
		servers, _ := loadMCPServers(resolveMCPConfigPath(cfg.WithDefaults()))
		return servers[name]
	}
	mask := maskSecret(leakyToken)

	// 同一个地址，只改超时：令牌照旧。
	if _, err := manager.installMCP(context.Background(), "remote", mcpServerConfig{URL: "https://mcp.example.com/other", Headers: map[string]string{"Authorization": "Bearer " + mask}, ToolTimeoutSec: 30, Enabled: &disabled}, true); err != nil {
		t.Fatalf("同源交回掩码应当保留令牌：%v", err)
	}
	if got := stored("remote").Headers["Authorization"]; got != "Bearer "+leakyToken {
		t.Fatalf("令牌被掩码覆盖了：%q", got)
	}
	// 换到别的主机：拒绝，原配置不动。
	if _, err := manager.installMCP(context.Background(), "remote", mcpServerConfig{URL: "https://attacker.example.net/mcp", Headers: map[string]string{"Authorization": "Bearer " + mask}, Enabled: &disabled}, true); err == nil {
		t.Fatal("换了地址还交回掩码，应当拒绝")
	}
	if got := stored("remote"); got.URL != "https://mcp.example.com/other" || got.Headers["Authorization"] != "Bearer "+leakyToken {
		t.Fatalf("被拒绝的改动不能落盘：%#v", got)
	}

	// stdio 同理：命令和参数不变才把环境变量里的令牌填回去。
	if _, err := manager.installMCP(context.Background(), "local", mcpServerConfig{Command: "gitea-mcp", Args: []string{"-t", "stdio"}, Env: map[string]string{"GITEA_ACCESS_TOKEN": mask}, Enabled: &disabled}, true); err != nil {
		t.Fatalf("命令不变交回掩码应当保留令牌：%v", err)
	}
	if got := stored("local").Env["GITEA_ACCESS_TOKEN"]; got != leakyToken {
		t.Fatalf("令牌被掩码覆盖了：%q", got)
	}
	if _, err := manager.installMCP(context.Background(), "local", mcpServerConfig{Command: "sh", Args: []string{"-c", "env"}, Env: map[string]string{"GITEA_ACCESS_TOKEN": mask}, Enabled: &disabled}, true); err == nil {
		t.Fatal("换了命令还交回掩码，应当拒绝")
	}
	// 对不上的掩码、新装时交掩码，都不能当令牌存。
	if _, err := manager.installMCP(context.Background(), "local", mcpServerConfig{Command: "gitea-mcp", Args: []string{"-t", "stdio"}, Env: map[string]string{"GITEA_ACCESS_TOKEN": "ghp_****zzzz"}, Enabled: &disabled}, true); err == nil {
		t.Fatal("对不上的掩码应当拒绝")
	}
	if _, err := manager.installMCP(context.Background(), "fresh", mcpServerConfig{URL: "https://mcp.example.com/mcp", Headers: map[string]string{"Authorization": "Bearer " + mask}, Enabled: &disabled}, false); err == nil {
		t.Fatal("新装的服务没有原值可沿用，交掩码应当拒绝")
	}
}

// MCP 配置用 ${NAME} 引用进程环境里的令牌时，run_command 继承下来的环境里不能有它：
// 沙箱挡的是文件，挡不住环境变量，白名单里有 env 就能打出来。
func TestRunCommandDropsMCPReferencedEnvironment(t *testing.T) {
	t.Setenv("DIANA_TEST_MCP_TOKEN", leakyToken)
	t.Setenv("DIANA_TEST_UNRELATED", "still-here")
	cfg := Config{WorkDir: t.TempDir(), CommandAllowlist: []string{"env"}, CommandSandbox: CommandSandboxOff}
	writeMCPServers(t, cfg, map[string]mcpServerConfig{
		"remote": {URL: "https://mcp.example.com/mcp", Headers: map[string]string{"Authorization": "Bearer ${DIANA_TEST_MCP_TOKEN}"}},
	})
	registry, err := NewDefaultToolRegistry(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer registry.Close()
	tool, ok := registry.Get("run_command")
	if !ok {
		t.Fatal("run_command 不在")
	}
	environ := tool.(*RunCommandTool).commandEnvironment()
	joined := strings.Join(environ, "\n")
	if strings.Contains(joined, "DIANA_TEST_MCP_TOKEN") || !strings.Contains(joined, "DIANA_TEST_UNRELATED=still-here") {
		t.Fatalf("应当只摘掉被 MCP 引用的变量：%v", environ)
	}
	if runtime.GOOS == "windows" {
		return
	}
	if _, err := os.Stat("/usr/bin/env"); err != nil {
		t.Skip("没有 env 命令")
	}
	output, err := tool.Run(context.Background(), map[string]any{"command": "env"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output, leakyToken) || !strings.Contains(output, "still-here") {
		t.Fatalf("env 的输出里不该有 MCP 令牌：%s", output)
	}
}
