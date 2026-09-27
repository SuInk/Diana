// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"errors"
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
}

func (a *cooldownRegistryAdapter) Generate(context.Context, llm.ModelDefinition, llm.ChatRequest) (llm.ChatResponse, error) {
	a.generateCalls++
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
		generateErrs: []error{errors.New("503 Service Unavailable"), errors.New("503 Service Unavailable"), nil},
		response:     "恢复了",
	}
	newProvider := cooldownProviderFactory(t, table, only)

	if _, err := newProvider().Generate(context.Background(), cooldownRequest()); err == nil {
		t.Fatal("两次 503 之后第一次调用应当失败")
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
	table.markFailure(primaryProfile, errors.New("429 Too Many Requests"))
	table.markFailure(backupProfile, errors.New("429 Too Many Requests"))
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

// 额度、限流类错误不走 700ms 的原地重试，单候选时也一样；普通 503 仍重试一次。
func TestRateLimitedErrorsSkipSameProfileRetry(t *testing.T) {
	cases := []struct {
		err      error
		attempts int
	}{
		{errors.New(accountsLimitedErrText), 1},
		{errors.New("503 Service Unavailable: RESOURCE_EXHAUSTED"), 1},
		{errors.New("429 Too Many Requests"), 1},
		{errors.New("503 Service Unavailable"), 2},
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
	runtime.reportLLMEvent(newLLMFailoverEvent("chat", unnamed, sol, false, errors.New(accountsLimitedErrText)))
	runtime.reportLLMEvent(newLLMFailoverEvent("chat", sol, terra, true, errors.New("503")))

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
