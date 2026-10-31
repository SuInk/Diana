// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SuInk/diana/model/agent"
	"github.com/SuInk/diana/model/llm"
)

const renderFinalizeTestTable = "| 名次 | 模型 | 得分 |\n|---|---|---:|\n| 1 | A | 100% |\n| 2 | B | 96% |"

const renderFinalizeFieldName = "render"

// renderFinalizeLLMProvider 在收尾时按需填 render；sawField 记下收尾工具是否带了这个字段。
type renderFinalizeLLMProvider struct {
	capturingLLMProvider
	table    string
	sawField bool
}

func (p *renderFinalizeLLMProvider) Generate(ctx context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	response, err := p.capturingLLMProvider.Generate(ctx, req)
	if err != nil || response.Text != p.reply {
		return response, err
	}
	for _, tool := range req.Tools {
		if tool.Name != "send" {
			continue
		}
		properties, _ := tool.Parameters["properties"].(map[string]any)
		_, p.sawField = properties[renderFinalizeFieldName]
		arguments := map[string]any{"content": response.Text}
		if p.table != "" {
			arguments[renderFinalizeFieldName] = p.table
		}
		response.ToolCalls = []llm.ToolCall{{ID: "finalize", Name: tool.Name, Arguments: arguments}}
		response.Text = ""
	}
	return response, nil
}

func renderFinalizeRuntime(t *testing.T, provider *renderFinalizeLLMProvider, browser bool) (*Runtime, *recordingChannel) {
	t.Helper()
	channel := &recordingChannel{}
	// 默认插件表里「网页渲染」是开着的；空插件表等于它没装。
	plugins := NewPluginManager()
	if browser {
		plugins = NewDefaultPluginManager()
	}
	rt := NewRuntime(BotConfig{AgentEnabled: true}.WithDefaults(), channel, plugins, nil, nil, nil, func() (LLMProvider, error) { return provider, nil })
	return rt, channel
}

// 普通榜单回复只发文字；即使内置浏览器可用，收尾协议也不再提供隐式出图字段。
func TestReplyFinalizeDoesNotOfferRenderField(t *testing.T) {
	withFastSendTiming(t)
	for _, browser := range []bool{true, false} {
		provider := &renderFinalizeLLMProvider{capturingLLMProvider: capturingLLMProvider{reply: "前几名如下<diana-br/>1. A：100%<diana-br/>2. B：96%"}}
		rt, channel := renderFinalizeRuntime(t, provider, browser)
		event := MessageEvent{Kind: EventKindPrivate, UserID: "10001", MessageID: "rank", RawMessage: "来个榜单看看"}
		if _, err := rt.replyTo(context.Background(), event, event.RawMessage); err != nil {
			t.Fatal(err)
		}
		if provider.sawField {
			t.Fatal("agent_finalize still offered the render field")
		}
		var text strings.Builder
		for _, message := range channel.sentSnapshot() {
			if len(message.ImageURLs) != 0 {
				t.Fatalf("unexpected rendered image: %#v", message)
			}
			text.WriteString(message.Text)
		}
		if !strings.Contains(text.String(), "96%") {
			t.Fatalf("ranking lost: %q", text.String())
		}
	}
}

// 模型可能沿用历史里的旧信封，未声明的 render 字段不再触发任何出图。
func TestReplyFinalizeIgnoresLegacyRenderField(t *testing.T) {
	withFastSendTiming(t)
	provider := &renderFinalizeLLMProvider{capturingLLMProvider: capturingLLMProvider{reply: "前几名如下"}, table: renderFinalizeTestTable}
	rt, channel := renderFinalizeRuntime(t, provider, true)
	event := MessageEvent{Kind: EventKindPrivate, UserID: "10001", MessageID: "rank", RawMessage: "来个榜单看看"}
	if _, err := rt.replyTo(context.Background(), event, event.RawMessage); err != nil {
		t.Fatal(err)
	}
	if provider.sawField {
		t.Fatal("agent_finalize offered the removed field")
	}
	var text strings.Builder
	for _, message := range channel.sentSnapshot() {
		if len(message.ImageURLs) != 0 {
			t.Fatalf("legacy field sent an image: %#v", message)
		}
		text.WriteString(message.Text + "\n")
	}
	if !strings.Contains(text.String(), "前几名如下") {
		t.Fatalf("reply lost: %q", text.String())
	}
}

