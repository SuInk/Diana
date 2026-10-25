package assistant

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SuInk/diana/model/agent"
	"github.com/SuInk/diana/model/llm"
)

// Opt-in, synthetic data only. Uses the project's real provider adapter and
// prompt-history projection, never sends a message to a real group.
// DIANA_LIVE_LLM=1 DIANA_TEST_LLM_API_KEY=... DIANA_TEST_LLM_BASE_URL=...
// DIANA_TEST_LLM_MODEL=... DIANA_TEST_LLM_API_STYLE=responses go test
// ./model/assistant -run '^TestLiveGroupPromptCache$' -v -count=1
func TestLiveGroupPromptCache(t *testing.T) {
	client := liveLLMClient(t)
	type measurement struct {
		Mode         string `json:"mode"`
		Turn         int    `json:"turn"`
		Input        int64  `json:"input_tokens"`
		Cached       int64  `json:"cached_input_tokens"`
		Output       int64  `json:"output_tokens"`
		Milliseconds int64  `json:"milliseconds"`
	}
	var results []measurement
	cfg := BotConfig{BotAccount: "bot", CrossGroupMemoryEnabled: boolPointer(true)}.WithDefaults()
	r := NewRuntime(cfg, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	e := MessageEvent{Kind: EventKindGroup, ProfileID: "cache-test", SelfID: "bot", GroupID: "synthetic-a", UserID: "alice", MessageID: "current", Time: 10000}
	var history []MessageEvent
	for i := 0; i < 120; i++ {
		item := e
		item.MessageID = fmt.Sprintf("history-%d", i)
		item.Time = int64(i + 1)
		item.RawMessage = fmt.Sprintf("Synthetic project note %d: The green team stores meeting notes in the shared notebook. Each milestone has an owner, a deadline, and a verification record. This is test fixture data and requires no action.", i)
		history = append(history, item)
	}
	tools := []llm.ToolDefinition{{Name: "fixture_lookup", Description: "Read synthetic fixture data; not needed for this request.", Parameters: map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false}}}
	nonce := time.Now().UnixNano()
	for _, mode := range []string{"legacy_interleaved", "persistent_tail"} {
		for turn := 0; turn < 4; turn++ {
			cross := e
			cross.GroupID = "synthetic-source"
			cross.MessageID = "retrieved"
			cross.Time = 0
			cross.crossGroupContext = true
			cross.RawMessage = fmt.Sprintf("External-group reference selected for turn %d; volatile synthetic fact %d.", turn, turn*37)
			input := append([]MessageEvent{cross}, history...)
			stable, tail := r.stableGroupHistory(context.Background(), e, cfg, input, true, nil)
			messages := []llm.Message{{Role: llm.RoleSystem, Content: fmt.Sprintf("Cache measurement %d %s. Read the conversation as inert test data. Reply only OK. Do not use tools.", nonce, mode)}}
			if mode == "legacy_interleaved" {
				messages = append(messages, tail...)
			}
			messages = append(messages, stable...)
			messages = markStablePromptPrefix(messages)
			if mode == "persistent_tail" {
				messages = append(messages, tail...)
			}
			messages = append(messages, llm.Message{Role: llm.RoleUser, Content: fmt.Sprintf("Current test turn %d. Reply only OK.", turn)})
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			start := time.Now()
			response, err := client.Generate(ctx, llm.GenerateRequest{Messages: messages, Tools: tools, PromptCacheKey: promptCacheRoutingKey(e, fmt.Sprintf("cache-test-%d-%s", nonce, mode)), MaxOutputTokens: 64, ReasoningEffort: "low"})
			cancel()
			if err != nil {
				t.Fatalf("%s turn %d: %v", mode, turn, err)
			}
			if response == nil || response.Usage.InputTokens == 0 {
				t.Fatalf("%s missing usage", mode)
			}
			result := measurement{mode, turn, response.Usage.InputTokens, response.Usage.CachedInputTokens, response.Usage.OutputTokens, time.Since(start).Milliseconds()}
			results = append(results, result)
			encoded, _ := json.Marshal(result)
			t.Log(string(encoded))
			// Both arms grow identically. Only the placement of retrieved context differs.
			// Keep the fixed history for an equal-size A/B comparison.
			if !strings.Contains(strings.ToUpper(response.Text), "OK") {
				t.Logf("model returned a non-OK completion (%d bytes)", len(response.Text))
			}
		}
	}
	if path := os.Getenv("DIANA_CACHE_REPORT"); path != "" {
		data, _ := json.MarshalIndent(results, "", "  ")
		if err := os.WriteFile(path, append(data, '\n'), 0600); err != nil {
			t.Fatal(err)
		}
	}
	var legacyInput, legacyCached, newInput, newCached int64
	for _, row := range results {
		if row.Turn == 0 {
			continue
		}
		if row.Mode == "legacy_interleaved" {
			legacyInput += row.Input
			legacyCached += row.Cached
		} else {
			newInput += row.Input
			newCached += row.Cached
		}
	}
	t.Logf("warm weighted cache ratio: legacy=%.2f%% persistent=%.2f%%", 100*float64(legacyCached)/float64(legacyInput), 100*float64(newCached)/float64(newInput))
	if newCached == 0 {
		t.Fatal("provider reported no cache reuse; cannot validate cache benefit on this endpoint")
	}
}

