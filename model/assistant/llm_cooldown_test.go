// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/SuInk/diana/model/llm"
)

// 线上 9/25–9/27 实测的上游原文：账号池被限流，提示要等 3.6 天。
const accountsLimitedErrText = "Error 503, Message: Token error: All accounts limited. Wait 311000s., Status: 503 Service Unavailable"

// cooldownRegistryAdapter 是一个可以按需失败的注册表适配器。
type cooldownRegistryAdapter struct {
	streamCalls   int
	generateCalls int
	generateErrs  []error
	streamOpenErr error
	streamEvents  []llm.ChatEvent
	response      string
	// block 让调用一直卡到上下文结束；delay 让成功的调用先等一会儿。
	block bool
	delay time.Duration
}

func (a *cooldownRegistryAdapter) Generate(ctx context.Context, _ llm.ModelDefinition, _ llm.ChatRequest) (llm.ChatResponse, error) {
	a.generateCalls++
	if a.block {
		<-ctx.Done()
		return llm.ChatResponse{}, ctx.Err()
	}
	if a.delay > 0 {
		select {
		case <-time.After(a.delay):
		case <-ctx.Done():
			return llm.ChatResponse{}, ctx.Err()
		}
	}
	if len(a.generateErrs) > 0 {
		err := a.generateErrs[0]
		if len(a.generateErrs) > 1 {
			a.generateErrs = a.generateErrs[1:]
		}
		if err != nil {
			return llm.ChatResponse{}, err
		}
	}
	return llm.ChatResponse{Text: a.response}, nil
}

func (a *cooldownRegistryAdapter) Stream(context.Context, llm.ModelDefinition, llm.ChatRequest) (<-chan llm.ChatEvent, error) {
	a.streamCalls++
	if a.streamOpenErr != nil {
		return nil, a.streamOpenErr
	}
	events := a.streamEvents
	if events == nil {
		events = []llm.ChatEvent{{Type: llm.ChatEventTextDelta, Text: a.response}, {Type: llm.ChatEventDone}}
	}
	ch := make(chan llm.ChatEvent, len(events))
	for _, event := range events {
		ch <- event
	}
	close(ch)
	return ch, nil
}

// cooldownProviderFactory 模拟运行时：注册表和冷却表跨请求共享，provider 每次调用新建。
func cooldownProviderFactory(t *testing.T, table *llmCooldownTable, adapters ...*cooldownRegistryAdapter) func() *registryFailoverLLMProvider {
	t.Helper()
	registry := llm.NewProviderRegistry()
	profiles := make([]llm.Profile, 0, len(adapters))
	for index, adapter := range adapters {
		id := []string{"primary", "backup", "third"}[index]
		if err := registry.RegisterProvider(llm.ProviderDefinition{ID: id, Name: id, Protocol: llm.ProtocolOpenAIResponses, Enabled: true}, adapter); err != nil {
			t.Fatal(err)
		}
		if err := registry.RegisterModel(llm.ModelDefinition{ID: id + ":" + id + "-model", ProviderID: id, ModelID: id + "-model"}); err != nil {
			t.Fatal(err)
		}
		profiles = append(profiles, llm.Profile{ID: id, Name: id + "-name", Group: "chat", Config: llm.ProviderConfig{Model: id + "-model"}})
	}
	return func() *registryFailoverLLMProvider {
		provider, err := newRegistryFailoverLLMProvider(registry, profiles, true, len(profiles) > 1)
		if err != nil {
			t.Fatal(err)
		}
		provider.cooldowns = table
		return provider
	}
}

func cooldownRequest() llm.GenerateRequest {
	return llm.GenerateRequest{Messages: []llm.Message{{Role: llm.RoleUser, Content: "你好"}}}
}

// 线上现象：主档一直回「All accounts limited」，每次调用都先撞它、原地重试一次再切。
// 冷却表跨请求保留后，第一次撞上就记下，后面的调用直接从备用档开始。
func TestLLMCooldownSkipsLimitedCandidateAcrossCalls(t *testing.T) {
	table := &llmCooldownTable{}
	primary := &cooldownRegistryAdapter{generateErrs: []error{errors.New(accountsLimitedErrText)}}
	backup := &cooldownRegistryAdapter{response: "备用回复"}
	newProvider := cooldownProviderFactory(t, table, primary, backup)

	for call := 1; call <= 3; call++ {
		resp, err := newProvider().Generate(context.Background(), cooldownRequest())
		if err != nil || resp == nil || resp.Text != "备用回复" {
			t.Fatalf("第 %d 次调用 resp=%#v err=%v", call, resp, err)
		}
	}
	// 额度类错误不原地重试，也只在第一次调用时撞一下。
	if primary.generateCalls != 1 {
		t.Fatalf("主档应当只被调用 1 次，实际 %d", primary.generateCalls)
	}
	if backup.generateCalls != 3 {
		t.Fatalf("备用档应当被调用 3 次，实际 %d", backup.generateCalls)
	}
	left := table.remaining(llm.Profile{ID: "primary", Config: llm.ProviderConfig{Model: "primary-model"}})
	if left <= 0 || left > llmCooldownMax {
		t.Fatalf("主档冷却应当封顶在 %s 以内，实际 %s", llmCooldownMax, left)
	}
}

