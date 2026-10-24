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

// 行为设置接进 bot_config 以后，主人用一句话说「别按换行拆了」「复读概率调到 50」，
// 模型要能找到 bot_config 的 behavior 并真的保存下来，而不是回一句「请去后台改」。
//
// DIANA_LIVE_LLM=1 DIANA_TEST_LLM_API_KEY=... DIANA_TEST_LLM_BASE_URL=...
// DIANA_TEST_LLM_MODEL=... go test ./model/assistant -run '^TestLiveBotConfigBehaviorFromNaturalLanguage$' -count=1 -v
func TestLiveBotConfigBehaviorFromNaturalLanguage(t *testing.T) {
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
	cases := []struct {
		name   string
		prompt string
		check  func(BotConfig) bool
	}{
		{"line split off", "以后回复别再按换行拆成好几条发了，合成一条。", func(c BotConfig) bool { return c.ReplyLineSplitEnabled != nil && !*c.ReplyLineSplitEnabled }},
		{"repeat probability", "把随机复读打开，概率调到 50%。", func(c BotConfig) bool {
			return c.RandomRepeat != nil && c.RandomRepeat.Enabled && c.RandomRepeat.Probability == 50
		}},
		{"max chars", "你回复太长了，每次最多 500 字。", func(c BotConfig) bool { return c.MaxReplyChars == 500 }},
		{"muted images", "被禁言的时候就别识别图片了，省点钱。", func(c BotConfig) bool {
			return c.MutedImageDescriptionEnabled != nil && !*c.MutedImageDescriptionEnabled
		}},
		{"typing delay", "发消息前模拟一下打字延迟吧。", func(c BotConfig) bool { return c.TypingDelayEnabled != nil && *c.TypingDelayEnabled }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("APP_DB_PATH", filepath.Join(t.TempDir(), "app.db"))
			bot := BotConfig{ID: "bot", OwnerID: "owner", AgentEnabled: true, MaxReplyChars: 3500}
			saver := &participationToolSaver{cfg: bot}
			r := NewRuntime(bot, nilChannel{}, NewPluginManager(), nil, nil, saver, func() (LLMProvider, error) { return client, nil })
			r.SetProfiles(ProfileSet{Profiles: []BotConfig{bot}})
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
			if !tc.check(saver.cfg) {
				t.Fatalf("setting not saved: steps=%v", called)
			}
		})
	}
}
