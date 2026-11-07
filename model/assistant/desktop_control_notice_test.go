// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/SuInk/diana/model/desktopctl"
)

// fakeDesktopBridge 模拟执行器：收到写操作时按 context 里挂的通知方报一条待确认。
type fakeDesktopBridge struct {
	req desktopctl.ConfirmationRequest
}

func (f *fakeDesktopBridge) Ready() bool                  { return true }
func (f *fakeDesktopBridge) Jobs() *desktopctl.JobManager { return nil }
func (f *fakeDesktopBridge) Dispatch(ctx context.Context, cmd desktopctl.Command) (desktopctl.Result, error) {
	if notify := desktopctl.ConfirmationNotifierFrom(ctx); notify != nil {
		req := f.req
		req.Command = cmd
		notify(ctx, req)
	}
	return desktopctl.Result{OK: true}, nil
}

func TestDesktopControlForTellsOwnerAboutPendingConfirmation(t *testing.T) {
	r := &Runtime{}
	r.SetDesktopControl(&fakeDesktopBridge{req: desktopctl.ConfirmationRequest{AppName: "文本编辑", ExpiresAt: time.Now().Add(desktopctl.ActionConfirmationWait)}})
	type notice struct{ profile, text string }
	got := make(chan notice, 1)
	r.desktopConfirmationNotify = func(_ context.Context, profileID, text string) error {
		got <- notice{profileID, text}
		return nil
	}
	if r.desktopControlFor(BotConfig{ID: "bot-a"}) != nil {
		t.Fatal("desktop tools registered without the bot switch")
	}
	if r.desktopControlFor(BotConfig{ID: "bot-a", AgentDesktopControlEnabled: true, AgentMode: AgentModeSafe}) != nil {
		t.Fatal("desktop tools registered in safe mode")
	}
	bridge := r.desktopControlFor(BotConfig{ID: "bot-a", AgentDesktopControlEnabled: true})
	if bridge == nil {
		t.Fatal("desktop tools missing with the bot switch on")
	}
	if _, err := bridge.Dispatch(context.Background(), desktopctl.Command{Op: desktopctl.OpWindowType, Text: "今日测试草稿"}); err != nil {
		t.Fatal(err)
	}
	select {
	case n := <-got:
		if n.profile != "bot-a" || !strings.Contains(n.text, "「文本编辑」里输入「今日测试草稿」") || !strings.Contains(n.text, "5 分钟内有效") {
			t.Fatalf("notice=%+v", n)
		}
	case <-time.After(time.Second):
		t.Fatal("owner was not told about the pending confirmation")
	}
}

func TestDesktopConfirmationNoticeText(t *testing.T) {
	now := time.Now()
	x, y := 120.4, 48.0
	cases := []struct {
		req  desktopctl.ConfirmationRequest
		want string
	}{
		{desktopctl.ConfirmationRequest{AppName: "Safari", TargetLabel: "发送", Command: desktopctl.Command{Op: desktopctl.OpWindowClick}, ExpiresAt: now.Add(5 * time.Minute)}, "「Safari」里点击「发送」"},
		{desktopctl.ConfirmationRequest{Command: desktopctl.Command{Op: desktopctl.OpWindowClick, ExpectedBundleID: "com.apple.TextEdit", X: &x, Y: &y}, ExpiresAt: now.Add(5 * time.Minute)}, "「com.apple.TextEdit」里点击窗口内 (120, 48)"},
		{desktopctl.ConfirmationRequest{AppName: "Notes", Command: desktopctl.Command{Op: desktopctl.OpWindowKey, Key: "cmd+s"}, ExpiresAt: now.Add(90 * time.Second)}, "里按 cmd+s，正在等你确认"},
		{desktopctl.ConfirmationRequest{AppName: "Notes", Command: desktopctl.Command{Op: desktopctl.OpWindowType, Text: strings.Repeat("长", 60)}, ExpiresAt: now.Add(5 * time.Minute)}, strings.Repeat("长", 40) + "...」"},
	}
	for _, c := range cases {
		if text := desktopConfirmationNoticeText(c.req, now); !strings.Contains(text, c.want) {
			t.Errorf("want %q in %q", c.want, text)
		}
	}
	if text := desktopConfirmationNoticeText(cases[2].req, now); !strings.Contains(text, "2 分钟内有效") {
		t.Errorf("90s should round to 2 minutes: %q", text)
	}
}
