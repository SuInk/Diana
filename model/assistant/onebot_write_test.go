package assistant

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

type oneBotWriteTestConn struct {
	net.Conn
	mu       sync.Mutex
	writes   int
	deadline time.Time
	written  chan struct{}
	writeErr error
	closed   bool
}

func (c *oneBotWriteTestConn) Close() error {
	c.mu.Lock()
	c.closed = true
	c.mu.Unlock()
	return c.Conn.Close()
}

func (c *oneBotWriteTestConn) isClosed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}

func (c *oneBotWriteTestConn) Write(p []byte) (int, error) {
	c.mu.Lock()
	c.writes++
	writeErr := c.writeErr
	c.mu.Unlock()
	if writeErr != nil {
		return 0, writeErr
	}
	n, err := c.Conn.Write(p)
	select {
	case c.written <- struct{}{}:
	default:
	}
	return n, err
}

func (c *oneBotWriteTestConn) SetWriteDeadline(deadline time.Time) error {
	c.mu.Lock()
	c.deadline = deadline
	c.mu.Unlock()
	return c.Conn.SetWriteDeadline(deadline)
}

func (c *oneBotWriteTestConn) snapshot() (int, time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.writes, c.deadline
}

func newOneBotWriteTestConn(t *testing.T) (*websocket.Conn, *oneBotWriteTestConn) {
	t.Helper()
	peers := make(chan *websocket.Conn, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upgrader := websocket.Upgrader{}
		peer, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade: %v", err)
			return
		}
		peers <- peer
	}))
	t.Cleanup(server.Close)
	var tracked *oneBotWriteTestConn
	dialer := websocket.Dialer{
		NetDialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			conn, err := (&net.Dialer{}).DialContext(ctx, network, address)
			if err != nil {
				return nil, err
			}
			tracked = &oneBotWriteTestConn{Conn: conn, written: make(chan struct{}, 1)}
			return tracked, nil
		},
	}
	conn, _, err := dialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	peer := <-peers
	t.Cleanup(func() { _ = peer.Close() })
	tracked.mu.Lock()
	tracked.writes = 0
	tracked.deadline = time.Time{}
	tracked.mu.Unlock()
	select {
	case <-tracked.written:
	default:
	}
	return conn, tracked
}

type oneBotWriteTestCaller struct {
	mu      *sync.Mutex
	pending *sync.Map
	call    func(context.Context, string, map[string]any) (map[string]any, error)
}

func newOneBotWriteTestCaller(conn *websocket.Conn, reverse bool) oneBotWriteTestCaller {
	if reverse {
		server := &OneBotReverseServer{conn: conn}
		return oneBotWriteTestCaller{&server.writeMu, &server.pending, server.CallAPI}
	}
	channel := &OneBotChannel{conn: conn}
	return oneBotWriteTestCaller{&channel.writeMu, &channel.pending, channel.CallAPI}
}

func oneBotWriteTestPending(pending *sync.Map) int {
	count := 0
	pending.Range(func(_, _ any) bool {
		count++
		return true
	})
	return count
}

func waitOneBotWriteTestResult(t *testing.T, done <-chan error) {
	t.Helper()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("CallAPI error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("CallAPI did not return after cancellation")
	}
}

func TestOneBotCallAPIWriteCancellation(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		name := "forward"
		if reverse {
			name = "reverse"
		}
		t.Run(name, func(t *testing.T) {
			for _, locked := range []bool{false, true} {
				name := "already_canceled"
				if locked {
					name = "waiting_for_lock"
				}
				t.Run(name, func(t *testing.T) {
					conn, tracked := newOneBotWriteTestConn(t)
					caller := newOneBotWriteTestCaller(conn, reverse)
					ctx, cancel := context.WithCancel(context.Background())
					defer cancel()
					if locked {
						caller.mu.Lock()
						defer func() {
							if locked {
								caller.mu.Unlock()
							}
						}()
					} else {
						cancel()
					}
					done := make(chan error, 1)
					go func() {
						_, err := caller.call(ctx, "send_group_msg", map[string]any{"group_id": 12345, "message": "canceled"})
						done <- err
					}()
					if locked {
						deadline := time.Now().Add(time.Second)
						for oneBotWriteTestPending(caller.pending) == 0 && time.Now().Before(deadline) {
							time.Sleep(time.Millisecond)
						}
						if oneBotWriteTestPending(caller.pending) != 1 {
							t.Fatal("CallAPI did not register pending request")
						}
						cancel()
						select {
						case err := <-done:
							if !errors.Is(err, context.Canceled) {
								t.Errorf("CallAPI error = %v, want context.Canceled", err)
							}
							if count := oneBotWriteTestPending(caller.pending); count != 0 {
								t.Errorf("pending requests after cancellation = %d, want 0", count)
							}
							caller.mu.Unlock()
							locked = false
						case <-time.After(250 * time.Millisecond):
							t.Error("CallAPI waited for the write lock after context cancellation")
							caller.mu.Unlock()
							locked = false
							waitOneBotWriteTestResult(t, done)
						}
					} else {
						waitOneBotWriteTestResult(t, done)
					}
					if count := oneBotWriteTestPending(caller.pending); count != 0 {
						t.Errorf("pending requests = %d, want 0", count)
					}
					if writes, deadline := tracked.snapshot(); writes != 0 || !deadline.IsZero() {
						t.Errorf("canceled CallAPI touched connection: writes=%d deadline=%v", writes, deadline)
					}
					if tracked.isClosed() {
						t.Fatal("cancellation closed healthy connection")
					}
					if err := writeOneBotJSON(context.Background(), conn, caller.mu, map[string]any{"action": "get_login_info"}); err != nil {
						t.Fatalf("healthy connection unusable after cancellation: %v", err)
					}
				})
			}
		})
	}
}

