// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"strings"
	"testing"
	"time"

	"github.com/SuInk/diana/model/agent"
)

func TestClaimSourcesSurviveIntoTheNextTurn(t *testing.T) {
	runtime := &Runtime{plugins: NewDefaultPluginManager(), recentClaimSources: map[string][]claimSourceRecord{}}
	event := MessageEvent{GroupID: "42"}
	runtime.rememberClaimSources(event, []agent.ClaimTrace{{
		ID: "commit", Statement: "最新提交做了什么", Summary: "提交页写明改了环境代理",
		Evidence: []agent.ClaimEvidence{{URL: "https://official.example/commit/abc123"}},
	}})
	context := runtime.claimSourceContext(event)
	if !strings.Contains(context, "https://official.example/commit/abc123") {
		t.Fatalf("下一轮拿不到来源链接：%q", context)
	}
	if !strings.Contains(context, "提交页写明改了环境代理") {
		t.Fatalf("来源缺少可对应的结论：%q", context)
	}
	if !strings.Contains(context, "有人索取链接时") {
		t.Fatalf("缺少索取链接才给出的约束：%q", context)
	}
	if other := runtime.claimSourceContext(MessageEvent{GroupID: "43"}); other != "" {
		t.Fatalf("来源不应跨会话可见：%q", other)
	}
}

func TestClaimSourcesIgnoreClaimsWithoutEvidence(t *testing.T) {
	runtime := &Runtime{plugins: NewDefaultPluginManager(), recentClaimSources: map[string][]claimSourceRecord{}}
	event := MessageEvent{UserID: "u1"}
	runtime.rememberClaimSources(event, []agent.ClaimTrace{{
		ID: "state", Statement: "状态是否成立", Summary: "没有找到来源",
	}})
	if context := runtime.claimSourceContext(event); context != "" {
		t.Fatalf("没有证据时不该注入来源块：%q", context)
	}
}

func TestClaimSourcesSwitchStopsRecordingAndClearsWhatWasKept(t *testing.T) {
	plugins := NewDefaultPluginManager()
	runtime := &Runtime{plugins: plugins, recentClaimSources: map[string][]claimSourceRecord{}}
	event := MessageEvent{GroupID: "88"}
	claims := []agent.ClaimTrace{{
		ID: "commit", Summary: "提交页写明改了环境代理",
		Evidence: []agent.ClaimEvidence{{URL: "https://official.example/commit/abc123"}},
	}}
	runtime.rememberClaimSources(event, claims)
	if runtime.claimSourceContext(event) == "" {
		t.Fatal("默认应保留来源以便追问")
	}
	if _, err := plugins.UpdateSettings(webSearchPluginID, map[string]any{webSearchSettingSourceRecall: false}); err != nil {
		t.Fatal(err)
	}
	if context := runtime.claimSourceContext(event); context != "" {
		t.Fatalf("关掉开关后不该再注入来源：%q", context)
	}
	runtime.mu.RLock()
	kept := len(runtime.recentClaimSources[sessionKey(event)])
	runtime.mu.RUnlock()
	if kept != 0 {
		t.Fatalf("关掉开关后已存来源应被清掉，仍剩 %d 条", kept)
	}
	runtime.rememberClaimSources(event, claims)
	if context := runtime.claimSourceContext(event); context != "" {
		t.Fatalf("关掉开关后不该继续记录：%q", context)
	}
}

func TestReplyLinkPolicyStaysSilentOnDefaultAndOverridesOtherwise(t *testing.T) {
	plugins := NewDefaultPluginManager()
	runtime := &Runtime{plugins: plugins, recentClaimSources: map[string][]claimSourceRecord{}}
	event := MessageEvent{GroupID: "99"}
	if rule := runtime.replyLinkPolicyContext(event); rule != "" {
		t.Fatalf("默认档不该注入链接规则，避免和人设打架：%q", rule)
	}
	if _, err := plugins.UpdateSettings(webSearchPluginID, map[string]any{webSearchSettingLinkPolicy: replyLinkPolicyAlways}); err != nil {
		t.Fatal(err)
	}
	if rule := runtime.replyLinkPolicyContext(event); !strings.Contains(rule, "最多一条") {
		t.Fatalf("附带链接档应给出明确规则：%q", rule)
	}
	if _, err := plugins.UpdateSettings(webSearchPluginID, map[string]any{webSearchSettingLinkPolicy: replyLinkPolicyNever}); err != nil {
		t.Fatal(err)
	}
	if rule := runtime.replyLinkPolicyContext(event); !strings.Contains(rule, "不要在回复里给出 URL") {
		t.Fatalf("禁链档应给出明确规则：%q", rule)
	}
}

