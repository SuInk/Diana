package assistant

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/SuInk/diana/model/agent"
	"github.com/SuInk/diana/model/llm"
)

type liveCacheFixtureTool struct {
	name  string
	calls int
}

func (t *liveCacheFixtureTool) Name() string { return t.name }
func (*liveCacheFixtureTool) Description() string {
	return "Read the current synthetic fixture value. Has no external side effects."
}
func (*liveCacheFixtureTool) InputSchema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false}
}
func (t *liveCacheFixtureTool) Run(context.Context, map[string]any) (string, error) {
	t.calls++
	return `{"value":"fixture-green-42"}`, nil
}

type liveCacheRecordingClient struct {
	llm.LLMClient
	key      string
	t        *testing.T
	requests []llm.GenerateRequest
	calls    []string
}

func (c *liveCacheRecordingClient) Generate(ctx context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	req.PromptCacheKey = c.key
	req.ReasoningEffort = "low"
	req.MaxOutputTokens = 512
	c.requests = append(c.requests, req)
	response, err := c.LLMClient.Generate(ctx, req)
	if response != nil {
		for _, call := range response.ToolCalls {
			c.calls = append(c.calls, call.Name)
		}
		c.t.Logf("request=%d input=%d cached=%d tools_called=%v", len(c.requests), response.Usage.InputTokens, response.Usage.CachedInputTokens, c.calls)
	}
	return response, err
}

func TestLiveGroupToolSession(t *testing.T) {
	client := liveLLMClient(t)
	e := MessageEvent{Kind: EventKindGroup, ProfileID: "test", SelfID: "bot", GroupID: "synthetic-tools", UserID: "alice"}
	probe := &liveCacheRecordingClient{LLMClient: client, key: promptCacheRoutingKey(e, fmt.Sprint(time.Now().UnixNano())), t: t}
	store := &promptSessionTestStore{memoryMessageHistoryStore: newMemoryMessageHistoryStore(), states: map[string]GroupPromptSession{}}
	for turn := 0; turn < 2; turn++ {
		// New Runtime + new Runner simulates restart. No tool instance is reused.
		r := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
		r.SetMessageHistoryStore(store)
		session := r.groupPromptSession(e)
		lookup := &liveCacheFixtureTool{name: "fixture_lookup"}
		runner, err := agent.NewRunner(probe, agent.Config{WorkDir: t.TempDir(), CoreTools: []string{"fixture_common"}, MaxSteps: 6}, agent.NewToolRegistry(&liveCacheFixtureTool{name: "fixture_common"}, lookup))
		if err != nil {
			t.Fatal(err)
		}
		instruction := "First call tools_load with names=[fixture_lookup], then call tools_execute with name=fixture_lookup and input={} to read its live value, then call agent_finalize with that value in content. Do not call any other tool."
		if turn == 1 {
			instruction = "The fixture_lookup contract is already loaded. Call tools_execute with name=fixture_lookup and input={} directly, then call agent_finalize. Do not call tools_load."
		}
		before := len(probe.requests)
		callOffset := len(probe.calls)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		_, err = runner.Run(ctx, agent.Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: instruction}}, LoadedTools: session.loadedTools(), ToolsLoaded: session.rememberTools})
		cancel()
		runner.Close()
		if err != nil {
			t.Fatal(err)
		}
		if lookup.calls != 1 {
			t.Fatalf("turn %d fixture calls=%d", turn, lookup.calls)
		}
		if turn == 0 {
			if len(session.loadedTools()) != 1 || session.loadedTools()[0] != "fixture_lookup" {
				t.Fatal("discovery not saved")
			}
		} else {
			if system := probe.requests[before].Messages[0].Content; !strings.Contains(system, `"name":"fixture_lookup"`) {
				t.Fatal("restarted first request lost persisted tool contract")
			}
			for _, name := range probe.calls[callOffset:] {
				if name == agent.ToolsLoadToolName {
					t.Fatal("restarted run reloaded a known tool")
				}
			}
		}
	}
}
