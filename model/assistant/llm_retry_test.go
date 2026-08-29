// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/SuInk/diana/model/agent"
	"github.com/SuInk/diana/model/llm"
)

type retryPreservationTool struct {
	calls int
}

type timeoutSequenceProvider struct {
	calls     int
	deadlines []time.Duration
	succeedAt int
}

type managedTimeoutProvider struct {
	timeoutSequenceProvider
}

func (*managedTimeoutProvider) ManagesAttemptTimeout() bool { return true }

type fixedRetryErrorProvider struct {
	calls int
	err   error
}

type retryRegistryAdapter struct {
	calls     int
	succeedAt int
}

func (a *retryRegistryAdapter) Generate(context.Context, llm.ModelDefinition, llm.ChatRequest) (llm.ChatResponse, error) {
	a.calls++
	if a.succeedAt > 0 && a.calls >= a.succeedAt {
		return llm.ChatResponse{Text: "ok"}, nil
	}
	return llm.ChatResponse{}, fmt.Errorf("registry request failed: %w", context.DeadlineExceeded)
}

func (a *retryRegistryAdapter) Stream(context.Context, llm.ModelDefinition, llm.ChatRequest) (<-chan llm.ChatEvent, error) {
	return nil, fmt.Errorf("unexpected stream call")
}

func newRetryTestRegistry(t *testing.T, adapter llm.LLMAdapter) (*llm.ProviderRegistry, llm.ProfileSet) {
	t.Helper()
	registry := llm.NewProviderRegistry()
	if err := registry.RegisterProvider(llm.ProviderDefinition{
		ID: "registry-test", Name: "Registry Test", Protocol: llm.ProtocolOpenAIResponses, Enabled: true,
	}, adapter); err != nil {
		t.Fatal(err)
	}
	if err := registry.RegisterModel(llm.ModelDefinition{
		ID: "registry-test:model", ProviderID: "registry-test", ModelID: "model", Name: "model",
	}); err != nil {
		t.Fatal(err)
	}
	return registry, llm.ProfileSet{
		Profiles: []llm.Profile{{ID: "registry-test", Group: llm.GroupChat, Config: llm.ProviderConfig{Model: "model"}}},
	}
}

func (p *fixedRetryErrorProvider) Generate(context.Context, llm.GenerateRequest) (*llm.GenerateResponse, error) {
	p.calls++
	return nil, p.err
}

func (p *timeoutSequenceProvider) Generate(ctx context.Context, _ llm.GenerateRequest) (*llm.GenerateResponse, error) {
	p.calls++
	if deadline, ok := ctx.Deadline(); ok {
		p.deadlines = append(p.deadlines, time.Until(deadline))
	}
	if p.succeedAt > 0 && p.calls >= p.succeedAt {
		return &llm.GenerateResponse{Text: "ok"}, nil
	}
	return nil, context.DeadlineExceeded
}

func TestTransientLLMTimeoutRetriesThreeTimesWithFullTimeout(t *testing.T) {
	provider := &timeoutSequenceProvider{succeedAt: 4}
	resp, err := generateWithTransientRetryPolicy(context.Background(), provider, llm.GenerateRequest{}, true, 8*time.Second, 3, 0)
	if err != nil {
		t.Fatal(err)
	}
	if resp == nil || resp.Text != "ok" || provider.calls != 4 {
		t.Fatalf("resp=%#v calls=%d", resp, provider.calls)
	}
	want := []time.Duration{8 * time.Second, 8 * time.Second, 8 * time.Second, 8 * time.Second}
	if len(provider.deadlines) != len(want) {
		t.Fatalf("deadlines=%v", provider.deadlines)
	}
	for i := range want {
		if delta := provider.deadlines[i] - want[i]; delta < -50*time.Millisecond || delta > 50*time.Millisecond {
			t.Fatalf("attempt %d timeout=%s want about %s", i+1, provider.deadlines[i], want[i])
		}
	}
}

func TestTransientLLMRetryStopsAfterThreeRetries(t *testing.T) {
	provider := &timeoutSequenceProvider{}
	_, err := generateWithTransientRetryPolicy(context.Background(), provider, llm.GenerateRequest{}, true, time.Second, 3, 0)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err=%v", err)
	}
	if provider.calls != 4 {
		t.Fatalf("calls=%d, want initial request plus three retries", provider.calls)
	}
}

