// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"context"
	"path/filepath"
	"slices"
	"testing"
)

// 核实过的成员要在重新打开库之后还在，并且按平台、机器人、群分开。
func TestIdentityBodyAccountsSurviveReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "body.db")
	store, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, userID := range []string{"70007", "70008", "70007"} {
		if err := store.SaveIdentityBodyAccount(ctx, "onebot-v11", "bot", "50005", userID); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.SaveIdentityBodyAccount(ctx, "onebot-v11", "bot", "50006", "70009"); err != nil {
		t.Fatal(err)
	}
	_ = store.Close()

	store, err = NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	got, err := store.LoadIdentityBodyAccounts(ctx, "onebot-v11", "bot", "50005")
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(got)
	if !slices.Equal(got, []string{"70007", "70008"}) {
		t.Fatalf("重开后成员 = %v", got)
	}
	if other, _ := store.LoadIdentityBodyAccounts(ctx, "telegram", "bot", "50005"); len(other) != 0 {
		t.Fatalf("不同平台不应共享结论: %v", other)
	}
}
