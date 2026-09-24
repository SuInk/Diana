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
	usedCalls int64
	err       error
	calls     int
	lastWin   time.Duration
}

func (s *stubGroupUsageLog) AppendLog(context.Context, applog.Entry) error { return nil }

func (s *stubGroupUsageLog) GroupLLMUsageSince(_ context.Context, _, _ string, since, until time.Time) (applog.GroupUsage, error) {
	s.calls++
	s.lastWin = until.Sub(since)
	return applog.GroupUsage{Calls: s.usedCalls}, s.err
}

func quotaRuntime(t *testing.T, usage *stubGroupUsageLog, quota int64) (*Runtime, MessageEvent) {
	return quotaRuntimeWith(t, usage, BotConfig{ID: "qq", OwnerID: "10001"}, GroupConfig{GroupID: "20001", BotProfileID: "qq", Enabled: true, ModelCallQuota: quota})
}

func quotaRuntimeWith(t *testing.T, usage *stubGroupUsageLog, bot BotConfig, cfg GroupConfig) (*Runtime, MessageEvent) {
	t.Helper()
	runtime := NewRuntime(bot, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.SetAppLogWriter(usage)
	runtime.SetGroupConfigStore(&stubGroupConfigStore{configs: map[string]GroupConfig{cfg.GroupID: cfg}})
	return runtime, MessageEvent{Kind: EventKindGroup, ProfileID: "qq", GroupID: cfg.GroupID, UserID: "20002"}
}

// 额度按滚动 5 小时算，和上游订阅套餐的窗口一致。
func TestGroupQuotaUsesFiveHourWindow(t *testing.T) {
	usage := &stubGroupUsageLog{usedCalls: 10}
	runtime, event := quotaRuntime(t, usage, 100)
	if runtime.groupModelQuotaExceeded(context.Background(), event).Exceeded {
		t.Fatal("没超额不该拦")
	}
	if usage.lastWin != 5*time.Hour {
		t.Fatalf("窗口 = %s，应当是 5 小时", usage.lastWin)
	}
}

// 次数正好用满就拦，理由写清用了多少、上限多少。
func TestGroupQuotaBlocksWhenExhausted(t *testing.T) {
	usage := &stubGroupUsageLog{usedCalls: 100}
	runtime, event := quotaRuntime(t, usage, 100)
	verdict := runtime.groupModelQuotaExceeded(context.Background(), event)
	if !verdict.Exceeded || verdict.Usage.Calls != 100 || verdict.Calls != 100 {
		t.Fatalf("verdict = %#v", verdict)
	}
	if !strings.Contains(verdict.Reason, "100/100") {
		t.Fatalf("理由要写清用量和上限：%q", verdict.Reason)
	}
}

// 主人不受限：额度用完之后改配置、查用量还得靠主人，锁死自己没有道理。
func TestGroupQuotaExemptsOwner(t *testing.T) {
	usage := &stubGroupUsageLog{usedCalls: 99999}
	runtime, event := quotaRuntime(t, usage, 100)
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

// 每条消息都扫一遍日志太贵：窗口内的读数缓存一小会儿。
func TestGroupQuotaCachesReading(t *testing.T) {
	usage := &stubGroupUsageLog{usedCalls: 10}
	runtime, event := quotaRuntime(t, usage, 100)
	for range 5 {
		runtime.groupModelQuotaExceeded(context.Background(), event)
	}
	if usage.calls != 1 {
		t.Fatalf("用量查询次数 = %d，应当只查一次", usage.calls)
	}
}

// 群配置归一化不能把额度和抽样率抹掉。
func TestGroupQuotaSurvivesNormalization(t *testing.T) {
	cfg := GroupConfig{GroupID: "20001", BotProfileID: "qq", ModelCallQuota: 400, ReplySamplePercent: 30}
	normalized := cfg.WithDefaults("20001", BotConfig{ID: "qq"})
	if normalized.ModelCallQuota != 400 || normalized.ReplySamplePercent != 30 {
		t.Fatalf("归一化之后额度 = %d，抽样率 = %d", normalized.ModelCallQuota, normalized.ReplySamplePercent)
	}
}

// 群里留空就跟随机器人那一档，和这张表单里其它「留空跟随机器人」的设置一个规矩。
func TestGroupQuotaFallsBackToBotConfig(t *testing.T) {
	usage := &stubGroupUsageLog{usedCalls: 150}
	runtime, event := quotaRuntimeWith(t, usage, BotConfig{ID: "qq", OwnerID: "10001", ModelCallQuota: 100},
		GroupConfig{GroupID: "20001", BotProfileID: "qq", Enabled: true})
	verdict := runtime.groupModelQuotaExceeded(context.Background(), event)
	if !verdict.Exceeded || verdict.Calls != 100 {
		t.Fatalf("群里没填该跟随机器人那一档：%#v", verdict)
	}
}

// 群里填了以群为准，哪怕比机器人那档宽。
func TestGroupQuotaOverridesBotConfig(t *testing.T) {
	usage := &stubGroupUsageLog{usedCalls: 150}
	runtime, event := quotaRuntimeWith(t, usage, BotConfig{ID: "qq", OwnerID: "10001", ModelCallQuota: 100},
		GroupConfig{GroupID: "20001", BotProfileID: "qq", Enabled: true, ModelCallQuota: 1000})
	if verdict := runtime.groupModelQuotaExceeded(context.Background(), event); verdict.Exceeded {
		t.Fatalf("群里放宽了就该按群的来：%#v", verdict)
	}
}

// 两边都没填 = 不限，一次用量都不查。
func TestGroupQuotaUnsetEverywhereSkipsLookup(t *testing.T) {
	usage := &stubGroupUsageLog{usedCalls: 99999999}
	runtime, event := quotaRuntime(t, usage, 0)
	if runtime.groupModelQuotaExceeded(context.Background(), event).Exceeded {
		t.Fatal("两边都没填不该拦")
	}
	if usage.calls != 0 {
		t.Fatalf("两边都没填不该去查用量，实际查了 %d 次", usage.calls)
	}
}