func TestLLMCooldownPublicSnapshotReportsCoolingAndRetainedFailures(t *testing.T) {
	now := time.Date(2026, 10, 12, 14, 0, 0, 0, time.UTC)
	table := &llmCooldownTable{now: func() time.Time { return now }}
	profile := llm.Profile{ID: "limited", Config: llm.ProviderConfig{Model: "chat-model"}}
	table.markFailure(profile, errors.New("429 Too Many Requests: quota exceeded"))

	snapshot := table.publicSnapshot()
	if !snapshot.CheckedAt.Equal(now) || len(snapshot.Candidates) != 1 {
		t.Fatalf("snapshot = %#v", snapshot)
	}
	health := snapshot.Candidates[0]
	if health.ProfileID != "limited" || health.Model != "chat-model" || health.FailureCount != 1 || !health.LastFailure.Equal(now) {
		t.Fatalf("health = %#v", health)
	}
	if health.CooldownUntil == nil || !health.CooldownUntil.After(now) {
		t.Fatalf("cooldown_until = %v", health.CooldownUntil)
	}

	now = now.Add(llmCooldownDefault + time.Second)
	health = table.publicSnapshot().Candidates[0]
	if health.CooldownUntil != nil || health.FailureCount != 1 {
		t.Fatalf("冷却到期后应保留失败次数但不再报告冷却：%#v", health)
	}
	table.clear(profile)
	if got := table.publicSnapshot().Candidates; len(got) != 0 {
		t.Fatalf("成功后应清除公开健康状态：%#v", got)
	}
}

func TestLLMCustomRecoveryPolicyAndManualReset(t *testing.T) {
	now := time.Date(2026, 10, 15, 15, 0, 0, 0, time.UTC)
	table := &llmCooldownTable{now: func() time.Time { return now }}
	policy := llmRecoveryPolicy{
		transientRetries:  0,
		retryDelay:        0,
		transientCooldown: 3 * time.Second,
		statusCooldown:    4 * time.Second,
		maxCooldown:       5 * time.Second,
		respectRetryAfter: false,
	}
	transient := llm.Profile{ID: "provider-a", Config: llm.ProviderConfig{Model: "shared-model"}}
	limited := llm.Profile{ID: "provider-b", Config: llm.ProviderConfig{Model: "shared-model"}}

	if duration := table.markFailureWithPolicy(transient, errors.New("HTTP 503 service unavailable"), policy).cooldown; duration != 3*time.Second {
		t.Fatalf("transient cooldown = %s", duration)
	}
	if duration := table.markFailureWithPolicy(limited, errors.New("HTTP 429 retry after 60s"), policy).cooldown; duration != 4*time.Second {
		t.Fatalf("status cooldown ignored custom Retry-After policy: %s", duration)
	}
	if cleared := table.clearMatching([]string{"provider-a"}, "shared-model"); cleared != 1 {
		t.Fatalf("cleared = %d", cleared)
	}
	if table.remaining(transient) != 0 || table.remaining(limited) <= 0 {
		t.Fatalf("manual reset affected wrong candidates: transient=%s limited=%s", table.remaining(transient), table.remaining(limited))
	}

	adapter := &cooldownRegistryAdapter{generateErrs: []error{errors.New("HTTP 503 service unavailable"), nil}, response: "recovered"}
	provider := cooldownProviderFactory(t, &llmCooldownTable{}, adapter)()
	provider.recoveryPolicy = policy
	if _, err := provider.Generate(context.Background(), cooldownRequest()); err == nil {
		t.Fatal("custom zero-retry policy should return the first transient failure")
	}
	if adapter.generateCalls != 1 {
		t.Fatalf("zero-retry policy calls = %d", adapter.generateCalls)
	}
}

// 冷却到期后先探一次：上游恢复了就重新用它，而不是被「Wait 311000s」锁三天。
func TestLLMCooldownExpiresAndProbesAgain(t *testing.T) {
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	table := &llmCooldownTable{now: func() time.Time { return now }}
	primary := &cooldownRegistryAdapter{generateErrs: []error{errors.New(accountsLimitedErrText), nil}, response: "主档恢复"}
	backup := &cooldownRegistryAdapter{response: "备用回复"}
	newProvider := cooldownProviderFactory(t, table, primary, backup)

	if _, err := newProvider().Generate(context.Background(), cooldownRequest()); err != nil {
		t.Fatal(err)
	}
	now = now.Add(llmCooldownMax + time.Second)
	resp, err := newProvider().Generate(context.Background(), cooldownRequest())
	if err != nil || resp.Text != "主档恢复" {
		t.Fatalf("冷却到期后应当重新先试主档：resp=%#v err=%v", resp, err)
	}
	if primary.generateCalls != 2 || backup.generateCalls != 1 {
		t.Fatalf("primary=%d backup=%d，期望 2/1", primary.generateCalls, backup.generateCalls)
	}
}

