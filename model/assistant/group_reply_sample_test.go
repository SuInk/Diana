// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"strings"
	"testing"
)

func sampleRuntime(t *testing.T, bot BotConfig, group GroupConfig, roll int) (*Runtime, MessageEvent) {
	t.Helper()
	runtime, event := quotaRuntimeWith(t, &stubGroupUsageLog{}, bot, group)
	runtime.replySampleRoll = func() int { return roll }
	return runtime, event
}

func TestEffectiveGroupReplySamplePercent(t *testing.T) {
	for _, tc := range []struct {
		bot, group, want int
	}{
		{0, 0, 100},
		{40, 0, 40},
		{40, 70, 70},
		{0, 25, 25},
		{150, 0, 100},
		{-5, 0, 100},
	} {
		got := EffectiveGroupReplySamplePercent(BotConfig{ReplySamplePercent: tc.bot}, GroupConfig{ReplySamplePercent: tc.group})
		if got != tc.want {
			t.Errorf("bot=%d group=%d: got %d, want %d", tc.bot, tc.group, got, tc.want)
		}
	}
}

// 点数落在抽样率以内放行，以外跳过，理由写明比例。
func TestGroupReplySampleRollDecides(t *testing.T) {
	group := GroupConfig{GroupID: "20001", BotProfileID: "qq", Enabled: true, ReplySamplePercent: 30}
	runtime, event := sampleRuntime(t, BotConfig{ID: "qq", OwnerID: "10001"}, group, 29)
	if _, skip := runtime.groupReplySampleSkips(event); skip {
		t.Fatal("点数 29 < 30，应当抽中")
	}
	runtime, event = sampleRuntime(t, BotConfig{ID: "qq", OwnerID: "10001"}, group, 30)
	reason, skip := runtime.groupReplySampleSkips(event)
	if !skip || !strings.Contains(reason, "30%") {
		t.Fatalf("点数 30 应当跳过并写明比例：skip=%t reason=%q", skip, reason)
	}
}

// 没设抽样率的群不掷点，也就永远不跳过。
func TestGroupReplySampleUnsetNeverSkips(t *testing.T) {
	runtime, event := sampleRuntime(t, BotConfig{ID: "qq", OwnerID: "10001"}, GroupConfig{GroupID: "20001", BotProfileID: "qq", Enabled: true}, 99)
	if _, skip := runtime.groupReplySampleSkips(event); skip {
		t.Fatal("没设抽样率不该跳过")
	}
}

// 群里留空跟随机器人那一档。
func TestGroupReplySampleFollowsBot(t *testing.T) {
	runtime, event := sampleRuntime(t, BotConfig{ID: "qq", OwnerID: "10001", ReplySamplePercent: 10}, GroupConfig{GroupID: "20001", BotProfileID: "qq", Enabled: true}, 50)
	if _, skip := runtime.groupReplySampleSkips(event); !skip {
		t.Fatal("群里没填该跟随机器人的 10%")
	}
}

// 主人不参与抽样，和额度的规矩一样。
func TestGroupReplySampleExemptsOwner(t *testing.T) {
	runtime, event := sampleRuntime(t, BotConfig{ID: "qq", OwnerID: "10001"}, GroupConfig{GroupID: "20001", BotProfileID: "qq", Enabled: true, ReplySamplePercent: 1}, 99)
	event.UserID = "10001"
	if _, skip := runtime.groupReplySampleSkips(event); skip {
		t.Fatal("主人不该被抽样跳过")
	}
}

// 没抽中的消息在路由模型之前就收住：一次模型调用都不花，事件理由写明是抽样。
// 抽中的照常进路由。
func TestGroupReplySampleSkipsRouterCall(t *testing.T) {
	h := newDisabledGroupSkipHarness(t, BotConfig{ReplySamplePercent: 20}, true)
	h.runtime.replySampleRoll = func() int { return 90 }
	prepared, _, handled, _ := h.runtime.prepareMessageEvent(context.Background(), disabledGroupSignalEvent())
	if handled {
		t.Fatal("没抽中的消息不该回复")
	}
	if got := h.provider.callCount(); got != 0 {
		t.Fatalf("没抽中还调了 %d 次模型", got)
	}
	if !strings.Contains(prepared.routingReason, "抽样") {
		t.Fatalf("事件理由要写明是抽样跳过：%q", prepared.routingReason)
	}

	h.runtime.replySampleRoll = func() int { return 0 }
	event := disabledGroupSignalEvent()
	event.MessageID = "m2"
	h.runtime.prepareMessageEvent(context.Background(), event)
	if h.provider.callCount() == 0 {
		t.Fatal("抽中了却没进路由模型")
	}
}

// 被 @ 的消息不参与抽样：被点名却随机不理人，看起来就是坏了。
func TestGroupReplySampleLeavesDirectMessagesAlone(t *testing.T) {
	h := newDisabledGroupSkipHarness(t, BotConfig{ReplySamplePercent: 1}, true)
	h.runtime.replySampleRoll = func() int { return 99 }
	event := disabledGroupSignalEvent()
	event.ToMe = true
	if _, _, handled, outcome := h.runtime.prepareMessageEvent(context.Background(), event); !handled {
		t.Fatalf("被 @ 的消息该照常回复，outcome=%q", outcome)
	}
}