func TestOneBotCallAPIWriteFailureClosesConnection(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		name := "forward"
		if reverse {
			name = "reverse"
		}
		t.Run(name, func(t *testing.T) {
			conn, tracked := newOneBotWriteTestConn(t)
			caller := newOneBotWriteTestCaller(conn, reverse)
			writeErr := &net.OpError{Op: "write", Net: "tcp", Err: context.DeadlineExceeded}
			tracked.mu.Lock()
			tracked.writeErr = writeErr
			tracked.mu.Unlock()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			_, err := caller.call(ctx, "set_input_status", map[string]any{"user_id": 12345, "event_type": 1})
			if err == nil || err.Error() != writeErr.Error() {
				t.Fatalf("CallAPI error = %v, want injected write failure", err)
			}
			if !tracked.isClosed() {
				t.Error("failed write left corrupted connection open")
			}
			if count := oneBotWriteTestPending(caller.pending); count != 0 {
				t.Errorf("pending requests = %d, want 0", count)
			}
		})
	}
}

func TestOneBotCallAPIWriteDeadline(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		name := "forward"
		if reverse {
			name = "reverse"
		}
		t.Run(name, func(t *testing.T) {
			for _, timeout := range []time.Duration{time.Second, time.Minute} {
				t.Run(timeout.String(), func(t *testing.T) {
					conn, tracked := newOneBotWriteTestConn(t)
					caller := newOneBotWriteTestCaller(conn, reverse)
					ctx, cancel := context.WithTimeout(context.Background(), timeout)
					defer cancel()
					started := time.Now()
					done := make(chan error, 1)
					go func() {
						_, err := caller.call(ctx, "get_login_info", nil)
						done <- err
					}()
					select {
					case <-tracked.written:
					case <-time.After(time.Second):
						t.Fatal("CallAPI did not write request")
					}
					_, deadline := tracked.snapshot()
					// 写超时始终固定，短 ctx 也不能压缩，否则排队耗尽预算会关掉共享连接。
					if deadline.Before(started.Add(oneBotWriteTimeout)) || deadline.After(time.Now().Add(oneBotWriteTimeout)) {
						t.Errorf("write deadline = %v, want default timeout %v", deadline, oneBotWriteTimeout)
					}
					cancel()
					waitOneBotWriteTestResult(t, done)
					if count := oneBotWriteTestPending(caller.pending); count != 0 {
						t.Errorf("pending requests = %d, want 0", count)
					}
				})
			}
		})
	}
}

// 排队耗掉调用方几乎全部预算时，写超时仍应是完整的 oneBotWriteTimeout。
func TestOneBotWriteDeadlineIgnoresNearlyExpiredContext(t *testing.T) {
	conn, tracked := newOneBotWriteTestConn(t)
	caller := newOneBotWriteTestCaller(conn, false)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	started := time.Now()
	done := make(chan error, 1)
	go func() {
		_, err := caller.call(ctx, "send_typing", nil)
		done <- err
	}()
	select {
	case <-tracked.written:
	case <-time.After(time.Second):
		t.Fatal("CallAPI did not write request")
	}
	if _, deadline := tracked.snapshot(); deadline.Before(started.Add(oneBotWriteTimeout)) {
		t.Errorf("write deadline = %v, shortened below %v", deadline, oneBotWriteTimeout)
	}
	cancel()
	waitOneBotWriteTestResult(t, done)
}