// 所有候选都在冷却时仍按原顺序尝试，不能让冷却把整条链路判死；硬试成功就解除冷却。
func TestLLMCooldownAllCoolingStillTriesInOrder(t *testing.T) {
	table := &llmCooldownTable{}
	quota := errors.New("429 Too Many Requests: quota exceeded")
	primary := &cooldownRegistryAdapter{generateErrs: []error{quota, nil}, response: "主档恢复"}
	backup := &cooldownRegistryAdapter{generateErrs: []error{quota}}
	newProvider := cooldownProviderFactory(t, table, primary, backup)

	if _, err := newProvider().Generate(context.Background(), cooldownRequest()); err == nil {
		t.Fatal("两档都限流时第一次调用应当失败")
	}
	resp, err := newProvider().Generate(context.Background(), cooldownRequest())
	if err != nil || resp.Text != "主档恢复" {
		t.Fatalf("全部冷却时应当仍从主档开始硬试：resp=%#v err=%v", resp, err)
	}
	if primary.generateCalls != 2 || backup.generateCalls != 1 {
		t.Fatalf("primary=%d backup=%d，期望 2/1", primary.generateCalls, backup.generateCalls)
	}
	if left := table.remaining(llm.Profile{ID: "primary", Config: llm.ProviderConfig{Model: "primary-model"}}); left != 0 {
		t.Fatalf("硬试成功后应当解除冷却，还剩 %s", left)
	}
}

// 只有一个候选时，一次偶发 503 不能把它锁死：下一次调用照常用它。
func TestLLMCooldownNeverLocksSingleCandidate(t *testing.T) {
	table := &llmCooldownTable{}
	only := &cooldownRegistryAdapter{
		generateErrs: []error{errors.New("503 Service Unavailable"), errors.New("503 Service Unavailable"), errors.New("503 Service Unavailable"), nil},
		response:     "恢复了",
	}
	newProvider := cooldownProviderFactory(t, table, only)

	if _, err := newProvider().Generate(context.Background(), cooldownRequest()); err == nil {
		t.Fatal("三次 503 之后第一次调用应当失败")
	}
	resp, err := newProvider().Generate(context.Background(), cooldownRequest())
	if err != nil || resp.Text != "恢复了" {
		t.Fatalf("唯一候选冷却中也应当照常调用：resp=%#v err=%v", resp, err)
	}
}

// 流式打开时就失败：同样记冷却，下一次流式调用直接从备用档开始。
func TestLLMCooldownStreamOpenFailureSkipsCandidateNextCall(t *testing.T) {
	table := &llmCooldownTable{}
	primary := &cooldownRegistryAdapter{streamOpenErr: errors.New(accountsLimitedErrText)}
	backup := &cooldownRegistryAdapter{response: "备用回复"}
	newProvider := cooldownProviderFactory(t, table, primary, backup)

	for call := 1; call <= 2; call++ {
		resp, err := (&streamingLLMProvider{provider: newProvider()}).Generate(context.Background(), cooldownRequest())
		if err != nil || resp == nil || resp.Text != "备用回复" {
			t.Fatalf("第 %d 次调用 resp=%#v err=%v", call, resp, err)
		}
	}
	// 额度类错误打开流时也不原地重试。
	if primary.streamCalls != 1 {
		t.Fatalf("主档流式应当只被打开 1 次，实际 %d", primary.streamCalls)
	}
	if backup.streamCalls != 2 {
		t.Fatalf("备用档流式应当被打开 2 次，实际 %d", backup.streamCalls)
	}
}

// Gemini 的限流是流打开后作为第一个事件出来的，走 skipRejectedCandidate，也要记冷却。
func TestLLMCooldownMidStreamLimitSkipsCandidateNextCall(t *testing.T) {
	table := &llmCooldownTable{}
	limited := errors.New(accountsLimitedErrText)
	primary := &cooldownRegistryAdapter{streamEvents: []llm.ChatEvent{{Type: llm.ChatEventError, Error: limited.Error(), ErrorCause: limited}}}
	backup := &cooldownRegistryAdapter{response: "备用回复"}
	newProvider := cooldownProviderFactory(t, table, primary, backup)

	for call := 1; call <= 2; call++ {
		resp, err := (&streamingLLMProvider{provider: newProvider()}).Generate(context.Background(), cooldownRequest())
		if err != nil || resp == nil || resp.Text != "备用回复" {
			t.Fatalf("第 %d 次调用 resp=%#v err=%v", call, resp, err)
		}
	}
	if primary.streamCalls != 1 || primary.generateCalls != 0 {
		t.Fatalf("主档 stream=%d generate=%d，期望 1/0", primary.streamCalls, primary.generateCalls)
	}
	if backup.streamCalls != 2 {
		t.Fatalf("备用档流式应当被打开 2 次，实际 %d", backup.streamCalls)
	}
}