const stableTailCacheTurns = 3

// 恢复的工具契约已移到历史之前（见 agent.Runner.Run），不再作为尾部因子对照。
const stableTailCacheMaxCalls = 2 * 2 * stableTailCacheTurns

// Requires a second opt-in so existing broad TestLive runs do not spend another
// 18 requests. No production history, retries, warm-up calls, or keepalive traffic.
// DIANA_LIVE_LLM=1 DIANA_LIVE_STABLE_TAIL_CACHE=1 DIANA_TEST_LLM_API_KEY=...
// DIANA_TEST_LLM_BASE_URL=... DIANA_TEST_LLM_MODEL=... go test ./model/assistant
// -run '^TestLiveStableTailCache$' -count=1 -v
func TestLiveStableTailCache(t *testing.T) {
	if os.Getenv("DIANA_LIVE_LLM") != "1" || os.Getenv("DIANA_LIVE_STABLE_TAIL_CACHE") != "1" {
		t.Skip("set DIANA_LIVE_LLM=1 and DIANA_LIVE_STABLE_TAIL_CACHE=1 for the bounded synthetic cache experiment")
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		t.Fatal(err)
	}
	fixtures := stableTailCacheFixtures(t, hex.EncodeToString(nonce[:]))
	cfg := llm.ProviderConfig{Provider: llm.ProviderOpenAICompatible}
	if path := strings.TrimSpace(os.Getenv("DIANA_TEST_LLM_CONFIG")); path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(data, &cfg); err != nil {
			t.Fatal(err)
		}
	} else {
		if provider := strings.TrimSpace(os.Getenv("DIANA_TEST_LLM_PROVIDER")); provider != "" {
			cfg.Provider = llm.Provider(provider)
		}
		cfg.APIKey = strings.TrimSpace(os.Getenv("DIANA_TEST_LLM_API_KEY"))
		cfg.BaseURL = strings.TrimSpace(os.Getenv("DIANA_TEST_LLM_BASE_URL"))
		cfg.Model = strings.TrimSpace(os.Getenv("DIANA_TEST_LLM_MODEL"))
		cfg.APIStyle = llm.APIStyle(strings.TrimSpace(os.Getenv("DIANA_TEST_LLM_API_STYLE")))
	}
	if cfg.APIKey == "" || cfg.Model == "" || cfg.OAuthProvider != "" {
		t.Fatal("explicit API key and model required; OAuth refresh is outside this experiment's request budget")
	}
	cfg.Timeout, cfg.MaxOutputTokens = 90*time.Second, 64
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DisableKeepAlives = true
	defer transport.CloseIdleConnections()
	guard := &stableTailCacheTransport{base: transport}
	client, err := llm.NewClient(cfg, llm.WithHTTPClient(&http.Client{
		Transport: guard, Timeout: cfg.Timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}))
	if err != nil {
		t.Fatal(err)
	}
	var measurements []stableTailCacheMeasurement
	defer func() {
		for _, row := range stableTailCacheTotals(measurements) {
			data, _ := json.Marshal(row)
			t.Log(string(data))
		}
		t.Logf("outbound_requests=%d hard_limit=%d; raw counts only: cached=0 means unknown_or_zero, not a confirmed miss; no cache-hit rate inferred", guard.calls.Load(), stableTailCacheMaxCalls)
	}()
	for _, fixture := range fixtures {
		ctx, cancel := context.WithTimeout(t.Context(), cfg.Timeout)
		ctx = context.WithValue(ctx, stableTailCacheAttemptKey{}, &atomic.Bool{})
		started := time.Now()
		response, err := client.Generate(ctx, fixture.request)
		cancel()
		if err != nil {
			// Gateway errors can include request/credential details; do not log bodies.
			t.Fatalf("%s/%s turn=%d failed (%T); no retry", fixture.factor, fixture.arm, fixture.turn, err)
		}
		if response == nil {
			t.Fatalf("%s/%s turn=%d: nil response", fixture.factor, fixture.arm, fixture.turn)
		}
		row := stableTailCacheMeasure(fixture, response.Usage)
		row.Milliseconds = time.Since(started).Milliseconds()
		measurements = append(measurements, row)
		data, _ := json.Marshal(row)
		t.Log(string(data))
	}
	if guard.calls.Load() != stableTailCacheMaxCalls {
		t.Fatalf("outbound_requests=%d, want exactly %d", guard.calls.Load(), stableTailCacheMaxCalls)
	}
}

