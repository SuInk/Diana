// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"context"
	"path/filepath"
	"testing"
)

func TestOutboundDeliveryStepLedgerRoundTrip(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "outbound-steps.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer store.Close()
	ctx := context.Background()

	if _, ok, err := store.OutboundStepDelivered(ctx, "turn-1", "1:abc"); err != nil || ok {
		t.Fatalf("unknown step reported as delivered: ok=%v err=%v", ok, err)
	}
	if err := store.RecordOutboundStep(ctx, "turn-1", "1:abc", "msg-1"); err != nil {
		t.Fatalf("RecordOutboundStep() error = %v", err)
	}
	messageID, ok, err := store.OutboundStepDelivered(ctx, "turn-1", "1:abc")
	if err != nil || !ok || messageID != "msg-1" {
		t.Fatalf("delivered step = %q, %v, err=%v", messageID, ok, err)
	}
	// 同一条入站事件的另一个步骤、以及别的事件，都不受影响。
	if _, ok, _ := store.OutboundStepDelivered(ctx, "turn-1", "2:abc"); ok {
		t.Fatal("a different step key was reported as delivered")
	}
	if _, ok, _ := store.OutboundStepDelivered(ctx, "turn-2", "1:abc"); ok {
		t.Fatal("a different turn was reported as delivered")
	}
	// 重复登记不应该抹掉已有的消息 ID。
	if err := store.RecordOutboundStep(ctx, "turn-1", "1:abc", ""); err != nil {
		t.Fatalf("re-record error = %v", err)
	}
	if messageID, _, _ := store.OutboundStepDelivered(ctx, "turn-1", "1:abc"); messageID != "msg-1" {
		t.Fatalf("message id after re-record = %q, want msg-1", messageID)
	}
	if err := store.ClearOutboundSteps(ctx, "turn-1"); err != nil {
		t.Fatalf("ClearOutboundSteps() error = %v", err)
	}
	if _, ok, _ := store.OutboundStepDelivered(ctx, "turn-1", "1:abc"); ok {
		t.Fatal("step survived cleanup")
	}
}