func TestDefaultTransientLLMRetryStopsAfterOneRetry(t *testing.T) {
	provider := &timeoutSequenceProvider{}
	_, err := generateWithTransientRetryTimeout(context.Background(), provider, llm.GenerateRequest{}, true, time.Second)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err=%v", err)
	}
	if provider.calls != 2 {
		t.Fatalf("calls=%d, want initial request plus one retry", provider.calls)
	}
}

func TestRegistryChatRetriesTransientFailure(t *testing.T) {
	adapter := &retryRegistryAdapter{succeedAt: 2}
	registry, profiles := newRetryTestRegistry(t, adapter)
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), &stubLLMProfileStore{set: profiles}, nil, nil, nil)
	runtime.SetLLMProviderRegistry(registry)

	result, err := runtime.runLLMProvider(context.Background(), func(provider LLMProvider) (string, error) {
		response, generateErr := provider.Generate(context.Background(), llm.GenerateRequest{})
		if generateErr != nil {
			return "", generateErr
		}
		return response.Text, nil
	})
	if err != nil || result != "ok" || adapter.calls != 2 {
		t.Fatalf("result=%q err=%v calls=%d, want successful retry on second call", result, err, adapter.calls)
	}
}

func TestRegistryRouterRetryPolicyHonorsCallerMode(t *testing.T) {
	t.Run("retry", func(t *testing.T) {
		adapter := &retryRegistryAdapter{succeedAt: 2}
		registry, profiles := newRetryTestRegistry(t, adapter)
		profiles.Profiles[0].Group = llm.GroupIntent
		runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), &stubLLMProfileStore{set: profiles}, nil, nil, nil)
		runtime.SetLLMProviderRegistry(registry)

		result, err := runtime.runLLMRouterProvider(context.Background(), func(provider LLMProvider) (string, error) {
			response, generateErr := provider.Generate(context.Background(), llm.GenerateRequest{})
			if generateErr != nil {
				return "", generateErr
			}
			return response.Text, nil
		})
		if err != nil || result != "ok" || adapter.calls != 2 {
			t.Fatalf("result=%q err=%v calls=%d", result, err, adapter.calls)
		}
	})

	t.Run("once", func(t *testing.T) {
		adapter := &retryRegistryAdapter{succeedAt: 2}
		registry, profiles := newRetryTestRegistry(t, adapter)
		profiles.Profiles[0].Group = llm.GroupIntent
		runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), &stubLLMProfileStore{set: profiles}, nil, nil, nil)
		runtime.SetLLMProviderRegistry(registry)

		_, err := runtime.runLLMRouterProviderOnce(context.Background(), func(provider LLMProvider) (string, error) {
			_, generateErr := provider.Generate(context.Background(), llm.GenerateRequest{})
			return "", generateErr
		})
		if !errors.Is(err, context.DeadlineExceeded) || adapter.calls != 1 {
			t.Fatalf("err=%v calls=%d, want one attempt", err, adapter.calls)
		}
	})
}

func TestEmptyLLMOutputRetriesOnce(t *testing.T) {
	provider := &fixedRetryErrorProvider{err: fmt.Errorf(
		"llm: openai-compatible chat completions output is empty: %w",
		llm.ErrCompletionEmpty,
	)}
	_, err := generateWithTransientRetryTimeout(context.Background(), provider, llm.GenerateRequest{}, true, time.Second)
	if err == nil || !strings.Contains(err.Error(), "output is empty") {
		t.Fatalf("err=%v", err)
	}
	if provider.calls != 2 {
		t.Fatalf("calls=%d, want initial request plus one retry", provider.calls)
	}
}

func TestClassifiedNoTextErrorsSkipSameProfileRetry(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{name: "truncated", err: fmt.Errorf("model result: %w", llm.ErrCompletionTruncatedNoText)},
		{name: "terminal", err: fmt.Errorf("model result: %w", llm.ErrCompletionHasNoText)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := &fixedRetryErrorProvider{err: tt.err}
			_, err := generateWithTransientRetryTimeout(context.Background(), provider, llm.GenerateRequest{}, true, time.Second)
			if !errors.Is(err, tt.err) {
				t.Fatalf("err=%v, want %v", err, tt.err)
			}
			if provider.calls != 1 {
				t.Fatalf("calls=%d, want one attempt", provider.calls)
			}
		})
	}
}