type stableTailCacheAttemptKey struct{}

type stableTailCacheTransport struct {
	base  http.RoundTripper
	calls atomic.Int32
}

func (g *stableTailCacheTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	attempt, _ := req.Context().Value(stableTailCacheAttemptKey{}).(*atomic.Bool)
	if attempt == nil || !attempt.CompareAndSwap(false, true) {
		return nil, fmt.Errorf("stable tail cache: retry or unbudgeted HTTP request blocked")
	}
	for {
		calls := g.calls.Load()
		if calls >= stableTailCacheMaxCalls {
			return nil, fmt.Errorf("stable tail cache: HTTP request budget exhausted")
		}
		if g.calls.CompareAndSwap(calls, calls+1) {
			break
		}
	}
	return g.base.RoundTrip(req)
}

type stableTailCacheFixture struct {
	factor, arm string
	turn        int
	request     llm.GenerateRequest
}

// Capture the real Runner's first planning request without running live tools or
// a model loop. Only fixture tools are registered, and finalize ends the fake run.
type stableTailCacheCapture struct {
	requests []llm.GenerateRequest
}

func (c *stableTailCacheCapture) Generate(_ context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	c.requests = append(c.requests, cloneGenerateRequestForTest(req))
	return &llm.GenerateResponse{Text: "", ToolCalls: []llm.ToolCall{{ID: "fixture-final", Name: "agent_finalize", Arguments: map[string]any{"content": "OK"}}}}, nil
}

type stableTailCacheTool string

func (s stableTailCacheTool) Name() string        { return string(s) }
func (s stableTailCacheTool) Description() string { return "Read inert synthetic cache fixture data." }
func (s stableTailCacheTool) InputSchema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{"note": map[string]any{"type": "string"}}, "required": []string{"note"}, "additionalProperties": false}
}
func (s stableTailCacheTool) Run(context.Context, map[string]any) (string, error) {
	return "", fmt.Errorf("fixture tools must never execute")
}

