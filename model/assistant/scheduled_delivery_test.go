// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// fakeOneBotClient 是连进反向监听器的接入端：回应每个 action，记下发出来的消息。
type fakeOneBotClient struct {
	mu        sync.Mutex
	failSends bool
	sends     []string
	conn      *websocket.Conn
	done      chan struct{}
}

func (c *fakeOneBotClient) connect(t *testing.T, endpoint string) {
	t.Helper()
	conn := dialReverse(t, endpoint)
	done := make(chan struct{})
	c.mu.Lock()
	c.conn, c.done = conn, done
	c.mu.Unlock()
	go func() {
		defer close(done)
		for {
			var frame struct {
				Action string         `json:"action"`
				Params map[string]any `json:"params"`
				Echo   string         `json:"echo"`
			}
			if err := conn.ReadJSON(&frame); err != nil {
				return
			}
			reply := map[string]any{"status": "ok", "retcode": 0, "data": map[string]any{"message_id": 1}, "echo": frame.Echo}
			if strings.HasPrefix(frame.Action, "send_") {
				body, _ := json.Marshal(frame.Params)
				c.mu.Lock()
				c.sends = append(c.sends, frame.Action+" "+string(body))
				fail := c.failSends
				c.mu.Unlock()
				if fail {
					reply = map[string]any{"status": "failed", "retcode": 1200, "echo": frame.Echo}
				}
			}
			if err := conn.WriteJSON(reply); err != nil {
				return
			}
		}
	}()
}

func (c *fakeOneBotClient) disconnect() {
	c.mu.Lock()
	conn, done := c.conn, c.done
	c.mu.Unlock()
	_ = conn.Close()
	<-done
}

func (c *fakeOneBotClient) sent() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.sends...)
}

func scheduledDeliveryRuntime(t *testing.T, items []Reminder, limit time.Duration) (*Runtime, *OneBotReverseServer, string, *stubReminderStore) {
	t.Helper()
	server, endpoint := startReverseTestServer(t)
	store := &stubReminderStore{items: items}
	rt := NewRuntime(BotConfig{OwnerID: "10001"}, server, NewPluginManager(), nil, store, nil, nil)
	rt.mu.Lock()
	rt.scheduledDeliveryWait = scheduledDeliveryWaitTiming{limit: limit, poll: 10 * time.Millisecond}
	rt.mu.Unlock()
	return rt, server, endpoint, store
}

func snapshotReminders(rt *Runtime, store *stubReminderStore) map[string]Reminder {
	rt.reminderMu.Lock()
	defer rt.reminderMu.Unlock()
	out := map[string]Reminder{}
	for _, item := range store.Reminders() {
		out[item.ID] = item
	}
	return out
}

func claimedReminders(rt *Runtime) int {
	rt.reminderMu.Lock()
	defer rt.reminderMu.Unlock()
	return len(rt.activeReminders)
}

// startupReminders 是重启前就到点、还没送出去的三种投递：一次性提醒（私聊）、定时
// 查询和 RSS 订阅（都已经把要发的内容存进了 PendingDelivery，发到群里）。
func startupReminders(now time.Time) []Reminder {
	return []Reminder{
		{
			ID: "remind-early", Kind: ReminderKindMessage, OwnerID: "10001", UserID: "10001",
			Message: "去听课", TriggerAt: now.Add(-time.Minute), CreatedAt: now.Add(-time.Hour),
		},
		{
			ID: "query-early", Kind: ReminderKindQuery, OwnerID: "10001", GroupID: "12345", UserID: "10001",
			Message: "查询最新公告", TriggerAt: now.Add(-time.Minute), IntervalSeconds: int64((6 * time.Hour) / time.Second),
			PendingDelivery: "最新公告没有变化", PendingSince: now.Add(-10 * time.Minute), CreatedAt: now.Add(-7 * time.Hour),
		},
		{
			ID: "rss-early", Kind: ReminderKindRSSWatch, OwnerID: "10001", GroupID: "12346", UserID: "10001",
			FeedURL: "https://example.com/feed.xml", TriggerAt: now.Add(-time.Minute), IntervalSeconds: int64(time.Hour / time.Second),
			PendingDelivery: "新文章：连接就绪之后再发", PendingSince: now.Add(-10 * time.Minute), CreatedAt: now.Add(-7 * time.Hour),
		},
	}
}

