// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/SuInk/diana/model/agent"
	"github.com/SuInk/diana/model/desktopctl"
)

// desktopConfirmationNoticeTimeout 是发一条「等你确认」私聊的期限。
const desktopConfirmationNoticeTimeout = 15 * time.Second

// desktopControlOwnerBridge 给这台机器人发起的桌面指令挂上提醒：执行器里新出现
// 一条待确认的写操作时，私聊告诉主人去控制台确认。提醒只是通知，确认仍然只能在
// 控制台完成；模型那边的工具调用同时停在执行器里等结果。
type desktopControlOwnerBridge struct {
	agent.DesktopControlBridge
	runtime   *Runtime
	profileID string
}

func (b desktopControlOwnerBridge) Dispatch(ctx context.Context, cmd desktopctl.Command) (desktopctl.Result, error) {
	return b.DesktopControlBridge.Dispatch(desktopctl.WithConfirmationNotifier(ctx, b.notify), cmd)
}

func (b desktopControlOwnerBridge) notify(ctx context.Context, req desktopctl.ConfirmationRequest) {
	ctx, cancel := context.WithTimeout(ctx, desktopConfirmationNoticeTimeout)
	defer cancel()
	if err := b.runtime.notifyOwnerDesktopConfirmation(ctx, b.profileID, desktopConfirmationNoticeText(req, time.Now())); err != nil {
		log.Printf("diana: desktop control: 私聊提醒主人确认失败: %v", err)
	}
}

// notifyOwnerDesktopConfirmation 往主人在平台上的私聊发提醒；没填主人账号时
// 只有 WebUI 能看到待确认操作，不发。
func (r *Runtime) notifyOwnerDesktopConfirmation(ctx context.Context, profileID, text string) error {
	if r.desktopConfirmationNotify != nil {
		return r.desktopConfirmationNotify(ctx, profileID, text)
	}
	cfg, event, err := r.ownerChatEvent(profileID)
	if err != nil {
		return err
	}
	if strings.TrimSpace(cfg.OwnerID) == "" {
		return errors.New("机器人还没填主人账号")
	}
	event.webChat = false
	event.Time = time.Now().Unix()
	return r.sendSubscriberNotice(ctx, event, text)
}

func desktopConfirmationNoticeText(req desktopctl.ConfirmationRequest, now time.Time) string {
	app := firstNonEmpty(req.AppName, req.Command.ExpectedBundleID, "目标应用")
	var action string
	switch req.Command.Op {
	case desktopctl.OpWindowType:
		action = "输入「" + truncateRunes(req.Command.Text, 40) + "」"
	case desktopctl.OpWindowKey:
		action = "按 " + req.Command.Key
	case desktopctl.OpWindowScroll:
		action = "滚动窗口"
	default:
		switch {
		case strings.TrimSpace(req.TargetLabel) != "":
			action = "点击「" + truncateRunes(req.TargetLabel, 30) + "」"
		case req.Command.X != nil && req.Command.Y != nil:
			action = fmt.Sprintf("点击窗口内 (%.0f, %.0f)", *req.Command.X, *req.Command.Y)
		default:
			action = "点击"
		}
	}
	minutes := int((req.ExpiresAt.Sub(now) + 30*time.Second) / time.Minute)
	if minutes < 1 {
		minutes = 1
	}
	return fmt.Sprintf("【桌面控制】我想在「%s」里%s，正在等你确认：到 WebUI「设置 → 桌面控制」核对画面后点「确认这条操作」，%d 分钟内有效，过期就不执行。", app, action, minutes)
}
