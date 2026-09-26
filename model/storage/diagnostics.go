package storage

import (
	"context"
	"database/sql"
	"log"
	"sync"
	"time"
)

const (
	// 写连接只有一条，250ms 已经足以让后面排队的入队、领取明显变慢；读池是
	// 多连接并发的，检索、记录列表本来就可能跑几百毫秒，同一阈值会把正常的
	// 读查询刷成噪音，所以读放宽到 500ms。
	slowStorageWriteThreshold = 250 * time.Millisecond
	slowStorageReadThreshold  = 500 * time.Millisecond
	// 同一操作一分钟内最多打一行。生产上一次写锁拥堵会让几十个调用同时变慢，
	// 逐条打印时整份日志四分之一都是它，真正的错误反而被淹没。第一次出现照常
	// 立刻打印，之后的合并成一行计数、最大值和平均值。
	slowStorageLogInterval = time.Minute
)

// slowStorageSample 是一次慢调用的测量结果。
//
// wait 是等连接的时间，exec 是拿到连接后真正执行的时间。事务开始时由
// beginWriteTx 直接计时（BEGIN DEFERRED 不取锁，耗时就是排队等写连接），
// 这时 waitExact 为 true。其余调用拿不到自己的等待时长：database/sql 的
// Stats 是整个连接池的累计值，时间窗口里别的调用等待结束也会算进来。这种情况
// 下 wait 取「池等待增量」与总耗时中较小者，是本次等待的上界；exec 相应是
// 执行时间的下界——exec 很大时可以确定是 SQL 本身慢，而不是在排队。
type slowStorageSample struct {
	operation string
	pool      string
	elapsed   time.Duration
	wait      time.Duration
	exec      time.Duration
	waitExact bool
	inUse     int
	idle      int
	ctxErr    error
}

// slowStorageLog 按操作限频输出慢调用。
//
// 只写进程日志，不写 app_logs：数据库堵住时，排查它的日志不能再依赖同一个
// 数据库。日志里不带 SQL 参数和消息正文。
type slowStorageLog struct {
	mu        sync.Mutex
	now       func() time.Time
	logf      func(format string, args ...any)
	afterFunc func(time.Duration, func())
	interval  time.Duration
	write     time.Duration
	read      time.Duration
	ops       map[string]*slowStorageWindow
}

// slowStorageWindow 记录一个操作从上次打印以来被压下的慢调用。
type slowStorageWindow struct {
	operation string
	pool      string
	lastLog   time.Time
	count     int
	total     time.Duration
	max       time.Duration
	maxWait   time.Duration
	maxExec   time.Duration
	ctxErrs   int
	flushing  bool
}

func newSlowStorageLog() *slowStorageLog {
	return &slowStorageLog{
		now:       time.Now,
		logf:      log.Printf,
		afterFunc: func(delay time.Duration, fn func()) { time.AfterFunc(delay, fn) },
		interval:  slowStorageLogInterval,
		write:     slowStorageWriteThreshold,
		read:      slowStorageReadThreshold,
		ops:       make(map[string]*slowStorageWindow),
	}
}

// storageSlowLog 是进程内唯一的慢调用日志。放在包级而不是 SQLiteStore 上，
// 是因为同一进程只开一个库，而且限频窗口本来就该按进程算。
var storageSlowLog = newSlowStorageLog()

func (l *slowStorageLog) threshold(pool string) time.Duration {
	if pool == "read" {
		return l.read
	}
	return l.write
}

// record 判断一次调用是否算慢，并按限频规则输出。
//
// 只看耗时：上下文已经取消、但几乎没花时间的调用不是数据库慢，是调用方在
// 进来之前就放弃了（生产上 AppendLog、PendingInboundCount 大量这样的 0ms
// 记录）。取消且确实等了很久的仍然记，并带上 context_error。
func (l *slowStorageLog) record(sample slowStorageSample) {
	if sample.elapsed < l.threshold(sample.pool) {
		return
	}
	key := sample.pool + "|" + sample.operation
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	window := l.ops[key]
	if window == nil {
		window = &slowStorageWindow{operation: sample.operation, pool: sample.pool}
		l.ops[key] = window
	}
	if window.lastLog.IsZero() || now.Sub(window.lastLog) >= l.interval {
		if window.count == 0 {
			l.logSample(sample)
		} else {
			window.add(sample)
			l.logWindow(window, now)
		}
		window.reset(now)
		return
	}
	window.add(sample)
	if !window.flushing {
		// 被压下的记录不能等到下一次变慢才出现：一阵拥堵过去之后可能很久都没
		// 有下一次，这一分钟的计数就永远看不到了。
		window.flushing = true
		l.afterFunc(window.lastLog.Add(l.interval).Sub(now), func() { l.flush(key) })
	}
}