func assertNoReminderFailure(t *testing.T, items map[string]Reminder) {
	t.Helper()
	for id, item := range items {
		if item.ConsecutiveFailures != 0 || item.LastError != "" || !item.FailureAlertedAt.IsZero() {
			t.Fatalf("%s 被记成了失败：failures=%d err=%q", id, item.ConsecutiveFailures, item.LastError)
		}
	}
}

// TestScheduledDeliveryWaitsForReverseWebSocketThenSendsOnce 复现反向 WebSocket 启动时
// 第一轮调度跑在接入端连上之前：提醒和订阅要等连上再发，不记失败；每条只发一次，
// 断线重连也不重发。
func TestScheduledDeliveryWaitsForReverseWebSocketThenSendsOnce(t *testing.T) {
	rt, server, endpoint, store := scheduledDeliveryRuntime(t, startupReminders(time.Now()), time.Minute)
	ctx, stop := context.WithCancel(context.Background())
	loopDone := make(chan struct{})
	go func() {
		defer close(loopDone)
		rt.runReminderLoop(ctx)
	}()
	defer func() {
		stop()
		<-loopDone
		waitForCondition(t, 5*time.Second, func() bool { return claimedReminders(rt) == 0 })
	}()

	// 调度每秒一拍：三条都已认领、都在等连接，期间不算失败。
	waitForCondition(t, 5*time.Second, func() bool { return claimedReminders(rt) == 3 })
	time.Sleep(100 * time.Millisecond)
	assertNoReminderFailure(t, snapshotReminders(rt, store))

	client := &fakeOneBotClient{}
	client.connect(t, endpoint)
	waitForCondition(t, 5*time.Second, func() bool { return len(client.sent()) == 3 })
	waitForCondition(t, 5*time.Second, func() bool { return claimedReminders(rt) == 0 })
	joined := strings.Join(client.sent(), "\n")
	for _, want := range []string{"去听课", "最新公告没有变化", "连接就绪之后再发"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("没发出 %q：%s", want, joined)
		}
	}
	items := snapshotReminders(rt, store)
	assertNoReminderFailure(t, items)
	if items["remind-early"].LastRunAt.IsZero() {
		t.Fatalf("一次性提醒没标成已送达：%#v", items["remind-early"])
	}
	for _, id := range []string{"query-early", "rss-early"} {
		if items[id].PendingDelivery != "" || !items[id].TriggerAt.After(time.Now()) {
			t.Fatalf("%s 没按送达收尾：%#v", id, items[id])
		}
	}

	// 断线重连：什么都不该再发。
	client.disconnect()
	waitForCondition(t, 5*time.Second, func() bool { return !server.Status().Connected })
	client.connect(t, endpoint)
	waitForCondition(t, 5*time.Second, func() bool { return server.Status().Connected })
	time.Sleep(1500 * time.Millisecond)
	if got := client.sent(); len(got) != 3 {
		t.Fatalf("重连后重复发送：%#v", got)
	}
}

