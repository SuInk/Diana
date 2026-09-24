// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SuInk/diana/internal/secretmask"
	"github.com/SuInk/diana/model/llm"
)

// 测试里的假凭据一律拆开拼接，公开仓库审计按连续字面量找凭据。

// httpFailingTool 真的发一次连不上的 HTTP 请求，把 net/http 的原始报错交回去——
// 报错里带着整条地址，查询参数里是令牌。插件、平台接口、订阅抓取都是这个形状。
type httpFailingTool struct{ url string }

func (*httpFailingTool) Name() string        { return "fetch_feed" }
func (*httpFailingTool) Description() string { return "fetches a feed" }
func (t *httpFailingTool) Run(ctx context.Context, _ map[string]any) (string, error) {
	client := &http.Client{Timeout: 2 * time.Second}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, t.url, nil)
	if err != nil {
		return "", err
	}
	response, err := client.Do(request)
	if err != nil {
		return "", fmt.Errorf("抓取 Feed 失败: %w", err)
	}
	_ = response.Body.Close()
	return "", errors.New("unexpected success")
}

type echoTool struct{ output string }

func (*echoTool) Name() string                                          { return "echo_config" }
func (*echoTool) Description() string                                   { return "echoes" }
func (t *echoTool) Run(context.Context, map[string]any) (string, error) { return t.output, nil }

func closedLocalAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	_ = listener.Close()
	return address
}

func modelVisibleText(requests []llm.GenerateRequest) string {
	var builder strings.Builder
	for _, request := range requests {
		for _, message := range request.Messages {
			builder.WriteString(message.Content)
			for _, part := range message.Parts {
				builder.WriteString(part.Text)
			}
			builder.WriteString("\n")
		}
	}
	return builder.String()
}

// 工具报错进模型上下文之前统一过一遍掩码：*url.Error 里的查询参数令牌不能原样
// 进工具结果，也不能进运行记录（下一轮的 carryover 读的就是它）。
func TestRunnerMasksCredentialsInToolErrors(t *testing.T) {
	token := "feedtok_" + "Q2w3E4r5T6y7U8i9"
	registry := NewToolRegistry()
	registry.Register(&httpFailingTool{url: "http://" + closedLocalAddress(t) + "/o/r.rss?access_token=" + token})
	client := &scriptedClient{responses: []string{
		`{"action":"tool","tool":"fetch_feed","input":{}}`,
		`{"action":"final","content":"抓不到"}`,
	}}
	runner, err := NewRunner(client, Config{WorkDir: t.TempDir(), MaxSteps: 2}, registry)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := runner.Run(context.Background(), Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "看看订阅"}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Steps) != 1 || resp.Steps[0].Error == "" {
		t.Fatalf("应当有一步失败的工具调用：%#v", resp.Steps)
	}
	if strings.Contains(resp.Steps[0].Error, token) {
		t.Fatalf("运行记录里有令牌原文：%s", resp.Steps[0].Error)
	}
	if !strings.Contains(resp.Steps[0].Error, "access_token="+secretmask.Mask(token)) {
		t.Fatalf("报错应当保留地址、只遮令牌，模型才知道是哪条请求失败：%s", resp.Steps[0].Error)
	}
	if visible := modelVisibleText(client.requests); strings.Contains(visible, token) {
		t.Fatalf("模型上下文里有令牌原文：%s", visible)
	}
}

// 已登记的凭据（LLM Key、平台令牌、插件 Cookie）出现在工具正常输出里也只给掩码。
func TestRunnerMasksRegisteredSecretsInToolOutput(t *testing.T) {
	apiKey := "sk-" + "runner0123456789abcdefgh"
	secretmask.Register(apiKey)
	signed := "https://cdn.example/v.mp4?signature=0123456789abcdef&e=1"
	registry := NewToolRegistry()
	registry.Register(&echoTool{output: `{"base_url":"https://relay.example/v1","echo":"invalid key ` + apiKey + `","video":"` + signed + `"}`})
	client := &scriptedClient{responses: []string{
		`{"action":"tool","tool":"echo_config","input":{}}`,
		`{"action":"final","content":"好了"}`,
	}}
	runner, err := NewRunner(client, Config{WorkDir: t.TempDir(), MaxSteps: 2}, registry)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := runner.Run(context.Background(), Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "看配置"}}})
	if err != nil {
		t.Fatal(err)
	}
	visible := modelVisibleText(client.requests)
	if strings.Contains(visible, apiKey) || strings.Contains(resp.Steps[0].Output, apiKey) {
		t.Fatalf("工具输出里的已登记凭据没遮：%s", visible)
	}
	if !strings.Contains(visible, signed) {
		t.Fatalf("正常输出里的签名链接不该被改：%s", visible)
	}
}

// run_command 的环境里不能有主人的凭据：白名单里有 env、printenv 时它们就是原文。
func TestCommandEnvironmentDropsCredentialVariables(t *testing.T) {
	proxyPassword := "pp" + "0123456789ab"
	environ := []string{
		"PATH=/usr/bin",
		"PWD=/tmp/work",
		"LANG=zh_CN.UTF-8",
		"TAVILY_API_KEY=" + "tvly-" + "0123456789abcdef",
		"DIANA_BILI_SESSDATA=" + "abc0123456789",
		"DIANA_CODEX_KEY=" + "sk-" + "codex0123456789abcdef",
		"DIANA_RESOLVER_PROXY=http://" + "u:" + proxyPassword + "@127.0.0.1:7890",
		"DIANA_SECRETS_FILE=/run/secrets/diana",
	}
	got := strings.Join(commandEnvironmentFor(environ, ""), "\n")
	for _, dropped := range []string{"TAVILY_API_KEY", "DIANA_BILI_SESSDATA", "DIANA_CODEX_KEY", proxyPassword} {
		if strings.Contains(got, dropped) {
			t.Fatalf("凭据变量 %s 不该留在命令环境里：\n%s", dropped, got)
		}
	}
	for _, kept := range []string{"PATH=/usr/bin", "PWD=/tmp/work", "LANG=zh_CN.UTF-8", "DIANA_SECRETS_FILE=/run/secrets/diana"} {
		if !strings.Contains(got, kept) {
			t.Fatalf("普通变量 %s 被误摘了：\n%s", kept, got)
		}
	}
}