// 流式正文读完才算成功：全部冷却时硬试主档、流式读完后解除它的冷却。
func TestLLMCooldownClearedAfterStreamSucceeds(t *testing.T) {
	table := &llmCooldownTable{}
	primaryProfile := llm.Profile{ID: "primary", Config: llm.ProviderConfig{Model: "primary-model"}}
	backupProfile := llm.Profile{ID: "backup", Config: llm.ProviderConfig{Model: "backup-model"}}
	table.markFailureWithPolicy(primaryProfile, errors.New("429 Too Many Requests"), immediateCooldownPolicy())
	table.markFailureWithPolicy(backupProfile, errors.New("429 Too Many Requests"), immediateCooldownPolicy())
	primary := &cooldownRegistryAdapter{response: "主档恢复"}
	backup := &cooldownRegistryAdapter{response: "备用回复"}
	newProvider := cooldownProviderFactory(t, table, primary, backup)

	resp, err := (&streamingLLMProvider{provider: newProvider()}).Generate(context.Background(), cooldownRequest())
	if err != nil || resp.Text != "主档恢复" {
		t.Fatalf("resp=%#v err=%v", resp, err)
	}
	if left := table.remaining(primaryProfile); left != 0 {
		t.Fatalf("流式成功后应当解除主档冷却，还剩 %s", left)
	}
	if left := table.remaining(backupProfile); left == 0 {
		t.Fatal("没被调用的备用档冷却不该被顺手清掉")
	}
}

// 旧配置档路径（profileFailoverLLMProvider）也每次调用新建，同样要跨请求记冷却。
func TestLLMCooldownProfileFailoverProvider(t *testing.T) {
	table := &llmCooldownTable{}
	profiles := []llm.Profile{
		{ID: "a", Name: "主档", Group: "chat", Config: llm.ProviderConfig{Model: "model-a"}},
		{ID: "b", Name: "备用", Group: "chat", Config: llm.ProviderConfig{Model: "model-b"}},
	}
	var attempts []string
	factory := func(cfg llm.ProviderConfig) (LLMProvider, error) {
		attempts = append(attempts, cfg.Model)
		if cfg.Model == "model-a" {
			return failingLLMProvider{err: errors.New(accountsLimitedErrText)}, nil
		}
		return &capturingLLMProvider{reply: "备用回复"}, nil
	}
	for call := 1; call <= 2; call++ {
		provider, err := newProfileFailoverLLMProvider(profiles, factory, true, nil, true)
		if err != nil {
			t.Fatal(err)
		}
		provider.cooldowns = table
		if _, err := provider.Generate(context.Background(), cooldownRequest()); err != nil {
			t.Fatalf("第 %d 次调用 err=%v", call, err)
		}
	}
	want := []string{"model-a", "model-b", "model-b"}
	if strings.Join(attempts, ",") != strings.Join(want, ",") {
		t.Fatalf("attempts = %v, want %v", attempts, want)
	}
}

// 控制台要显示每条路由上次调用的时间：失败和成功都记，成功清冷却、手动恢复也
// 不能把它一起抹掉。
func TestLLMCooldownSnapshotKeepsLastCallAcrossClear(t *testing.T) {
	now := time.Date(2026, 10, 27, 9, 0, 0, 0, time.UTC)
	table := &llmCooldownTable{now: func() time.Time { return now }}
	profiles := []llm.Profile{
		{ID: "a", Name: "主档", Group: "chat", Config: llm.ProviderConfig{Model: "model-a"}},
		{ID: "b", Name: "备用", Group: "chat", Config: llm.ProviderConfig{Model: "model-b"}},
	}
	factory := func(cfg llm.ProviderConfig) (LLMProvider, error) {
		if cfg.Model == "model-a" {
			return failingLLMProvider{err: errors.New(accountsLimitedErrText)}, nil
		}
		return &capturingLLMProvider{reply: "备用回复"}, nil
	}
	provider, err := newProfileFailoverLLMProvider(profiles, factory, true, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	provider.cooldowns = table
	if _, err := provider.Generate(context.Background(), cooldownRequest()); err != nil {
		t.Fatal(err)
	}
	calls := table.publicSnapshot().Calls
	if len(calls) != 2 || calls[0].ProfileID != "a" || calls[0].OK || !calls[0].At.Equal(now) || calls[1].ProfileID != "b" || calls[1].Model != "model-b" || !calls[1].OK {
		t.Fatalf("calls = %#v", calls)
	}
	if table.clearMatching([]string{"a"}, "model-a") != 1 {
		t.Fatal("手动恢复应清掉主档的冷却")
	}
	if got := table.publicSnapshot().Calls; len(got) != 2 || got[0].OK {
		t.Fatalf("手动恢复不应改动调用记录：%#v", got)
	}
}

// 额度用完不走原地重试，单候选时也一样；限流、并发超限和普通 503 先在原模型上
// 重试两次；Retry-After 超过原地等待上限时直接交给降级链。
func TestSameProfileRetryByFailureKind(t *testing.T) {
	cases := []struct {
		err      error
		attempts int
	}{
		{errors.New(accountsLimitedErrText), 1},
		{errors.New("503 Service Unavailable: RESOURCE_EXHAUSTED"), 1},
		{errors.New("429 Too Many Requests"), 3},
		{errors.New("llm stream failed: Concurrency limit exceeded for account, please retry later"), 3},
		{errors.New("429 Too Many Requests: retry after 1ms"), 3},
		{errors.New("429 Too Many Requests: retry after 60s"), 1},
		{errors.New("503 Service Unavailable"), 3},
	}
	for _, tc := range cases {
		provider := &countingFailLLMProvider{err: tc.err}
		_, err := generateWithTransientRetryPolicy(context.Background(), provider, cooldownRequest(), true, 0, llmTransientMaxRetries, 0)
		if err == nil {
			t.Fatalf("%q: 应当返回错误", tc.err)
		}
		if provider.calls != tc.attempts {
			t.Fatalf("%q: 调用 %d 次，期望 %d", tc.err, provider.calls, tc.attempts)
		}
	}
}

type countingFailLLMProvider struct {
	err   error
	calls int
}

func (p *countingFailLLMProvider) Generate(context.Context, llm.GenerateRequest) (*llm.GenerateResponse, error) {
	p.calls++
	return nil, p.err
}

func TestLLMCooldownDuration(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want time.Duration
	}{
		{"账号池限流按提示封顶", errors.New(accountsLimitedErrText), llmCooldownMax},
		{"提示短于上限照用", errors.New("429 Too Many Requests: Please retry in 34.5s."), 34500 * time.Millisecond},
		{"限流无提示用默认值", errors.New("429 Too Many Requests"), llmCooldownDefault},
		{"密钥失效用默认值", errors.New("401 Unauthorized: invalid api key"), llmCooldownDefault},
		{"普通 503 很短", errors.New("503 Service Unavailable"), llmCooldownTransient},
		{"超时不冷却", context.DeadlineExceeded, 0},
		{"内容拦截不冷却", errContentPolicyRejection, 0},
		{"上游拦截文案不冷却", llm.ErrUnverifiedRejection, 0},
		{"与上游无关的错误不冷却", errors.New("json: cannot unmarshal"), 0},
	}
	for _, tc := range cases {
		if got := llmCooldownDuration(tc.err); got != tc.want {
			t.Errorf("%s: got %s, want %s", tc.name, got, tc.want)
		}
	}
}

