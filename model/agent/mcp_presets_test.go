// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// 预设装出来的必须是一份能过校验的普通 MCP 配置，不然「一键装上」只是把错误
// 推迟到连接的时候。
func TestGiteaPresetProducesValidConfigs(t *testing.T) {
	http, err := mcpPresetConfig("gitea", "http", map[string]string{"url": "http://127.0.0.1:8080/mcp"})
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

	stdio, err := mcpPresetConfig("gitea", "stdio", map[string]string{"host": "https://git.example.com", "token": "abc"})
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
	custom, err := mcpPresetConfig("gitea", "stdio", map[string]string{"host": "https://git.example.com", "token": "abc", "command": "/opt/bin/gitea-mcp"})
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
	config, err := mcpPresetConfig("gitea", "http", map[string]string{"url": "http://127.0.0.1:8080/mcp", "authorization": "Bearer t"})
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
	_, err := mcpPresetConfig("gitea", "stdio", map[string]string{"host": "https://git.example.com"})
	if err == nil || !strings.Contains(err.Error(), "访问令牌") {
		t.Fatalf("err = %v，应当点名缺的是访问令牌", err)
	}
	if _, err := mcpPresetConfig("gitea", "carrier-pigeon", nil); err == nil {
		t.Fatal("不存在的接法应当报错")
	}
	if _, err := mcpPresetConfig("nope", "http", nil); err == nil {
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
