// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"errors"
	"log"
	"strings"
	"time"
)

// 老配置里可能还留着已下线平台（见 retiredPlatformNames）的机器人。它们照常加载、
// 照常在 WebUI 里列出，只是不建连：配置集里一台起不来，不能把其它机器人一起拖下水。
// 通道工厂给它们一个占位通道，状态里写明原因，界面和日志都能看到。

// errPlatformUnavailable 是占位通道收发时返回的错误。
var errPlatformUnavailable = errors.New("assistant: platform is not available in this version")

// unsupportedPlatformChannel 是不受支持平台的占位通道：不连接、不收发，只报告原因。
type unsupportedPlatformChannel struct {
	platform string
	reason   string
	since    time.Time
}

func newUnsupportedPlatformChannel(cfg BotConfig) *unsupportedPlatformChannel {
	reason := UnsupportedPlatformMessage(cfg.Platform)
	log.Printf("diana 机器人「%s」(%s) 未接入：%s", strings.TrimSpace(cfg.Name), strings.TrimSpace(cfg.ID), reason)
	return &unsupportedPlatformChannel{platform: NormalizePlatformID(cfg.Platform), reason: reason, since: time.Now()}
}

// Connect 一直挂到运行时停止。立即返回错误会让 MultiChannel 按退避反复重连、
// 每次都打一行日志，而这种配置重试多少次都不会变好。
func (c *unsupportedPlatformChannel) Connect(ctx context.Context, _ EventHandler) error {
	<-ctx.Done()
	return ctx.Err()
}

func (c *unsupportedPlatformChannel) Send(context.Context, OutgoingMessage) error {
	return errPlatformUnavailable
}

func (c *unsupportedPlatformChannel) CallAPI(context.Context, string, map[string]any) (map[string]any, error) {
	return nil, errPlatformUnavailable
}

func (c *unsupportedPlatformChannel) Status() ChannelStatus {
	return ChannelStatus{Platform: c.platform, LastError: c.reason, UpdatedAt: c.since}
}

func (c *unsupportedPlatformChannel) Close() error { return nil }

// profileRunnable 判断这台机器人在当前版本里能不能跑起来：平台不在注册表里的
// （包括已下线的）一律当作未启用处理，不参与启动校验和入站调度。
func profileRunnable(cfg BotConfig) bool {
	return ValidatePlatform(cfg.Platform) == nil
}
