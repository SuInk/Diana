package assistant

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"reflect"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SuInk/diana/model/llm"
)

const (
	stableTailLongHistoryUpdates       = 6
	stableTailLongHistoryCalls         = 2 * (2 + stableTailLongHistoryUpdates)
	stableTailLongHistoryEstimate      = 78000
	stableTailLongHistoryResponseLimit = 1 << 20
)

type stableTailLongHistoryFixture struct {
	arm, phase string
	turn       int
	request    llm.GenerateRequest
}

// Reuse the real Runtime/Runner capture, but freeze all other changing blocks.
// Each pair differs only in summary placement and an equal-length arm namespace.
func stableTailLongHistoryFixtures(t *testing.T, nonce string) []stableTailLongHistoryFixture {
	t.Helper()
	var base llm.GenerateRequest
	for _, f := range stableTailCacheFixtures(t, nonce) {
		if f.factor == "summary" && f.arm == "tail" && f.turn == 0 {
			base = cloneGenerateRequestForTest(f.request)
			break
		}
	}
	if len(base.Messages) == 0 {
		t.Fatal("missing captured long-history template")
	}
	base.Messages[0].Content = "Synthetic long cache run " + nonce + "/arm-0.\n" + strings.SplitN(base.Messages[0].Content, "\n", 2)[1]
	base.PromptCacheKey = "" // Gemini ignores this field; the leading text isolates arms.
	base.MaxOutputTokens = 1024
	first, boundary := -1, -1
	for i, m := range base.Messages {
		if first < 0 && m.Role != llm.RoleSystem {
			first = i
		}
		if m.CacheBreakpoint && boundary < 0 {
			boundary = i
		}
	}
	if first < 0 || boundary < first {
		t.Fatal("missing real stable history boundary")
	}
	for pass := 0; llm.EstimateRequestInputTokens(base) < stableTailLongHistoryEstimate; pass++ {
		for i := first; i <= boundary; i++ {
			base.Messages[i].Content += fmt.Sprintf("\nSynthetic archive section %05d record %05d: The green team keeps a notebook of sample project milestones. Alice records the owner and target date, Bob checks the verification notes, and the blue team reviews the completed checklist. These fictional records describe routine planning and require no external action.", pass, i-first)
		}
	}
	var fixtures []stableTailLongHistoryFixture
	for slot := 0; slot < 2+stableTailLongHistoryUpdates; slot++ {
		turn, phase := 0, "baseline"
		if slot == 1 {
			phase = "exact_repeat"
		}
		if slot >= 2 {
			turn, phase = slot-1, "updated"
			base.Messages[boundary].CacheBreakpoint = false
			added := []llm.Message{
				{Role: llm.RoleUser, Content: fmt.Sprintf("Synthetic appended question %02d: Is the notebook verification recorded?", turn)},
				{Role: llm.RoleAssistant, Content: fmt.Sprintf("Synthetic appended acknowledgment %02d: The verification is recorded.", turn), CacheBreakpoint: true},
			}
			messages := append([]llm.Message(nil), base.Messages[:boundary+1]...)
			messages = append(messages, added...)
			base.Messages = append(messages, base.Messages[boundary+1:]...)
			boundary += len(added)
			for i := range base.Messages {
				if base.Messages[i].Priority == llm.MessagePrioritySummary {
					base.Messages[i].Content = fmt.Sprintf("%sSynthetic earlier summary revision %d: Alice owns the green notebook; Bob checks the verification record.", traceReplaySummary, turn)
				}
			}
			base.Messages[len(base.Messages)-1].Content = fmt.Sprintf("Synthetic current turn %d. Reply only OK; do not run tools.", turn)
		}
		pair := [2]llm.GenerateRequest{stableTailCacheLegacy(t, cloneGenerateRequestForTest(base), "summary", ""), cloneGenerateRequestForTest(base)}
		pair[1].Messages[0].Content = strings.Replace(pair[1].Messages[0].Content, "/arm-0.\n", "/arm-1.\n", 1)
		order := []int{0, 1}
		if slot%2 == 1 {
			order = []int{1, 0}
		}
		for _, arm := range order {
			fixtures = append(fixtures, stableTailLongHistoryFixture{arm: []string{"legacy", "tail"}[arm], phase: phase, turn: turn, request: pair[arm]})
		}
	}
	return fixtures
}

type stableTailLongHistoryField struct {
	Present bool
	Value   *int64
}

type stableTailLongHistoryUsage struct {
	Parsed, MetadataPresent bool
	Input, Cached, Output   stableTailLongHistoryField
}

