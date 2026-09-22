// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/SuInk/diana/model/applog"
)

type stubGroupUsageLog struct {
	applog.Writer
	tokens    int64
	usedCalls int64
	err       error
	calls     int
	lastWin   time.Duration
}

func (s *stubGroupUsageLog) AppendLog(context.Context, applog.Entry) error { return nil }

func (s *stubGroupUsageLog) GroupLLMUsageSince(_ context.Context, _, _ string, since, until time.Time) (applog.GroupUsage, error) {
	s.calls++
	s.lastWin = until.Sub(since)
	return applog.GroupUsage{Tokens: s.tokens, Calls: s.usedCalls}, s.err
}

func quotaRuntime(t *testing.T, usage *stubGroupUsageLog, quota int64) (*Runtime, MessageEvent) {
	return quotaRuntimeWith(t, usage, GroupConfig{GroupID: "20001", BotProfileID: "qq", Enabled: true, ModelTokenQuota: quota})
}

func quotaRuntimeWith(t *testing.T, usage *stubGroupUsageLog, cfg GroupConfig) (*Runtime, MessageEvent) {
	t.Helper()
	runtime := NewRuntime(BotConfig{ID: "qq", OwnerID: "10001"}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.SetAppLogWriter(usage)
	runtime.SetGroupConfigStore(&stubGroupConfigStore{configs: map[string]GroupConfig{cfg.GroupID: cfg}})
	return runtime, MessageEvent{Kind: EventKindGroup, ProfileID: "qq", GroupID: cfg.GroupID, UserID: "20002"}
}

// 额度按滚动 5 小时算，和按 token 计费的套餐窗口一致。
func TestGroupQuotaUsesFiveHourWindow(t *testing.T) {
	usage := &stubGroupUsageLog{tokens: 10}
	runtime, event := quotaRuntime(t, usage, 1000)
	if runtime.groupModelQuotaExceeded(context.Background(), event).Exceeded {
		t.Fatal("没超额不该拦")
	}
	if usage.lastWin != 5*time.Hour {
		t.Fatalf("窗口 = %s，应当是 5 小时", usage.lastWin)
	}
}

// 用满就拦。
func TestGroupQuotaBlocksWhenExhausted(t *testing.T) {
	usage := &stubGroupUsageLog{tokens: 1200}
	runtime, event := quotaRuntime(t, usage, 1000)
	verdict := runtime.groupModelQuotaExceeded(context.Background(), event)
	if !verdict.Exceeded || verdict.Usage.Tokens != 1200 || verdict.Tokens != 1000 {
		t.Fatalf("verdict = %#v", verdict)
	}
}

// 主人不受限：额度用完之后改配置、查用量还得靠主人，锁死自己没有道理。
func TestGroupQuotaExemptsOwner(t *testing.T) {
	usage := &stubGroupUsageLog{tokens: 99999}
	runtime, event := quotaRuntime(t, usage, 1000)
	event.UserID = "10001"
	if runtime.groupModelQuotaExceeded(context.Background(), event).Exceeded {
		t.Fatal("主人不该被额度拦住")
	}
}

// 读不到用量时放行：额度是省钱用的，不该因为日志存储不可用就让整个群哑掉。
func TestGroupQuotaFailsOpen(t *testing.T) {
	usage := &stubGroupUsageLog{err: context.DeadlineExceeded}
	runtime, event := quotaRuntime(t, usage, 1)
	if runtime.groupModelQuotaExceeded(context.Background(), event).Exceeded {
		t.Fatal("读不到用量时该放行")
	}
}

// 没配额度的群一次用量都不用查。
func TestGroupQuotaSkipsWhenUnset(t *testing.T) {
	usage := &stubGroupUsageLog{tokens: 99999}
	runtime, event := quotaRuntime(t, usage, 0)
	if runtime.groupModelQuotaExceeded(context.Background(), event).Exceeded {
		t.Fatal("没配额度不该拦")
	}
	if usage.calls != 0 {
		t.Fatalf("没配额度不该去查用量，实际查了 %d 次", usage.calls)
	}
}

// 每条消息都扫一遍日志太贵：窗口内的读数缓存一小会儿。
func TestGroupQuotaCachesReading(t *testing.T) {
	usage := &stubGroupUsageLog{tokens: 10}
	runtime, event := quotaRuntime(t, usage, 1000)
	for range 5 {
		runtime.groupModelQuotaExceeded(context.Background(), event)
	}
	if usage.calls != 1 {
		t.Fatalf("用量查询次数 = %d，应当只查一次", usage.calls)
	}
}

// 群配置归一化不能把额度抹掉：它没有机器人级的对应项，纯属群自己的设置。
func TestGroupQuotaSurvivesNormalization(t *testing.T) {
	cfg := GroupConfig{GroupID: "20001", BotProfileID: "qq", ModelTokenQuota: 500000}
	normalized := cfg.WithDefaults("20001", BotConfig{ID: "qq"})
	if normalized.ModelTokenQuota != 500000 {
		t.Fatalf("归一化之后额度 = %d", normalized.ModelTokenQuota)
	}
}

// 次数上限独立于 token：句句短但刷个不停的群，token 还没用完就该被次数拦住。
func TestGroupQuotaBlocksOnCallCount(t *testing.T) {
	usage := &stubGroupUsageLog{tokens: 10, usedCalls: 300}
	runtime, event := quotaRuntimeWith(t, usage, GroupConfig{
		GroupID: "20001", BotProfileID: "qq", Enabled: true,
		ModelTokenQuota: 1000000, ModelCallQuota: 200,
	})
	verdict := runtime.groupModelQuotaExceeded(context.Background(), event)
	if !verdict.Exceeded {
		t.Fatalf("次数用满该拦：%#v", verdict)
	}
	if verdict.Reason == "" || verdict.Usage.Calls != 300 {
		t.Fatalf("理由要写清是哪一档先到：%#v", verdict)
	}
}

// 只配次数、不配 token 时照样生效。
func TestGroupQuotaCallOnly(t *testing.T) {
	usage := &stubGroupUsageLog{tokens: 999999, usedCalls: 5}
	runtime, event := quotaRuntimeWith(t, usage, GroupConfig{
		GroupID: "20001", BotProfileID: "qq", Enabled: true, ModelCallQuota: 10,
	})
	if runtime.groupModelQuotaExceeded(context.Background(), event).Exceeded {
		t.Fatal("次数没用满、又没配 token 上限，不该拦")
	}
	usage2 := &stubGroupUsageLog{usedCalls: 10}
	runtime2, event2 := quotaRuntimeWith(t, usage2, GroupConfig{
		GroupID: "20001", BotProfileID: "qq", Enabled: true, ModelCallQuota: 10,
	})
	if !runtime2.groupModelQuotaExceeded(context.Background(), event2).Exceeded {
		t.Fatal("次数正好用满该拦")
	}
}

// 两档都配时先到先得：token 先满就报 token，次数先满就报次数。
func TestGroupQuotaReportsWhicheverHitsFirst(t *testing.T) {
	usage := &stubGroupUsageLog{tokens: 5000, usedCalls: 3}
	runtime, event := quotaRuntimeWith(t, usage, GroupConfig{
		GroupID: "20001", BotProfileID: "qq", Enabled: true,
		ModelTokenQuota: 1000, ModelCallQuota: 100,
	})
	verdict := runtime.groupModelQuotaExceeded(context.Background(), event)
	if !verdict.Exceeded || !strings.Contains(verdict.Reason, "token") {
		t.Fatalf("token 先到该报 token：%#v", verdict)
	}
}

// 群里留空就跟随机器人那一档，和这张表单里其它「留空跟随机器人」的设置一个规矩。
func TestGroupQuotaFallsBackToBotConfig(t *testing.T) {
	usage := &stubGroupUsageLog{tokens: 1500}
	runtime := NewRuntime(BotConfig{ID: "qq", OwnerID: "10001", ModelTokenQuota: 1000}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.SetAppLogWriter(usage)
	runtime.SetGroupConfigStore(&stubGroupConfigStore{configs: map[string]GroupConfig{
		"20001": {GroupID: "20001", BotProfileID: "qq", Enabled: true},
	}})
	event := MessageEvent{Kind: EventKindGroup, ProfileID: "qq", GroupID: "20001", UserID: "20002"}
	verdict := runtime.groupModelQuotaExceeded(context.Background(), event)
	if !verdict.Exceeded || verdict.Tokens != 1000 {
		t.Fatalf("群里没填该跟随机器人那一档：%#v", verdict)
	}
}

// 群里填了以群为准，哪怕比机器人那档宽。
func TestGroupQuotaOverridesBotConfig(t *testing.T) {
	usage := &stubGroupUsageLog{tokens: 1500}
	runtime := NewRuntime(BotConfig{ID: "qq", OwnerID: "10001", ModelTokenQuota: 1000}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.SetAppLogWriter(usage)
	runtime.SetGroupConfigStore(&stubGroupConfigStore{configs: map[string]GroupConfig{
		"20001": {GroupID: "20001", BotProfileID: "qq", Enabled: true, ModelTokenQuota: 100000},
	}})
	event := MessageEvent{Kind: EventKindGroup, ProfileID: "qq", GroupID: "20001", UserID: "20002"}
	if verdict := runtime.groupModelQuotaExceeded(context.Background(), event); verdict.Exceeded {
		t.Fatalf("群里放宽了就该按群的来：%#v", verdict)
	}
}

// 两边都没填 = 不限，一次用量都不查。
func TestGroupQuotaUnsetEverywhereSkipsLookup(t *testing.T) {
	usage := &stubGroupUsageLog{tokens: 99999999}
	runtime := NewRuntime(BotConfig{ID: "qq", OwnerID: "10001"}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.SetAppLogWriter(usage)
	runtime.SetGroupConfigStore(&stubGroupConfigStore{configs: map[string]GroupConfig{
		"20001": {GroupID: "20001", BotProfileID: "qq", Enabled: true},
	}})
	event := MessageEvent{Kind: EventKindGroup, ProfileID: "qq", GroupID: "20001", UserID: "20002"}
	if runtime.groupModelQuotaExceeded(context.Background(), event).Exceeded {
		t.Fatal("两边都没填不该拦")
	}
	if usage.calls != 0 {
		t.Fatalf("两边都没填不该去查用量，实际查了 %d 次", usage.calls)
	}
}
