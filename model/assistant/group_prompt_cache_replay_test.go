package assistant

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SuInk/diana/model/llm"
)

const (
	traceReplaySummary    = "【较早上下文压缩摘要，仅用于理解背景，不要直接回复摘要】\n"
	traceReplayContracts  = "当前群会话已经加载、可直接通过 tools_execute 调用的完整契约：\n"
	traceReplaySupplement = "【系统补充指令，不是用户发言】\n"
	traceReplayMaxBytes   = 32 << 20
)

// This is a first-model-step placement counterfactual, NOT a full Runtime replay.
// Input: a private JSON array of 1–3 original debug_trace envelopes, each with
// metadata.phase=model_request, metadata.purpose=reply, request and response.
// Select pre-change, text-only initial planning requests, ending with the current
// user message. Native/text-compatible tool continuations and omitted protocol/media
// are rejected. All arms must fit the current client's local budget without trimming.
// Only the three recognized current-turn blocks move; trace tools remain inert.
// Custom presentation overrides fail closed rather than silently testing defaults.
//
// DIANA_LIVE_LLM=1 DIANA_LIVE_PROMPT_TRACE_REPLAY=1
// DIANA_PROMPT_TRACE_REPLAY_IN=/private/traces.json
// DIANA_PROMPT_TRACE_REPLAY_OUT=/private/new-report.json
// DIANA_TEST_LLM_PROVIDER=openai_compatible|gemini|anthropic
// DIANA_TEST_LLM_API_KEY=... DIANA_TEST_LLM_BASE_URL=... DIANA_TEST_LLM_MODEL=...
// Optional: DIANA_TEST_LLM_API_STYLE=responses|chat_completions
// Optional: DIANA_PROMPT_TRACE_REPLAY_MAX_OUTPUT=1024 (Gemini only; default 512)
// Optional: DIANA_PROMPT_TRACE_REPLAY_CONTEXT_WINDOW / DIANA_PROMPT_TRACE_REPLAY_MAX_CONTEXT
// (positive integers; unset uses the same ProviderConfig defaults as the client).
// Offline: disable both live switches, set DIANA_PROMPT_TRACE_REPLAY_OFFLINE=1,
// IN/provider/model and the same output/context settings; run only
// '^TestGroupPromptTraceReplayOffline$'. No key, endpoint or OUT is needed.
// go test ./model/assistant -run '^TestLiveGroupPromptTraceReplay$' -count=1 -v -timeout=20m
// IN must be a regular owner-only file. OUT must not exist, and its existing parent
// must be owner-only. No raw chat, tool arguments, URLs or provider errors are logged.
func TestLiveGroupPromptTraceReplay(t *testing.T) {
	if os.Getenv("DIANA_LIVE_LLM") != "1" || os.Getenv("DIANA_LIVE_PROMPT_TRACE_REPLAY") != "1" {
		t.Skip("set both live and prompt-trace-replay opt-ins to enable private trace replay")
	}
	// applyContextBudget also consults an unexported, process-local learned limit.
	// Run alone, once: no earlier provider errors can seed that memo, and this
	// test stops on the first error instead of sending a later arm under a new limit.
	if flag.Lookup("test.run").Value.String() != "^TestLiveGroupPromptTraceReplay$" || flag.Lookup("test.count").Value.String() != "1" || flag.Lookup("test.cpu").Value.String() != "" || flag.Lookup("test.bench").Value.String() != "" || flag.Lookup("test.fuzz").Value.String() != "" {
		t.Fatal("live replay requires a fresh standalone go test with -run '^TestLiveGroupPromptTraceReplay$' -count=1 and no -cpu, benchmarks or fuzzing; learned context limits cannot be inspected")
	}
	cfg, err := traceReplayConfig()
	if err != nil {
		t.Fatal(err) // Configuration errors contain fixed messages only.
	}
	envelopes, err := readTraceReplayInput(os.Getenv("DIANA_PROMPT_TRACE_REPLAY_IN"))
	if err != nil {
		t.Fatal(err)
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		t.Fatal("replay nonce generation failed")
	}
	report := traceReplayReport{
		Scope:       "first-step original-request block-placement counterfactual; NOT a full Runtime replay; no tools or message delivery executed",
		CacheCaveat: "zero or absent cached usage is unknown; samples need not share a prefix; no cache-hit rate or cache-benefit conclusion is inferred",
		Provider:    cfg.Provider, Model: cfg.Model, MaxOutput: cfg.MaxOutputTokens,
		Limit: len(envelopes) * 2,
	}
	requests := make([][2]llm.GenerateRequest, len(envelopes))
	for i, envelope := range envelopes {
		pair, factors, err := traceReplayPair(envelope.Metadata.Request)
		if err != nil {
			t.Fatalf("sample %d: %s", i, err)
		}
		for arm := range pair {
			pair[arm].Model = cfg.Model // Never let a trace override the selected production model.
			pair[arm].MaxOutputTokens = cfg.MaxOutputTokens
			// Keep each arm's routing namespace separate without adding text to the prompt.
			key := sha256.Sum256([]byte(fmt.Sprintf("%x/%d/%s/%s/%s", nonce, arm,
				envelope.Metadata.ProfileID, envelope.Metadata.GroupID, envelope.Metadata.Request.PromptCacheKey)))
			pair[arm].PromptCacheKey = hex.EncodeToString(key[:])
		}
		budget, err := traceReplayPreflight(pair, cfg)
		if err != nil {
			t.Fatalf("sample %d: %s", i, err)
		}
		requests[i] = pair
		report.Samples = append(report.Samples, traceReplaySample{Index: i, Factors: factors, Budget: budget,
			OriginalResponse: envelope.Metadata.Response, OriginalDuration: envelope.Metadata.Duration,
			OriginalUsage: envelope.Metadata.Usage})
	}
	out, err := createTraceReplayOutput(os.Getenv("DIANA_PROMPT_TRACE_REPLAY_OUT"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		data, err := json.MarshalIndent(report, "", "  ")
		if err == nil {
			// In addition to never logging errors, redact exact configured credentials
			// if the provider unexpectedly echoes them into generated content.
			for _, secret := range []string{cfg.APIKey, cfg.BaseURL} {
				encoded, _ := json.Marshal(secret)
				if len(encoded) > 2 {
					data = []byte(strings.ReplaceAll(string(data), string(encoded[1:len(encoded)-1]), "[redacted]"))
				}
			}
			_, err = out.Write(append(data, '\n'))
		}
		closeErr := out.Close()
		if err != nil || closeErr != nil {
			t.Error("private replay report write failed")
		}
	}()

	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DisableKeepAlives = true
	defer transport.CloseIdleConnections()
	guard := &traceReplayTransport{base: &stableTailCacheTransport{base: transport}, limit: int32(report.Limit)}
	client, err := llm.NewClient(cfg, llm.WithHTTPClient(&http.Client{
		Transport: guard, Timeout: cfg.Timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}))
	if err != nil {
		t.Fatal("replay provider initialization failed")
	}
	defer func() { report.Outbound = guard.base.calls.Load() }()
	first := true
	for i, pair := range requests {
		for arm, request := range pair {
			if !first {
				// Serial, at least five seconds after the previous call completed.
				timer := time.NewTimer(5 * time.Second)
				select {
				case <-timer.C:
				case <-t.Context().Done():
					timer.Stop()
					t.Fatal("replay canceled during rate limiting")
				}
			}
			first = false
			ctx, cancel := context.WithTimeout(t.Context(), cfg.Timeout)
			ctx = context.WithValue(ctx, stableTailCacheAttemptKey{}, &atomic.Bool{})
			started := time.Now()
			response, callErr := client.Generate(ctx, request)
			cancel()
			row := traceReplayResult{Arm: []string{"original", "tail"}[arm], Milliseconds: time.Since(started).Milliseconds(), Response: response, CacheStatus: "unknown"}
			if response != nil {
				row.Input, row.Output = response.Usage.InputTokens, response.Usage.OutputTokens
				if response.Usage.CachedInputTokens > 0 {
					cached := response.Usage.CachedInputTokens
					row.Cached, row.CacheStatus = &cached, "positive_confirmed"
				}
				row.ResultKind = traceReplayResultKind(response)
			}
			if callErr != nil {
				row.Failure = "provider_error_no_retry"
			} else if response == nil || (strings.TrimSpace(response.Text) == "" && len(response.ToolCalls) == 0) {
				row.Failure = "empty_response"
			} else if row.Input <= 0 || row.Output <= 0 {
				row.Failure = "missing_token_usage"
			} else if row.ResultKind == "invalid_finalize" {
				row.Failure = "invalid_finalize"
			}
			report.Samples[i].Results = append(report.Samples[i].Results, row)
			if row.Failure != "" {
				t.Fatalf("sample %d arm %d failed (%s); details are private; no retry", i, arm, row.Failure)
			}
		}
	}
	if guard.base.calls.Load() != int32(report.Limit) {
		t.Fatal("replay did not make exactly one HTTP request per sample/arm")
	}
	report.Complete = true
	t.Logf("completed %d samples, %d requests; private report only; behavioral quality requires manual comparison", len(envelopes), report.Limit)
}

type traceReplayEnvelope struct {
	Metadata struct {
		Phase     string                `json:"phase"`
		Purpose   string                `json:"purpose"`
		Error     string                `json:"error"`
		ProfileID string                `json:"profile_id"`
		GroupID   string                `json:"group_id"`
		Request   llm.GenerateRequest   `json:"request"`
		Response  *llm.GenerateResponse `json:"response"`
		Usage     llm.Usage             `json:"usage"`
		Duration  int64                 `json:"duration_ms"`
	} `json:"metadata"`
}

type traceReplayReport struct {
	Scope       string              `json:"scope"`
	CacheCaveat string              `json:"cache_caveat"`
	Provider    llm.Provider        `json:"provider"`
	Model       string              `json:"model"`
	MaxOutput   int64               `json:"max_output_tokens"`
	Limit       int                 `json:"request_limit"`
	Outbound    int32               `json:"outbound_requests"`
	Complete    bool                `json:"complete"`
	Samples     []traceReplaySample `json:"samples"`
}

type traceReplaySample struct {
	Index            int                   `json:"sample_index"`
	Factors          []string              `json:"moved_factors"`
	Budget           [2]traceReplayBudget  `json:"budget_preflight"`
	OriginalResponse *llm.GenerateResponse `json:"original_response,omitempty"`
	OriginalUsage    llm.Usage             `json:"original_usage"`
	OriginalDuration int64                 `json:"original_duration_ms"`
	Results          []traceReplayResult   `json:"results"`
}

type traceReplayResult struct {
	Arm          string                `json:"arm"`
	Input        int64                 `json:"input_tokens"`
	Cached       *int64                `json:"cached_input_tokens"`
	Output       int64                 `json:"output_tokens"`
	CacheStatus  string                `json:"cache_status"`
	Milliseconds int64                 `json:"milliseconds"`
	ResultKind   string                `json:"result_kind"`
	Failure      string                `json:"failure,omitempty"`
	Response     *llm.GenerateResponse `json:"response,omitempty"`
}

func traceReplayConfig() (llm.ProviderConfig, error) {
	cfg, err := traceReplayBudgetConfig()
	if err != nil {
		return cfg, err
	}
	cfg.APIKey = strings.TrimSpace(os.Getenv("DIANA_TEST_LLM_API_KEY"))
	cfg.BaseURL = strings.TrimSpace(os.Getenv("DIANA_TEST_LLM_BASE_URL"))
	if cfg.APIKey == "" || cfg.BaseURL == "" {
		return cfg, errors.New("explicit API key and base URL are required")
	}
	u, err := url.Parse(cfg.BaseURL)
	if err != nil || u.Host == "" || u.Scheme != "https" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return cfg, errors.New("base URL must be HTTPS without userinfo, query or fragment")
	}
	return cfg.WithDefaults(), nil
}

