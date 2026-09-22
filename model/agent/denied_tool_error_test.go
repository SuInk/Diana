// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/llm"
)

// 权限不够的工具在注册之前就被拦掉了，注册表本来不知道有过这个名字，于是模型只会
// 被告知「不存在」——它照字面理解成拼错了，换个名字接着猜，把工具预算耗光。线上真
// 发生过：非主人在群里让机器人开 issue，github 工具因为没权限没注册，模型连猜四个
// 名字直到额度用尽，issue 一个也没提出去。
func TestDeniedToolSaysNoPermissionInsteadOfMissing(t *testing.T) {
	registry := NewToolRegistry(&countingTool{name: "alpha"}, &countingTool{name: "agent_finalize"})
	registry.DenyTools("github")
	loader := newDeferredToolLoader(registry, []string{"agent_finalize"})
	if loader == nil {
		t.Fatal("延迟加载器未启用，这个测试没有意义")
	}

	_, err := loader.Run(t.Context(), map[string]any{"names": []any{"github"}})
	if err == nil || !strings.Contains(err.Error(), "没有权限") || !strings.Contains(err.Error(), "不要重试") {
		t.Fatalf("没权限的工具报错 = %v", err)
	}
	// 真的拼错了仍然要说「不存在」并让它换名字，否则模型会把打字错误当成权限问题。
	_, err = loader.Run(t.Context(), map[string]any{"names": []any{"github_issues"}})
	if err == nil || !strings.Contains(err.Error(), "不存在或已禁用") {
		t.Fatalf("未知工具报错 = %v", err)
	}
	// 登记只影响报错，不该让工具凭空可用。
	if _, ok := registry.Get("github"); ok {
		t.Fatal("登记为没权限的工具变得可调用了")
	}
}

// 没有启用延迟加载时模型直接按名字调用，走的是另一条修复路径：那里原本把整份工具
// 目录再抄一遍给模型，等于请它换个名字继续猜。
func TestDirectCallOnDeniedToolSkipsTheCatalogDump(t *testing.T) {
	registry := NewToolRegistry(&countingTool{name: "alpha"}, &countingTool{name: "agent_finalize"})
	registry.DenyTools("github")
	client := &scriptedClient{responses: []string{
		`{"action":"tool","tool":"github","input":{}}`,
		`{"action":"final","content":"这件事得主人来做"}`,
	}}
	runner := &Runner{client: client, cfg: Config{}.WithDefaults(), registry: registry}
	if _, err := runner.Run(context.Background(), Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "开个 issue"}}}); err != nil {
		t.Fatal(err)
	}
	repair := ""
	for _, message := range client.requests[len(client.requests)-1].Messages {
		if strings.Contains(message.Content, "github") && message.Role == llm.RoleUser {
			repair = message.Content
		}
	}
	if !strings.Contains(repair, "没有权限") {
		t.Fatalf("修复提示 = %q", repair)
	}
	if strings.Contains(repair, "可用工具：") {
		t.Fatalf("没权限时仍然抄了一遍工具目录: %q", repair)
	}
}