func TestParseLLMRetryAfterText(t *testing.T) {
	cases := map[string]time.Duration{
		accountsLimitedErrText:                                   311000 * time.Second,
		"rate limited, retry after 30":                           30 * time.Second,
		"Retry-After: 120":                                       120 * time.Second,
		"Please retry in 34.5s.":                                 34500 * time.Millisecond,
		"Rate limit reached. Please try again in 1m30s.":         90 * time.Second,
		"Please try again in 250ms.":                             250 * time.Millisecond,
		`details: [{"@type": "RetryInfo", "retryDelay": "20s"}]`: 20 * time.Second,
		"503 Service Unavailable":                                0,
		"waiting for upstream":                                   0,
	}
	for text, want := range cases {
		if got := parseLLMRetryAfterText(text); got != want {
			t.Errorf("%q: got %s, want %s", text, got, want)
		}
	}
}

// 切换日志和运行日志要显示配置档名字；同一个配置档下换模型时带上模型。
func TestLLMFailoverLabels(t *testing.T) {
	antigravity := llm.Profile{ID: "b888a613-1234-5678-9abc-def012345678", Name: "antigravity", Config: llm.ProviderConfig{Model: "gemini-3.8-flash-low"}}
	backup := llm.Profile{ID: "c999", Name: "sub2api", Config: llm.ProviderConfig{Model: "gpt-6-sol"}}
	from, to := llmFailoverLabels(antigravity, backup)
	if from != "antigravity(b888a613)" || to != "sub2api(c999)" {
		t.Fatalf("from=%q to=%q", from, to)
	}
	sol := llm.Profile{ID: "p1", Name: "sub2api", Config: llm.ProviderConfig{Model: "gpt-6-sol"}}
	terra := llm.Profile{ID: "p1", Name: "sub2api", Config: llm.ProviderConfig{Model: "gpt-5.6-terra"}}
	from, to = llmFailoverLabels(sol, terra)
	if from != "sub2api(p1)/gpt-6-sol" || to != "sub2api(p1)/gpt-5.6-terra" {
		t.Fatalf("同档换模型 from=%q to=%q", from, to)
	}
	unnamed := llm.Profile{ID: "b888a613-1234"}
	if label := llmCandidateLabel(unnamed, false); label != "b888a613-1234" {
		t.Fatalf("没有名字时应当退回完整 ID：%q", label)
	}
}

func TestReportLLMFailoverShowsProfileNames(t *testing.T) {
	logs := &captureAppLogs{}
	runtime := &Runtime{}
	runtime.SetAppLogWriter(logs)

	sol := llm.Profile{ID: "p1", Name: "sub2api", Config: llm.ProviderConfig{Model: "gpt-6-sol"}}
	terra := llm.Profile{ID: "p1", Name: "sub2api", Config: llm.ProviderConfig{Model: "gpt-5.6-terra"}}
	unnamed := llm.Profile{ID: "b888a613", Config: llm.ProviderConfig{Model: "gemini-3.8-flash-low"}}
	runtime.reportLLMEvent(newLLMFailoverEvent("chat", unnamed, sol, false, errors.New(accountsLimitedErrText), llmFailureMark{}))
	runtime.reportLLMEvent(newLLMFailoverEvent("chat", sol, terra, true, errors.New("503"), llmFailureMark{}))

	entries := logs.entriesSnapshot()
	if len(entries) != 2 {
		t.Fatalf("entries = %#v", entries)
	}
	if got := entries[0].Message; got != "模型配置「b888a613」调用失败，已切到「sub2api」" {
		t.Fatalf("message = %q", got)
	}
	if got := entries[1].Message; got != "模型配置「sub2api / gpt-6-sol」调用失败，已切到「sub2api / gpt-5.6-terra」" {
		t.Fatalf("同档换模型 message = %q", got)
	}
	if entries[1].Metadata["from"] != "p1" || entries[1].Metadata["to"] != "p1" || entries[1].Metadata["to_model"] != "gpt-5.6-terra" {
		t.Fatalf("metadata = %#v", entries[1].Metadata)
	}
}

