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
	"sort"
	"strings"
	"sync"
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
	// 显式停用工具不会由浏览器重新补上。
	tools, err = manager.AgentToolsWithOverrides(map[string]bool{webSearchPluginID: false})
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
		webSearchSettingMode:          webSearchModeAPI,
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
	runtime := NewRuntime(BotConfig{BotAccount: "10000", AgentEnabled: true}, &recordingChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
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
	var mu sync.Mutex
	var rendered []string
	plugin := &WebSearchPlugin{renderer: agent.PageRendererFunc(func(_ context.Context, rawURL string) (agent.RenderedPage, error) {
		mu.Lock()
		rendered = append(rendered, rawURL)
		mu.Unlock()
		return agent.RenderedPage{URL: rawURL, Title: "结果", Text: "摘要", Links: []agent.RenderedLink{{URL: "https://example.com/a", Text: "结果一"}}}, nil
	})}
	tools, err := plugin.AgentTools(SettingValues{
		webSearchSettingMode:       webSearchModeEngine,
		webSearchSettingEngines:    "Bing，unknown, bing, baidu",
		webSearchSettingExaEnabled: false,
	})
	if err != nil || len(tools) != 1 || tools[0].Name() != agent.WebSearchToolName {
		t.Fatalf("tools=%#v err=%v", tools, err)
	}
	output, err := tools[0].Run(context.Background(), map[string]any{"query": "测试"})
	if err != nil {
		t.Fatal(err)
	}
	// 两个引擎分两路并行查，结果合并去重。
	sort.Strings(rendered)
	if len(rendered) != 2 || !strings.HasPrefix(rendered[0], "https://www.baidu.com/s?") || !strings.HasPrefix(rendered[1], "https://www.bing.com/search?") {
		t.Fatalf("rendered = %v", rendered)
	}
	if !strings.Contains(output, `"provider":"bing"`) || !strings.Contains(output, "https://example.com/a") {
		t.Fatalf("output = %s", output)
	}
	if got := webSearchEngineOrder(" , nope"); strings.Join(got, ",") != strings.Join(agent.DefaultSearchEngines, ",") {
		t.Fatalf("empty order = %v", got)
	}
}

