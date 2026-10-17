// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SuInk/diana/model/agent"
	"github.com/SuInk/diana/model/llm"
)

// 管理会话补齐工具面以后，主人私聊里那些按需加载的工具要靠模型自己 tools_load。
// 这里拿真实模型确认它真会去加载并调用，而不是回一句「没有这个工具」。
//
// DIANA_LIVE_LLM=1 DIANA_TEST_LLM_API_KEY=... DIANA_TEST_LLM_BASE_URL=...
// DIANA_TEST_LLM_MODEL=... go test ./model/assistant -run '^TestLiveAdminChatCallsDeferredTools$' -count=1 -v
func TestLiveAdminChatCallsDeferredTools(t *testing.T) {
	if os.Getenv("DIANA_LIVE_LLM") != "1" {
		t.Skip("set DIANA_LIVE_LLM=1 to call a real model")
	}
	cfg := llm.ProviderConfig{
		Provider: llm.ProviderOpenAICompatible,
		APIKey:   strings.TrimSpace(os.Getenv("DIANA_TEST_LLM_API_KEY")),
		BaseURL:  strings.TrimSpace(os.Getenv("DIANA_TEST_LLM_BASE_URL")),
		Model:    strings.TrimSpace(os.Getenv("DIANA_TEST_LLM_MODEL")),
		APIStyle: llm.APIStyle(strings.TrimSpace(os.Getenv("DIANA_TEST_LLM_API_STYLE"))),
		Timeout:  120 * time.Second,
	}
	if cfg.APIKey == "" || cfg.Model == "" {
		t.Fatal("DIANA_TEST_LLM_API_KEY and DIANA_TEST_LLM_MODEL are required")
	}
	client, err := llm.NewClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct{ prompt, tool string }{
		{"这个机器人现在有哪些后台任务或编码任务在跑？查一下。", "tasks"},
		{"帮我看看记事本里记了些什么。", "notebook"},
		{"用回显插件把 hello 回显一下。", "plugin.echo"},
	}
	for _, tc := range cases {
		t.Run(tc.tool, func(t *testing.T) {
			t.Setenv("APP_DB_PATH", filepath.Join(t.TempDir(), "app.db"))
			echo := &echoAgentTool{}
			r := NewRuntime(BotConfig{ID: "bot", OwnerID: "owner", AgentEnabled: true}, nilChannel{}, NewPluginManager(&echoAgentToolPlugin{tool: echo}), nil, nil, nil, func() (LLMProvider, error) { return client, nil })
			defer r.closeAgentRegistryCache()
			ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
			defer cancel()
			resp, err := r.RunAdminChat(ctx, "bot", agent.Request{Messages: AdminChatMessages([]llm.Message{{Role: llm.RoleUser, Content: tc.prompt}})})
			if err != nil {
				t.Fatal(err)
			}
			var called []string
			for _, step := range resp.Steps {
				name := step.Tool
				if name == agent.ToolsExecuteToolName {
					name = strings.TrimSpace(configToolString(step.Input, "name"))
				}
				called = append(called, name)
			}
			t.Logf("model=%s steps=%v reply=%q", resp.Model, called, resp.Text)
			for _, name := range called {
				if name == tc.tool {
					return
				}
			}
			t.Fatalf("%s was never called: %v", tc.tool, called)
		})
	}
}
