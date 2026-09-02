// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// 表达计数：同一句话累加次数，换人说才涨说话人数；查询按门槛和时间窗过滤。
func TestGroupExpressionBumpAndTop(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "expr.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()

	now := time.Now()
	// 「哈哈哈哈」两个人各刷两次；「刷屏梗」一个人刷十次。
	for range 2 {
		if err := store.BumpGroupExpression(ctx, "bot|g1", "哈哈哈哈", "u1", now); err != nil {
			t.Fatal(err)
		}
		if err := store.BumpGroupExpression(ctx, "bot|g1", "哈哈哈哈", "u2", now); err != nil {
			t.Fatal(err)
		}
	}
	for range 10 {
		if err := store.BumpGroupExpression(ctx, "bot|g1", "刷屏梗", "u1", now); err != nil {
			t.Fatal(err)
		}
	}

	top, err := store.TopGroupExpressions(ctx, "bot|g1", now.Add(-time.Hour), 4, 2, 10)
	if err != nil {
		t.Fatal(err)
	}
	// 一个人刷出来的不算群的表达：min_users=2 把刷屏梗挡在外面。
	if len(top) != 1 || top[0].Phrase != "哈哈哈哈" || top[0].Count != 4 {
		t.Fatalf("top = %#v", top)
	}

	// 别的群查不到这个群的口癖。
	other, err := store.TopGroupExpressions(ctx, "bot|g2", now.Add(-time.Hour), 1, 1, 10)
	if err != nil || len(other) != 0 {
		t.Fatalf("cross-group leak: %#v err=%v", other, err)
	}

	// 过期淘汰。
	if err := store.PruneGroupExpressions(ctx, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	top, err = store.TopGroupExpressions(ctx, "bot|g1", now.Add(-time.Hour), 1, 1, 10)
	if err != nil || len(top) != 0 {
		t.Fatalf("prune left rows: %#v err=%v", top, err)
	}
}
