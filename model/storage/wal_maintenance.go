// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
	"time"
)

// WAL 自动 checkpoint 只在写完一次事务、且 WAL 超过 1000 页（约 4 MB）时顺手做一下，
// 做不完就算了——有读连接占着、或者正好赶上下一笔写入，它就悄悄放弃。官方把这个状态
// 叫 checkpoint starvation：只要读事务始终重叠，checkpoint 永远跑不完，WAL 无限增长。
// 生产上（2.7 GB 的库、常年有读）实测 WAL 涨到 113 MB，从 09-10 起就没回落过。
//
// 补一个自己会回收的班：定期看一眼 WAL 多大，超过阈值就主动收一次。
//
// 说清楚它治什么、不治什么——本地量过（325 MB → 1 GB 的 WAL，小事务写入 p50 42µs →
// 45µs），**WAL 变大并不会让写入变慢**，所以这不是延迟优化：它治的是磁盘被一个只涨不
// 落的文件占着、崩溃恢复要重放的量越来越大，以及「这个数字没人管」本身。线上 claim
// 慢到秒级是另一回事，那是多个 worker 排队等唯一那条写连接，见 inbound_queue.go。
const (
	// walCheckpointInterval 是巡检间隔。不用太勤：正常情况下自动 checkpoint 就够了，
	// 这一条是兜底。
	walCheckpointInterval = 5 * time.Minute
	// walCheckpointThresholdBytes 是触发阈值。自动 checkpoint 的线是 4 MB，这里留足
	// 余量，只在明显回收不掉时才插手——正常波动不该惊动它。
	walCheckpointThresholdBytes = 32 << 20
	// walCheckpointTimeout 给单次 checkpoint 封顶。它占着唯一那条写连接，等太久等于
	// 把业务写入一起拖着；超时就放掉，下一轮再来。
	walCheckpointTimeout = 30 * time.Second
	// walCheckpointBusyTimeoutMS 是做 checkpoint 期间临时调小的 busy_timeout。写连接
	// 平时是 5000 毫秒——那是为业务写入准备的耐心，checkpoint 不配：读事务占着的时候
	// 它该立刻放手，而不是攥着唯一那条写连接干等 5 秒。rqlite 这里用的是 250 毫秒，
	// 同一个道理。
	walCheckpointBusyTimeoutMS = 250
	// walWriteBusyTimeoutMS 是写连接平时的 busy_timeout，checkpoint 完要原样还回去。
	walWriteBusyTimeoutMS = 5000
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

// checkpointWALIfLarge 在 WAL 超过阈值时回收一次，返回新的连续失败计数。返回值而不是
// 改字段：巡检只有一个 goroutine 在跑，状态留在栈上更好推理。
//
// 两件事决定它不会自己变成新的卡顿源：
//
//  1. 写连接正忙就直接跳过这一轮。整个 store 只有一条写连接，业务写入全排在它上面；
//     巡检插队等于把「回收日志」的时间加在某条消息的回复延迟里。5 分钟一轮，等下一轮
//     没有任何损失。
//  2. 先 PASSIVE 再决定要不要 TRUNCATE。PASSIVE 不抢锁、不等读事务，能搬多少搬多少，
//     日常靠它就够；只有搬完文件还是很大（说明尾部被读事务钉住）才升级到 TRUNCATE，
//     那一下是要抢锁的，能少做就少做。
func (s *SQLiteStore) checkpointWALIfLarge(ctx context.Context, busyStreak int) int {
	if s.db.Stats().InUse > 0 {
		// 有人正在用写连接，这一轮让开——先看这个再看大小：忙的时候连 stat 都不必做，
		// 更不该排队等锁。
		return busyStreak
	}
	before := s.walSizeBytes()
	if before < walCheckpointThresholdBytes {
		return 0
	}
	busy, checkpointed, total, elapsed, err := s.checkpointWAL(ctx, "PASSIVE")
	if err != nil {
		log.Printf("diana sqlite wal checkpoint failed: mode=PASSIVE wal_bytes=%d err=%v", before, err)
		return busyStreak + 1
	}
	after := s.walSizeBytes()
	if after < walCheckpointThresholdBytes {
		log.Printf("diana sqlite wal checkpoint: mode=PASSIVE wal_bytes %d -> %d checkpointed=%d total=%d elapsed_ms=%d", before, after, checkpointed, total, elapsed.Milliseconds())
		return 0
	}
	// PASSIVE 搬完还是大：尾部被读事务钉着，或者根本没搬动。TRUNCATE 会等写锁并把
	// 文件截掉，代价大但一劳永逸。
	busy, checkpointed, total, elapsed, err = s.checkpointWAL(ctx, "TRUNCATE")
	if err != nil {
		log.Printf("diana sqlite wal checkpoint failed: mode=TRUNCATE wal_bytes=%d err=%v", after, err)
		return busyStreak + 1
	}
	truncated := s.walSizeBytes()
	if busy != 0 {
		busyStreak++
		// 一次两次做不成很正常（正好有读事务在），连着做不成才值得说一句：
		// 那通常意味着有个长活读事务一直占着快照，WAL 只会继续涨。
		if busyStreak >= walBusyWarnStreak {
			log.Printf("diana sqlite wal checkpoint blocked %d times: wal_bytes=%d checkpointed=%d total=%d elapsed_ms=%d（有长活读事务占着快照？）", busyStreak, truncated, checkpointed, total, elapsed.Milliseconds())
		}
		return busyStreak
	}
	log.Printf("diana sqlite wal checkpoint: mode=TRUNCATE wal_bytes %d -> %d checkpointed=%d total=%d elapsed_ms=%d", before, truncated, checkpointed, total, elapsed.Milliseconds())
	return 0
}

// checkpointWAL 做一次 checkpoint 并报出耗时。耗时要记：这件事发生在唯一那条写连接上，
// 它慢多久，后面排队的写入就多等多久，光看「做没做成」看不出代价。
//
// 整段钉在同一条连接上，因为 busy_timeout 是连接级的：先调小、做完还原，中间不能被
// 别的连接串进来。调小是为了让 checkpoint 撞上读事务时立刻放手——攥着唯一那条写连接
// 干等 5 秒，比 WAL 大一点严重得多。
func (s *SQLiteStore) checkpointWAL(ctx context.Context, mode string) (busy, checkpointed, total int, elapsed time.Duration, err error) {
	ctx, cancel := context.WithTimeout(ctx, walCheckpointTimeout)
	defer cancel()
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return 0, 0, 0, 0, err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, fmt.Sprintf("PRAGMA busy_timeout = %d", walCheckpointBusyTimeoutMS)); err != nil {
		return 0, 0, 0, 0, err
	}
	defer func() {
		if _, resetErr := conn.ExecContext(context.WithoutCancel(ctx), fmt.Sprintf("PRAGMA busy_timeout = %d", walWriteBusyTimeoutMS)); resetErr != nil && err == nil {
			err = fmt.Errorf("restore busy_timeout: %w", resetErr)
		}
	}()
	startedAt := time.Now()
	// PRAGMA wal_checkpoint 返回一行三列：busy、日志总页数、已搬回主库的页数。
	row := conn.QueryRowContext(ctx, `PRAGMA wal_checkpoint(`+mode+`)`)
	if scanErr := row.Scan(&busy, &total, &checkpointed); scanErr != nil {
		elapsed = time.Since(startedAt)
		if scanErr == sql.ErrNoRows {
			return 0, 0, 0, elapsed, nil
		}
		return 0, 0, 0, elapsed, scanErr
	}
	return busy, checkpointed, total, time.Since(startedAt), nil
}