// flush 把某个操作被压下的慢调用汇总成一行。窗口期内又有新记录时由 record
// 负责打印，这里什么也不做。
func (l *slowStorageLog) flush(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	window := l.ops[key]
	if window == nil {
		return
	}
	window.flushing = false
	if window.count == 0 {
		return
	}
	now := l.now()
	l.logWindow(window, now)
	window.reset(now)
}

func (l *slowStorageLog) logSample(sample slowStorageSample) {
	waitSource := "pool_stats"
	if sample.waitExact {
		waitSource = "conn"
	}
	l.logf("diana sqlite slow operation=%s pool=%s elapsed_ms=%d wait_ms=%d exec_ms=%d wait_source=%s in_use=%d idle=%d context_error=%v",
		sample.operation, sample.pool, sample.elapsed.Milliseconds(), sample.wait.Milliseconds(), sample.exec.Milliseconds(), waitSource, sample.inUse, sample.idle, sample.ctxErr)
}

func (l *slowStorageLog) logWindow(window *slowStorageWindow, now time.Time) {
	span := now.Sub(window.lastLog)
	l.logf("diana sqlite slow operation=%s pool=%s count=%d window_s=%d avg_ms=%d max_ms=%d max_wait_ms=%d max_exec_ms=%d context_errors=%d",
		window.operation, window.pool, window.count, int64(span.Seconds()), (window.total / time.Duration(window.count)).Milliseconds(),
		window.max.Milliseconds(), window.maxWait.Milliseconds(), window.maxExec.Milliseconds(), window.ctxErrs)
}

func (w *slowStorageWindow) add(sample slowStorageSample) {
	w.count++
	w.total += sample.elapsed
	w.max = max(w.max, sample.elapsed)
	w.maxWait = max(w.maxWait, sample.wait)
	w.maxExec = max(w.maxExec, sample.exec)
	if sample.ctxErr != nil {
		w.ctxErrs++
	}
}

func (w *slowStorageWindow) reset(now time.Time) {
	w.lastLog = now
	w.count, w.ctxErrs = 0, 0
	w.total, w.max, w.maxWait, w.maxExec = 0, 0, 0, 0
}

// observeStorage 测量一次存储调用。pool 必须和调用实际使用的连接池一致，
// 否则池等待和连接占用会取错池。
func (s *SQLiteStore) observeStorage(ctx context.Context, operation string, pool string) func() {
	if s == nil || s.db == nil {
		return func() {}
	}
	db := s.db
	if pool == "read" {
		db = s.eventReader()
	}
	before := db.Stats()
	started := time.Now()
	return func() {
		elapsed := time.Since(started)
		if elapsed < storageSlowLog.threshold(pool) {
			return
		}
		after := db.Stats()
		wait := min(after.WaitDuration-before.WaitDuration, elapsed)
		storageSlowLog.record(slowStorageSample{
			operation: operation, pool: pool, elapsed: elapsed,
			wait: wait, exec: elapsed - wait,
			inUse: after.InUse, idle: after.Idle, ctxErr: ctx.Err(),
		})
	}
}

func (s *SQLiteStore) beginWriteTx(ctx context.Context, operation string) (*sql.Tx, error) {
	started := time.Now()
	tx, err := s.db.BeginTx(ctx, nil)
	// BEGIN DEFERRED 不取数据库锁，这段耗时就是在等唯一的写连接。
	elapsed := time.Since(started)
	if elapsed < storageSlowLog.threshold("write") {
		return tx, err
	}
	stats := s.db.Stats()
	storageSlowLog.record(slowStorageSample{
		operation: operation + ".begin_transaction", pool: "write", elapsed: elapsed,
		wait: elapsed, waitExact: true,
		inUse: stats.InUse, idle: stats.Idle, ctxErr: ctx.Err(),
	})
	return tx, err
}

// observeTransaction 记录持有写事务的时长。拿到连接之后才开始计时，所以这里
// 全部是执行时间。
func observeTransaction(operation string) func() {
	started := time.Now()
	return func() {
		elapsed := time.Since(started)
		storageSlowLog.record(slowStorageSample{
			operation: operation + ".transaction", pool: "write", elapsed: elapsed,
			exec: elapsed, waitExact: true,
		})
	}
}