func stableTailLongHistoryRawUsage(body []byte) stableTailLongHistoryUsage {
	var envelope map[string]json.RawMessage
	if json.Unmarshal(body, &envelope) != nil || envelope == nil {
		return stableTailLongHistoryUsage{}
	}
	u := stableTailLongHistoryUsage{Parsed: true}
	metadata, ok := envelope["usageMetadata"]
	u.MetadataPresent = ok
	var fields map[string]json.RawMessage
	if !ok || json.Unmarshal(metadata, &fields) != nil {
		return u
	}
	for name, dst := range map[string]*stableTailLongHistoryField{"promptTokenCount": &u.Input, "cachedContentTokenCount": &u.Cached, "candidatesTokenCount": &u.Output} {
		raw, present := fields[name]
		dst.Present = present
		var value int64
		if present && string(raw) != "null" && json.Unmarshal(raw, &value) == nil && value >= 0 {
			dst.Value = &value
		}
	}
	return u
}

type stableTailLongHistoryAttempt struct {
	once   atomic.Bool
	status int
	usage  stableTailLongHistoryUsage
}

type stableTailLongHistoryAttemptKey struct{}

type stableTailLongHistoryTransport struct {
	base    http.RoundTripper
	calls   atomic.Int32
	stopped atomic.Bool
}

func (g *stableTailLongHistoryTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	a, _ := req.Context().Value(stableTailLongHistoryAttemptKey{}).(*stableTailLongHistoryAttempt)
	if a == nil || !a.once.CompareAndSwap(false, true) || g.stopped.Load() {
		return nil, errors.New("long-history retry or unbudgeted request blocked")
	}
	for {
		count := g.calls.Load()
		if count >= stableTailLongHistoryCalls {
			return nil, errors.New("long-history HTTP budget exhausted")
		}
		if g.calls.CompareAndSwap(count, count+1) {
			break
		}
	}
	response, err := g.base.RoundTrip(req)
	if err != nil {
		g.stopped.Store(true)
		return nil, errors.New("long-history transport failed")
	}
	if response == nil || response.Body == nil {
		g.stopped.Store(true)
		return nil, errors.New("long-history response missing")
	}
	a.status = response.StatusCode
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		g.stopped.Store(true) // Includes 429, redirects and failures; never send another arm.
		return response, nil
	}
	body, readErr := io.ReadAll(io.LimitReader(response.Body, stableTailLongHistoryResponseLimit+1))
	_ = response.Body.Close()
	if readErr != nil || len(body) > stableTailLongHistoryResponseLimit {
		g.stopped.Store(true)
		return nil, errors.New("long-history response read failed or exceeded cap")
	}
	a.usage = stableTailLongHistoryRawUsage(body)
	response.Body = io.NopCloser(bytes.NewReader(body))
	return response, nil
}

type stableTailLongHistoryTotal struct {
	Arm                string `json:"arm"`
	Phase              string `json:"phase"`
	Samples            int    `json:"samples"`
	Parsed             int    `json:"parsed_json_samples"`
	MetadataPresent    int    `json:"usage_metadata_present"`
	InputPresent       int    `json:"input_field_present"`
	CachedPresent      int    `json:"cached_field_present"`
	CachedMissing      int    `json:"cached_field_missing"`
	CachedInvalid      int    `json:"cached_field_invalid"`
	CachedExplicitZero int    `json:"cached_explicit_zero"`
	CachedPositive     int    `json:"cached_positive"`
	OutputPresent      int    `json:"output_field_present"`
	Input              int64  `json:"reported_input_tokens"`
	Cached             int64  `json:"reported_cached_tokens"`
	Output             int64  `json:"reported_output_tokens"`
	Milliseconds       int64  `json:"milliseconds"`
}

func (s *stableTailLongHistoryTotal) add(u stableTailLongHistoryUsage, elapsed time.Duration) {
	s.Samples++
	s.Milliseconds += elapsed.Milliseconds()
	if u.Parsed {
		s.Parsed++
	}
	if u.MetadataPresent {
		s.MetadataPresent++
	}
	if u.Input.Present {
		s.InputPresent++
	}
	if u.Output.Present {
		s.OutputPresent++
	}
	if u.Input.Value != nil {
		s.Input += *u.Input.Value
	}
	if u.Output.Value != nil {
		s.Output += *u.Output.Value
	}
	if !u.Cached.Present {
		s.CachedMissing++
		return
	}
	s.CachedPresent++
	if u.Cached.Value == nil {
		s.CachedInvalid++
		return
	}
	s.Cached += *u.Cached.Value
	if *u.Cached.Value > 0 {
		s.CachedPositive++
	} else {
		s.CachedExplicitZero++
	}
}

