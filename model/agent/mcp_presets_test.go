// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// 预设装出来的必须是一份能过校验的普通 MCP 配置，不然「一键装上」只是把错误
// 推迟到连接的时候。
func TestGiteaPresetProducesValidConfigs(t *testing.T) {
	http, err := mcpPresetConfig("gitea", "http", map[string]string{"url": "http://127.0.0.1:8080/mcp"}, false)
	if err != nil {
		t.Fatal(err)
	}
	server, err := mcpServerConfigFromInput(http)
	if err != nil {
		t.Fatal(err)
	}
	if err := server.validate(); err != nil {
		t.Fatalf("远程预设配置不合法：%v", err)
	}
	if len(server.Headers) != 0 {
		t.Fatalf("没填 Authorization 时不该带请求头：%#v", server.Headers)
	}

	stdio, err := mcpPresetConfig("gitea", "stdio", map[string]string{"host": "https://git.example.com", "token": "abc"}, false)
	if err != nil {
		t.Fatal(err)
	}
	server, err = mcpServerConfigFromInput(stdio)
	if err != nil {
		t.Fatal(err)
	}
	if err := server.validate(); err != nil {
		t.Fatalf("stdio 预设配置不合法：%v", err)
	}
	if filepath.Base(server.Command) != bundledGiteaMCPName() || server.Env["GITEA_HOST"] != "https://git.example.com" || server.Env["GITEA_ACCESS_TOKEN"] != "abc" {
		t.Fatalf("stdio 预设没按 gitea-mcp 的约定拼：%#v", server)
	}

	// 填了可执行文件就按填的走，自己编译的版本不能被自带的那份顶掉。
	custom, err := mcpPresetConfig("gitea", "stdio", map[string]string{"host": "https://git.example.com", "token": "abc", "command": "/opt/bin/gitea-mcp"}, false)
	if err != nil {
		t.Fatal(err)
	}
	server, err = mcpServerConfigFromInput(custom)
	if err != nil {
		t.Fatal(err)
	}
	if server.Command != "/opt/bin/gitea-mcp" {
		t.Fatalf("自填的可执行文件被改写了：%q", server.Command)
	}
}

// 填了 Authorization 才带请求头，并且按填的原样带。
func TestGiteaPresetKeepsAuthorizationHeader(t *testing.T) {
	config, err := mcpPresetConfig("gitea", "http", map[string]string{"url": "http://127.0.0.1:8080/mcp", "authorization": "Bearer t"}, false)
	if err != nil {
		t.Fatal(err)
	}
	server, err := mcpServerConfigFromInput(config)
	if err != nil {
		t.Fatal(err)
	}
	if server.Headers["Authorization"] != "Bearer t" {
		t.Fatalf("请求头 = %#v", server.Headers)
	}
}

// 缺必填项要直接说缺哪个，不能等连接失败再让人回头猜。
func TestPresetConfigReportsMissingField(t *testing.T) {
	_, err := mcpPresetConfig("gitea", "stdio", map[string]string{"host": "https://git.example.com"}, false)
	if err == nil || !strings.Contains(err.Error(), "访问令牌") {
		t.Fatalf("err = %v，应当点名缺的是访问令牌", err)
	}
	if _, err := mcpPresetConfig("gitea", "carrier-pigeon", nil, false); err == nil {
		t.Fatal("不存在的接法应当报错")
	}
	if _, err := mcpPresetConfig("nope", "http", nil, false); err == nil {
		t.Fatal("不存在的预设应当报错")
	}
}

// 清单是拿去渲染界面的，改坏了界面就没东西可填。
func TestPresetListIsRenderable(t *testing.T) {
	presets := MCPPresetList()
	if len(presets) == 0 {
		t.Fatal("内置清单是空的")
	}
	for _, preset := range presets {
		if preset.ID == "" || preset.Name == "" || preset.Title == "" || preset.Summary == "" || len(preset.Transports) == 0 {
			t.Fatalf("预设缺少界面要用的字段：%#v", preset)
		}
		if !mcpServerNamePattern.MatchString(preset.Name) {
			t.Fatalf("预设的默认名字 %q 不符合 MCP 名称规则", preset.Name)
		}
		for _, transport := range preset.Transports {
			if transport.ID == "" || transport.Label == "" || len(transport.Fields) == 0 || transport.config == nil {
				t.Fatalf("预设 %s 的接法不完整：%#v", preset.ID, transport)
			}
			for _, field := range transport.Fields {
				if field.Key == "" || field.Label == "" {
					t.Fatalf("预设 %s 的字段不完整：%#v", preset.ID, field)
				}
			}
		}
	}
}