// Shared by live and offline preflight; never loads credentials or production config.
func traceReplayBudgetConfig() (llm.ProviderConfig, error) {
	cfg := llm.ProviderConfig{
		Provider:        llm.Provider(strings.TrimSpace(os.Getenv("DIANA_TEST_LLM_PROVIDER"))),
		Model:           strings.TrimSpace(os.Getenv("DIANA_TEST_LLM_MODEL")),
		APIStyle:        llm.APIStyle(strings.TrimSpace(os.Getenv("DIANA_TEST_LLM_API_STYLE"))),
		MaxOutputTokens: 512, Timeout: 150 * time.Second,
	}
	if cfg.Model == "" {
		return cfg, errors.New("explicit current production model is required")
	}
	switch cfg.Provider {
	case llm.ProviderOpenAICompatible, llm.ProviderGemini, llm.ProviderAnthropic:
	default:
		return cfg, errors.New("explicit supported DIANA_TEST_LLM_PROVIDER is required")
	}
	if cfg.APIStyle != "" && cfg.APIStyle != llm.APIStyleResponses && cfg.APIStyle != llm.APIStyleChatCompletions {
		return cfg, errors.New("unsupported DIANA_TEST_LLM_API_STYLE")
	}
	switch os.Getenv("DIANA_PROMPT_TRACE_REPLAY_MAX_OUTPUT") {
	case "", "512":
	case "1024":
		if cfg.Provider != llm.ProviderGemini && !strings.Contains(strings.ToLower(cfg.Model), "gemini") {
			return cfg, errors.New("1024 output allowance is only for Gemini thinking; otherwise use 512")
		}
		cfg.MaxOutputTokens = 1024
	default:
		return cfg, errors.New("output limit must be 512, or 1024 for Gemini")
	}
	for key, target := range map[string]*int64{
		"DIANA_PROMPT_TRACE_REPLAY_CONTEXT_WINDOW": &cfg.ContextWindowTokens,
		"DIANA_PROMPT_TRACE_REPLAY_MAX_CONTEXT":    &cfg.MaxContextTokens,
	} {
		if raw := os.Getenv(key); raw != "" {
			value, err := strconv.ParseInt(raw, 10, 64)
			if err != nil || value <= 0 {
				return cfg, errors.New("explicit context limits must be positive integers")
			}
			*target = value
		}
	}
	return cfg.WithDefaults(), nil
}