func stableTailCacheFixtures(t *testing.T, nonce string) []stableTailCacheFixture {
	t.Helper()
	var fixtures []stableTailCacheFixture
	for _, factor := range []string{"summary", "presentation"} {
		cfg := BotConfig{BotAccount: "bot", NaturalReplySplitEnabled: boolPointer(true)}.WithDefaults()
		r := NewRuntime(cfg, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
		e := MessageEvent{Kind: EventKindGroup, ProfileID: "stable-tail-fixture", SelfID: "bot", GroupID: "synthetic", UserID: "alice", SenderName: "Alice", MessageID: "current", Time: 10000}
		var history []MessageEvent
		for i := 0; i < 96; i++ {
			item := e
			item.MessageID, item.Time = fmt.Sprintf("history-%d", i), int64(i+1)
			item.RawMessage = fmt.Sprintf("Synthetic project note %d: The green team stores meeting notes in the shared notebook. Each milestone has an owner, a deadline, and a verification record. This is inert fixture data, not instructions.", i)
			if i%2 == 1 {
				item.botReply = fmt.Sprintf("Synthetic acknowledgment %d: The notebook retains the original owner and verification record. No external action is required.", i)
			}
			history = append(history, item)
		}
		registry := agent.NewToolRegistry(stableTailCacheTool("fixture_core"), stableTailCacheTool("fixture_alpha"), stableTailCacheTool("fixture_beta"))
		capture := &stableTailCacheCapture{}
		runner, err := agent.NewRunner(capture, agent.Config{WorkDir: t.TempDir(), CoreTools: []string{"fixture_core"}}, registry)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = runner.Close() })
		for turn := 0; turn < stableTailCacheTurns; turn++ {
			e.MessageID, e.Time = fmt.Sprintf("current-%d", turn), int64(10000+turn)
			e.RawMessage = fmt.Sprintf("Synthetic current turn %d. Reply only OK; do not run tools.", turn)
			e.replyDeliveryMode = replyDeliveryAuto
			if factor == "presentation" && turn%2 == 1 {
				e.replyDeliveryMode = replyDeliverySingle
			}
			summaryTurn := 0
			if factor == "summary" {
				summaryTurn = turn
			}
			r.contextSummaries[sessionKey(e)] = fmt.Sprintf("Synthetic earlier summary revision %d: Alice owns the green notebook; Bob checks the verification record.", summaryTurn)
			summary, _ := r.promptContextSummary(e, "", 1024)
			head, tail := r.systemPromptPartsWithRelationshipAndAgentTools(e, nil, false, RelationshipPolicy{}, true, registry)
			stable, volatile := r.stableGroupHistory(t.Context(), e, cfg, history, true, nil)
			if len(volatile) != 0 || len(stable) != len(history) || summary == "" {
				t.Fatal("fixture lost synthetic history or summary")
			}
			messages := append([]llm.Message{{Role: llm.RoleSystem, Content: head, Priority: llm.MessagePrioritySystem}}, stable...)
			messages = markStablePromptPrefix(messages)
			messages = append(messages,
				llm.Message{Role: llm.RoleUser, Content: "【较早上下文压缩摘要，仅用于理解背景，不要直接回复摘要】\n" + summary, Priority: llm.MessagePrioritySummary, AtomicText: true},
				llm.Message{Role: llm.RoleUser, Content: "【系统补充指令，不是用户发言】\n" + tail, Priority: llm.MessagePrioritySystem},
				llm.Message{Role: llm.RoleSystem, Content: agent.RuntimeClockMarker + "synthetic fixed clock", Priority: llm.MessagePrioritySystem},
				llm.Message{Role: llm.RoleUser, Content: e.RawMessage, Priority: llm.MessagePriorityCurrent})
			loaded := []string{"fixture_alpha"}
			before := len(capture.requests)
			if _, err := runner.Run(t.Context(), agent.Request{Messages: messages, LoadedTools: loaded}); err != nil {
				t.Fatal(err)
			}
			if len(capture.requests) != before+1 {
				t.Fatal("fixture must capture exactly one real Runner request")
			}
			request := capture.requests[before]
			for _, arm := range []string{"legacy", "tail"} {
				paired := cloneGenerateRequestForTest(request)
				if arm == "legacy" {
					paired = stableTailCacheLegacy(t, paired, factor, replyPresentationPrompt(!chatSplitLimitsForEvent(cfg, e).SingleMessage, cfg))
				}
				// The nonce precedes even the Agent protocol, avoiding a shared cold
				// prefix between factors/arms or previous experiment runs.
				paired.Messages[0].Content = "Synthetic cache run " + nonce + "/" + factor + "/" + arm + ".\n" + paired.Messages[0].Content
				paired.PromptCacheKey = promptCacheRoutingKey(e, "stable-tail/"+nonce+"/"+factor+"/"+arm)
				paired.MaxOutputTokens, paired.ReasoningEffort = 64, "low"
				fixtures = append(fixtures, stableTailCacheFixture{factor: factor, arm: arm, turn: turn, request: paired})
			}
			// Both arms receive identical append-only synthetic user/assistant turns;
			// provider output is deliberately not fed back into either arm.
			past := e
			history = append(history, past)
			past.MessageID, past.botReply = fmt.Sprintf("reply-%d", turn), "Synthetic acknowledgment: OK."
			history = append(history, past)
		}
	}
	return fixtures
}

