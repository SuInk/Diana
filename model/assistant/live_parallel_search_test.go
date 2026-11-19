// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SuInk/diana/model/agent"
	"github.com/SuInk/diana/model/llm"
)

// The request does not tell the model to batch. Sources are synthetic; only the
// model, runtime prompt and tool contracts are real. No delivery tools exist.
func TestLiveIndependentSearchUsesBatch(t *testing.T) {
	client := &liveResearchRecordedClient{client: liveLLMClient(t)}
	cfg := BotConfig{ResponseMode: ResponseModeCustom}
	if path := strings.TrimSpace(os.Getenv("DIANA_TEST_BOT_CONFIG")); path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(data, &cfg); err != nil {
			t.Fatal(err)
		}
	}
	const goURL = "https://go.dev/doc/devel/release"
	const rustURL = "https://blog.rust-lang.org/releases/"
	const pythonURL = "https://www.python.org/downloads/"
	pages := map[string]string{
		goURL:     "Official Go releases. Latest stable: Go 1.27.3, released 2026-11-01.",
		rustURL:   "Official Rust releases. Latest stable: Rust 1.92.0, released 2026-10-30.",
		pythonURL: "Official Python releases. Latest stable: Python 3.15.1, released 2026-10-31.",
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"results": []map[string]string{
			{"title": "Go releases", "url": goURL, "content": "Read official release history for latest stable version and release date."},
			{"title": "Rust releases", "url": rustURL, "content": "Read official release history for latest stable version and release date."},
			{"title": "Python releases", "url": pythonURL, "content": "Read official release history for latest stable version and release date."},
		}})
	}))
	defer server.Close()
	search, err := agent.NewWebSearchTool(agent.WebSearchToolOptions{
		Config:  agent.WebSearchConfig{Providers: []agent.WebSearchProviderConfig{{Name: "fixture", Type: "tavily", URL: server.URL}}},
		APIKeys: map[string]string{"fixture": "fixture-key"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	read := map[string]bool{}
	browser := agent.NewBrowserRenderTool(agent.PageRendererFunc(func(_ context.Context, raw string) (agent.RenderedPage, error) {
		mu.Lock()
		read[raw] = true
		mu.Unlock()
		return agent.RenderedPage{URL: raw, RequestedURL: raw, Text: pages[raw], Stable: true}, nil
	}))
	registry := agent.NewToolRegistry(search, browser)
	runtime := NewRuntime(cfg.WithDefaults(), nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	event := MessageEvent{Kind: EventKindPrivate, UserID: "replay", RawMessage: "查 Go、Rust 和 Python 各自最新稳定版及发布日期，做个简短对比。"}
	prompt := runtime.systemPromptWithRelationshipAndAgentTools(event, nil, false, RelationshipPolicy{}, true, registry)
	runner, err := agent.NewRunner(client, agent.Config{MaxSteps: 10}, registry)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	resp, err := runner.Run(ctx, agent.Request{Messages: []llm.Message{
		{Role: llm.RoleSystem, Content: prompt},
		{Role: llm.RoleSystem, Content: "当前时间：2026-11-02 00:00 UTC+08:00。"},
		{Role: llm.RoleUser, Content: "【当前需要回复的消息】" + event.RawMessage},
	}})
	if err != nil {
		t.Fatal(err)
	}
	batched := false
	for _, request := range client.calls {
		calls, _ := request["tool_calls"].([]llm.ToolCall)
		queries := map[string]bool{}
		for _, call := range calls {
			if call.Name != agent.WebSearchToolName {
				continue
			}
			input, _ := json.Marshal(call.Arguments)
			t.Logf("search input=%s", input)
			var args struct {
				Query   string   `json:"query"`
				Queries []string `json:"queries"`
			}
			if json.Unmarshal(input, &args) == nil {
				for _, query := range append(args.Queries, args.Query) {
					if query = strings.ToLower(strings.Join(strings.Fields(query), " ")); query != "" {
						queries[query] = true
					}
				}
			}
		}
		batched = batched || len(queries) > 1
	}
	t.Logf("batched=%v read=%v answer=%s", batched, read, resp.Text)
	if !batched {
		t.Error("model did not batch independent searches")
	}
	for source := range pages {
		if !read[source] {
			t.Errorf("source not read: %s", source)
		}
	}
	for _, version := range []string{"1.27.3", "1.92.0", "3.15.1"} {
		if !strings.Contains(resp.Text, version) {
			t.Errorf("answer omitted verified version %s", version)
		}
	}
}
