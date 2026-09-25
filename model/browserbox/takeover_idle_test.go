// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package browserbox

import (
	"context"
	"sync"
	"testing"
	"time"
)

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

// newIdleTestManager 换上假时钟；定时器期限设得很长，免得真定时器在测试中途插一脚，
// 闲置判断由测试直接调 releaseIdleTakeover 驱动。
func newIdleTestManager(t *testing.T) (*Manager, *fakeClock) {
	t.Helper()
	manager := New(context.Background(), &memoryStore{}, t.TempDir())
	clock := &fakeClock{now: time.Date(2026, 9, 25, 10, 0, 0, 0, time.UTC)}
	manager.mu.Lock()
	manager.now = clock.Now
	manager.takeoverLeave = time.Hour
	manager.mu.Unlock()
	t.Cleanup(func() { manager.Bot("bot-a").SetTakeover(false) })
	return manager, clock
}

// 接管后闲置满期限就自动交还，模型那一侧恢复可用，并通知 WebUI 记一条。
func TestTakeoverIdleReleases(t *testing.T) {
	manager, clock := newIdleTestManager(t)
	var released []string
	manager.OnAutoRelease(func(botID, _ string, idle time.Duration) {
		if idle != TakeoverIdleTimeout {
			t.Errorf("回调带的期限不对：%s", idle)
		}
		released = append(released, botID)
	})
	bot := manager.Bot("bot-a")
	bot.SetTakeover(true)

	clock.Advance(TakeoverIdleTimeout - time.Second)
	if manager.releaseIdleTakeover("bot-a") || !bot.Takeover() {
		t.Fatal("还没闲够就不该交还")
	}
	clock.Advance(time.Second)
	if !manager.releaseIdleTakeover("bot-a") || bot.Takeover() {
		t.Fatal("闲置满期限应自动交还")
	}
	if len(released) != 1 || released[0] != "bot-a" {
		t.Fatalf("自动交还应回调一次，实际 %v", released)
	}
	if manager.releaseIdleTakeover("bot-a") {
		t.Fatal("已经交还过就不该再交还一次")
	}
}

// 接管期间有意操作要把闲置期限往后推：人还在填表，不能到点就被收走。
func TestTakeoverTouchPostponesIdleRelease(t *testing.T) {
	manager, clock := newIdleTestManager(t)
	bot := manager.Bot("bot-a")
	bot.SetTakeover(true)

	clock.Advance(TakeoverIdleTimeout - time.Second)
	bot.TouchTakeover()
	clock.Advance(2 * time.Second)
	if manager.releaseIdleTakeover("bot-a") || !bot.Takeover() {
		t.Fatal("刚操作过就不该交还")
	}
	clock.Advance(TakeoverIdleTimeout)
	if !manager.releaseIdleTakeover("bot-a") {
		t.Fatal("操作之后再闲置满期限应交还")
	}
}

// 没在接管时碰一下不该打开接管。
func TestTouchWithoutTakeoverIsNoop(t *testing.T) {
	manager, _ := newIdleTestManager(t)
	manager.Bot("bot-a").TouchTakeover()
	if manager.Bot("bot-a").Takeover() {
		t.Fatal("TouchTakeover 不该打开接管")
	}
}

// 真定时器也要能自己把接管交还，不靠有人来读状态。
func TestTakeoverIdleTimerFires(t *testing.T) {
	manager := New(context.Background(), &memoryStore{}, t.TempDir())
	manager.mu.Lock()
	manager.takeoverIdle = 20 * time.Millisecond
	manager.mu.Unlock()
	done := make(chan string, 1)
	manager.OnAutoRelease(func(botID, _ string, _ time.Duration) { done <- botID })
	manager.Bot("bot-a").SetTakeover(true)
	select {
	case id := <-done:
		if id != "bot-a" || manager.Bot("bot-a").Takeover() {
			t.Fatalf("定时器交还的机器人不对或没交还：%s", id)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("闲置定时器没有触发")
	}
}

// 手动交还后定时器要撤掉，不能事后再回调一次「自动交还」。
func TestManualReleaseCancelsIdleTimer(t *testing.T) {
	manager := New(context.Background(), &memoryStore{}, t.TempDir())
	manager.mu.Lock()
	manager.takeoverIdle = 20 * time.Millisecond
	manager.mu.Unlock()
	fired := make(chan struct{}, 1)
	manager.OnAutoRelease(func(string, string, time.Duration) { fired <- struct{}{} })
	manager.Bot("bot-a").SetTakeover(true)
	manager.Bot("bot-a").SetTakeover(false)
	select {
	case <-fired:
		t.Fatal("手动交还后不该再自动交还")
	case <-time.After(100 * time.Millisecond):
	}
}

// 接管中的人离开画面（最后一条画面连接断开）满宽限还没回来，就交还给机器人，记下原因。
func TestTakeoverReleasedAfterViewerLeaves(t *testing.T) {
	manager := New(context.Background(), &memoryStore{}, t.TempDir())
	manager.takeoverLeave = 30 * time.Millisecond
	type release struct {
		bot, reason string
	}
	released := make(chan release, 1)
	manager.OnAutoRelease(func(botID, reason string, _ time.Duration) { released <- release{botID, reason} })
	bot := manager.Bot("bot-a")
	t.Cleanup(func() { bot.SetTakeover(false) })

	detach := bot.AttachViewer()
	bot.SetTakeover(true)
	time.Sleep(80 * time.Millisecond)
	if !bot.Takeover() {
		t.Fatal("还有人在看画面，不该交还")
	}
	detach()
	detach() // 重复调用只算一次离开
	select {
	case got := <-released:
		if got.bot != "bot-a" || got.reason != AutoReleaseLeft {
			t.Fatalf("交还回调参数不对：%+v", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("人离开画面之后没有自动交还")
	}
	if bot.Takeover() {
		t.Fatal("交还之后接管应当关掉")
	}
}

// 画面断开后很快又连回来（断线重连、代理掐掉长连接）不算离开，接管保留。
func TestViewerReturningWithinGraceKeepsTakeover(t *testing.T) {
	manager := New(context.Background(), &memoryStore{}, t.TempDir())
	manager.takeoverLeave = 60 * time.Millisecond
	manager.OnAutoRelease(func(string, string, time.Duration) { t.Error("回来了就不该交还") })
	bot := manager.Bot("bot-a")
	t.Cleanup(func() {
		manager.OnAutoRelease(nil)
		bot.SetTakeover(false)
	})

	first := bot.AttachViewer()
	bot.SetTakeover(true)
	first()
	time.Sleep(20 * time.Millisecond)
	second := bot.AttachViewer()
	defer second()
	time.Sleep(120 * time.Millisecond)
	if !bot.Takeover() {
		t.Fatal("宽限内连回来了，接管应当保留")
	}
}

// 两个窗口都开着画面，关掉一个不算离开。
func TestTakeoverKeptWhileAnotherViewerStays(t *testing.T) {
	manager := New(context.Background(), &memoryStore{}, t.TempDir())
	manager.takeoverLeave = 30 * time.Millisecond
	bot := manager.Bot("bot-a")
	t.Cleanup(func() { bot.SetTakeover(false) })

	a := bot.AttachViewer()
	b := bot.AttachViewer()
	defer b()
	bot.SetTakeover(true)
	a()
	time.Sleep(80 * time.Millisecond)
	if !bot.Takeover() {
		t.Fatal("另一个窗口还在看，不该交还")
	}
}
