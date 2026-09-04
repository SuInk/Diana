// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/agent"
)

func TestCapabilityKnowledgePluginRetrievesRelevantCapabilities(t *testing.T) {
	plugin := NewCapabilityKnowledgePlugin()
	tool := plugin.AgentTools()[0]
	raw, err := tool.Run(context.Background(), map[string]any{"query": "你能看懂并解析视频吗", "limit": 3})
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Items []capabilitySearchHit `json:"items"`
	}
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Items) == 0 || result.Items[0].ID != "core:media" {
		t.Fatalf("items=%#v", result.Items)
	}
}

func TestCapabilityKnowledgeUsesCurrentPlatformOnly(t *testing.T) {
	plugin := NewCapabilityKnowledgePlugin()
	tool := capabilityToolForConfig(plugin.AgentTools()[0], BotConfig{Platform: PlatformTelegram}.WithDefaults())
	raw, err := tool.Run(context.Background(), map[string]any{"query": "当前窗口支持 Markdown 吗", "limit": 8})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(raw, `"id": "runtime:platform-output"`) || !strings.Contains(raw, "当前会话运行在 Telegram") {
		t.Fatalf("current platform capability missing: %s", raw)
	}
	if strings.Contains(raw, "OneBot v11 消息不渲染 Markdown") {
		t.Fatalf("another platform rule leaked into capability result: %s", raw)
	}
}

func TestCapabilityKnowledgeFiltersUnsupportedPluginsForPlatform(t *testing.T) {
	manager := NewDefaultPluginManager()
	var capabilityTool agent.Tool
	tools, err := manager.AgentToolsWithOverrides(nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range tools {
		if tool.Name() == "diana.capabilities" {
			capabilityTool = capabilityToolForConfig(tool, BotConfig{Platform: PlatformTelegram}.WithDefaults())
			break
		}
	}
	if capabilityTool == nil {
		t.Fatal("capability tool missing")
	}
	raw, err := capabilityTool.Run(context.Background(), map[string]any{"query": "点歌 音乐 语音", "limit": 8})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(raw, `"id": "plugin:`+musicPluginID+`"`) || strings.Contains(raw, `"id": "plugin:`+voiceTTSPluginID+`"`) {
		t.Fatalf("OneBot-only plugin leaked into Telegram capability result: %s", raw)
	}
}

func TestImageCapabilityRequiresFamiliarRelationship(t *testing.T) {
	for _, document := range coreCapabilityDocuments {
		if document.ID != "core:image" {
			continue
		}
		if document.Required != relationshipImageTierName || !strings.Contains(document.Content, "熟悉等级可生成和编辑图片") {
			t.Fatalf("image capability = %#v", document)
		}
		return
	}
	t.Fatal("core:image capability missing")
}

func TestDefaultPluginManagerExposesCapabilityRAGAndLivePluginStates(t *testing.T) {
	manager := NewDefaultPluginManager()
	state, ok := manager.Get(capabilityKnowledgePluginID)
	if !ok || !state.Installed || !state.Enabled {
		t.Fatalf("state=%#v ok=%v", state, ok)
	}
	var toolFound bool
	tools, err := manager.AgentToolsWithOverrides(nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range tools {
		if tool.Name() != "diana.capabilities" {
			continue
		}
		toolFound = true
		raw, err := tool.Run(context.Background(), map[string]any{"query": "有哪些链接解析插件"})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(raw, resolverPluginID) || !strings.Contains(raw, `"source": "plugin_manifest"`) {
			t.Fatalf("raw=%s", raw)
		}
	}
	if !toolFound {
		t.Fatal("diana.capabilities tool missing")
	}
}

func TestRuntimeAgentUsesCapabilityRAGForSelfKnowledge(t *testing.T) {
	provider := &sequenceLLMProvider{replies: []string{
		`{"action":"none","prompt":""}`,
		`{"action":"tool","tool":"diana.capabilities","input":{"query":"你能解析视频吗","limit":3}}`,
		`{"action":"final","content":"可以，我能读取视频并抽取多帧理解内容。"}`,
	}}
	runtime := NewRuntime(BotConfig{OwnerID: "owner", AgentEnabled: true, AgentMaxSteps: 3}, &recordingChannel{}, NewDefaultPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
		return provider, nil
	})
	event := MessageEvent{Kind: EventKindPrivate, UserID: "user", MessageID: "capability", Segments: []MessageSegment{{Type: "text", Data: map[string]string{"text": "你能解析视频吗"}}}}
	reply, err := runtime.replyTo(context.Background(), event, "你能解析视频吗")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(reply, "抽取多帧") || len(provider.requests) != 3 {
		t.Fatalf("reply=%q requests=%d", reply, len(provider.requests))
	}
	if !requestMessagesContain(provider.requests[2].Messages, `"id": "core:media"`) {
		t.Fatalf("retrieval missing: %#v", provider.requests[2].Messages)
	}
	if !requestMessagesContain(provider.requests[1].Messages, "必须先调用 diana.capabilities") {
		t.Fatalf("capability guidance missing: %#v", provider.requests[1].Messages)
	}
}
