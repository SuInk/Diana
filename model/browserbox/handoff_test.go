// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package browserbox

import (
	"context"
	"testing"
	"time"
)

func newHandoffTestBot(t *testing.T) (*Manager, *Bot) {
	t.Helper()
	manager := New(context.Background(), &memoryStore{}, t.TempDir())
	manager.mu.Lock()
	manager.settings.Enabled = true
	manager.mu.Unlock()
	return manager, manager.Bot("bot-a")
}

func waitOutcome(t *testing.T, outcomes <-chan string, want string) {
	t.Helper()
	select {
	case got := <-outcomes:
		if got != want {
			t.Fatalf("交接结果应当是 %s，得到 %s", want, got)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("等不到交接结果 %s", want)
	}
}

// 主人点「完成」：回调拿到 done，接管顺手交还，状态里不再挂着这条。
func TestHandoffDoneReleasesTakeover(t *testing.T) {
	_, bot := newHandoffTestBot(t)
	outcomes := make(chan string, 1)
	id, err := bot.RequestHandoff("登录小红书", time.Minute, func(outcome string) { outcomes <- outcome })
	if err != nil {
		t.Fatal(err)
	}
	if status := bot.Status(); status.Handoff == nil || status.Handoff.Reason != "登录小红书" {
		t.Fatalf("状态里应当看得到这条交接：%+v", status.Handoff)
	}
	bot.SetTakeover(true)
	if bot.ResolveHandoff("别的", HandoffDone) {
		t.Fatal("ID 对不上不该算数")
	}
	if !bot.ResolveHandoff(id, HandoffDone) {
		t.Fatal("交接应当能完成")
	}
	waitOutcome(t, outcomes, HandoffDone)
	if bot.Takeover() {
		t.Fatal("完成之后浏览器应当回到机器人手里")
	}
	if _, pending := bot.PendingHandoff(); pending || bot.ResolveHandoff(id, HandoffDone) {
		t.Fatal("处理过的交接不该还挂着，也不该能再处理一次")
	}
}

// 没人处理就到期；新的交接顶掉旧的；浏览器停了就取消。
func TestHandoffExpiresReplacesAndCancels(t *testing.T) {
	manager, bot := newHandoffTestBot(t)
	expired := make(chan string, 1)
	if _, err := bot.RequestHandoff("扫码", 30*time.Millisecond, func(outcome string) { expired <- outcome }); err != nil {
		t.Fatal(err)
	}
	waitOutcome(t, expired, HandoffExpired)

	first := make(chan string, 1)
	if _, err := bot.RequestHandoff("登录", time.Minute, func(outcome string) { first <- outcome }); err != nil {
		t.Fatal(err)
	}
	second := make(chan string, 1)
	if _, err := bot.RequestHandoff("输验证码", time.Minute, func(outcome string) { second <- outcome }); err != nil {
		t.Fatal(err)
	}
	waitOutcome(t, first, HandoffCancelled)
	if pending, _ := bot.PendingHandoff(); pending.Reason != "输验证码" {
		t.Fatalf("应当只剩新的那条：%+v", pending)
	}
	manager.mu.Lock()
	manager.instanceLocked("bot-a").cmd = nil
	manager.mu.Unlock()
	manager.stop("bot-a")
	waitOutcome(t, second, HandoffCancelled)
}

// 内置浏览器关着、或者没说请主人做什么，都不登记。
func TestHandoffNeedsReasonAndEnabledBrowser(t *testing.T) {
	_, bot := newHandoffTestBot(t)
	if _, err := bot.RequestHandoff("  ", time.Minute, nil); err == nil {
		t.Fatal("没写请主人做什么应当被拒")
	}
	off := New(context.Background(), &memoryStore{}, t.TempDir()).Bot("bot-a")
	if _, err := off.RequestHandoff("登录", time.Minute, nil); err == nil {
		t.Fatal("内置浏览器关着时不该登记")
	}
}