func bundledGiteaMCPName() string {
	if runtime.GOOS == "windows" {
		return "gitea-mcp.exe"
	}
	return "gitea-mcp"
}

// 自带的二进制放在主程序旁边，不在 PATH 里：解析不出绝对路径，stdio 预设装上去
// 就是一条起不来的服务。两头都没有时必须退回裸名字，让 PATH 还有机会兜住。
func TestBundledGiteaMCPCommandPrefersNeighbourBinary(t *testing.T) {
	name := bundledGiteaMCPName()
	dir := t.TempDir()
	fake := filepath.Join(dir, name)
	if err := os.WriteFile(fake, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Skipf("拿不到当前可执行文件：%v", err)
	}
	if got := bundledGiteaMCPCommand(); got != name && filepath.Dir(got) != filepath.Dir(executable) {
		t.Fatalf("解析结果既不在主程序旁边也不是裸名字：%q", got)
	}
	if got := bundledCommandIn(dir, name); got != fake {
		t.Fatalf("旁边就有一份却没用上：%q", got)
	}
	if got := bundledCommandIn(filepath.Join(dir, "empty"), name); got != name {
		t.Fatalf("目录里没有时应当退回裸名字：%q", got)
	}
}

// giteaAPIStub 扮演一个 Gitea 实例：只认一个令牌，其余一律 401。
func giteaAPIStub(t *testing.T, validToken string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/user" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Header.Get("Authorization") != "token "+validToken {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"login":"diana","id":7}`))
	}))
	t.Cleanup(server.Close)
	return server
}

// 令牌对不对，必须在保存前就能看出来。gitea-mcp 的握手和工具发现根本不碰令牌，
// 只靠「测试连接」的话填错的令牌要到真正调用工具时才 401。
func TestGiteaPresetVerifiesToken(t *testing.T) {
	gitea := giteaAPIStub(t, "good-token")
	config, err := mcpPresetConfig("gitea", "stdio", map[string]string{"host": gitea.URL, "token": "good-token"}, false)
	if err != nil {
		t.Fatal(err)
	}
	server, err := mcpServerConfigFromInput(config)
	if err != nil {
		t.Fatal(err)
	}
	server.Preset, server.PresetTransport = "gitea", "stdio"
	account, supported, err := presetVerifyConfig(context.Background(), server)
	if err != nil || !supported {
		t.Fatalf("有效令牌应当验证通过：account=%q supported=%v err=%v", account, supported, err)
	}
	if account != "diana" {
		t.Fatalf("应当报出换到的用户名，实际 %q", account)
	}

	server.Env["GITEA_ACCESS_TOKEN"] = "stale-token"
	if _, _, err := presetVerifyConfig(context.Background(), server); !errors.Is(err, ErrPresetCredentialRejected) {
		t.Fatalf("过期令牌要被判成凭据问题，实际 %v", err)
	}

	// 连不上和令牌错必须分开：前者不该说人家令牌不对。
	server.Env["GITEA_HOST"] = "http://127.0.0.1:1"
	server.Env["GITEA_ACCESS_TOKEN"] = "good-token"
	_, _, err = presetVerifyConfig(context.Background(), server)
	if err == nil || errors.Is(err, ErrPresetCredentialRejected) {
		t.Fatalf("连不上应当报成连接问题，实际 %v", err)
	}

	// 地址指到一个不是 Gitea 的服务上，要说清是地址的问题。
	notGitea := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("<html>hello</html>"))
	}))
	defer notGitea.Close()
	server.Env["GITEA_HOST"] = notGitea.URL
	if _, _, err := presetVerifyConfig(context.Background(), server); err == nil || !strings.Contains(err.Error(), "Gitea") {
		t.Fatalf("非 Gitea 地址应当点名地址不对，实际 %v", err)
	}
}

