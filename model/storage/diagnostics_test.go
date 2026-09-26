package storage

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// testSlowLog 换上一份可控的慢日志：时钟手动拨，定时器手动触发，输出收进切片。
type testSlowLog struct {
	*slowStorageLog
	mu      sync.Mutex
	clock   time.Time
	lines   []string
	pending []func()
}

func useTestSlowLog(t *testing.T, write, read time.Duration) *testSlowLog {
	t.Helper()
	fake := &testSlowLog{clock: time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)}
	logger := newSlowStorageLog()
	logger.write, logger.read = write, read
	logger.now = func() time.Time {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		return fake.clock
	}
	logger.logf = func(format string, args ...any) {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		fake.lines = append(fake.lines, fmt.Sprintf(format, args...))
	}
	// 不能同步调用 fn：record 持有锁时安排定时器，同步回调会死锁。
	logger.afterFunc = func(_ time.Duration, fn func()) {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		fake.pending = append(fake.pending, fn)
	}
	fake.slowStorageLog = logger
	previous := storageSlowLog
	storageSlowLog = logger
	t.Cleanup(func() { storageSlowLog = previous })
	return fake
}

func (f *testSlowLog) advance(d time.Duration) {
	f.mu.Lock()
	f.clock = f.clock.Add(d)
	f.mu.Unlock()
}

func (f *testSlowLog) fireTimers() {
	f.mu.Lock()
	pending := f.pending
	f.pending = nil
	f.mu.Unlock()
	for _, fn := range pending {
		fn()
	}
}

func (f *testSlowLog) output() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.lines...)
}

func requireFields(t *testing.T, line string, fields ...string) {
	t.Helper()
	for _, field := range fields {
		if !strings.Contains(line, field) {
			t.Fatalf("missing %s: %s", field, line)
		}
	}
}

// 事务开始的耗时就是等写连接的时间，日志要把它明确标成本次调用自己的等待。
func TestStorageDiagnosticsIdentifyConnectionWait(t *testing.T) {
	s, _ := seedEventBrowsing(t, 1)
	logs := useTestSlowLog(t, 10*time.Millisecond, 10*time.Millisecond)
	writer, err := s.db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	if _, err := s.beginWriteTx(ctx, "test_enqueue"); err == nil {
		t.Fatal("write unexpectedly acquired connection")
	}
	lines := logs.output()
	if len(lines) != 1 {
		t.Fatalf("want one line, got %q", lines)
	}
	requireFields(t, lines[0], "operation=test_enqueue.begin_transaction", "pool=write", "wait_source=conn", "exec_ms=0", "in_use=1", "context_error=context deadline exceeded")
}