func TestLLMCooldownSnapshotUsesOneClockAndExpiresFailures(t *testing.T) {
	now := time.Unix(100, 0)
	calls := 0
	table := &llmCooldownTable{now: func() time.Time { calls++; return now }}
	profiles := []llm.Profile{{ID: "a"}, {ID: "b"}}
	for _, profile := range profiles {
		table.markFailureWithPolicy(profile, errors.New("HTTP 500"), immediateCooldownPolicy())
	}
	calls = 0
	health := table.snapshot(profiles)
	if calls != 1 || health[0] != health[1] || health[0].failures != 1 || health[0].remaining != llmCooldownTransient {
		t.Fatalf("calls=%d health=%+v", calls, health)
	}
	now = now.Add(24 * time.Hour)
	for _, state := range table.snapshot(profiles) {
		if state != (llmCandidateHealth{}) {
			t.Fatalf("expired health=%+v", state)
		}
	}
	if len(table.entries) != 2 {
		t.Fatal("expired entries must not need physical cleanup")
	}
	if got := (*llmCooldownTable)(nil).snapshot(profiles); len(got) != 2 || got[0] != (llmCandidateHealth{}) {
		t.Fatalf("nil snapshot=%+v", got)
	}
}

func TestLLMCooldownConcurrentSnapshots(t *testing.T) {
	profiles := []llm.Profile{{ID: "a"}, {ID: "b"}}
	table := &llmCooldownTable{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 200 {
			for _, profile := range profiles {
				table.markFailure(profile, errors.New("HTTP 500"))
				table.clear(profile)
			}
		}
	}()
	for range 200 {
		health := table.snapshot(profiles)
		for _, state := range health {
			if state.failures < 0 || state.remaining < 0 || (state.failures == 0 && state.remaining > 0) {
				t.Fatalf("inconsistent health=%+v", health)
			}
		}
		if got := table.order("chat", len(profiles), 0, func(i int) llm.Profile { return profiles[i] }); len(got) == 0 {
			t.Fatal("no probe selected")
		}
	}
	<-done
}

func TestLLMCooldownBackoffRetentionAndRecovery(t *testing.T) {
	now := time.Unix(100, 0)
	table := &llmCooldownTable{now: func() time.Time { return now }}
	profile := llm.Profile{ID: "a", Config: llm.ProviderConfig{Model: "m"}}
	policy := immediateCooldownPolicy()
	for n := 0; n < 12; n++ {
		duration := table.markFailureWithPolicy(profile, errors.New("HTTP 500"), policy).cooldown
		want := min(llmCooldownTransient*time.Duration(1<<n), llmCooldownMax)
		if duration != want {
			t.Fatalf("n=%d duration=%s want=%s", n, duration, want)
		}
		now = now.Add(duration + time.Second)
	}
	duration := table.markFailureWithPolicy(profile, errors.New("HTTP 429 retry after 3s"), policy).cooldown
	if duration != 3*time.Second {
		t.Fatal(duration)
	}
	table.clear(profile)
	duration = table.markFailureWithPolicy(profile, errors.New("HTTP 500"), policy).cooldown
	if duration != llmCooldownTransient {
		t.Fatal(duration)
	}
	now = now.Add(25 * time.Hour)
	duration = table.markFailureWithPolicy(profile, errors.New("HTTP 500"), policy).cooldown
	if duration != llmCooldownTransient {
		t.Fatal(duration)
	}
}

func TestLLMCooldownFiltersAndProbesOnlyEarliest(t *testing.T) {
	now := time.Unix(100, 0)
	table := &llmCooldownTable{now: func() time.Time { return now }}
	profiles := []llm.Profile{{ID: "a", Config: llm.ProviderConfig{Model: "m"}}, {ID: "b", Config: llm.ProviderConfig{Model: "m"}}, {ID: "a", Config: llm.ProviderConfig{Model: "other"}}}
	at := func(i int) llm.Profile { return profiles[i] }
	table.markFailure(profiles[0], errors.New("HTTP 429 retry after 60s"))
	table.markFailure(profiles[1], errors.New("HTTP 429 retry after 20s"))
	order := table.order("chat", 3, 0, at)
	if len(order) != 1 || order[0] != 2 {
		t.Fatal(order)
	}
	table.markFailure(profiles[2], errors.New("HTTP 429 retry after 40s"))
	order = table.order("chat", 3, 0, at)
	if len(order) != 1 || order[0] != 1 {
		t.Fatal(order)
	}
}

