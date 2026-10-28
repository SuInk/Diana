// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"strings"
	"testing"
	"time"
)

type stubWalletStore struct {
	summary WalletSummary
	limit   int
}

func (s *stubWalletStore) WalletSummary(_ context.Context, _ string, limit int) (WalletSummary, error) {
	s.limit = limit
	return s.summary, nil
}

func walletTestRuntime(enabled bool, store WalletSummaryStore) *Runtime {
	runtime := NewRuntime(BotConfig{OwnSpaceEnabled: boolPointer(enabled)}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	if store != nil {
		runtime.SetWalletStore(store)
	}
	return runtime
}

func TestWalletContextHonorsOwnSpaceGate(t *testing.T) {
	event := MessageEvent{Kind: EventKindPrivate, UserID: "10001", ProfileID: "bot-1"}
	at := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	store := &stubWalletStore{summary: WalletSummary{
		BalanceCents: 4250,
		Recent: []WalletEntry{
			{Kind: WalletEntryAdjust, AmountCents: -750, BalanceAfter: 4250, Reason: "多发了", CreatedAt: at},
			{Kind: WalletEntryAllowance, AmountCents: 5000, BalanceAfter: 5000, CreatedAt: at},
		},
	}}

	if block := walletTestRuntime(true, nil).walletContext(context.Background(), event); block != "" {
		t.Fatalf("context without store = %q", block)
	}
	// 小窝默认关着：升级后不该突然多出一段账本。
	if block := walletTestRuntime(false, store).walletContext(context.Background(), event); block != "" {
		t.Fatalf("disabled context = %q", block)
	}
	noProfile := event
	noProfile.ProfileID = ""
	if block := walletTestRuntime(true, store).walletContext(context.Background(), noProfile); block != "" {
		t.Fatalf("context without profile = %q", block)
	}
	// 一笔账都没有时不注入「余额 0 元」。
	if block := walletTestRuntime(true, &stubWalletStore{}).walletContext(context.Background(), event); block != "" {
		t.Fatalf("empty ledger context = %q", block)
	}

	block := walletTestRuntime(true, store).walletContext(context.Background(), event)
	for _, want := range []string{"余额：42.50 元", "主人调账 -7.50 元（多发了）", "主人发零花钱 +50.00 元", "不是用户消息"} {
		if !strings.Contains(block, want) {
			t.Fatalf("context missing %q:\n%s", want, block)
		}
	}
	if store.limit != walletContextRecentEntries {
		t.Fatalf("limit = %d", store.limit)
	}
}
