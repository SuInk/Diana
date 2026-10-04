// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/agent"
	"github.com/SuInk/diana/model/llm"
)

func TestWebSearchPluginIsBuiltInAndHonorsOverrides(t *testing.T) {
	manager := NewDefaultPluginManager()
	state, ok := manager.Get(webSearchPluginID)
	if !ok {
		t.Fatal("web search plugin missing")
	}
	if !state.Installed || !state.Enabled || !state.Manifest.Official || !state.Manifest.BuiltIn {
		t.Fatalf("initial state = %#v", state)
	}
	tools, err := manager.AgentToolsWithOverrides(nil)
	if err != nil || !hasAgentToolNamed(tools, agent.WebSearchToolName) {
		t.Fatalf("built-in tools=%#v err=%v", tools, err)
	}

	if _, err := manager.Install(webSearchPluginID); !errors.Is(err, ErrBuiltInPluginAction) {
		t.Fatalf("Install() error = %v", err)
	}
	if _, err := manager.Uninstall(webSearchPluginID); !errors.Is(err, ErrBuiltInPluginAction) {
		t.Fatalf("Uninstall() error = %v", err)
	}
	state, _ = manager.Get(webSearchPluginID)
	if !state.Installed || !state.Enabled {
		t.Fatalf("built-in state changed after lifecycle request: %#v", state)
	}
	// 只关联网搜索时网页渲染还在，会补一个搜索引擎方式的 web_search（见
	// TestDisabledWebSearchFallsBackToSearchEngineWhenBrowserIsOn），这里两个一起关。
	tools, err = manager.AgentToolsWithOverrides(map[string]bool{webSearchPluginID: false, sandboxedBrowserPluginID: false})
	if err != nil || hasAgentToolNamed(tools, agent.WebSearchToolName) {
		t.Fatalf("disabled override tools=%#v err=%v", tools, err)
	}
}

func hasAgentToolNamed(tools []agent.Tool, name string) bool {
	for _, tool := range tools {
		if tool != nil && tool.Name() == name {
			return true
		}
	}
	return false
}

func TestWebSearchPluginSecretsAreRedacted(t *testing.T) {
	manager := NewDefaultPluginManager()
	if _, err := manager.UpdateSettings(webSearchPluginID, map[string]any{
		webSearchSettingExaAPIKey:    "exa-secret",
		webSearchSettingTavilyAPIKey: "tavily-secret",
	}); err != nil {
		t.Fatal(err)
	}
	state, _ := manager.Get(webSearchPluginID)
	redacted := state.Redacted()
	if redacted.Settings != nil {
		t.Fatalf("redacted settings = %#v", redacted.Settings)
	}
	if !redacted.SecretsConfigured[webSearchSettingExaAPIKey] || !redacted.SecretsConfigured[webSearchSettingTavilyAPIKey] {
		t.Fatalf("configured flags = %#v", redacted.SecretsConfigured)
	}
}

func TestWebSearchPluginCanDisableAllProviders(t *testing.T) {
	plugin := NewWebSearchPlugin(nil)
	tools, err := plugin.AgentTools(SettingValues{
		webSearchSettingExaEnabled:    false,
		webSearchSettingTavilyEnabled: false,
	})
	if err != nil || len(tools) != 0 {
		t.Fatalf("tools=%#v err=%v", tools, err)
	}
}

