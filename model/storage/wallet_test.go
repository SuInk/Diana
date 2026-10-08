// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/SuInk/diana/model/assistant"
)

func TestWalletLedgerBalanceAndRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wallet.db")
	store, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	now := time.Unix(1_788_247_000, 0)
	if _, err := store.RecordWalletEntry(ctx, assistant.WalletWriteRequest{
		ProfileID: "bot-1", Kind: assistant.WalletEntryAllowance, AmountCents: 10_000, Reason: "十月零花钱", Now: now,
	}); err != nil {
		t.Fatal(err)
	}
	spent, err := store.RecordWalletEntry(ctx, assistant.WalletWriteRequest{
		ProfileID: "bot-1", Kind: assistant.WalletEntrySpend, AmountCents: -3_900, Reason: "画集", Now: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if spent.BalanceAfter != 6_100 {
		t.Fatalf("balance after spend = %d, want 6100", spent.BalanceAfter)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	summary, err := store.WalletSummary(ctx, "bot-1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if summary.BalanceCents != 6_100 || len(summary.Recent) != 2 || summary.Recent[0].Reason != "画集" {
		t.Fatalf("summary = %#v", summary)
	}
	// 另一台机器人看不到这本账。
	other, err := store.WalletSummary(ctx, "bot-2", 0)
	if err != nil {
		t.Fatal(err)
	}
	if other.BalanceCents != 0 || len(other.Recent) != 0 {
		t.Fatalf("other profile summary = %#v", other)
	}
}

func TestWalletLedgerRejectsOverdraftAndBadAmount(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "wallet.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	ctx := context.Background()
	if _, err := store.RecordWalletEntry(ctx, assistant.WalletWriteRequest{
		ProfileID: "bot-1", Kind: assistant.WalletEntrySpend, AmountCents: -1,
	}); !errors.Is(err, assistant.ErrWalletInsufficient) {
		t.Fatalf("overdraft err = %v", err)
	}
	for _, amount := range []int64{0, assistant.MaximumWalletAmountCents + 1} {
		if _, err := store.RecordWalletEntry(ctx, assistant.WalletWriteRequest{
			ProfileID: "bot-1", Kind: assistant.WalletEntryAllowance, AmountCents: amount,
		}); !errors.Is(err, assistant.ErrWalletAmount) {
			t.Fatalf("amount %d err = %v", amount, err)
		}
	}
	if _, err := store.RecordWalletEntry(ctx, assistant.WalletWriteRequest{
		ProfileID: "bot-1", Kind: "gift", AmountCents: 100,
	}); err == nil {
		t.Fatal("unknown kind accepted")
	}
	summary, err := store.WalletSummary(ctx, "bot-1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(summary.Recent) != 0 {
		t.Fatalf("rejected writes left rows: %#v", summary.Recent)
	}
}

// 两个会话同时扣款：余额只够一笔，只能成交一笔。
func TestWalletLedgerConcurrentSpendDoesNotOverdraw(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "wallet.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	ctx := context.Background()
	if _, err := store.RecordWalletEntry(ctx, assistant.WalletWriteRequest{
		ProfileID: "bot-1", Kind: assistant.WalletEntryAllowance, AmountCents: 1_000,
	}); err != nil {
		t.Fatal(err)
	}
	const spenders = 8
	var wg sync.WaitGroup
	results := make(chan error, spenders)
	for range spenders {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := store.RecordWalletEntry(ctx, assistant.WalletWriteRequest{
				ProfileID: "bot-1", Kind: assistant.WalletEntrySpend, AmountCents: -800,
			})
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	succeeded := 0
	for err := range results {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, assistant.ErrWalletInsufficient):
		default:
			t.Fatalf("unexpected err = %v", err)
		}
	}
	if succeeded != 1 {
		t.Fatalf("succeeded = %d, want 1", succeeded)
	}
	summary, err := store.WalletSummary(ctx, "bot-1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if summary.BalanceCents != 200 {
		t.Fatalf("balance = %d, want 200", summary.BalanceCents)
	}
}