// A fresh standalone process prevents earlier provider errors from teaching a
// smaller context limit. No production data, output feedback or tool execution.
func TestLiveStableTailCacheLongHistory(t *testing.T) {
	if os.Getenv("DIANA_LIVE_LLM") != "1" || os.Getenv("DIANA_LIVE_STABLE_TAIL_LONG_HISTORY") != "1" {
		t.Skip("set both live and long-history opt-ins")
	}
	if flag.Lookup("test.run").Value.String() != "^TestLiveStableTailCacheLongHistory$" || flag.Lookup("test.count").Value.String() != "1" || flag.Lookup("test.cpu").Value.String() != "" || flag.Lookup("test.bench").Value.String() != "" || flag.Lookup("test.fuzz").Value.String() != "" {
		t.Fatal("requires standalone exact -run '^TestLiveStableTailCacheLongHistory$' -count=1 without cpu, benchmarks or fuzzing")
	}
	cfg, err := traceReplayConfig()
	if err != nil {
		t.Fatal("long-history configuration invalid")
	}
	if cfg.Provider != llm.ProviderGemini {
		t.Fatal("long-history experiment requires explicit Gemini API-key configuration")
	}
	cfg.MaxOutputTokens = 1024
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		t.Fatal("long-history nonce generation failed")
	}
	fixtures := stableTailLongHistoryFixtures(t, hex.EncodeToString(nonce[:]))
	for i := range fixtures {
		fixtures[i].request.Model = cfg.Model
		if _, err := traceReplayPreflight([2]llm.GenerateRequest{fixtures[i].request, fixtures[i].request}, cfg); err != nil {
			t.Fatal("long-history fixture would be locally trimmed")
		}
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DisableKeepAlives = true
	defer transport.CloseIdleConnections()
	guard := &stableTailLongHistoryTransport{base: transport}
	client, err := llm.NewClient(cfg, llm.WithHTTPClient(&http.Client{Transport: guard, Timeout: cfg.Timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}))
	if err != nil {
		t.Fatal("long-history client initialization failed")
	}
	var totals []stableTailLongHistoryTotal
	defer func() {
		for _, total := range totals {
			encoded, _ := json.Marshal(total)
			t.Log(string(encoded))
		}
		t.Logf("outbound_requests=%d hard_limit=%d; missing cached fields remain unknown; controls and updated rounds are separate", guard.calls.Load(), stableTailLongHistoryCalls)
	}()
	for i, fixture := range fixtures {
		if i > 0 {
			timer := time.NewTimer(3 * time.Second)
			select {
			case <-timer.C:
			case <-t.Context().Done():
				timer.Stop()
				t.Fatal("long-history experiment canceled")
			}
		}
		attempt := &stableTailLongHistoryAttempt{}
		ctx, cancel := context.WithTimeout(context.WithValue(t.Context(), stableTailLongHistoryAttemptKey{}, attempt), cfg.Timeout)
		started := time.Now()
		response, callErr := client.Generate(ctx, fixture.request)
		cancel()
		index := -1
		for j := range totals {
			if totals[j].Arm == fixture.arm && totals[j].Phase == fixture.phase {
				index = j
				break
			}
		}
		if index < 0 {
			index = len(totals)
			totals = append(totals, stableTailLongHistoryTotal{Arm: fixture.arm, Phase: fixture.phase})
		}
		totals[index].add(attempt.usage, time.Since(started))
		if callErr != nil || response == nil {
			t.Fatalf("long-history %s/%s turn=%d failed; HTTP status=%d; stopped without retry", fixture.arm, fixture.phase, fixture.turn, attempt.status)
		}
		if attempt.usage.Input.Value == nil || *attempt.usage.Input.Value <= 0 {
			t.Fatal("long-history input usage missing or invalid; stopped")
		}
		if response.Usage.InputTokens != *attempt.usage.Input.Value || (attempt.usage.Cached.Value != nil && response.Usage.CachedInputTokens != *attempt.usage.Cached.Value) {
			t.Fatal("raw and adapted usage disagree; stopped")
		}
	}
	if guard.calls.Load() != stableTailLongHistoryCalls {
		t.Fatal("unexpected long-history outbound count")
	}
}