// config.yaml、数据库、编码代理的登录目录这些凭据落脚点登记之后，文件工具和命令
// 沙箱都不放行。目录按前缀挡，里面的每一层都算。
func TestProtectRuntimePathsBlocksFilesAndDirectories(t *testing.T) {
	workDir := t.TempDir()
	authDir := filepath.Join(workDir, "coding-runtime", "auth")
	authFile := filepath.Join(authDir, "codex", "p1", "auth.json")
	if err := os.MkdirAll(filepath.Dir(authFile), 0o700); err != nil {
		t.Fatal(err)
	}
	refreshToken := "rt_" + "0123456789abcdefghij"
	if err := os.WriteFile(authFile, []byte(`{"refresh_token":"`+refreshToken+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	configCopy := filepath.Join(workDir, "config.yaml")
	if err := os.WriteFile(configCopy, []byte("admin:\n  password: "+refreshToken+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ProtectRuntimeDirs(authDir)
	ProtectRuntimeFiles(configCopy)
	registry, err := NewDefaultToolRegistry(Config{WorkDir: workDir}.WithDefaults())
	if err != nil {
		t.Fatal(err)
	}
	defer registry.Close()
	run := func(name string, input map[string]any) (string, error) {
		tool, ok := registry.Get(name)
		if !ok {
			t.Fatalf("%s 没注册", name)
		}
		return tool.Run(context.Background(), input)
	}
	for _, path := range []string{"coding-runtime/auth/codex/p1/auth.json", "config.yaml"} {
		if out, err := run("read_file", map[string]any{"path": path}); err == nil {
			t.Fatalf("read_file 读出了 %s：%s", path, out)
		}
	}
	if out, err := run("list_files", map[string]any{"path": "coding-runtime/auth/codex"}); err == nil {
		t.Fatalf("list_files 列出了登录目录：%s", out)
	}
	if out, _ := run("grep", map[string]any{"pattern": "rt_"}); strings.Contains(out, refreshToken) {
		t.Fatalf("grep 把登录令牌捞出来了：%s", out)
	}
	// 发附件、看图走 WorkspaceFileProtected，运行时登记的路径同样要挡。
	for _, rel := range []string{"coding-runtime/auth/codex/p1/auth.json", "config.yaml"} {
		if !WorkspaceFileProtected(Config{WorkDir: workDir}, rel) {
			t.Fatalf("WorkspaceFileProtected 放行了登记过的凭据 %s", rel)
		}
	}

	protected := agentProtectedFiles(Config{WorkDir: workDir})
	existing := protected.existingPaths()
	joined := strings.Join(existing, "\n")
	resolvedAuth, _ := filepath.EvalSymlinks(authDir)
	if !strings.Contains(joined, resolvedAuth) && !strings.Contains(joined, authDir) {
		t.Fatalf("沙箱拿到的挡读清单里没有登录目录：%v", existing)
	}
	profile := sandboxExecProfile(workDir, false, []string{authDir, configCopy})
	if !strings.Contains(profile, `(deny file-read* (subpath "`+authDir+`"))`) || !strings.Contains(profile, `(deny file-read* (literal "`+configCopy+`"))`) {
		t.Fatalf("sandbox-exec 应当按子路径挡目录、按字面挡文件：%s", profile)
	}
	cmd := wrapWithBubblewrap("bwrap")(context.Background(), workDir, false, []string{authDir, configCopy}, "cat", nil)
	args := strings.Join(cmd.Args, " ")
	if !strings.Contains(args, "--tmpfs "+authDir) || !strings.Contains(args, "--ro-bind "+os.DevNull+" "+configCopy) {
		t.Fatalf("bubblewrap 应当用空 tmpfs 盖住目录、用 /dev/null 盖住文件：%v", cmd.Args)
	}
}

// 常驻浏览器带着主人的登录态，browser_eval 读 document.cookie 交回来的会话 Cookie
// 只给掩码。
func TestBrowserEvalMasksPageCookies(t *testing.T) {
	session := "sess" + "0123456789abcdefXYZ"
	f := newFakeCDP(t, "https://example.com/")
	f.cookies = []string{session, "zh-CN"}
	f.evalValue = "lang=zh-CN; SESSDATA=" + session
	registry := browserToolsFor(t, f, newBrowserTabRegistry(), "bot\x00group:1", 5*time.Second)
	if _, err := runBrowserTool(context.Background(), registry, "browser_open", map[string]any{"url": "https://example.com/"}); err != nil {
		t.Fatal(err)
	}
	out, err := runBrowserTool(context.Background(), registry, "browser_eval", map[string]any{"script": "document.cookie"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, session) {
		t.Fatalf("会话 Cookie 原样交给了模型：%s", out)
	}
	if !strings.Contains(out, "lang=zh-CN") || !strings.Contains(out, secretmask.Mask(session)) {
		t.Fatalf("短值不该被遮，长值应当换成掩码：%s", out)
	}
}