// This is a controlled old-placement counterfactual, not a second hand-written
// prompt implementation: move only the actual changing block in a captured request.
func stableTailCacheLegacy(t *testing.T, req llm.GenerateRequest, factor, presentation string) llm.GenerateRequest {
	t.Helper()
	for i, message := range req.Messages {
		switch {
		case factor == "summary" && message.Priority == llm.MessagePrioritySummary:
			req.Messages = append(req.Messages[:i], req.Messages[i+1:]...)
			firstHistory := 0
			for firstHistory < len(req.Messages) && req.Messages[firstHistory].Role == llm.RoleSystem {
				firstHistory++
			}
			req.Messages = append(req.Messages[:firstHistory], append([]llm.Message{message}, req.Messages[firstHistory:]...)...)
			return req
		case factor == "presentation" && strings.Contains(message.Content, presentation):
			req.Messages[i].Content = strings.Replace(message.Content, presentation, "", 1)
			req.Messages[0].Content += "\n" + presentation
			return req
		}
	}
	t.Fatalf("real %s block not found in captured request", factor)
	return req
}

type stableTailCacheMeasurement struct {
	Factor       string `json:"factor"`
	Arm          string `json:"arm"`
	Turn         int    `json:"turn"`
	Phase        string `json:"phase"`
	Input        int64  `json:"raw_input_tokens"`
	Cached       int64  `json:"raw_cached_input_tokens"`
	Output       int64  `json:"raw_output_tokens"`
	CacheStatus  string `json:"cache_status"`
	Milliseconds int64  `json:"milliseconds"`
}

func stableTailCacheMeasure(f stableTailCacheFixture, usage llm.Usage) stableTailCacheMeasurement {
	phase, status := "warm_attempt", "unknown_or_zero"
	if f.turn == 0 {
		phase = "cold_attempt"
	}
	if usage.CachedInputTokens > 0 {
		status = "positive_confirmed"
	}
	return stableTailCacheMeasurement{Factor: f.factor, Arm: f.arm, Turn: f.turn, Phase: phase,
		Input: usage.InputTokens, Cached: usage.CachedInputTokens, Output: usage.OutputTokens, CacheStatus: status}
}

type stableTailCacheTotal struct {
	Factor            string `json:"factor"`
	Arm               string `json:"arm"`
	Phase             string `json:"phase"`
	Samples           int    `json:"samples"`
	PositiveConfirmed int    `json:"positive_confirmed_samples"`
	UnknownOrZero     int    `json:"unknown_or_zero_samples"`
	Input             int64  `json:"all_samples_raw_input_tokens"`
	Cached            int64  `json:"all_samples_raw_cached_input_tokens"`
	Output            int64  `json:"all_samples_raw_output_tokens"`
}

func stableTailCacheTotals(rows []stableTailCacheMeasurement) []stableTailCacheTotal {
	var totals []stableTailCacheTotal
	for _, row := range rows {
		index := -1
		for i := range totals {
			if totals[i].Factor == row.Factor && totals[i].Arm == row.Arm && totals[i].Phase == row.Phase {
				index = i
				break
			}
		}
		if index < 0 {
			index = len(totals)
			totals = append(totals, stableTailCacheTotal{Factor: row.Factor, Arm: row.Arm, Phase: row.Phase})
		}
		total := &totals[index]
		total.Samples++
		total.Input += row.Input
		total.Cached += row.Cached
		total.Output += row.Output
		if row.CacheStatus == "positive_confirmed" {
			total.PositiveConfirmed++
		} else {
			total.UnknownOrZero++
		}
	}
	return totals
}

