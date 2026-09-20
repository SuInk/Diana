// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
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
	if server.Command != "gitea-mcp" || server.Env["GITEA_HOST"] != "https://git.example.com" || server.Env["GITEA_ACCESS_TOKEN"] != "abc" {
		t.Fatalf("stdio 预设没按 gitea-mcp 的约定拼：%#v", server)
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