// 生产日志里大量 elapsed_ms=0 的「慢」记录其实是调用方早就取消了的上下文。
func TestSlowStorageLogIgnoresCanceledFastCalls(t *testing.T) {
	s, _ := seedEventBrowsing(t, 1)
	logs := useTestSlowLog(t, 250*time.Millisecond, 500*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.PendingInboundCount(ctx); err == nil {
		t.Fatal("canceled context should fail the query")
	}
	s.observeStorage(ctx, "AppendLog", "write")()
	logs.record(slowStorageSample{operation: "AppendLog", pool: "write", ctxErr: context.Canceled})
	if lines := logs.output(); len(lines) != 0 {
		t.Fatalf("canceled but fast calls must not be logged as slow: %q", lines)
	}
	// 取消了但确实等了很久的仍然要记，并带上原因。
	logs.record(slowStorageSample{operation: "AppendLog", pool: "write", elapsed: 300 * time.Millisecond, wait: 300 * time.Millisecond, ctxErr: context.DeadlineExceeded})
	lines := logs.output()
	if len(lines) != 1 {
		t.Fatalf("slow canceled call must be logged: %q", lines)
	}
	requireFields(t, lines[0], "operation=AppendLog", "elapsed_ms=300", "wait_ms=300", "exec_ms=0", "wait_source=pool_stats", "context_error=context deadline exceeded")
}

func TestSlowStorageLogUsesSeparateReadAndWriteThresholds(t *testing.T) {
	logs := useTestSlowLog(t, 250*time.Millisecond, 500*time.Millisecond)
	logs.record(slowStorageSample{operation: "SearchMessageEvents", pool: "read", elapsed: 400 * time.Millisecond})
	if lines := logs.output(); len(lines) != 0 {
		t.Fatalf("400ms read is under the read threshold: %q", lines)
	}
	logs.record(slowStorageSample{operation: "AppendMessageEvent", pool: "write", elapsed: 400 * time.Millisecond})
	logs.record(slowStorageSample{operation: "SearchMessageEvents", pool: "read", elapsed: 600 * time.Millisecond})
	lines := logs.output()
	if len(lines) != 2 {
		t.Fatalf("want write and read lines, got %q", lines)
	}
	requireFields(t, lines[0], "operation=AppendMessageEvent", "pool=write")
	requireFields(t, lines[1], "operation=SearchMessageEvents", "pool=read")
}

// 同一操作第一次立刻打印，之后一分钟内的合并成一行汇总；不同操作互不影响。
func TestSlowStorageLogAggregatesPerOperation(t *testing.T) {
	logs := useTestSlowLog(t, 250*time.Millisecond, 500*time.Millisecond)
	claim := func(elapsed time.Duration, err error) {
		logs.record(slowStorageSample{operation: "ClaimMemoryJobBatch.begin_transaction", pool: "write", elapsed: elapsed, wait: elapsed, waitExact: true, ctxErr: err})
	}
	claim(400*time.Millisecond, nil)
	if lines := logs.output(); len(lines) != 1 {
		t.Fatalf("first occurrence must be immediate: %q", lines)
	}
	logs.advance(10 * time.Second)
	claim(300*time.Millisecond, nil)
	logs.advance(10 * time.Second)
	claim(900*time.Millisecond, context.DeadlineExceeded)
	logs.advance(10 * time.Second)
	claim(600*time.Millisecond, nil)
	logs.record(slowStorageSample{operation: "ClaimNextInboundEvent", pool: "write", elapsed: 500 * time.Millisecond})
	lines := logs.output()
	if len(lines) != 2 || !strings.Contains(lines[1], "operation=ClaimNextInboundEvent") {
		t.Fatalf("repeats within a minute must be held back, other operations not: %q", lines)
	}
	if len(logs.pending) != 1 {
		t.Fatalf("want exactly one flush timer for the held-back operation, got %d", len(logs.pending))
	}

	// 一阵拥堵过去以后可能再也没有下一次变慢，汇总靠定时器打出来。
	logs.advance(30 * time.Second)
	logs.fireTimers()
	lines = logs.output()
	if len(lines) != 3 {
		t.Fatalf("want aggregated line, got %q", lines)
	}
	requireFields(t, lines[2], "operation=ClaimMemoryJobBatch.begin_transaction", "pool=write", "count=3", "window_s=60", "avg_ms=600", "max_ms=900", "max_wait_ms=900", "context_errors=1")

	// 汇总之后开始新的一分钟窗口。
	logs.advance(10 * time.Second)
	claim(300*time.Millisecond, nil)
	if lines := logs.output(); len(lines) != 3 {
		t.Fatalf("still inside the new window: %q", lines)
	}
	// 窗口过后的下一次变慢把压下的一起带出来，不必等定时器。
	logs.advance(time.Minute)
	claim(700*time.Millisecond, nil)
	lines = logs.output()
	if len(lines) != 4 {
		t.Fatalf("a slow call after the window prints the held-back summary: %q", lines)
	}
	requireFields(t, lines[3], "count=2", "window_s=70", "avg_ms=500", "max_ms=700", "context_errors=0")
	logs.fireTimers()
	if lines := logs.output(); len(lines) != 4 {
		t.Fatalf("a timer with nothing held back must stay silent: %q", lines)
	}
}

func TestObserveTransactionReportsExecution(t *testing.T) {
	logs := useTestSlowLog(t, time.Millisecond, time.Second)
	done := observeTransaction("ApplyMemoryCandidates")
	time.Sleep(5 * time.Millisecond)
	done()
	lines := logs.output()
	if len(lines) != 1 {
		t.Fatalf("want one line: %q", lines)
	}
	requireFields(t, lines[0], "operation=ApplyMemoryCandidates.transaction", "pool=write", "wait_ms=0", "wait_source=conn")
}