type traceReplayBudget struct {
	ContextLimit int64 `json:"context_limit"`
	InputBudget  int64 `json:"input_budget"`
	Estimated    int64 `json:"estimated_input_tokens"`
}

// Generate calls withDefaults then applyContextBudget before provider conversion.
// For accepted text-only requests these exact estimators prove its early-return
// path: messages + tools + tool choice fit, so missing priorities/groups cannot
// affect selection. This is not a tokenizer or a reconstruction of historical wire.
func traceReplayPreflight(pair [2]llm.GenerateRequest, cfg llm.ProviderConfig) ([2]traceReplayBudget, error) {
	var result [2]traceReplayBudget
	cfg = cfg.WithDefaults()
	for arm, request := range pair {
		if request.Model != cfg.Model || request.MaxOutputTokens != cfg.MaxOutputTokens || request.MaxOutputTokens <= 0 {
			return result, errors.New("preflight requires the same model and positive output allowance as the replay client")
		}
		limit := cfg.MaxContextTokensWithDefault()
		if request.MaxContextTokens > 0 && request.MaxContextTokens < limit {
			limit = request.MaxContextTokens
		}
		result[arm] = traceReplayBudget{
			ContextLimit: limit,
			InputBudget:  llm.InputTokenBudget(limit, request.MaxOutputTokens),
			Estimated:    llm.EstimateRequestInputTokens(request),
		}
		if result[arm].Estimated > result[arm].InputBudget {
			return result, fmt.Errorf("arm %d would require local context trimming; estimated input %d exceeds budget %d; replay rejected", arm, result[arm].Estimated, result[arm].InputBudget)
		}
	}
	return result, nil
}