func TestLastStreamCandidateStillRecordsHealth(t *testing.T) {
	table := &llmCooldownTable{}
	adapter := &cooldownRegistryAdapter{streamEvents: midStreamLimitedEvents()}
	provider := cooldownProviderFactory(t, table, adapter)()
	if _, err := (&streamingLLMProvider{provider: provider}).Generate(context.Background(), cooldownRequest()); err == nil {
		t.Fatal("expected error")
	}
	if table.remaining(llm.Profile{ID: "primary", Config: llm.ProviderConfig{Model: "primary-model"}}) <= 0 {
		t.Fatal("last candidate failure was not recorded")
	}
	if adapter.streamCalls != 1 || adapter.generateCalls != 0 {
		t.Fatalf("adapter=%+v", adapter)
	}
}

// 只使用显式测试环境变量，不读取配置文件；两轮最多各一次真实生成。
func TestLiveFallbackRecoversAndSkipsCoolingPrimary(t *testing.T) {
	if os.Getenv("DIANA_LIVE_LLM") != "1" || strings.TrimSpace(os.Getenv("DIANA_TEST_LLM_API_KEY")) == "" || strings.TrimSpace(os.Getenv("DIANA_TEST_LLM_MODEL")) == "" {
		t.Skip("requires DIANA_LIVE_LLM=1, DIANA_TEST_LLM_API_KEY and an explicit current DIANA_TEST_LLM_MODEL")
	}
	// liveLLMClient 支持文件配置；本测试刻意只走已提供的环境变量。
	t.Setenv("DIANA_TEST_LLM_CONFIG", "")
	client := &liveFallbackBudgetProvider{provider: liveLLMClient(t)}
	primary := &countingFailLLMProvider{err: errors.New("HTTP 429: injected local rate limit")}
	table := &llmCooldownTable{}
	profiles := []llm.Profile{{ID: "injected-primary", Config: llm.ProviderConfig{Model: "injected"}}, {ID: "live-backup", Config: llm.ProviderConfig{Model: os.Getenv("DIANA_TEST_LLM_MODEL")}}}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	for range 2 {
		// 每轮重新构建路由，确保第二轮跳过主档来自共享冷却，而非 current 游标。
		provider, err := newProfileFailoverLLMProvider(profiles, func(config llm.ProviderConfig) (LLMProvider, error) {
			if config.Model == "injected" {
				return primary, nil
			}
			return client, nil
		}, false, nil, false)
		if err != nil {
			t.Fatal(err)
		}
		provider.cooldowns = table
		response, err := provider.Generate(ctx, llm.GenerateRequest{Messages: []llm.Message{{Role: llm.RoleUser, Content: "Reply with exactly OK."}}, MaxOutputTokens: 64})
		if err != nil {
			t.Fatal("live backup failed (upstream error omitted to avoid exposing endpoint details)")
		}
		if response == nil || strings.TrimSpace(response.Text) == "" {
			t.Fatal("live backup returned no text")
		}
	}
	if primary.calls != 1 || table.remaining(profiles[0]) <= 0 || table.remaining(profiles[1]) != 0 {
		t.Fatalf("unexpected health state: primary calls=%d", primary.calls)
	}
}

// 上下文收缩等独立重试也不能突破真实调用预算。
type liveFallbackBudgetProvider struct {
	provider LLMProvider
	calls    int
}

func (p *liveFallbackBudgetProvider) Generate(ctx context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	if p.calls >= 2 {
		return nil, errors.New("live fallback request budget exhausted")
	}
	p.calls++
	return p.provider.Generate(ctx, req)
}

func immediateCooldownPolicy() llmRecoveryPolicy {
	policy := defaultLLMRecoveryPolicy()
	policy.failuresBeforeCooldown = 1
	return policy
}

// 限流、5xx 要连续失败到阈值才冷却；鉴权失败、带等待时间的限流当场冷却；
// 隔太久的失败不算连续。
func TestLLMCooldownWaitsForFailureStreak(t *testing.T) {
	now := time.Unix(1000, 0)
	table := &llmCooldownTable{now: func() time.Time { return now }}
	profile := llm.Profile{ID: "a", Config: llm.ProviderConfig{Model: "m"}}
	limited := errors.New("429 Too Many Requests")
	for n := 1; n < llmFailuresBeforeCooldownDefault; n++ {
		mark := table.markFailureWithPolicy(profile, limited, defaultLLMRecoveryPolicy())
		if mark.cooldown != 0 || !mark.counted || mark.failures != n || table.remaining(profile) != 0 {
			t.Fatalf("第 %d 次失败不该冷却：%+v", n, mark)
		}
		now = now.Add(time.Second)
	}
	mark := table.markFailureWithPolicy(profile, limited, defaultLLMRecoveryPolicy())
	if mark.cooldown != llmCooldownDefault || !mark.fresh || !mark.until.Equal(now.Add(llmCooldownDefault)) {
		t.Fatalf("攒够连续失败应当冷却：%+v", mark)
	}

	table.clear(profile)
	table.markFailureWithPolicy(profile, limited, defaultLLMRecoveryPolicy())
	now = now.Add(llmFailureStreakWindow + time.Second)
	if mark := table.markFailureWithPolicy(profile, limited, defaultLLMRecoveryPolicy()); mark.failures != 1 {
		t.Fatalf("隔太久的失败应当重新数：%+v", mark)
	}

	for _, text := range []string{"401 Unauthorized: invalid api key", "429 Too Many Requests: retry after 30s"} {
		other := llm.Profile{ID: text, Config: llm.ProviderConfig{Model: "m"}}
		if mark := table.markFailureWithPolicy(other, errors.New(text), defaultLLMRecoveryPolicy()); mark.cooldown <= 0 {
			t.Fatalf("%q 应当当场冷却：%+v", text, mark)
		}
	}
}