// TestScheduledDeliveryReleasesClaimWhenConnectionStaysDown 等满一段仍没连上时，这次
// 认领放掉，不计连败、不推迟、不告警，存好的内容原样留着；连上之后的下一轮只发一次。
func TestScheduledDeliveryReleasesClaimWhenConnectionStaysDown(t *testing.T) {
	now := time.Now()
	rt, _, endpoint, store := scheduledDeliveryRuntime(t, startupReminders(now), 50*time.Millisecond)
	ctx := context.Background()

	rt.fireDueReminders(ctx)
	items := snapshotReminders(rt, store)
	assertNoReminderFailure(t, items)
	for _, id := range []string{"remind-early", "query-early", "rss-early"} {
		if items[id].TriggerAt.After(now) {
			t.Fatalf("%s 被推迟了：%v", id, items[id].TriggerAt)
		}
	}
	if !items["remind-early"].LastRunAt.IsZero() || items["query-early"].PendingDelivery == "" || items["rss-early"].PendingDelivery == "" {
		t.Fatalf("没送达的内容没留住：%#v", items)
	}

	client := &fakeOneBotClient{}
	client.connect(t, endpoint)
	waitForCondition(t, 5*time.Second, func() bool { return rt.channel.Status().Connected })
	rt.fireDueReminders(ctx)
	rt.fireDueReminders(ctx)
	if got := client.sent(); len(got) != 3 {
		t.Fatalf("sends = %#v", got)
	}
	assertNoReminderFailure(t, snapshotReminders(rt, store))
}

// TestScheduledDeliveryRealFailureKeepsRetryAndAlert 连接在、对端回了失败：这是真的发
// 失败，照旧计连败、按投递退避推迟 30 分钟，并发失败通知。
func TestScheduledDeliveryRealFailureKeepsRetryAndAlert(t *testing.T) {
	now := time.Now()
	items := startupReminders(now)[:1]
	rt, _, endpoint, store := scheduledDeliveryRuntime(t, items, time.Minute)
	client := &fakeOneBotClient{failSends: true}
	client.connect(t, endpoint)
	waitForCondition(t, 5*time.Second, func() bool { return rt.channel.Status().Connected })

	started := time.Now()
	rt.fireDueReminders(context.Background())
	if elapsed := time.Since(started); elapsed > 10*time.Second {
		t.Fatalf("真失败不该等连接：%s", elapsed)
	}
	failed := snapshotReminders(rt, store)["remind-early"]
	if failed.ConsecutiveFailures != 1 || failed.LastError == "" || !failed.LastRunAt.IsZero() {
		t.Fatalf("真失败没走原来的重试：%#v", failed)
	}
	if retryIn := time.Until(failed.TriggerAt); retryIn < 30*time.Minute-2*time.Second || retryIn > 36*time.Minute+2*time.Second {
		t.Fatalf("retry scheduled in %s", retryIn)
	}
	sends := client.sent()
	if len(sends) != 2 || !strings.Contains(sends[0], "去听课") || !strings.Contains(sends[1], "自动重试") {
		t.Fatalf("应当是一次提醒加一条失败通知：%#v", sends)
	}
}

func TestDeliveryNotReadyClassification(t *testing.T) {
	notConnected := newChannelNotConnectedError("diana: onebot reverse websocket is not connected")
	wrapped := &outboundSendError{Cause: notConnected}
	offline := offlineOutboundSendError("12345")
	real := &outboundSendError{Cause: errors.New("onebot api failed: status=failed retcode=1200")}
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"not connected", notConnected, true},
		{"wrapped not connected", wrapped, true},
		{"group gate offline", offline, true},
		{"repository stage", repositoryWatchStageFailure(repositoryWatchFailureStageDelivery, wrapped), true},
		{"all targets not ready", errors.Join(wrapped, offline), true},
		{"one target really failed", errors.Join(wrapped, real), false},
		{"real failure", real, false},
		{"disabled", ErrDeliveryTargetDisabled, false},
	}
	for _, tc := range cases {
		if got := deliveryNotReady(tc.err); got != tc.want {
			t.Fatalf("%s: deliveryNotReady = %v", tc.name, got)
		}
	}
	if notConnected.Error() != "diana: onebot reverse websocket is not connected" {
		t.Fatalf("原报错文字变了：%q", notConnected.Error())
	}
}