// Offline revalidation uses only the private input and explicit budget settings.
// It never constructs a client, reads API credentials, or creates a report file.
func TestGroupPromptTraceReplayOffline(t *testing.T) {
	if os.Getenv("DIANA_PROMPT_TRACE_REPLAY_OFFLINE") != "1" {
		t.Skip("set DIANA_PROMPT_TRACE_REPLAY_OFFLINE=1 to validate private inputs without requests")
	}
	if os.Getenv("DIANA_LIVE_LLM") == "1" || os.Getenv("DIANA_LIVE_PROMPT_TRACE_REPLAY") == "1" {
		t.Fatal("offline preflight requires both live switches disabled")
	}
	cfg, err := traceReplayBudgetConfig()
	if err != nil {
		t.Fatal(err)
	}
	envelopes, err := readTraceReplayInput(os.Getenv("DIANA_PROMPT_TRACE_REPLAY_IN"))
	if err != nil {
		t.Fatal(err)
	}
	for i, envelope := range envelopes {
		pair, factors, err := traceReplayPair(envelope.Metadata.Request)
		if err != nil {
			t.Errorf("sample %d: %s", i, err)
			continue
		}
		for arm := range pair {
			pair[arm].Model, pair[arm].MaxOutputTokens = cfg.Model, cfg.MaxOutputTokens
		}
		budget, err := traceReplayPreflight(pair, cfg)
		if err != nil {
			t.Errorf("sample %d: %s", i, err)
			continue
		}
		t.Logf("sample %d: continuation guard passed; factors=%v; original=%+v tail=%+v", i, factors, budget[0], budget[1])
	}
	t.Log("offline current-budget check only; no HTTP calls; not historical wire reconstruction or full Runtime replay")
}

