// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"testing"
	"time"

	"github.com/SuInk/diana/model/applog"
)

type stubGroupUsageLog struct {
	applog.Writer
	tokens  int64
	err     error
	calls   int
	lastWin time.Duration
}

func (s *stubGroupUsageLog) AppendLog(context.Context, applog.Entry) error { return nil }

func (s *stubGroupUsageLog) GroupLLMTokensSince(_ context.Context, _, _ string, since, until time.Time) (int64, error) {
	s.calls++
	s.lastWin = until.Sub(since)
	return s.tokens, s.err
}

func quotaRuntime(t *testing.T, usage *stubGroupUsageLog, quota int64) (*Runtime, MessageEvent) {
	t.Helper()
	runtime := NewRuntime(BotConfig{ID: "qq", OwnerID: "10001"}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.SetAppLogWriter(usage)
	runtime.SetGroupConfigStore(&stubGroupConfigStore{configs: map[string]GroupConfig{
		"20001": {GroupID: "20001", BotProfileID: "qq", Enabled: true, ModelTokenQuota: quota},
	}})
	return runtime, MessageEvent{Kind: EventKindGroup, ProfileID: "qq", GroupID: "20001", UserID: "20002"}
}

// 额度按滚动 5 小时算，和按 token 计费的套餐窗口一致。
func TestGroupQuotaUsesFiveHourWindow(t *testing.T) {
	usage := &stubGroupUsageLog{tokens: 10}
	runtime, event := quotaRuntime(t, usage, 1000)
	if exceeded, _, _ := runtime.groupModelQuotaExceeded(context.Background(), event); exceeded {
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
	exceeded, used, quota := runtime.groupModelQuotaExceeded(context.Background(), event)
	if !exceeded || used != 1200 || quota != 1000 {
		t.Fatalf("exceeded=%v used=%d quota=%d", exceeded, used, quota)
	}
}

// 主人不受限：额度用完之后改配置、查用量还得靠主人，锁死自己没有道理。
func TestGroupQuotaExemptsOwner(t *testing.T) {
	usage := &stubGroupUsageLog{tokens: 99999}
	runtime, event := quotaRuntime(t, usage, 1000)
	event.UserID = "10001"
	if exceeded, _, _ := runtime.groupModelQuotaExceeded(context.Background(), event); exceeded {
		t.Fatal("主人不该被额度拦住")
	}
}

// 读不到用量时放行：额度是省钱用的，不该因为日志存储不可用就让整个群哑掉。
func TestGroupQuotaFailsOpen(t *testing.T) {
	usage := &stubGroupUsageLog{err: context.DeadlineExceeded}
	runtime, event := quotaRuntime(t, usage, 1)
	if exceeded, _, _ := runtime.groupModelQuotaExceeded(context.Background(), event); exceeded {
		t.Fatal("读不到用量时该放行")
	}
}

// 没配额度的群一次用量都不用查。
func TestGroupQuotaSkipsWhenUnset(t *testing.T) {
	usage := &stubGroupUsageLog{tokens: 99999}
	runtime, event := quotaRuntime(t, usage, 0)
	if exceeded, _, _ := runtime.groupModelQuotaExceeded(context.Background(), event); exceeded {
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