func TestRoutingEmptyOutputRetriesThenFailsOverWithinGroup(t *testing.T) {
	store := &stubLLMProfileStore{set: llm.ProfileSet{
		Profiles: []llm.Profile{
			{ID: "main", Group: "default", Config: llm.ProviderConfig{Provider: llm.ProviderOpenAICompatible, Model: "main-model"}},
			{ID: "routing-a", Group: "routing", Config: llm.ProviderConfig{Provider: llm.ProviderOpenAICompatible, Model: "routing-a"}},
			{ID: "routing-b", Group: "routing", Config: llm.ProviderConfig{Provider: llm.ProviderOpenAICompatible, Model: "routing-b"}},
		},
	}}
	primary := &fixedRetryErrorProvider{err: fmt.Errorf(
		"llm: openai-compatible chat completions output is empty: %w",
		llm.ErrCompletionEmpty,
	)}
	secondary := &capturingLLMProvider{reply: `{"reply":true}`}
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), store, nil, nil, nil)
	var configuredModels []string
	runtime.SetLLMProviderConfigFactory(func(cfg llm.ProviderConfig) (LLMProvider, error) {
		configuredModels = append(configuredModels, cfg.Model)
		switch cfg.Model {
		case "routing-a":
			return primary, nil
		case "routing-b":
			return secondary, nil
		default:
			return nil, fmt.Errorf("unexpected model %q", cfg.Model)
		}
	})

	reply, err := runtime.runLLMRouterProvider(context.Background(), func(client LLMProvider) (string, error) {
		resp, generateErr := client.Generate(context.Background(), llm.GenerateRequest{})
		if generateErr != nil {
			return "", generateErr
		}
		return resp.Text, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if reply != `{"reply":true}` {
		t.Fatalf("reply=%q", reply)
	}
	if primary.calls != 2 {
		t.Fatalf("primary calls=%d, want initial request plus one retry", primary.calls)
	}
	if got, want := strings.Join(configuredModels, ","), "routing-a,routing-b"; got != want {
		t.Fatalf("configured models=%q, want %q", got, want)
	}
}

func TestContentPolicyRejectionKeepsOriginalErrorAndStopsRetry(t *testing.T) {
	raw := "Request was rejected because it was considered high risk by the content policy"
	provider := &fixedRetryErrorProvider{err: errors.New(raw)}
	_, err := generateWithTransientRetryPolicy(context.Background(), provider, llm.GenerateRequest{}, true, time.Second, 3, 0)
	if err == nil || err.Error() != raw {
		t.Fatalf("err=%v, want original error %q", err, raw)
	}
	if !errors.Is(err, errContentPolicyRejection) {
		t.Fatalf("err=%v is not classified as a content policy rejection", err)
	}
	if provider.calls != 1 {
		t.Fatalf("calls=%d, want one request without retry", provider.calls)
	}
	if shouldFailoverLLMError(err) {
		t.Fatal("content policy rejection must not fail over to another provider")
	}
}

func TestContentPolicyRejectionStopsProfileFailover(t *testing.T) {
	first := &fixedRetryErrorProvider{err: errors.New("content_policy_violation: blocked")}
	second := &capturingLLMProvider{reply: "unexpected"}
	secondFactoryCalls := 0
	provider, err := newProfileFailoverLLMProvider([]llm.Profile{
		{ID: "first", Group: "default", Config: llm.ProviderConfig{Model: "model-a"}},
		{ID: "second", Group: "default", Config: llm.ProviderConfig{Model: "model-a"}},
	}, func(cfg llm.ProviderConfig) (LLMProvider, error) {
		if cfg.Model == "model-a" && first.calls == 0 {
			return first, nil
		}
		secondFactoryCalls++
		return second, nil
	}, true, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	_, err = provider.Generate(context.Background(), llm.GenerateRequest{})
	if !errors.Is(err, errContentPolicyRejection) {
		t.Fatalf("err=%v, want content policy rejection", err)
	}
	if first.calls != 1 || secondFactoryCalls != 0 {
		t.Fatalf("first calls=%d second factory calls=%d", first.calls, secondFactoryCalls)
	}
}

func TestManagedAttemptTimeoutDoesNotReceiveWholeRequestDeadline(t *testing.T) {
	provider := &managedTimeoutProvider{}
	_, err := generateWithTransientRetryPolicy(context.Background(), provider, llm.GenerateRequest{}, false, 8*time.Second, 3, 0)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err=%v", err)
	}
	if provider.calls != 1 || len(provider.deadlines) != 0 {
		t.Fatalf("calls=%d deadlines=%v, want one attempt without a wrapper deadline", provider.calls, provider.deadlines)
	}
}

func TestKnownHeaderAndIdleTimeoutsSkipSameProfileRetry(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{name: "response header", err: fmt.Errorf("llm: response header timeout after 60s: %w", context.DeadlineExceeded)},
		{name: "stream idle", err: fmt.Errorf("llm: stream idle timeout after 60s: %w", context.DeadlineExceeded)},
		{name: "json body idle", err: fmt.Errorf("llm: response body idle timeout after 60s: %w", context.DeadlineExceeded)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := &fixedRetryErrorProvider{err: tt.err}
			_, err := generateWithTransientRetryPolicy(context.Background(), provider, llm.GenerateRequest{}, true, time.Second, 3, 0)
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("err=%v", err)
			}
			if provider.calls != 1 {
				t.Fatalf("calls=%d, want direct failover after one attempt", provider.calls)
			}
		})
	}
}