// 编辑时那张表要能填好：非机密字段从配置里取回，令牌永远不回显。
func TestGiteaPresetValuesForEditing(t *testing.T) {
	config, err := mcpPresetConfig("gitea", "stdio", map[string]string{"host": "https://git.example.com", "token": "abc"}, false)
	if err != nil {
		t.Fatal(err)
	}
	server, err := mcpServerConfigFromInput(config)
	if err != nil {
		t.Fatal(err)
	}
	server.Preset, server.PresetTransport = "gitea", "stdio"
	values := presetValuesFromConfig(server)
	if values["host"] != "https://git.example.com" {
		t.Fatalf("实例地址没取回来：%#v", values)
	}
	if _, ok := values["token"]; ok {
		t.Fatalf("令牌不该回显：%#v", values)
	}
	// 用的是自带的那份时「可执行文件」保持留空，不然像是用户自己填过路径。
	if _, ok := values["command"]; ok {
		t.Fatalf("自带的二进制不该回填成路径：%#v", values)
	}
	server.Command = "/opt/bin/gitea-mcp"
	if values := presetValuesFromConfig(server); values["command"] != "/opt/bin/gitea-mcp" {
		t.Fatalf("自填的路径要取回来：%#v", values)
	}

	// 没有 verify 的接法要老实说自己验不了，不能假装验过。
	httpConfig, err := mcpPresetConfig("gitea", "http", map[string]string{"url": "http://127.0.0.1:8080/mcp"}, false)
	if err != nil {
		t.Fatal(err)
	}
	remote, err := mcpServerConfigFromInput(httpConfig)
	if err != nil {
		t.Fatal(err)
	}
	remote.Preset, remote.PresetTransport = "gitea", "http"
	if _, supported, err := presetVerifyConfig(context.Background(), remote); supported || err != nil {
		t.Fatalf("HTTP 接法没有凭据可验：supported=%v err=%v", supported, err)
	}
}

// 麦当劳和瑞幸都是「官方托管远程 MCP + 一个 Bearer 令牌」，配置要拼对，令牌要能
// 在保存前验出来——这两条服务的工具是会真的下单的，令牌错了不能等到下单时才发现。
func TestBearerTokenPresetsConfigAndVerify(t *testing.T) {
	t.Setenv("DIANA_ALLOW_PRIVATE_HTTP_FETCHES", "true")
	for _, presetID := range []string{"mcdonalds", "luckin"} {
		config, err := mcpPresetConfig(presetID, "http", map[string]string{"token": "good-token"}, false)
		if err != nil {
			t.Fatal(err)
		}
		server, err := mcpServerConfigFromInput(config)
		if err != nil {
			t.Fatal(err)
		}
		if err := server.validate(); err != nil {
			t.Fatalf("%s 预设配置不合法：%v", presetID, err)
		}
		if server.URL == "" {
			t.Fatalf("%s 没有填地址时应当用官方地址：%#v", presetID, server)
		}
		if server.Headers["Authorization"] != "Bearer good-token" {
			t.Fatalf("%s 的令牌没按 Bearer 拼：%#v", presetID, server.Headers)
		}
		// 整行粘贴（自带 Bearer 前缀）不能拼成 "Bearer Bearer …"。
		pasted, err := mcpPresetConfig(presetID, "http", map[string]string{"token": "Bearer good-token"}, false)
		if err != nil {
			t.Fatal(err)
		}
		if got := pasted["headers"].(map[string]any)["Authorization"]; got != "Bearer good-token" {
			t.Fatalf("%s 重复加了前缀：%v", presetID, got)
		}
		// 令牌留空是「沿用旧的」：这时候一个 Authorization 都不能写出去，
		// 否则保存那段会把 "Bearer " 当成新值，把旧令牌顶掉。
		blank, err := mcpPresetConfig(presetID, "http", map[string]string{}, true)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := blank["headers"]; ok {
			t.Fatalf("%s 令牌留空时不该写请求头：%#v", presetID, blank)
		}
	}

	// 令牌不对时远程网关在 HTTP 这层就打回来，要判成凭据问题而不是「连不上」。
	upstream := mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server { return newEchoMCPServer() }, &mcpsdk.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer good-token" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		upstream.ServeHTTP(w, r)
	}))
	defer remote.Close()

	config, err := mcpPresetConfig("mcdonalds", "http", map[string]string{"token": "good-token", "url": remote.URL}, false)
	if err != nil {
		t.Fatal(err)
	}
	server, err := mcpServerConfigFromInput(config)
	if err != nil {
		t.Fatal(err)
	}
	server.Preset, server.PresetTransport = "mcdonalds", "http"
	if _, supported, err := presetVerifyConfig(context.Background(), server); err != nil || !supported {
		t.Fatalf("有效令牌应当验证通过：supported=%v err=%v", supported, err)
	}

	server.Headers["Authorization"] = "Bearer stale-token"
	if _, _, err := presetVerifyConfig(context.Background(), server); !errors.Is(err, ErrPresetCredentialRejected) {
		t.Fatalf("被网关拒掉的令牌要判成凭据问题，实际 %v", err)
	}
}