// 没攒够连续失败时，下一次请求仍先用主档；切换日志写明这是第几次。
func TestLLMFailoverKeepsPrimaryUntilStreak(t *testing.T) {
	table := &llmCooldownTable{}
	limited := errors.New("503 Service Unavailable")
	primary := &cooldownRegistryAdapter{generateErrs: []error{limited}}
	backup := &cooldownRegistryAdapter{response: "备用回复"}
	newProvider := cooldownProviderFactory(t, table, primary, backup)
	var events []llmEvent
	for call := 1; call <= 4; call++ {
		provider := newProvider()
		provider.recoveryPolicy.retryDelay = 0
		provider.report = func(event llmEvent) { events = append(events, event) }
		if resp, err := provider.Generate(context.Background(), cooldownRequest()); err != nil || resp.Text != "备用回复" {
			t.Fatalf("第 %d 次调用 resp=%#v err=%v", call, resp, err)
		}
	}
	// 前三次请求各打主档三次（含两次原地重试），第三次攒够后冷却，第四次直接走备用。
	if primary.generateCalls != 9 || backup.generateCalls != 4 {
		t.Fatalf("primary=%d backup=%d", primary.generateCalls, backup.generateCalls)
	}
	if len(events) != 3 || events[0].Mark.failures != 1 || events[0].Mark.cooldown != 0 || events[2].Mark.cooldown <= 0 {
		t.Fatalf("events = %+v", events)
	}
	location := time.FixedZone("CST", 8*3600)
	if got := llmFailoverMessage(events[0], location); !strings.Contains(got, "连续第 1/3 次") || !strings.Contains(got, "下次仍先用它") {
		t.Fatalf("message = %q", got)
	}
	want := "冷却到 " + events[2].Mark.until.In(location).Format("15:04:05") + " 再试"
	if got := llmFailoverMessage(events[2], location); !strings.Contains(got, want) {
		t.Fatalf("message = %q, want %q", got, want)
	}
}

// 每个候选各自计时：主档卡住用满自己那份时间后，备用档拿到完整的一份重新计时。
func TestLLMCandidateBudgetRestartsOnFailover(t *testing.T) {
	table := &llmCooldownTable{}
	primary := &cooldownRegistryAdapter{block: true}
	backup := &cooldownRegistryAdapter{response: "备用回复", delay: 60 * time.Millisecond}
	provider := cooldownProviderFactory(t, table, primary, backup)()
	ctx, cancel := withLLMCandidateBudget(context.Background(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	resp, err := provider.Generate(ctx, cooldownRequest())
	if err != nil || resp.Text != "备用回复" {
		t.Fatalf("resp=%#v err=%v", resp, err)
	}
	// 共用一个期限的话备用档只剩 0ms；分开计时总耗时约 100ms + 60ms。
	if elapsed := time.Since(started); elapsed < 150*time.Millisecond {
		t.Fatalf("elapsed = %s", elapsed)
	}
	if health := table.snapshot([]llm.Profile{{ID: "primary", Config: llm.ProviderConfig{Model: "primary-model"}}}); health[0].failures != 1 {
		t.Fatalf("候选超时应当计入连续失败：%+v", health)
	}

	// 只有一个候选时总时长仍是一份预算，不会被放宽成三份。
	single := cooldownProviderFactory(t, &llmCooldownTable{}, &cooldownRegistryAdapter{block: true})()
	ctx, cancel = withLLMCandidateBudget(context.Background(), 50*time.Millisecond)
	defer cancel()
	started = time.Now()
	_, err = single.Generate(ctx, cooldownRequest())
	if !errors.Is(err, errLLMCandidateTimeout) || time.Since(started) > 500*time.Millisecond {
		t.Fatalf("err=%v elapsed=%s", err, time.Since(started))
	}
}

// 发送前审核：总期限不变，每个候选只拿一半。主档卡住时备用档还来得及答，而不是
// 主档吃满整段、审核超时放行。
func TestReplyAuditBudgetLeavesTimeForBackup(t *testing.T) {
	primary := &cooldownRegistryAdapter{block: true}
	backup := &cooldownRegistryAdapter{response: "备用回复", delay: 20 * time.Millisecond}
	provider := cooldownProviderFactory(t, &llmCooldownTable{}, primary, backup)()
	total := 200 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), total)
	defer cancel()
	ctx, cancelBudget := withLLMCandidateBudget(ctx, replyAuditCandidateBudget(total))
	defer cancelBudget()
	resp, err := provider.Generate(ctx, cooldownRequest())
	if err != nil || resp.Text != "备用回复" {
		t.Fatalf("resp=%#v err=%v", resp, err)
	}
}