func TestStableTailLongHistoryFixtures(t *testing.T) {
	fixtures := stableTailLongHistoryFixtures(t, "offline-long-history")
	if len(fixtures) != stableTailLongHistoryCalls || stableTailLongHistoryCalls > 24 {
		t.Fatal("request budget violated")
	}
	previous := map[string]llm.GenerateRequest{}
	for i := 0; i < len(fixtures); i += 2 {
		left, right := fixtures[i], fixtures[i+1]
		if left.phase != right.phase || left.turn != right.turn || left.arm == right.arm {
			t.Fatal("unpaired fixtures")
		}
		if (i/2)%2 == 0 && left.arm != "legacy" || (i/2)%2 == 1 && left.arm != "tail" {
			t.Fatal("arm ordering did not alternate")
		}
		pair := [2]llm.GenerateRequest{left.request, right.request}
		for arm := range pair {
			pair[arm].Model = "synthetic-gemini"
		}
		cfg := llm.ProviderConfig{Provider: llm.ProviderGemini, Model: "synthetic-gemini", MaxOutputTokens: 1024, ContextWindowTokens: 128000}
		budgets, err := traceReplayPreflight(pair, cfg)
		if err != nil || budgets[0].Estimated != budgets[1].Estimated || budgets[0].Estimated < stableTailLongHistoryEstimate || budgets[0].Estimated > 90000 {
			t.Fatal("unequal, short or over-budget long fixture")
		}
		for _, fixture := range []stableTailLongHistoryFixture{left, right} {
			if fixture.request.PromptCacheKey != "" {
				t.Fatal("Gemini fixture depends on ignored routing key")
			}
			old, ok := previous[fixture.arm]
			if ok && fixture.phase == "exact_repeat" && !reflect.DeepEqual(old, fixture.request) {
				t.Fatal("control request changed")
			}
			if ok && fixture.phase == "updated" {
				before, after := observePromptCachePayload(old), observePromptCachePayload(fixture.request)
				if after.StableBoundary != before.StableBoundary+2 || comparePromptCachePayload(before, after).Expected(before) != (fixture.arm == "tail") {
					t.Fatal("append-only prefix invariant failed")
				}
			}
			previous[fixture.arm] = fixture.request
		}
		// Undo precisely the two intentional differences, then require equality.
		legacy, tail := cloneGenerateRequestForTest(left.request), cloneGenerateRequestForTest(right.request)
		if left.arm == "tail" {
			legacy, tail = tail, legacy
		}
		tail = stableTailCacheLegacy(t, tail, "summary", "")
		tail.Messages[0].Content = strings.Replace(tail.Messages[0].Content, "/arm-1.\n", "/arm-0.\n", 1)
		if !reflect.DeepEqual(legacy, tail) {
			t.Fatal("arms differ in more than summary placement and namespace")
		}
	}
	t.Logf("synthetic requests=%d estimated_input=%d..%d", len(fixtures), llm.EstimateRequestInputTokens(fixtures[0].request), llm.EstimateRequestInputTokens(fixtures[len(fixtures)-1].request))
}

func TestStableTailLongHistoryRawUsage(t *testing.T) {
	cases := []struct {
		body                      string
		parsed, metadata, present bool
		value                     *int64
	}{
		{`not-json`, false, false, false, nil},
		{`{}`, true, false, false, nil},
		{`{"usageMetadata":null}`, true, true, false, nil},
		{`{"usageMetadata":{"promptTokenCount":60000}}`, true, true, false, nil},
		{`{"usageMetadata":{"cachedContentTokenCount":null}}`, true, true, true, nil},
		{`{"usageMetadata":{"cachedContentTokenCount":"0"}}`, true, true, true, nil},
		{`{"usageMetadata":{"cachedContentTokenCount":-1}}`, true, true, true, nil},
		{`{"usageMetadata":{"cachedContentTokenCount":0}}`, true, true, true, new(int64)},
		{`{"usageMetadata":{"cachedContentTokenCount":59000}}`, true, true, true, func() *int64 { n := int64(59000); return &n }()},
	}
	var total stableTailLongHistoryTotal
	for _, c := range cases {
		u := stableTailLongHistoryRawUsage([]byte(c.body))
		if u.Parsed != c.parsed || u.MetadataPresent != c.metadata || u.Cached.Present != c.present || !reflect.DeepEqual(u.Cached.Value, c.value) {
			t.Fatal("raw usage presence/value mismatch")
		}
		total.add(u, time.Second)
	}
	if total.CachedMissing != 4 || total.CachedInvalid != 3 || total.CachedExplicitZero != 1 || total.CachedPositive != 1 || total.Cached != 59000 {
		t.Fatal("missing fields collapsed into explicit zeros")
	}
}

type stableTailLongHistoryRoundTripFunc func(*http.Request) (*http.Response, error)