func TestRenderDeliveryRuleFollowsAvailableTools(t *testing.T) {
	rt := NewRuntime(BotConfig{AgentEnabled: true}.WithDefaults(), nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	event := MessageEvent{Kind: EventKindPrivate, UserID: "10001"}
	for _, tools := range [][]agent.Tool{nil, {newDianaRenderTool(rt, event)}, {newDianaRenderMediaTool(rt, event, nil, RelationshipPolicy{})}} {
		registry := agent.NewToolRegistry(tools...)
		prompt := rt.systemPromptWithRelationshipAndAgentTools(event, nil, false, RelationshipPolicy{}, true, registry)
		if strings.Contains(prompt, promptToolRender) != (len(tools) > 0) {
			t.Fatalf("render rule presence differs from tool availability: %d tools", len(tools))
		}
	}
}

// 真实模型与生产人设，渲染工具只记录调用，不启动浏览器，也不向真实会话发送消息。
// DIANA_TEST_LLM_CONFIG / DIANA_TEST_BOT_CONFIG 指向私有配置，默认随常规测试跳过。
func TestLiveRenderDeliveryIntent(t *testing.T) {
	client := liveLLMClient(t)
	cfg := BotConfig{AgentEnabled: true}
	if path := strings.TrimSpace(os.Getenv("DIANA_TEST_BOT_CONFIG")); path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(data, &cfg); err != nil {
			t.Fatal(err)
		}
	}
	cfg = cfg.WithDefaults()
	t.Setenv("APP_DB_PATH", filepath.Join(t.TempDir(), "replay.db"))
	for _, tc := range []struct {
		name, history, request string
		wantRender             bool
	}{
		{"color_change", "正在讨论实际终端界面的配色修改，目标文件还没有提供。", "粉蓝白安排一下", false},
		{"ranking", "本轮要整理虚构比赛数据：A 100 分、B 96 分、C 90 分，数据已完整，不需要联网。", "来个榜单看看", false},
		{"text_only", "有 A、B、C 三种虚构方案，性能分别为 100、96、90，价格分别为 10、8、5。", "用文字帮我比较一下，不要图片", false},
		{"real_change", "正在讨论终端界面配色。", "按粉蓝白改下实际终端配色，不要给我画效果图", false},
		{"color_mockup", "正在讨论终端界面的配色设计。", "给我发一张粉蓝白终端配色效果图", true},
		{"chart", "本轮要整理虚构比赛数据：A 100 分、B 96 分、C 90 分，数据已完整，不需要联网。", "把这三项分数画成柱状图，图片发给我", true},
	} {
		if !t.Run(tc.name, func(t *testing.T) {
			rt := NewRuntime(cfg, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
			event := MessageEvent{Kind: EventKindGroup, GroupID: "replay", UserID: "member", RawMessage: tc.request}
			var calls atomic.Int64
			registry := agent.NewToolRegistry(
				&renderDeliveryReplayTool{Tool: newDianaRenderTool(rt, event), calls: &calls},
				&renderDeliveryReplayTool{Tool: newDianaRenderMediaTool(rt, event, nil, RelationshipPolicy{}), calls: &calls},
			)
			prompt := rt.systemPromptWithRelationshipAndAgentTools(event, nil, false, RelationshipPolicy{}, true, registry)
			runner, err := agent.NewRunner(client, agent.Config{MaxSteps: 4, CoreTools: []string{dianaRenderToolName, dianaRenderMediaToolName}}, registry)
			if err != nil {
				t.Fatal(err)
			}
			defer runner.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			resp, err := runner.Run(ctx, agent.Request{Messages: []llm.Message{
				{Role: llm.RoleSystem, Content: prompt},
				{Role: llm.RoleUser, Content: "【历史背景】" + tc.history},
				{Role: llm.RoleUser, Content: "【当前需要回复的消息】" + tc.request},
			}})
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("render calls=%d reply=%s", calls.Load(), resp.Text)
			if (calls.Load() > 0) != tc.wantRender {
				t.Fatalf("render calls=%d, wantRender=%v", calls.Load(), tc.wantRender)
			}
		}) {
			// 上游 429/503 时停止本轮回放，避免继续请求同一个受限服务。
			return
		}
	}
}

type renderDeliveryReplayTool struct {
	agent.Tool
	calls *atomic.Int64
}

func (t *renderDeliveryReplayTool) Run(context.Context, map[string]any) (string, error) {
	t.calls.Add(1)
	return `{"ok":true,"message":"图片已经发到会话里了。"}`, nil
}
