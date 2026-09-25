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
	manager.mu.Unlock()
	t.Cleanup(func() { manager.Bot("bot-a").SetTakeover(false) })
	return manager, clock
}

// 接管后闲置满期限就自动交还，模型那一侧恢复可用，并通知 WebUI 记一条。
func TestTakeoverIdleReleases(t *testing.T) {
	manager, clock := newIdleTestManager(t)
	var released []string
	manager.OnIdleRelease(func(botID string, idle time.Duration) {
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
	manager.OnIdleRelease(func(botID string, _ time.Duration) { done <- botID })
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
	manager.OnIdleRelease(func(string, time.Duration) { fired <- struct{}{} })
	manager.Bot("bot-a").SetTakeover(true)
	manager.Bot("bot-a").SetTakeover(false)
	select {
	case <-fired:
		t.Fatal("手动交还后不该再自动交还")
	case <-time.After(100 * time.Millisecond):
	}
}