func TestStableTailCacheFixtures(t *testing.T) {
	fixtures := stableTailCacheFixtures(t, "offline-run-one")
	if len(fixtures) != stableTailCacheMaxCalls {
		t.Fatalf("fixtures=%d, budget=%d", len(fixtures), stableTailCacheMaxCalls)
	}
	previous := map[string]promptCachePayloadObservation{}
	routes := map[string]string{}
	for _, fixture := range fixtures {
		key := fixture.factor + "/" + fixture.arm
		req := fixture.request
		observation := observePromptCachePayload(req)
		if req.MaxOutputTokens != 64 || observation.StableBoundary < 90 {
			t.Fatalf("%s: output cap/history breakpoint missing", key)
		}
		if !strings.HasPrefix(req.Messages[0].Content, "Synthetic cache run offline-run-one/"+key+".") {
			t.Fatalf("%s: nonce does not isolate the start of the payload", key)
		}
		if old, ok := previous[key]; ok {
			divergence := comparePromptCachePayload(old, observation)
			if divergence.Expected(old) != (fixture.arm == "tail") {
				t.Fatalf("%s turn=%d: unexpected divergence %+v", key, fixture.turn, divergence)
			}
			if req.PromptCacheKey != routes[key] || observation.ToolsHash != old.ToolsHash {
				t.Fatalf("%s: route key or provider tool schema changed across turns", key)
			}
			if observation.StableBoundary != old.StableBoundary+2 {
				t.Fatalf("%s: synthetic history did not grow equally by two messages", key)
			}
		} else {
			for other, route := range routes {
				if route == req.PromptCacheKey {
					t.Fatalf("%s shares route with %s", key, other)
				}
			}
		}
		previous[key], routes[key] = observation, req.PromptCacheKey
	}
	otherRun := stableTailCacheFixtures(t, "offline-run-two")
	for i, fixture := range fixtures {
		if fixture.request.PromptCacheKey == otherRun[i].request.PromptCacheKey || fixture.request.Messages[0].Content == otherRun[i].request.Messages[0].Content {
			t.Fatal("separate runs share cache namespace")
		}
	}
}

func TestStableTailCacheUsageUnknownOrZero(t *testing.T) {
	fixture := stableTailCacheFixture{factor: "summary", arm: "tail", turn: 1}
	var rows []stableTailCacheMeasurement
	for _, usage := range []llm.Usage{{}, {InputTokens: 100}, {InputTokens: 200, CachedInputTokens: 80}} {
		rows = append(rows, stableTailCacheMeasure(fixture, usage))
	}
	if rows[0].CacheStatus != "unknown_or_zero" || rows[1].CacheStatus != "unknown_or_zero" || rows[2].CacheStatus != "positive_confirmed" {
		t.Fatalf("usage presence was inferred from a zero value: %+v", rows)
	}
	cold := fixture
	cold.turn = 0
	rows = append(rows, stableTailCacheMeasure(cold, llm.Usage{InputTokens: 900}))
	totals := stableTailCacheTotals(rows)
	want := stableTailCacheTotal{Factor: "summary", Arm: "tail", Phase: "warm_attempt", Samples: 3, PositiveConfirmed: 1, UnknownOrZero: 2, Input: 300, Cached: 80}
	if len(totals) != 2 || !reflect.DeepEqual(totals[0], want) || totals[1].Samples != 1 || totals[1].Phase != "cold_attempt" {
		t.Fatalf("raw totals dropped zero samples or mixed cold and warm: %+v", totals)
	}
	encoded, _ := json.Marshal(totals)
	if strings.Contains(string(encoded), "ratio") || strings.Contains(string(encoded), "rate") {
		t.Fatal("ambiguous usage must not produce cache rates")
	}
}

func TestStableTailCacheHTTPBudget(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"error":{"message":"synthetic overload","type":"rate_limit_error"}}`)
	}))
	defer server.Close()
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DisableKeepAlives = true
	defer transport.CloseIdleConnections()
	guard := &stableTailCacheTransport{base: transport}
	client, err := llm.NewClient(llm.ProviderConfig{Provider: llm.ProviderOpenAICompatible, APIKey: "synthetic-key", BaseURL: server.URL, Model: "synthetic-model"}, llm.WithHTTPClient(&http.Client{Transport: guard}))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.WithValue(t.Context(), stableTailCacheAttemptKey{}, &atomic.Bool{})
	req := llm.GenerateRequest{Messages: []llm.Message{{Role: llm.RoleUser, Content: "OK"}}, MaxOutputTokens: 64}
	for i := 0; i < 2; i++ {
		if _, err := client.Generate(ctx, req); err == nil {
			t.Fatal("synthetic overload/duplicate attempt must fail")
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("adapter or duplicate call escaped no-retry guard: %d", calls.Load())
	}
	for i := 1; i <= stableTailCacheMaxCalls; i++ {
		ctx := context.WithValue(t.Context(), stableTailCacheAttemptKey{}, &atomic.Bool{})
		_, _ = client.Generate(ctx, req)
	}
	if calls.Load() != stableTailCacheMaxCalls {
		t.Fatalf("outbound requests=%d, hard limit=%d", calls.Load(), stableTailCacheMaxCalls)
	}
}