func TestTransientLLMRetryDisabledUsesOneFullAttempt(t *testing.T) {
	provider := &timeoutSequenceProvider{}
	_, err := generateWithTransientRetryPolicy(context.Background(), provider, llm.GenerateRequest{}, false, 8*time.Second, 3, 0)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err=%v", err)
	}
	if provider.calls != 1 || len(provider.deadlines) != 1 {
		t.Fatalf("calls=%d deadlines=%v", provider.calls, provider.deadlines)
	}
	if delta := provider.deadlines[0] - 8*time.Second; delta < -50*time.Millisecond || delta > 50*time.Millisecond {
		t.Fatalf("timeout=%s want about 8s", provider.deadlines[0])
	}
}

func TestTransientLLMRetrySharesAndDoesNotResetParentDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	parentDeadline, _ := ctx.Deadline()
	provider := &timeoutSequenceProvider{succeedAt: 4}
	if _, err := generateWithTransientRetryPolicy(ctx, provider, llm.GenerateRequest{}, true, 8*time.Second, 3, 0); err != nil {
		t.Fatal(err)
	}
	for i, remaining := range provider.deadlines {
		attemptDeadline := time.Now().Add(remaining)
		if attemptDeadline.After(parentDeadline.Add(50 * time.Millisecond)) {
			t.Fatalf("attempt %d reset parent deadline: attempt=%s parent=%s", i+1, attemptDeadline, parentDeadline)
		}
	}
}

func TestTransientLLMRetryDoesNotExtendCallerDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	before, _ := ctx.Deadline()
	provider := &timeoutSequenceProvider{succeedAt: 2}
	if _, err := generateWithTransientRetryPolicy(ctx, provider, llm.GenerateRequest{}, true, 8*time.Second, 1, 0); err != nil {
		t.Fatal(err)
	}
	after, _ := ctx.Deadline()
	if after != before {
		t.Fatalf("caller deadline changed from %s to %s", before, after)
	}
}

func (t *retryPreservationTool) Name() string { return "test.lookup" }

func (t *retryPreservationTool) Description() string {
	return `测试检索工具。input: {}`
}

func (t *retryPreservationTool) Run(context.Context, map[string]any) (string, error) {
	t.calls++
	return "已经取得的工具证据", nil
}

type retryPreservationProvider struct {
	calls       int
	sawEvidence bool
}

func (p *retryPreservationProvider) Generate(_ context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	p.calls++
	switch p.calls {
	case 1:
		return &llm.GenerateResponse{Text: `{"action":"tool","tool":"test.lookup","input":{}}`}, nil
	case 2:
		return nil, fmt.Errorf("request failed: %w", context.DeadlineExceeded)
	case 3:
		for _, message := range req.Messages {
			if strings.Contains(message.Content, "已经取得的工具证据") {
				p.sawEvidence = true
				break
			}
		}
		return &llm.GenerateResponse{Text: `{"action":"final","content":"根据已取得的证据完成回答"}`}, nil
	default:
		return nil, fmt.Errorf("unexpected Generate call %d", p.calls)
	}
}

