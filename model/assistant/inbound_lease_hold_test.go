package assistant

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

// lostLeaseStore 模拟租约已经被另一个 worker 接手：任何续租都失败。
type lostLeaseStore struct {
	*memoryInboundEventStore
	mu    sync.Mutex
	calls int
}

func (s *lostLeaseStore) ExtendInboundLease(_ context.Context, id string, _ string, _ time.Time) error {
	s.mu.Lock()
	s.calls++
	s.mu.Unlock()
	return fmt.Errorf("extend inbound event %q: %w", id, ErrInboundLeaseNotHeld)
}

func (s *lostLeaseStore) extendCalls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func (s *recordingLeaseStore) extendCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.extends)
}

func withLeaseRenewInterval(t *testing.T, d time.Duration) {
	t.Helper()
	previous := inboundLeaseRenewInterval
	inboundLeaseRenewInterval = d
	t.Cleanup(func() { inboundLeaseRenewInterval = previous })
}

// 生成回复跑得比租约还长时（多轮工具 + 搜索子任务），处理期间要自己续租，
// 不能等到发送那一刻才续，否则租约到期会被另一个 worker 领走重跑一遍。
func TestHoldInboundLeaseRenewsWhileProcessing(t *testing.T) {
	withLeaseRenewInterval(t, 5*time.Millisecond)
	store := &recordingLeaseStore{memoryInboundEventStore: newMemoryInboundEventStore()}
	// 领取时的租约马上就到期，心跳每次都得真的往后推。
	_, lease, stop := holdInboundLease(context.Background(), store, "event-1", "worker-1", time.Now())
	deadline := time.Now().Add(2 * time.Second)
	for store.extendCount() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	stop()
	if store.extendCount() == 0 {
		t.Fatal("lease was never renewed while the event was being processed")
	}
	if until := store.extends[0]; time.Until(until) < inboundLeaseDuration {
		t.Fatalf("renewed lease ends at %v, want at least a full lease duration ahead", until)
	}
	if lease.lost() {
		t.Fatal("lease reported lost although every renewal succeeded")
	}
	after := store.extendCount()
	time.Sleep(30 * time.Millisecond)
	if store.extendCount() != after {
		t.Fatal("lease kept renewing after processing finished")
	}
}

// 租约已经被另一个 worker 接手时，这一轮生成的回复不能再发出去。
func TestLostInboundLeaseBlocksOutboundSend(t *testing.T) {
	withLeaseRenewInterval(t, 5*time.Millisecond)
	store := &lostLeaseStore{memoryInboundEventStore: newMemoryInboundEventStore()}
	ctx, lease, stop := holdInboundLease(context.Background(), store, "event-1", "worker-1", time.Now())
	defer stop()
	deadline := time.Now().Add(2 * time.Second)
	for !lease.lost() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if !lease.lost() {
		t.Fatal("renewal did not notice the lease was taken over")
	}
	calls := store.extendCalls()
	time.Sleep(30 * time.Millisecond)
	if store.extendCalls() != calls {
		t.Fatal("kept renewing a lease that belongs to another worker")
	}

	r := NewRuntime(BotConfig{}, newScriptedBackoffChannel("123456"), NewPluginManager(), nil, nil, nil, nil)
	for _, event := range []MessageEvent{
		{Kind: EventKindGroup, GroupID: "123456"},
		{Kind: EventKindPrivate, UserID: "12345"},
	} {
		sent := false
		_, err := r.executeOutboundCall(ctx, event, "send_msg", func(context.Context) (map[string]any, error) {
			sent = true
			return map[string]any{"message_id": 1}, nil
		})
		if sent {
			t.Fatalf("%s: reply was sent after the lease was lost", event.Kind)
		}
		if !errors.Is(err, errInboundLeaseLost) {
			t.Fatalf("%s: err = %v, want errInboundLeaseLost", event.Kind, err)
		}
	}
}

// 发送那一刻才发现租约丢了（心跳还没轮到）也要拦住。
func TestOutboundSendChecksLeaseBeforeWriting(t *testing.T) {
	store := &lostLeaseStore{memoryInboundEventStore: newMemoryInboundEventStore()}
	ctx := withInboundLeaseExtension(context.Background(), store, "event-1", "worker-1", time.Now().Add(-time.Minute))
	r := NewRuntime(BotConfig{}, newScriptedBackoffChannel("123456"), NewPluginManager(), nil, nil, nil, nil)
	_, err := r.executeOutboundCall(ctx, MessageEvent{Kind: EventKindGroup, GroupID: "123456"}, "send_group_msg", func(context.Context) (map[string]any, error) {
		t.Fatal("reply was sent after the lease was lost")
		return nil, nil
	})
	if !errors.Is(err, errInboundLeaseLost) {
		t.Fatalf("err = %v, want errInboundLeaseLost", err)
	}
}

// 处理收尾之后还拿着 ctx 的后台任务不受租约影响：既不写库续租，也不被拦。
func TestFinishedInboundLeaseIgnoresLateExtensions(t *testing.T) {
	store := &lostLeaseStore{memoryInboundEventStore: newMemoryInboundEventStore()}
	ctx, _, stop := holdInboundLease(context.Background(), store, "event-1", "worker-1", time.Now())
	stop()
	if err := extendInboundLease(ctx, time.Hour); err != nil {
		t.Fatalf("late extension after processing finished: %v", err)
	}
	if store.extendCalls() != 0 {
		t.Fatal("late extension after processing finished still wrote the lease")
	}
}