// 关掉的搜索插件不再塞一个同名占位工具：以前它每轮都带着一整段搜索规则，模型照
// 规则去搜，必然失败，还会把「搜索没配置」说给用户听。
func TestRuntimeWithoutSearchPluginRegistersNoSearchTool(t *testing.T) {
	provider := &privacyAwareTestProvider{}
	sawAgentTools := false
	provider.generate = func(call int, req llm.GenerateRequest) (string, error) {
		for _, tool := range req.Tools {
			sawAgentTools = true
			if tool.Name == agent.WebSearchToolName {
				return "", fmt.Errorf("web_search 仍然登记在工具表里")
			}
		}
		if call == 1 {
			return `{"action":"none","tools":[],"context_message_ids":[],"keep_older_summary":false}`, nil
		}
		return `{"action":"final","content":"好"}`, nil
	}
	runtime := NewRuntime(BotConfig{BotAccount: "10000", AgentEnabled: true, AgentMaxSteps: 3}, &recordingChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
		return provider, nil
	})
	event := MessageEvent{Kind: EventKindPrivate, UserID: "user", MessageID: "no-search", RawMessage: "今天有什么新闻"}
	if _, err := runtime.replyAndRecord(context.Background(), event, "今天有什么新闻", "replied"); err != nil {
		t.Fatal(err)
	}
	if !sawAgentTools {
		t.Fatal("没有走到 Agent，测不出工具表")
	}
	if last := runtime.Status().LastError; last != "" {
		t.Fatalf("reply failed: %s", last)
	}
}

func TestWebSearchPluginSearchEngineMode(t *testing.T) {
	var rendered []string
	plugin := &WebSearchPlugin{renderer: agent.PageRendererFunc(func(_ context.Context, rawURL string) (agent.RenderedPage, error) {
		rendered = append(rendered, rawURL)
		return agent.RenderedPage{URL: rawURL, Title: "结果", Text: "摘要", Links: []agent.RenderedLink{{URL: "https://example.com/a", Text: "结果一"}}}, nil
	})}
	tools, err := plugin.AgentTools(SettingValues{
		webSearchSettingMode:    webSearchModeEngine,
		webSearchSettingEngines: "Bing，unknown, bing, baidu",
	})
	if err != nil || len(tools) != 1 || tools[0].Name() != agent.WebSearchToolName {
		t.Fatalf("tools=%#v err=%v", tools, err)
	}
	output, err := tools[0].Run(context.Background(), map[string]any{"query": "测试"})
	if err != nil {
		t.Fatal(err)
	}
	if len(rendered) != 2 || !strings.HasPrefix(rendered[0], "https://www.bing.com/search?") || rendered[1] != "https://example.com/a" {
		t.Fatalf("rendered = %v", rendered)
	}
	if !strings.Contains(output, `"provider": "bing"`) || !strings.Contains(output, "https://example.com/a") {
		t.Fatalf("output = %s", output)
	}
	if got := webSearchEngineOrder(" , nope"); strings.Join(got, ",") != strings.Join(agent.DefaultSearchEngines, ",") {
		t.Fatalf("empty order = %v", got)
	}
}

