// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"context"
	"database/sql"
	"log"
	"os"
	"time"
)

// WAL 自动 checkpoint 只在写完一次事务、且 WAL 超过 1000 页（约 4 MB）时顺手做一下，
// 做不完就算了——有读连接占着、或者正好赶上下一笔写入，它就悄悄放弃。生产上（2.7 GB
// 的库、常年有读）实测 WAL 涨到 113 MB：每笔写入都要在这条越来越长的日志上找位置，
// ClaimNextInboundEvent 慢到 5 秒撞上限，AppendLog 直接 context deadline exceeded——
// 那些日志行是真的没写进去，排查时才发现记录缺了一截。
//
// 所以补一个自己会回收的班：定期看一眼 WAL 多大，超过阈值就主动做一次完整 checkpoint
// 并把文件截回去。它跑在同一个写连接上，天然和业务写入排队，不会并发打架。
const (
	// walCheckpointInterval 是巡检间隔。不用太勤：正常情况下自动 checkpoint 就够了，
	// 这一条是兜底。
	walCheckpointInterval = 5 * time.Minute
	// walCheckpointThresholdBytes 是触发阈值。自动 checkpoint 的线是 4 MB，这里留足
	// 余量，只在明显回收不掉时才插手——正常波动不该惊动它。
	walCheckpointThresholdBytes = 32 << 20
	// walBusyWarnStreak 是连续做不成多少次之后开始报警。偶尔赶上读事务很正常，
	// 一直做不成说明有人长期占着快照，那是另一个要查的问题。
	walBusyWarnStreak = 3
)

// startWALMaintenance 启动 WAL 回收巡检。内存库和自定义 DSN 不参与：它们没有独立的
// WAL 文件，量也不会涨到需要人管。
func (s *SQLiteStore) startWALMaintenance() {
	if s == nil || s.db == nil || s.path == "" {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.walCancel = cancel
	s.walDone = make(chan struct{})
	go func() {
		defer recoverGoroutinePanic("wal_maintenance")
		defer close(s.walDone)
		ticker := time.NewTicker(walCheckpointInterval)
		defer ticker.Stop()
		busyStreak := 0
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				busyStreak = s.checkpointWALIfLarge(ctx, busyStreak)
			}
		}
	}()
}

func (s *SQLiteStore) stopWALMaintenance() {
	if s == nil || s.walCancel == nil {
		return
	}
	s.walCancel()
	<-s.walDone
	s.walCancel = nil
}

// walSizeBytes 返回 -wal 文件的大小；没有这个文件（还没写过、或已经回收干净）返回 0。
func (s *SQLiteStore) walSizeBytes() int64 {
	info, err := os.Stat(s.path + "-wal")
	if err != nil {
		return 0
	}
	return info.Size()
}

// checkpointWALIfLarge 在 WAL 超过阈值时做一次 TRUNCATE checkpoint，返回新的连续失败
// 计数。返回值而不是改字段：巡检只有一个 goroutine 在跑，状态留在栈上更好推理。
func (s *SQLiteStore) checkpointWALIfLarge(ctx context.Context, busyStreak int) int {
	before := s.walSizeBytes()
	if before < walCheckpointThresholdBytes {
		return 0
	}
	busy, checkpointed, total, err := s.checkpointWAL(ctx)
	if err != nil {
		log.Printf("diana sqlite wal checkpoint failed: wal_bytes=%d err=%v", before, err)
		return busyStreak + 1
	}
	after := s.walSizeBytes()
	if busy != 0 {
		busyStreak++
		// 一次两次做不成很正常（正好有读事务在），连着做不成才值得说一句：
		// 那通常意味着有个长活读事务一直占着快照，WAL 只会继续涨。
		if busyStreak >= walBusyWarnStreak {
			log.Printf("diana sqlite wal checkpoint blocked %d times: wal_bytes=%d checkpointed=%d total=%d（有长活读事务占着快照？）", busyStreak, after, checkpointed, total)
		}
		return busyStreak
	}
	log.Printf("diana sqlite wal checkpoint: wal_bytes %d -> %d checkpointed=%d total=%d", before, after, checkpointed, total)
	return 0
}

// checkpointWAL 做一次 TRUNCATE checkpoint：把 WAL 里的页全部搬回主库，然后把文件截到
// 零长度。PASSIVE 只搬不截，解决不了「文件一直很大」这件事。
func (s *SQLiteStore) checkpointWAL(ctx context.Context) (busy, checkpointed, total int, err error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	// PRAGMA wal_checkpoint 返回一行三列：busy、日志总页数、已搬回主库的页数。
	row := s.db.QueryRowContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`)
	if err := row.Scan(&busy, &total, &checkpointed); err != nil {
		if err == sql.ErrNoRows {
			return 0, 0, 0, nil
		}
		return 0, 0, 0, err
	}
	return busy, checkpointed, total, nil
}