func TestAgentTransientRetryPreservesCompletedToolResults(t *testing.T) {
	store := &stubLLMProfileStore{set: llm.ProfileSet{
		Profiles: []llm.Profile{{
			ID:     "main",
			Group:  "default",
			Config: llm.ProviderConfig{Provider: llm.ProviderOpenAICompatible, Model: "main"},
		}},
	}}
	provider := &retryPreservationProvider{}
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), store, nil, nil, nil)
	runtime.SetLLMProviderConfigFactory(func(llm.ProviderConfig) (LLMProvider, error) {
		return provider, nil
	})
	tool := &retryPreservationTool{}
	runCalls := 0
	reply, err := runtime.runLLMProvider(context.Background(), func(client LLMProvider) (string, error) {
		runCalls++
		runner, err := agent.NewRunner(client, agent.Config{WorkDir: t.TempDir(), MaxSteps: 3}, agent.NewToolRegistry(tool))
		if err != nil {
			return "", err
		}
		defer runner.Close()
		response, err := runner.Run(context.Background(), agent.Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "请检索后回答"}}})
		if err != nil {
			return "", err
		}
		return response.Text, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if reply != "根据已取得的证据完成回答" {
		t.Fatalf("reply = %q", reply)
	}
	if runCalls != 1 || tool.calls != 1 || provider.calls != 3 || !provider.sawEvidence {
		t.Fatalf("runCalls=%d toolCalls=%d providerCalls=%d sawEvidence=%v", runCalls, tool.calls, provider.calls, provider.sawEvidence)
	}
}

// 窗口按模型名推断有可能偏大；被供应商判为超限时必须收缩重试，而不是让这一轮
// 回复直接失败。
type contextOverflowProvider struct {
	limit    int64
	requests []llm.GenerateRequest
}

func (p *contextOverflowProvider) Generate(_ context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	p.requests = append(p.requests, req)
	budget := req.MaxContextTokens
	if budget <= 0 {
		budget = llm.DefaultMaxContextTokens
	}
	if budget > p.limit {
		return nil, fmt.Errorf("this model's maximum context length is %d tokens", p.limit)
	}
	return &llm.GenerateResponse{Text: "ok"}, nil
}

func TestContextOverflowShrinksBudgetAndRetries(t *testing.T) {
	provider := &contextOverflowProvider{limit: 32000}
	resp, err := generateWithTransientRetryPolicy(context.Background(), provider,
		llm.GenerateRequest{Messages: []llm.Message{{Role: llm.RoleUser, Content: "在吗"}}},
		true, 0, 0, 0)
	if err != nil {
		t.Fatalf("overflow was not recovered: %v", err)
	}
	if resp == nil || resp.Text != "ok" {
		t.Fatalf("resp = %#v", resp)
	}
	if len(provider.requests) < 2 {
		t.Fatalf("expected at least one retry, got %d attempts", len(provider.requests))
	}
	if first := provider.requests[0].MaxContextTokens; first != 0 {
		t.Fatalf("first attempt must use the profile budget, got %d", first)
	}
	last := provider.requests[len(provider.requests)-1].MaxContextTokens
	if last <= 0 || last > provider.limit {
		t.Fatalf("final attempt budget = %d, want a positive value within %d", last, provider.limit)
	}
}

func TestContextOverflowStopsAtTheFloorInsteadOfLooping(t *testing.T) {
	// 供应商窗口小到收缩也救不回来时，如实报错而不是无限重试。
	provider := &contextOverflowProvider{limit: 512}
	_, err := generateWithTransientRetryPolicy(context.Background(), provider,
		llm.GenerateRequest{Messages: []llm.Message{{Role: llm.RoleUser, Content: "在吗"}}},
		true, 0, 0, 0)
	if err == nil {
		t.Fatal("expected the unrecoverable overflow to surface")
	}
	if !llm.IsContextOverflowError(err) {
		t.Fatalf("error lost its context-overflow identity: %v", err)
	}
	if len(provider.requests) > maxContextShrinkRetries+1 {
		t.Fatalf("shrink retries were not bounded: %d attempts", len(provider.requests))
	}
}

func TestShrinkContextForRetryHalvesDownToTheFloor(t *testing.T) {
	req := llm.GenerateRequest{}
	seen := []int64{}
	for {
		next, ok := shrinkContextForRetry(req)
		if !ok {
			break
		}
		req = next
		seen = append(seen, req.MaxContextTokens)
		if len(seen) > 10 {
			t.Fatalf("shrink did not terminate: %v", seen)
		}
	}
	if len(seen) == 0 {
		t.Fatal("shrink never produced a smaller budget")
	}
	for index := 1; index < len(seen); index++ {
		if seen[index] >= seen[index-1] {
			t.Fatalf("budget did not shrink monotonically: %v", seen)
		}
	}
	if last := seen[len(seen)-1]; last != contextOverflowFloorTokens {
		t.Fatalf("final budget = %d, want the floor %d", last, contextOverflowFloorTokens)
	}
}