func TestWebSearchPluginCustomSearchEngineKeepsOrder(t *testing.T) {
	var rendered []string
	plugin := &WebSearchPlugin{renderer: agent.PageRendererFunc(func(_ context.Context, rawURL string) (agent.RenderedPage, error) {
		rendered = append(rendered, rawURL)
		if strings.HasPrefix(rawURL, "https://search.example.com/") {
			return agent.RenderedPage{URL: rawURL, Title: "没有结果", Text: "没有结果"}, nil
		}
		return agent.RenderedPage{URL: rawURL, Title: "结果", Text: "摘要", Links: []agent.RenderedLink{
			{URL: "https://www.bing.com/settings", Text: "设置"},
			{URL: "https://example.org/a", Text: "结果一"},
		}}, nil
	})}
	tools, err := plugin.AgentTools(SettingValues{
		webSearchSettingMode:    webSearchModeEngine,
		webSearchSettingEngines: "https://search.example.com/s?q={query}&lang=zh\nbing\nhttp://evil.example/?q={query}",
	})
	if err != nil || len(tools) != 1 {
		t.Fatalf("tools=%#v err=%v", tools, err)
	}
	output, err := tools[0].Run(context.Background(), map[string]any{"query": "a b&c"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"https://search.example.com/s?q=a+b%26c&lang=zh", "https://www.bing.com/search?q=a+b%26c", "https://example.org/a"}
	if strings.Join(rendered, " ") != strings.Join(want, " ") {
		t.Fatalf("rendered = %v", rendered)
	}
	if !strings.Contains(output, `"provider": "bing"`) || !strings.Contains(output, "https://example.org/a") || strings.Contains(output, "evil.example") {
		t.Fatalf("output = %s", output)
	}
}

func TestDisabledWebSearchFallsBackToSearchEngineWhenBrowserIsOn(t *testing.T) {
	var rendered []string
	renderer := agent.PageRendererFunc(func(_ context.Context, rawURL string) (agent.RenderedPage, error) {
		rendered = append(rendered, rawURL)
		return agent.RenderedPage{URL: rawURL, Title: "结果", Text: "摘要", Links: []agent.RenderedLink{{URL: "https://example.com/a", Text: "结果一"}}}, nil
	})
	manager := NewPluginManager(newSandboxedBrowserRenderPlugin(renderer), &WebSearchPlugin{renderer: renderer})
	if _, err := manager.UpdateSettings(webSearchPluginID, map[string]any{webSearchSettingEngines: "duckduckgo"}); err != nil {
		t.Fatal(err)
	}

	tools, err := manager.AgentToolsWithOverrides(map[string]bool{webSearchPluginID: false})
	if err != nil {
		t.Fatal(err)
	}
	var search agent.Tool
	for _, tool := range tools {
		if tool.Name() == agent.WebSearchToolName {
			search = tool
		}
	}
	if search == nil {
		t.Fatalf("网页渲染开着时应当补上搜索引擎方式的 web_search: %#v", tools)
	}
	if _, err := search.Run(context.Background(), map[string]any{"query": "测试"}); err != nil {
		t.Fatal(err)
	}
	if len(rendered) != 2 || !strings.HasPrefix(rendered[0], "https://duckduckgo.com/?") || rendered[1] != "https://example.com/a" {
		t.Fatalf("应当沿用插件里的引擎顺序: %v", rendered)
	}

	owners := manager.AgentToolOwners("", map[string]bool{webSearchPluginID: false}, nil)
	if strings.Join(owners[webSearchPluginID], ",") != agent.WebSearchToolName {
		t.Fatalf("兜底的 web_search 应当记在联网搜索插件名下: %v", owners)
	}

	tools, err = manager.AgentToolsWithOverrides(map[string]bool{webSearchPluginID: false, sandboxedBrowserPluginID: false})
	if err != nil || hasAgentToolNamed(tools, agent.WebSearchToolName) {
		t.Fatalf("两个插件都关掉就不该有 web_search: tools=%#v err=%v", tools, err)
	}
	if owners := manager.AgentToolOwners("", map[string]bool{webSearchPluginID: false, sandboxedBrowserPluginID: false}, nil); len(owners[webSearchPluginID]) != 0 {
		t.Fatalf("owners = %v", owners)
	}
}

func TestWebSearchPluginAPIModeReadsActualSource(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"results":[{"url":"https://example.com/docs","content":"只是搜索摘要"}]}`)
	}))
	defer server.Close()
	var requested string
	plugin := &WebSearchPlugin{renderer: agent.PageRendererFunc(func(_ context.Context, u string) (agent.RenderedPage, error) {
		requested = u
		return agent.RenderedPage{URL: u, Text: "实际原文，不能被摘要替代"}, nil
	})}
	tools, err := plugin.AgentTools(SettingValues{webSearchSettingMode: webSearchModeAPI, webSearchSettingExaEnabled: false, webSearchSettingTavilyEnabled: true, webSearchSettingTavilyURL: server.URL, webSearchSettingTavilyAPIKey: "unit-test"})
	if err != nil || len(tools) != 1 {
		t.Fatalf("tools=%v err=%v", tools, err)
	}
	out, err := tools[0].Run(context.Background(), map[string]any{"query": "source docs"})
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Documents []struct {
			Text string `json:"text"`
		} `json:"documents"`
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatal(err)
	}
	if requested != "https://example.com/docs" || len(result.Documents) != 1 || result.Documents[0].Text != "实际原文，不能被摘要替代" {
		t.Fatalf("API 模式未接原文读取：%s", out)
	}
}
