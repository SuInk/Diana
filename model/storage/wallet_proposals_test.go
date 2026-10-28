// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/SuInk/diana/model/assistant"
)

func TestWalletProposalApproveSpendsOnce(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "wallet.db"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	now := time.Unix(1_788_247_000, 0)
	if _, err := store.RecordWalletEntry(ctx, assistant.WalletWriteRequest{
		ProfileID: "bot-1", Kind: assistant.WalletEntryAllowance, AmountCents: 5_000, Now: now,
	}); err != nil {
		t.Fatal(err)
	}
	cheap, err := store.CreateWalletProposal(ctx, assistant.WalletProposalRequest{ProfileID: "bot-1", Item: "画集", PriceCents: 3_900, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	pricey, err := store.CreateWalletProposal(ctx, assistant.WalletProposalRequest{ProfileID: "bot-1", Item: "耳机", PriceCents: 2_000, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	// 别的机器人看不到、也批不了这份提议。
	if _, err := store.DecideWalletProposal(ctx, "bot-2", cheap.ID, true, "1", "", now); !errors.Is(err, assistant.ErrWalletProposalClosed) {
		t.Fatalf("cross-profile decide err = %v", err)
	}

	decided, err := store.DecideWalletProposal(ctx, "bot-1", cheap.ID, true, "1", "主人", now)
	if err != nil || decided.Status != assistant.WalletProposalApproved || decided.SpendEntryID == "" {
		t.Fatalf("approve = %#v err=%v", decided, err)
	}
	if _, err := store.DecideWalletProposal(ctx, "bot-1", cheap.ID, true, "1", "主人", now); !errors.Is(err, assistant.ErrWalletProposalClosed) {
		t.Fatalf("second approve err = %v", err)
	}
	summary, _ := store.WalletSummary(ctx, "bot-1", 5)
	if summary.BalanceCents != 1_100 || summary.Recent[0].Kind != assistant.WalletEntrySpend {
		t.Fatalf("summary after approve = %#v", summary)
	}

	// 余额不够：不扣款，提议保持待审批。
	if _, err := store.DecideWalletProposal(ctx, "bot-1", pricey.ID, true, "1", "", now); !errors.Is(err, assistant.ErrWalletInsufficient) {
		t.Fatalf("insufficient err = %v", err)
	}
	pending, _ := store.ListWalletProposals(ctx, "bot-1", true, now, 10)
	if len(pending) != 1 || pending[0].ID != pricey.ID {
		t.Fatalf("pending = %#v", pending)
	}
	// 过了时限就作废。
	later := now.Add(assistant.WalletProposalTTL)
	if _, err := store.DecideWalletProposal(ctx, "bot-1", pricey.ID, false, "1", "", later); !errors.Is(err, assistant.ErrWalletProposalClosed) {
		t.Fatalf("expired decide err = %v", err)
	}
	if pending, _ := store.ListWalletProposals(ctx, "bot-1", true, later, 10); len(pending) != 0 {
		t.Fatalf("pending after expiry = %#v", pending)
	}
}

func TestWalletProposalPendingLimit(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "wallet.db"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	now := time.Unix(1_788_247_000, 0)
	for i := 0; i < assistant.MaximumPendingWalletProposals; i++ {
		if _, err := store.CreateWalletProposal(ctx, assistant.WalletProposalRequest{ProfileID: "bot-1", Item: "零食", PriceCents: 100, Now: now}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.CreateWalletProposal(ctx, assistant.WalletProposalRequest{ProfileID: "bot-1", Item: "零食", PriceCents: 100, Now: now}); !errors.Is(err, assistant.ErrWalletProposalLimit) {
		t.Fatalf("over limit err = %v", err)
	}
}
