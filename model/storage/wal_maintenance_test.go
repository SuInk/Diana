// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"context"
	"path/filepath"
	"testing"
)

// WAL 涨起来之后必须能被主动收回去。生产上自动 checkpoint 没跟上，WAL 涨到 113 MB，
// 写操作排队到 5 秒、日志行直接写丢。
func TestCheckpointWALTruncatesFile(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "diana.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	if _, err := store.db.ExecContext(ctx, `CREATE TABLE wal_probe(id INTEGER PRIMARY KEY, body TEXT)`); err != nil {
		t.Fatal(err)
	}
	body := make([]byte, 4096)
	for i := range body {
		body[i] = 'x'
	}
	for i := 0; i < 400; i++ {
		if _, err := store.db.ExecContext(ctx, `INSERT INTO wal_probe(body) VALUES(?)`, string(body)); err != nil {
			t.Fatal(err)
		}
	}
	if store.walSizeBytes() == 0 {
		t.Skip("这个构建没有把 WAL 落成独立文件，没什么可回收的")
	}
	busy, _, _, err := store.checkpointWAL(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if busy != 0 {
		t.Fatalf("测试里没有并发读，checkpoint 不该被挡住：busy=%d", busy)
	}
	if after := store.walSizeBytes(); after != 0 {
		t.Fatalf("TRUNCATE checkpoint 之后 WAL 应当被截空，实际还有 %d 字节", after)
	}
}

// 没到阈值就不该动它：正常波动不值得为此抢写连接。
func TestCheckpointSkippedBelowThreshold(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "diana.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if streak := store.checkpointWALIfLarge(context.Background(), 2); streak != 0 {
		t.Fatalf("WAL 很小时应当直接跳过并清零连续失败计数，实际返回 %d", streak)
	}
}