func TestReplyLinkPolicyNeverKeepsSourcesOutOfContextButNotDiscarded(t *testing.T) {
	plugins := NewDefaultPluginManager()
	runtime := &Runtime{plugins: plugins, recentClaimSources: map[string][]claimSourceRecord{}}
	event := MessageEvent{GroupID: "100"}
	runtime.rememberClaimSources(event, []agent.ClaimTrace{{
		ID: "commit", Summary: "提交页写明改了环境代理",
		Evidence: []agent.ClaimEvidence{{URL: "https://official.example/commit/abc123"}},
	}})
	if _, err := plugins.UpdateSettings(webSearchPluginID, map[string]any{webSearchSettingLinkPolicy: replyLinkPolicyNever}); err != nil {
		t.Fatal(err)
	}
	if context := runtime.claimSourceContext(event); context != "" {
		t.Fatalf("禁链时不该把 URL 送进上下文：%q", context)
	}
	if _, err := plugins.UpdateSettings(webSearchPluginID, map[string]any{webSearchSettingLinkPolicy: replyLinkPolicyOnRequest}); err != nil {
		t.Fatal(err)
	}
	if context := runtime.claimSourceContext(event); !strings.Contains(context, "https://official.example/commit/abc123") {
		t.Fatalf("改回默认档后来源应立刻可用：%q", context)
	}
}

func TestClaimSourcesDedupeExpireAndCap(t *testing.T) {
	now := time.Now()
	records := []claimSourceRecord{
		{URL: "https://a.example/1", SavedAt: now},
		{URL: "https://a.example/1", SavedAt: now},
		{URL: "https://stale.example/old", SavedAt: now.Add(-recentClaimSourceTTL - time.Minute)},
	}
	for index := 0; index < recentClaimSourceLimit+3; index++ {
		records = append(records, claimSourceRecord{URL: "https://b.example/" + string(rune('a'+index)), SavedAt: now})
	}
	out := dedupeClaimSources(records, now)
	if len(out) != recentClaimSourceLimit {
		t.Fatalf("条目数=%d，want %d", len(out), recentClaimSourceLimit)
	}
	seen := map[string]bool{}
	for _, record := range out {
		if seen[record.URL] {
			t.Fatalf("出现重复来源：%#v", out)
		}
		seen[record.URL] = true
		if strings.Contains(record.URL, "stale.example") {
			t.Fatalf("过期来源没有被丢弃：%#v", out)
		}
	}
}

func TestClaimSourcesKeepNewestAcrossRuns(t *testing.T) {
	runtime := &Runtime{plugins: NewDefaultPluginManager(), recentClaimSources: map[string][]claimSourceRecord{}}
	event := MessageEvent{GroupID: "7"}
	runtime.rememberClaimSources(event, []agent.ClaimTrace{{
		ID: "first", Summary: "第一轮结论",
		Evidence: []agent.ClaimEvidence{{URL: "https://first.example/page"}},
	}})
	runtime.rememberClaimSources(event, []agent.ClaimTrace{{
		ID: "second", Summary: "第二轮结论",
		Evidence: []agent.ClaimEvidence{{URL: "https://second.example/page"}},
	}})
	context := runtime.claimSourceContext(event)
	first := strings.Index(context, "https://first.example/page")
	second := strings.Index(context, "https://second.example/page")
	if first < 0 || second < 0 {
		t.Fatalf("两轮来源都应保留：%q", context)
	}
	if second > first {
		t.Fatalf("最近一轮的来源应排在前面：%q", context)
	}
}