func (f stableTailLongHistoryRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func TestStableTailLongHistoryHTTPBudget(t *testing.T) {
	for _, status := range []int{200, 429, 302, 503} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var outbound int
			guard := &stableTailLongHistoryTransport{base: stableTailLongHistoryRoundTripFunc(func(*http.Request) (*http.Response, error) {
				outbound++
				return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(`{"usageMetadata":{"promptTokenCount":60000,"cachedContentTokenCount":0}}`)), Header: make(http.Header)}, nil
			})}
			for i := 0; i < 24; i++ {
				a := &stableTailLongHistoryAttempt{}
				req, _ := http.NewRequestWithContext(context.WithValue(t.Context(), stableTailLongHistoryAttemptKey{}, a), http.MethodPost, "https://example.invalid", nil)
				response, _ := guard.RoundTrip(req)
				if response != nil {
					body, _ := io.ReadAll(response.Body)
					_ = response.Body.Close()
					if len(body) == 0 {
						t.Fatal("transport consumed SDK body")
					}
				}
				if _, err := guard.RoundTrip(req); err == nil {
					t.Fatal("retry allowed")
				}
			}
			want := 1
			if status == 200 {
				want = stableTailLongHistoryCalls
			}
			if outbound != want || int(guard.calls.Load()) != want {
				t.Fatal("HTTP request cap or stop latch failed")
			}
		})
	}
}

func TestStableTailLongHistoryGeminiWire(t *testing.T) {
	fixtures := stableTailLongHistoryFixtures(t, "offline-wire")
	var bodies [][]byte
	guard := &stableTailLongHistoryTransport{base: stableTailLongHistoryRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		bodies = append(bodies, body)
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"candidates":[{"content":{"role":"model","parts":[{"text":"OK"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":60000,"candidatesTokenCount":1}}`))}, nil
	})}
	client, err := llm.NewClient(llm.ProviderConfig{Provider: llm.ProviderGemini, APIKey: "synthetic-key", BaseURL: "https://example.invalid", Model: "synthetic-gemini", MaxOutputTokens: 1024, ContextWindowTokens: 128000}, llm.WithHTTPClient(&http.Client{Transport: guard}))
	if err != nil {
		t.Fatal("offline Gemini client failed")
	}
	for _, fixture := range fixtures {
		a := &stableTailLongHistoryAttempt{}
		response, err := client.Generate(context.WithValue(t.Context(), stableTailLongHistoryAttemptKey{}, a), fixture.request)
		if err != nil || response == nil || a.usage.Cached.Present {
			t.Fatal("offline Gemini capture failed or invented usage")
		}
	}
	if len(bodies) != stableTailLongHistoryCalls || !bytes.Equal(bodies[0], bodies[3]) || !bytes.Equal(bodies[1], bodies[2]) {
		t.Fatal("wire controls are not exactly identical")
	}
	for i := 0; i < len(bodies); i += 2 {
		if len(bodies[i]) != len(bodies[i+1]) {
			t.Fatal("paired Gemini wire payload sizes differ")
		}
		var normalized [2][]byte
		for arm := range normalized {
			body := bytes.ReplaceAll(bodies[i+arm], []byte("/arm-1."), []byte("/arm-0."))
			var wire map[string]json.RawMessage
			if json.Unmarshal(body, &wire) != nil {
				t.Fatal("invalid paired wire payload")
			}
			var contents []json.RawMessage
			if json.Unmarshal(wire["contents"], &contents) != nil {
				t.Fatal("missing paired wire contents")
			}
			sort.Slice(contents, func(a, b int) bool { return bytes.Compare(contents[a], contents[b]) < 0 })
			wire["contents"], _ = json.Marshal(contents)
			normalized[arm], _ = json.Marshal(wire)
		}
		if !bytes.Equal(normalized[0], normalized[1]) {
			t.Fatal("paired Gemini payloads differ beyond content order and arm namespace")
		}
	}
	for i, body := range bodies {
		var wire struct {
			Contents []struct {
				Role  string `json:"role"`
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"contents"`
			SystemInstruction json.RawMessage `json:"systemInstruction"`
		}
		if json.Unmarshal(body, &wire) != nil {
			t.Fatal("invalid wire fixture")
		}
		wantContents := 0
		leading := true
		for _, message := range fixtures[i].request.Messages {
			if leading && message.Role == llm.RoleSystem {
				continue
			}
			leading = false
			wantContents++
		}
		if len(wire.Contents) != wantContents || len(wire.SystemInstruction) == 0 || bytes.Contains(body, []byte("promptCacheKey")) || bytes.Contains(body, []byte("cache_control")) {
			t.Fatal("wire lost history or invented Gemini cache controls")
		}
	}
}