func readTraceReplayInput(path string) ([]traceReplayEnvelope, error) {
	if !filepath.IsAbs(path) {
		return nil, errors.New("IN must be an absolute private file path")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > traceReplayMaxBytes {
		return nil, errors.New("IN must be a regular owner-only file of at most 32 MiB")
	}
	file, err := os.Open(path) // Read-only: never rewrite or chmod the caller's traces.
	if err != nil {
		return nil, errors.New("cannot open private replay input")
	}
	defer file.Close()
	var envelopes []traceReplayEnvelope
	decoder := json.NewDecoder(io.LimitReader(file, traceReplayMaxBytes+1))
	if err := decoder.Decode(&envelopes); err != nil {
		return nil, errors.New("IN must contain a JSON array of debug_trace envelopes")
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF || len(envelopes) < 1 || len(envelopes) > 3 {
		return nil, errors.New("IN must contain exactly one array with 1 to 3 samples")
	}
	for _, envelope := range envelopes {
		if envelope.Metadata.Phase != "model_request" || envelope.Metadata.Purpose != "reply" || envelope.Metadata.Error != "" || len(envelope.Metadata.Request.Messages) == 0 {
			return nil, errors.New("each envelope must be a successful reply model_request with request.messages")
		}
		response := envelope.Metadata.Response
		if response == nil || (strings.TrimSpace(response.Text) == "" && len(response.ToolCalls) == 0) || strings.Contains(response.Text, "[platform tool") {
			return nil, errors.New("each sample requires a nonempty, non-omitted original response")
		}
	}
	return envelopes, nil
}

func createTraceReplayOutput(path string) (*os.File, error) {
	if !filepath.IsAbs(path) {
		return nil, errors.New("OUT must be an absolute new private file path")
	}
	parent, err := os.Stat(filepath.Dir(path))
	if err != nil || !parent.IsDir() || parent.Mode().Perm()&0077 != 0 {
		return nil, errors.New("OUT needs an existing owner-only parent directory")
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return nil, errors.New("cannot exclusively create OUT; use a new path")
	}
	return file, nil
}

// The stable-tail helper enforces one transport attempt per Generate; this outer
// guard tightens its synthetic experiment's 18-call budget to this batch's 2–6.
// A denial consumes a slot too: retries can never acquire extra outbound budget.
type traceReplayTransport struct {
	base     *stableTailCacheTransport
	limit    int32
	attempts atomic.Int32
}

func (g *traceReplayTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if g.attempts.Add(1) > g.limit {
		return nil, errors.New("trace replay HTTP request limit exhausted")
	}
	return g.base.RoundTrip(req)
}

func traceReplayPair(original llm.GenerateRequest) ([2]llm.GenerateRequest, []string, error) {
	pair := [2]llm.GenerateRequest{cloneGenerateRequestForTest(original), cloneGenerateRequestForTest(original)}
	fail := func(reason string) ([2]llm.GenerateRequest, []string, error) { return pair, nil, errors.New(reason) }
	if len(original.Messages) < 3 || original.Messages[0].Role != llm.RoleSystem || original.Messages[len(original.Messages)-1].Role != llm.RoleUser {
		return fail("expected a leading protocol and a final current user message")
	}
	for _, message := range original.Messages {
		if len(message.Parts) != 0 || len(message.ToolCalls) != 0 || message.Role == llm.RoleTool || message.ToolCallID != "" || strings.Contains(message.Content, "[platform tool") || strings.Contains(message.Content, "[data URL omitted:") {
			return fail("multimodal, omitted protocol or tool-continuation sample is not replayable")
		}
		// runner.toolObservationMessage is used even when its preceding assistant
		// action is ordinary JSON text, with no native ToolCalls/ToolCallID. Treat
		// this marker anywhere (including quoted history) as ambiguous, fail closed.
		if strings.Contains(message.Content, "（剩余工具预算") {
			return fail("text-compatible tool observation is a continuation or ambiguous history, not a first-step sample")
		}
		if message.ContinuationScope != "" || message.ReasoningContent != nil || len(message.AnthropicThinking) != 0 || len(message.ResponsesOutput) != 0 {
			return fail("opaque continuation state is not replayable")
		}
		if message.Role != llm.RoleSystem && message.Role != llm.RoleUser && message.Role != llm.RoleAssistant {
			return fail("unsupported message role")
		}
	}
	if len(original.Tools) == 0 || !strings.Contains(original.Messages[0].Content, "send") {
		return fail("expected the production Agent protocol and trace tool definitions")
	}
	// Trace JSON omits priority, atomicity, grouping and cache/continuation metadata.
	// Restore only recognized moved blocks' current producer metadata; never invent
	// a history/cache boundary. Preflight rejects any input needing local trimming.
	req := &pair[1]
	leading := 0
	for leading < len(req.Messages) && req.Messages[leading].Role == llm.RoleSystem {
		leading++
	}
	var moved []llm.Message
	var factors []string
	presentationFound := false
	for i := 0; i < leading; i++ {
		for _, split := range []bool{true, false} {
			block := replyPresentationPrompt(split)
			if !strings.Contains(req.Messages[i].Content, block) {
				continue
			}
			if presentationFound || strings.Count(req.Messages[i].Content, block) != 1 {
				return fail("ambiguous presentation block")
			}
			presentationFound = true
			req.Messages[i].Content = strings.Replace(req.Messages[i].Content, block, "", 1)
			moved = append(moved, llm.Message{Role: llm.RoleUser, Content: traceReplaySupplement + block, Priority: llm.MessagePrioritySystem})
			factors = append(factors, "presentation")
		}
	}
	if !presentationFound {
		return fail("legacy default presentation block not found; custom overrides or post-change traces are unsupported")
	}
	for i := 0; i < leading; i++ {
		content := req.Messages[i].Content
		start := strings.Index(content, traceReplayContracts)
		if start < 0 {
			continue
		}
		if strings.Count(content, traceReplayContracts) != 1 || i != 0 {
			return fail("ambiguous restored-contract block")
		}
		bodyStart := start + len(traceReplayContracts)
		decoder := json.NewDecoder(strings.NewReader(content[bodyStart:]))
		var contracts []struct {
			Name        string         `json:"name"`
			InputSchema map[string]any `json:"inputSchema"`
		}
		if decoder.Decode(&contracts) != nil || len(contracts) == 0 {
			return fail("restored contracts are not a complete JSON array")
		}
		for _, contract := range contracts {
			if contract.Name == "" || contract.InputSchema == nil {
				return fail("restored contract is incomplete")
			}
		}
		end := bodyStart + int(decoder.InputOffset())
		moved = append(moved, llm.Message{Role: llm.RoleUser, Content: traceReplaySupplement + content[start:end], Priority: llm.MessagePrioritySystem})
		req.Messages[i].Content = content[:start] + content[end:]
		factors = append(factors, "restored_contracts")
	}
	var summaries int
	for i := leading; i < len(req.Messages)-1; i++ {
		if !strings.HasPrefix(req.Messages[i].Content, traceReplaySummary) {
			continue
		}
		if req.Messages[i].Role != llm.RoleUser || summaries > 0 || i != leading {
			return fail("summary must be the unique pre-history legacy checkpoint")
		}
		summaries++
		summary := req.Messages[i]
		summary.Priority, summary.AtomicText = llm.MessagePrioritySummary, true
		moved = append(moved, summary)
		req.Messages = append(req.Messages[:i], req.Messages[i+1:]...)
		factors = append(factors, "summary")
		i--
	}
	for _, message := range req.Messages[leading:] {
		if strings.Contains(message.Content, traceReplayContracts) || strings.Contains(message.Content, traceReplaySummary) {
			return fail("ambiguous dynamic block outside the legacy placement")
		}
	}
	if len(req.Messages) <= leading+1 {
		return fail("a nonempty stable history is required before the current message")
	}
	last := req.Messages[len(req.Messages)-1]
	req.Messages = append(req.Messages[:len(req.Messages)-1], moved...)
	req.Messages = append(req.Messages, last)
	return pair, factors, nil
}

func traceReplayResultKind(response *llm.GenerateResponse) string {
	for _, call := range response.ToolCalls {
		if call.Name != "send" {
			continue
		}
		content, _ := call.Arguments["content"].(string)
		silent, _ := call.Arguments["silent"].(bool)
		if strings.TrimSpace(content) == "" && !silent {
			return "invalid_finalize"
		}
		return "finalize"
	}
	if len(response.ToolCalls) > 0 {
		return "tool_call_not_executed"
	}
	return "text_or_compatibility_protocol_unexecuted"
}

func traceReplayFixture() llm.GenerateRequest {
	return llm.GenerateRequest{Model: "trace-model", Tools: []llm.ToolDefinition{{Name: "send", Parameters: map[string]any{"type": "object"}}}, Messages: []llm.Message{
		{Role: llm.RoleSystem, Content: "Fixed send protocol.\n\n" + traceReplayContracts + `[{"name":"fixture_lookup","description":"inert fixture","inputSchema":{"type":"object"}}]`},
		{Role: llm.RoleSystem, Content: "Fixed persona.\n\n" + replyPresentationPrompt(true) + "\n\nFixed closing."},
		{Role: llm.RoleUser, Content: traceReplaySummary + "Synthetic earlier notes."},
		{Role: llm.RoleUser, Content: "Synthetic fixed history."},
		{Role: llm.RoleAssistant, Content: "Synthetic old response."},
		{Role: llm.RoleUser, Content: traceReplaySupplement + "Unrelated volatile context."},
		{Role: llm.RoleUser, Content: "Synthetic current question."},
	}}
}

func TestGroupPromptTraceReplayPlacement(t *testing.T) {
	original := traceReplayFixture()
	before, _ := json.Marshal(original)
	pair, factors, err := traceReplayPair(original)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := json.Marshal(original)
	if string(before) != string(after) || !reflect.DeepEqual(pair[0], original) {
		t.Fatal("input/original arm mutated")
	}
	if !reflect.DeepEqual(factors, []string{"presentation", "restored_contracts", "summary"}) {
		t.Fatal("missing moved factor")
	}
	tail := pair[1]
	if !reflect.DeepEqual(tail.Tools, original.Tools) || tail.Model != original.Model || tail.Messages[len(tail.Messages)-1].Content != original.Messages[len(original.Messages)-1].Content {
		t.Fatal("non-placement request data changed")
	}
	for _, index := range []int{0, 1} {
		if strings.Contains(tail.Messages[index].Content, traceReplayContracts) || strings.Contains(tail.Messages[index].Content, replyPresentationPrompt(true)) {
			t.Fatal("dynamic block remained in leading system")
		}
	}
	if tail.Messages[2].Content != original.Messages[3].Content || tail.Messages[3].Content != original.Messages[4].Content || tail.Messages[4].Content != original.Messages[5].Content {
		t.Fatal("history or unrelated tail changed")
	}
	for _, message := range tail.Messages[len(tail.Messages)-4 : len(tail.Messages)-1] {
		if strings.HasPrefix(message.Content, traceReplaySummary) {
			if message.Priority != llm.MessagePrioritySummary || !message.AtomicText {
				t.Fatal("moved summary lost current production priority/atomicity")
			}
		} else if message.Priority != llm.MessagePrioritySystem || message.AtomicText {
			t.Fatal("moved presentation/contracts lost current production priority/atomicity")
		}
	}
	for _, message := range tail.Messages {
		if message.CacheBreakpoint {
			t.Fatal("invented cache boundary")
		}
	}
	if _, _, err := traceReplayPair(tail); err == nil {
		t.Fatal("post-change trace must not be silently moved twice")
	}
	for _, mutate := range []func(*llm.GenerateRequest){
		func(r *llm.GenerateRequest) {
			r.Messages[3].Parts = []llm.ContentPart{{Type: llm.ContentPartImageURL, ImageURL: "https://example.invalid/private.png"}}
		},
		func(r *llm.GenerateRequest) { r.Messages[4].ToolCalls = []llm.ToolCall{{Name: "fixture_lookup"}} },
		func(r *llm.GenerateRequest) { r.Messages[4].Content = "[platform tool Agent protocol payload omitted]" },
		func(r *llm.GenerateRequest) { r.Messages[1].Content = "custom presentation cannot be guessed" },
		func(r *llm.GenerateRequest) { r.Messages[0].Content = "send\n" + traceReplayContracts + "[{" },
	} {
		request := traceReplayFixture()
		mutate(&request)
		if _, _, err := traceReplayPair(request); err == nil {
			t.Fatal("unsupported trace accepted")
		}
	}
}

func TestGroupPromptTraceReplayCompatibilityContinuation(t *testing.T) {
	// Exact framing from agent/runner.go:toolObservationMessage. No native tool
	// fields, including the browser/search-specific and failed observation cases.
	for _, tool := range []string{"fixture_lookup", "web_search", "browser_render"} {
		for _, status := range []string{"成功", "失败"} {
			for _, appendCurrent := range []bool{false, true} {
				request := traceReplayFixture()
				request.Messages = append(request.Messages,
					llm.Message{Role: llm.RoleAssistant, Content: `{"action":"tool","tool":"` + tool + `","arguments":{}}`},
					llm.Message{Role: llm.RoleUser, Content: fmt.Sprintf("工具 %s 执行%s（剩余工具预算 2）：\nSynthetic output.\n\nSynthetic guidance.", tool, status)})
				if appendCurrent {
					request.Messages = append(request.Messages, llm.Message{Role: llm.RoleUser, Content: "Synthetic later question."})
				}
				if _, _, err := traceReplayPair(request); err == nil {
					t.Fatal("text-only tool continuation accepted as a first step")
				}
			}
		}
	}
	request := traceReplayFixture()
	request.Messages[len(request.Messages)-1].Content = "工具 fixture_lookup 执行成功（剩余工具预算"
	if _, _, err := traceReplayPair(request); err == nil {
		t.Fatal("partial observation marker must fail closed")
	}
	request = traceReplayFixture()
	request.Messages[len(request.Messages)-1].Content = "请解释工具预算是什么意思。"
	if _, _, err := traceReplayPair(request); err != nil {
		t.Fatal("ordinary tool discussion rejected")
	}
}

func TestGroupPromptTraceReplayBudgetPreflight(t *testing.T) {
	pair, _, err := traceReplayPair(traceReplayFixture())
	if err != nil {
		t.Fatal(err)
	}
	cfg := llm.ProviderConfig{Provider: llm.ProviderGemini, Model: "synthetic-gemini", MaxOutputTokens: 512}
	for arm := range pair {
		pair[arm].Model, pair[arm].MaxOutputTokens = cfg.Model, cfg.MaxOutputTokens
	}
	original := cloneGenerateRequestForTest(pair[0])
	tail := cloneGenerateRequestForTest(pair[1])
	budget, err := traceReplayPreflight(pair, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(pair, [2]llm.GenerateRequest{original, tail}) {
		t.Fatal("preflight mutated request data or metadata")
	}
	for arm, request := range pair {
		if budget[arm].ContextLimit != llm.DefaultMaxContextTokens || budget[arm].InputBudget != llm.InputTokenBudget(llm.DefaultMaxContextTokens, cfg.MaxOutputTokens) || budget[arm].Estimated != llm.EstimateRequestInputTokens(request) {
			t.Fatal("preflight does not use the adapter's default limit and estimator")
		}
		// The exported fitter shares fitMessagesToTokenBudget with the adapters.
		// Account for tool choice as well as schemas in the fixed reserve.
		fixed := llm.EstimateRequestInputTokens(llm.GenerateRequest{Tools: request.Tools, ToolChoice: request.ToolChoice})
		if fitted := llm.FitMessagesToContextBudget(request.Messages, budget[arm].ContextLimit, cfg.MaxOutputTokens+fixed); !reflect.DeepEqual(fitted, request.Messages) {
			t.Fatal("accepted arm lost content in the production message fitter")
		}
	}
	if budget[1].Estimated <= budget[0].Estimated {
		t.Fatal("fixture must expose added tail framing")
	}
	// Original fits exactly, tail does not: do not accept just the first arm.
	cfg.ContextWindowTokens = budget[0].Estimated + cfg.MaxOutputTokens + 128
	if result, err := traceReplayPreflight(pair, cfg); err == nil || result[0].Estimated != result[0].InputBudget || result[1].Estimated <= result[1].InputBudget {
		t.Fatal("tail-only clipping was not rejected")
	}
	cfg.ContextWindowTokens = budget[1].Estimated + cfg.MaxOutputTokens + 128
	if _, err := traceReplayPreflight(pair, cfg); err != nil {
		t.Fatal("exact-fit tail rejected")
	}
	cfg.MaxOutputTokens, pair[0].MaxOutputTokens, pair[1].MaxOutputTokens = 1024, 1024, 1024
	if _, err := traceReplayPreflight(pair, cfg); err == nil {
		t.Fatal("output allowance was not reserved")
	}
	cfg = llm.ProviderConfig{Provider: llm.ProviderGemini, Model: "synthetic-gemini", MaxOutputTokens: 1024, ContextWindowTokens: 128000, MaxContextTokens: 1024}
	if _, err := traceReplayPreflight(pair, cfg); err == nil {
		t.Fatal("explicit config context cap ignored")
	}
	cfg.MaxContextTokens = 0
	pair[1].MaxContextTokens = 1024
	if _, err := traceReplayPreflight(pair, cfg); err == nil {
		t.Fatal("request context cap ignored")
	}
	pair[1].MaxContextTokens = 0
	pair[0].Tools[0].Description = strings.Repeat("synthetic schema ", 30000)
	if _, err := traceReplayPreflight(pair, cfg); err == nil {
		t.Fatal("tool schema budget ignored")
	}
	pair[0] = original
	if _, err := traceReplayPreflight(pair, cfg); err == nil {
		t.Fatal("request/client output mismatch accepted")
	}
	pair[0].MaxOutputTokens = cfg.MaxOutputTokens
	pair[0].Model = "different-model"
	if _, err := traceReplayPreflight(pair, cfg); err == nil {
		t.Fatal("request/client model mismatch accepted")
	}
}

func TestGroupPromptTraceReplayPrivateFiles(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	var envelope traceReplayEnvelope
	envelope.Metadata.Phase, envelope.Metadata.Purpose, envelope.Metadata.Request = "model_request", "reply", traceReplayFixture()
	envelope.Metadata.Response = &llm.GenerateResponse{Text: "Synthetic original reply."}
	input := filepath.Join(dir, "in.json")
	data, _ := json.Marshal([]traceReplayEnvelope{envelope})
	if err := os.WriteFile(input, data, 0600); err != nil {
		t.Fatal(err)
	}
	if records, err := readTraceReplayInput(input); err != nil || len(records) != 1 {
		t.Fatal("valid private envelope rejected")
	}
	t.Run("offline_entrypoint_synthetic_input", func(t *testing.T) {
		for key, value := range map[string]string{
			"DIANA_LIVE_LLM": "0", "DIANA_LIVE_PROMPT_TRACE_REPLAY": "0",
			"DIANA_PROMPT_TRACE_REPLAY_OFFLINE": "1", "DIANA_PROMPT_TRACE_REPLAY_IN": input,
			"DIANA_TEST_LLM_PROVIDER": "gemini", "DIANA_TEST_LLM_MODEL": "synthetic-gemini",
			"DIANA_TEST_LLM_API_KEY": "", "DIANA_TEST_LLM_BASE_URL": "not-an-endpoint",
			"DIANA_TEST_LLM_API_STYLE": "", "DIANA_PROMPT_TRACE_REPLAY_MAX_OUTPUT": "512",
			"DIANA_PROMPT_TRACE_REPLAY_CONTEXT_WINDOW": "", "DIANA_PROMPT_TRACE_REPLAY_MAX_CONTEXT": "",
		} {
			t.Setenv(key, value)
		}
		TestGroupPromptTraceReplayOffline(t)
	})
	for _, invalid := range [][]byte{[]byte("[]"), []byte("{}"), append(append([]byte{}, data...), []byte(" {}")...)} {
		if err := os.WriteFile(input, invalid, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := readTraceReplayInput(input); err == nil {
			t.Fatal("invalid input accepted")
		}
	}
	many, _ := json.Marshal([]traceReplayEnvelope{envelope, envelope, envelope, envelope})
	if err := os.WriteFile(input, many, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readTraceReplayInput(input); err == nil {
		t.Fatal("more than three samples accepted")
	}
	if err := os.Chmod(input, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := readTraceReplayInput(input); err == nil {
		t.Fatal("public input accepted")
	}
	out := filepath.Join(dir, "out.json")
	file, err := createTraceReplayOutput(out)
	if err != nil {
		t.Fatal(err)
	}
	file.Close()
	info, err := os.Stat(out)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("report permissions not private")
	}
	if _, err := createTraceReplayOutput(out); err == nil {
		t.Fatal("existing output must never be overwritten")
	}
}

func TestGroupPromptTraceReplayHTTPBudget(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"error":{"message":"fixture overload"}}`)
	}))
	defer server.Close()
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DisableKeepAlives = true
	defer transport.CloseIdleConnections()
	guard := &traceReplayTransport{base: &stableTailCacheTransport{base: transport}, limit: 2}
	client, err := llm.NewClient(llm.ProviderConfig{Provider: llm.ProviderOpenAICompatible, APIKey: "synthetic-key", BaseURL: server.URL, Model: "synthetic-model"}, llm.WithHTTPClient(&http.Client{Transport: guard}))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		ctx := context.WithValue(t.Context(), stableTailCacheAttemptKey{}, &atomic.Bool{})
		if _, err := client.Generate(ctx, llm.GenerateRequest{Messages: []llm.Message{{Role: llm.RoleUser, Content: "synthetic"}}, MaxOutputTokens: 512}); err == nil {
			t.Fatal("HTTP 429 must fail")
		}
	}
	if calls.Load() > 2 || calls.Load() < 1 {
		t.Fatal("HTTP budget escaped")
	}
	response := &llm.GenerateResponse{ToolCalls: []llm.ToolCall{{Name: "fixture_lookup"}}}
	if traceReplayResultKind(response) != "tool_call_not_executed" {
		t.Fatal("tool response misclassified")
	}
	response.ToolCalls = []llm.ToolCall{{Name: "send", Arguments: map[string]any{}}}
	if traceReplayResultKind(response) != "invalid_finalize" {
		t.Fatal("empty finalize accepted")
	}
}

func TestGroupPromptTraceReplayConfig(t *testing.T) {
	for key, value := range map[string]string{
		"DIANA_TEST_LLM_PROVIDER": "gemini", "DIANA_TEST_LLM_API_KEY": "synthetic-key",
		"DIANA_TEST_LLM_BASE_URL": "https://example.invalid", "DIANA_TEST_LLM_MODEL": "synthetic-gemini-model",
		"DIANA_TEST_LLM_API_STYLE": "", "DIANA_PROMPT_TRACE_REPLAY_MAX_OUTPUT": "",
		"DIANA_PROMPT_TRACE_REPLAY_CONTEXT_WINDOW": "", "DIANA_PROMPT_TRACE_REPLAY_MAX_CONTEXT": "",
	} {
		t.Setenv(key, value)
	}
	if cfg, err := traceReplayConfig(); err != nil || cfg.MaxOutputTokens != 512 {
		t.Fatal("explicit Gemini configuration failed or exceeded default output cap")
	}
	live, err := traceReplayConfig()
	if err != nil {
		t.Fatal(err)
	}
	offline, err := traceReplayBudgetConfig()
	if err != nil || offline.APIKey != "" {
		t.Fatal("offline config loaded credentials")
	}
	if live.MaxContextTokensWithDefault() != offline.MaxContextTokensWithDefault() || live.MaxOutputTokens != offline.MaxOutputTokens || live.Model != offline.Model {
		t.Fatal("live/offline budget settings differ")
	}
	t.Setenv("DIANA_TEST_LLM_API_KEY", "")
	t.Setenv("DIANA_TEST_LLM_BASE_URL", "not-a-valid-url")
	if cfg, err := traceReplayBudgetConfig(); err != nil || cfg.APIKey != "" {
		t.Fatal("offline configuration depends on credentials/endpoint")
	}
	t.Setenv("DIANA_TEST_LLM_API_KEY", "synthetic-key")
	t.Setenv("DIANA_TEST_LLM_BASE_URL", "https://example.invalid")
	t.Setenv("DIANA_PROMPT_TRACE_REPLAY_CONTEXT_WINDOW", "64000")
	t.Setenv("DIANA_PROMPT_TRACE_REPLAY_MAX_CONTEXT", "32000")
	live, err = traceReplayConfig()
	if err != nil {
		t.Fatal(err)
	}
	offline, err = traceReplayBudgetConfig()
	if err != nil || live.ContextWindowTokens != 64000 || live.MaxContextTokensWithDefault() != 32000 || offline.MaxContextTokensWithDefault() != live.MaxContextTokensWithDefault() {
		t.Fatal("explicit live/offline context budgets differ")
	}
	for _, invalid := range []string{"-1", "0", "invalid", "9223372036854775808"} {
		t.Setenv("DIANA_PROMPT_TRACE_REPLAY_MAX_CONTEXT", invalid)
		if _, err := traceReplayBudgetConfig(); err == nil {
			t.Fatal("invalid context cap accepted")
		}
	}
	t.Setenv("DIANA_PROMPT_TRACE_REPLAY_CONTEXT_WINDOW", "")
	t.Setenv("DIANA_PROMPT_TRACE_REPLAY_MAX_CONTEXT", "")
	t.Setenv("DIANA_PROMPT_TRACE_REPLAY_MAX_OUTPUT", "1024")
	if cfg, err := traceReplayConfig(); err != nil || cfg.MaxOutputTokens != 1024 {
		t.Fatal("Gemini thinking allowance rejected")
	}
	t.Setenv("DIANA_TEST_LLM_PROVIDER", "openai_compatible")
	t.Setenv("DIANA_TEST_LLM_MODEL", "synthetic-other-model")
	if _, err := traceReplayConfig(); err == nil {
		t.Fatal("non-Gemini output cap exceeded")
	}
	t.Setenv("DIANA_PROMPT_TRACE_REPLAY_MAX_OUTPUT", "512")
	for _, base := range []string{"http://example.invalid", "https://key@example.invalid", "https://example.invalid?key=secret"} {
		t.Setenv("DIANA_TEST_LLM_BASE_URL", base)
		if _, err := traceReplayConfig(); err == nil {
			t.Fatal("unsafe provider URL accepted")
		}
	}
}
