// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"errors"
	"strings"
	"time"
)

// 内置机器人（PlatformLocal）不接任何聊天平台：人设、模型、记忆都和平台机器人一样，
// 只是说话的地方只有控制台聊天页、语音通话和本地群。这些地方的回复在发送的最后
// 一步交给页面（deliverToWebChat），根本走不到通道，所以通道只需要报「在线」，
// 真有什么想往平台上发的（主动提醒、订阅推送、跨平台转发）一律明确报错，不静默吞掉。

var errLocalBotNoPlatform = errors.New("内置机器人不接聊天平台，只能在控制台聊天页、电话和本地群里说话")

type localChannel struct {
	profileID string
	selfID    string
	since     time.Time
}

func newLocalChannel(cfg BotConfig) *localChannel {
	return &localChannel{profileID: strings.TrimSpace(cfg.ID), selfID: localBotSelfID(cfg), since: time.Now()}
}

// localBotSelfID 是内置机器人在历史里的账号：填了机器人账号就用它，否则按机器人 ID 编一个，
// 保证它在本地群里的发言能和别的成员区分开。
func localBotSelfID(cfg BotConfig) string {
	if account := strings.TrimSpace(cfg.BotAccount); account != "" {
		return account
	}
	return "local-" + strings.TrimSpace(cfg.ID)
}

// Connect 没有要连的东西，挂到运行时停止。
func (c *localChannel) Connect(ctx context.Context, _ EventHandler) error {
	<-ctx.Done()
	return ctx.Err()
}

func (c *localChannel) Send(context.Context, OutgoingMessage) error {
	return errLocalBotNoPlatform
}

func (c *localChannel) CallAPI(context.Context, string, map[string]any) (map[string]any, error) {
	return nil, errLocalBotNoPlatform
}

func (c *localChannel) Status() ChannelStatus {
	return ChannelStatus{
		ProfileID:            c.profileID,
		Platform:             PlatformLocal,
		Connected:            true,
		AccountStatusKnown:   true,
		AccountOnline:        true,
		AccountGood:          true,
		AccountStatusMessage: "内置机器人，只在 Diana 里",
		SelfID:               c.selfID,
		UpdatedAt:            c.since,
	}
}

func (c *localChannel) Close() error { return nil }
