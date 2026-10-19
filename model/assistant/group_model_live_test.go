// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/SuInk/diana/model/llm"
)

// Uses production provider configuration with synthetic requests and memory-only writes.
// No platform messages are sent and no production settings are modified.
func TestGroupModelLiveScope(t *testing.T) {
	path := os.Getenv("DIANA_GROUP_MODEL_LIVE_CONFIG")
	if path == "" {
		t.Skip("set DIANA_GROUP_MODEL_LIVE_CONFIG to run live scope replay")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cfg llm.ProviderConfig
	if err = json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	client, err := llm.NewClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	r, _, _, event := modelSwitchTestRuntime(t)
	event.Kind = EventKindGroup
	event.GroupID = "replay"
	store := &testWritableGroupConfigStore{}
	r.SetGroupConfigStore(store)
	tool := newDianaLLMConfigTool(r, event)
	for _, tc := range []struct{ prompt, scope, operation string }{
		{"只把本群聊天模型切到 Gateway B 的 other，不要动其他群。", "group", "update"},
		{"恢复本群跟随机器人的聊天模型。", "group", "reset"},
		{"把整个机器人的聊天模型切到 Gateway B 的 other，所有群的默认模型都改。", "bot", "update"},
	} {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		response, err := client.Generate(ctx, llm.GenerateRequest{Messages: []llm.Message{
			{Role: llm.RoleSystem, Content: "你是群聊里的机器人，当前发言者是主人，允许修改模型配置。已知提供商 Gateway B 的 ID 是 two，可用模型 other。按用户明确指定的范围执行配置请求。"},
			{Role: llm.RoleUser, Content: tc.prompt},
		}, Tools: []llm.ToolDefinition{{Name: tool.Name(), Description: tool.Description(), Parameters: tool.InputSchema()}}})
		cancel()
		if err != nil {
			t.Fatal(err)
		}
		if len(response.ToolCalls) != 1 {
			t.Fatalf("scope=%s: wanted one tool call, got %d", tc.scope, len(response.ToolCalls))
		}
		call := response.ToolCalls[0]
		if call.Name != tool.Name() || call.Arguments["scope"] != tc.scope || call.Arguments["operation"] != tc.operation {
			t.Fatalf("unexpected call: %s %+v", call.Name, call.Arguments)
		}
		if _, err := tool.Run(context.Background(), call.Arguments); err != nil {
			t.Fatal(err)
		}
		t.Logf("model=%s scope=%s operation=%s passed", cfg.Model, tc.scope, tc.operation)
	}
	// Exercise the real selected provider after applying a group override.
	r.setGroupReplayProvider(cfg)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := r.saveGroupChatModel(event, GroupChatModel{ProfileID: "live", Model: cfg.Model}); err != nil {
		t.Fatal(err)
	}
	ctx = withLLMUsagePurpose(withModelConfigEvent(ctx, event), PurposeReply)
	got, err := r.runRawLLMProviderForGroup(ctx, llm.GroupChat, func(p LLMProvider) (string, error) {
		response, err := p.Generate(ctx, llm.GenerateRequest{Messages: []llm.Message{{Role: llm.RoleUser, Content: "Reply with OK."}}})
		if err != nil {
			return "", err
		}
		return response.Text, nil
	})
	if err != nil || got == "" {
		t.Fatalf("group route generation failed: %v", err)
	}
	t.Logf("real group route generated with %s", cfg.Model)
}

func (r *Runtime) setGroupReplayProvider(cfg llm.ProviderConfig) {
	r.mu.Lock()
	r.llmStore = &stubLLMProfileStore{set: llm.ProfileSet{Profiles: []llm.Profile{{ID: "live", Name: "Live replay", Config: cfg}}}}
	r.llmCfgFactory = func(c llm.ProviderConfig) (LLMProvider, error) { return llm.NewClient(c) }
	r.mu.Unlock()
}