// fakeExaMCP 是一个最小的 Exa MCP 服务：available=false 时整个服务 503。
func fakeExaMCP(t *testing.T, available bool) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !available {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		var request struct {
			Method string `json:"method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeEvent := func(payload string) {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = fmt.Fprintf(w, "event: message\ndata: %s\n\n", payload)
		}
		switch request.Method {
		case "initialize":
			w.Header().Set("Mcp-Session-Id", "test-session")
			writeEvent(`{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-03-26","capabilities":{"tools":{}}}}`)
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/call":
			writeEvent(`{"jsonrpc":"2.0","id":2,"result":{"content":[{"type":"text","text":"Title: Exa 结果\nURL: https://example.net/exa\nText: 摘要"}],"isError":false}}`)
		default:
			http.Error(w, "unknown method", http.StatusBadRequest)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func TestWebSearchPluginSearchEngineModeAsksExaFirst(t *testing.T) {
	for _, tc := range []struct {
		name         string
		exaAvailable bool
		wantProvider string
		wantRendered int
	}{
		{name: "exa answers", exaAvailable: true, wantProvider: `"provider":"exa"`, wantRendered: 0},
		{name: "exa down falls back to engines", exaAvailable: false, wantProvider: `"provider":"bing"`, wantRendered: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			var rendered []string
			plugin := &WebSearchPlugin{renderer: agent.PageRendererFunc(func(_ context.Context, rawURL string) (agent.RenderedPage, error) {
				mu.Lock()
				rendered = append(rendered, rawURL)
				mu.Unlock()
				return agent.RenderedPage{URL: rawURL, Title: "结果", Text: "摘要", Links: []agent.RenderedLink{{URL: "https://example.com/a", Text: "结果一"}}}, nil
			})}
			tools, err := plugin.AgentTools(SettingValues{
				webSearchSettingMode:    webSearchModeEngine,
				webSearchSettingEngines: "bing,baidu",
				webSearchSettingExaURL:  fakeExaMCP(t, tc.exaAvailable).URL,
			})
			if err != nil || len(tools) != 1 {
				t.Fatalf("tools=%#v err=%v", tools, err)
			}
			output, err := tools[0].Run(context.Background(), map[string]any{"query": "测试"})
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(output, tc.wantProvider) {
				t.Fatalf("output = %s", output)
			}
			// Exa 有结果就不开浏览器；Exa 挂了才按引擎顺序往下换，第一家有结果就停。
			if len(rendered) != tc.wantRendered {
				t.Fatalf("rendered = %v", rendered)
			}
		})
	}
}

func TestWebSearchPluginCustomSearchEngineRunsAlongsideBuiltin(t *testing.T) {
	var mu sync.Mutex
	var rendered []string
	plugin := &WebSearchPlugin{renderer: agent.PageRendererFunc(func(_ context.Context, rawURL string) (agent.RenderedPage, error) {
		mu.Lock()
		rendered = append(rendered, rawURL)
		mu.Unlock()
		if strings.HasPrefix(rawURL, "https://search.example.com/") {
			return agent.RenderedPage{URL: rawURL, Title: "没有结果", Text: "没有结果"}, nil
		}
		return agent.RenderedPage{URL: rawURL, Title: "结果", Text: "摘要", Links: []agent.RenderedLink{
			{URL: "https://www.bing.com/settings", Text: "设置"},
			{URL: "https://example.org/a", Text: "结果一"},
		}}, nil
	})}
	tools, err := plugin.AgentTools(SettingValues{
		webSearchSettingMode:       webSearchModeEngine,
		webSearchSettingEngines:    "https://search.example.com/s?q={query}&lang=zh\nbing\nhttp://evil.example/?q={query}",
		webSearchSettingExaEnabled: false,
	})
	if err != nil || len(tools) != 1 {
		t.Fatalf("tools=%#v err=%v", tools, err)
	}
	output, err := tools[0].Run(context.Background(), map[string]any{"query": "a b&c"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"https://search.example.com/s?q=a+b%26c&lang=zh", "https://www.bing.com/search?q=a+b%26c"}
	sort.Strings(rendered)
	if strings.Join(rendered, " ") != strings.Join(want, " ") {
		t.Fatalf("rendered = %v", rendered)
	}
	if !strings.Contains(output, `"provider":"bing"`) || !strings.Contains(output, "https://example.org/a") || strings.Contains(output, "evil.example") {
		t.Fatalf("output = %s", output)
	}
}

func TestRobotSearchSwitchDecidesOverLegacyGlobal(t *testing.T) {
	manager := NewDefaultPluginManager()
	if err := manager.SaveSearchAssignment(WebSearchAssignment{Disabled: true, ProviderIDs: []string{"browser"}}, nil); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		overrides map[string]bool
		want      bool
	}{{nil, false}, {map[string]bool{webSearchPluginID: true}, true}, {map[string]bool{webSearchPluginID: false}, false}} {
		tools, err := manager.AgentToolsWithOverrides(tc.overrides)
		if err != nil || hasAgentToolNamed(tools, agent.WebSearchToolName) != tc.want {
			t.Fatalf("overrides=%v want=%v err=%v", tc.overrides, tc.want, err)
		}
		if owners := manager.AgentToolOwners("", tc.overrides, nil); (len(owners[webSearchPluginID]) != 0) != tc.want {
			t.Fatalf("owners=%v", owners)
		}
	}
}

func TestWebSearchPluginAPIModeReturnsDiscovery(t *testing.T) {
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
	if requested != "" || len(result.Documents) != 0 || !strings.Contains(out, "只是搜索摘要") {
		t.Fatalf("搜索不应替模型选择页面：%s", out)
	}
}
