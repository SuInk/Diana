// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"testing"
	"time"
)

// 反连监听器是进程内共享的一个实例，OneBot 和 Telegram 可以同时跑。激活 Telegram
// 那台之后触发的一次 OneBot 重连回填，把真实 QQ 群消息全写成了 Telegram——正文带着
// CQ 码和正数 QQ 群号，事件页却按 Telegram 分类。
//
// 身份要绑到产生这批消息的那个通道，不能跟着「当前激活配置」走。
func TestOneBotHistoryIdentityIgnoresActiveTelegramProfile(t *testing.T) {
	onebot := &recordingChannel{}
	telegram := &recordingChannel{}
	runtime := NewRuntime(BotConfig{ID: "tg", Platform: PlatformTelegram, BotAccount: "8100000001"},
		NewMultiChannel([]ChannelBinding{
			{ProfileID: "qq", Platform: PlatformOneBotV11, Channel: onebot},
			{ProfileID: "tg", Platform: PlatformTelegram, Channel: telegram},
		}), NewPluginManager(), nil, nil, nil, nil)
	runtime.SetProfiles(ProfileSet{
		ActiveID: "tg",
		Profiles: []BotConfig{
			{ID: "qq", Platform: PlatformOneBotV11, BotAccount: "10000001", Enabled: true},
			{ID: "tg", Platform: PlatformTelegram, BotAccount: "8100000001", Enabled: true},
		},
	})

	event := runtime.bindInboundEventIdentity(
		MessageEvent{Kind: EventKindGroup, GroupID: "765205730", UserID: "494942782"})

	if event.ProfileID != "qq" {
		t.Fatalf("ProfileID = %q，回填的 QQ 消息应当绑到 OneBot 那台", event.ProfileID)
	}
	if event.Platform != PlatformOneBotV11 {
		t.Fatalf("Platform = %q，不该被标成激活项的平台", event.Platform)
	}
	// self_id 同理：激活的是 Telegram 那台时，不能拿它的账号补给 QQ 消息。
	if account := runtime.oneBotBotAccount(); account != "10000001" {
		t.Fatalf("oneBotBotAccount = %q，应当是 OneBot 那台的账号", account)
	}
}

// 事件自带身份时不覆盖：活跃通道进来的消息 MultiChannel 已经打好标了。
func TestBindIdentityKeepsExplicitProfile(t *testing.T) {
	runtime := NewRuntime(BotConfig{ID: "tg", Platform: PlatformTelegram}, NewMultiChannel([]ChannelBinding{
		{ProfileID: "qq", Platform: PlatformOneBotV11, Channel: &recordingChannel{}},
		{ProfileID: "tg", Platform: PlatformTelegram, Channel: &recordingChannel{}},
	}), NewPluginManager(), nil, nil, nil, nil)

	event := runtime.bindInboundEventIdentityForPlatform(
		MessageEvent{Kind: EventKindGroup, ProfileID: "other", Platform: PlatformTelegram}, PlatformOneBotV11)
	if event.ProfileID != "other" || event.Platform != PlatformTelegram {
		t.Fatalf("自带身份被改写了：%q / %q", event.ProfileID, event.Platform)
	}
}

// 激活的本来就是 OneBot 那台时，行为和以前一样。
func TestOneBotHistoryIdentityUsesActiveProfileWhenItIsOneBot(t *testing.T) {
	runtime := NewRuntime(BotConfig{ID: "qq", Platform: PlatformOneBotV11, BotAccount: "10000001"},
		NewMultiChannel([]ChannelBinding{
			{ProfileID: "qq", Platform: PlatformOneBotV11, Channel: &recordingChannel{}},
		}), NewPluginManager(), nil, nil, nil, nil)

	event := runtime.bindInboundEventIdentityForPlatform(MessageEvent{Kind: EventKindGroup}, PlatformOneBotV11)
	if event.ProfileID != "qq" || event.Platform != PlatformOneBotV11 {
		t.Fatalf("ProfileID=%q Platform=%q", event.ProfileID, event.Platform)
	}
	if account := runtime.oneBotBotAccount(); account != "10000001" {
		t.Fatalf("oneBotBotAccount = %q", account)
	}
}

// 压根没有 OneBot 通道时宁可留空，也不要挂到一台不在这个平台上的机器人名下——
// 挂错了比没归属更难查：事件页会把它归到那台机器人下面，看起来完全正常。
func TestOneBotHistoryIdentityLeavesBlankWithoutOneBotChannel(t *testing.T) {
	runtime := NewRuntime(BotConfig{ID: "tg", Platform: PlatformTelegram, BotAccount: "8100000001"},
		NewMultiChannel([]ChannelBinding{
			{ProfileID: "tg", Platform: PlatformTelegram, Channel: &recordingChannel{}},
		}), NewPluginManager(), nil, nil, nil, nil)

	event := runtime.bindInboundEventIdentityForPlatform(MessageEvent{Kind: EventKindGroup}, PlatformOneBotV11)
	if event.ProfileID != "" || event.Platform != "" {
		t.Fatalf("没有 OneBot 通道时不该乱绑：%q / %q", event.ProfileID, event.Platform)
	}
	if account := runtime.oneBotBotAccount(); account != "" {
		t.Fatalf("oneBotBotAccount = %q，不该拿 Telegram 账号顶上", account)
	}
}

// 保存或激活配置会重建整套通道，Connect 被再调一次。旧那次醒来后不能把新那次的
// 连接关掉——监听器是进程内共享的一个实例，关错了接入端要等下一轮重连才恢复。
func TestReverseServerStaleConnectDoesNotCloseNewConnection(t *testing.T) {
	server := NewOneBotReverseServer(OneBotConfig{Endpoint: "/onebot/v11/ws"})

	oldCtx, cancelOld := context.WithCancel(context.Background())
	newCtx, cancelNew := context.WithCancel(context.Background())
	defer cancelNew()

	oldDone := make(chan struct{})
	go func() {
		defer close(oldDone)
		_ = server.Connect(oldCtx, func(context.Context, MessageEvent) error { return nil })
	}()
	// 等旧那次登记完再让新那次接管，模拟热重载的先后顺序。
	waitFor(t, func() bool {
		server.mu.RLock()
		defer server.mu.RUnlock()
		return server.connectGeneration == 1
	})

	newDone := make(chan struct{})
	go func() {
		defer close(newDone)
		_ = server.Connect(newCtx, func(context.Context, MessageEvent) error { return nil })
	}()
	waitFor(t, func() bool {
		server.mu.RLock()
		defer server.mu.RUnlock()
		return server.connectGeneration == 2
	})

	// 新那次接管之后，接入端连了上来。
	server.connMu.Lock()
	generationBefore := server.acceptGeneration
	server.connMu.Unlock()

	cancelOld()
	<-oldDone

	// 旧那次退出不该动 Close()：动了的话 acceptGeneration 会 +1，
	// 正在握手的接入端会被判成过期。
	server.connMu.RLock()
	generationAfter := server.acceptGeneration
	server.connMu.RUnlock()
	if generationAfter != generationBefore {
		t.Fatalf("旧 Connect 退出时关掉了新那次的连接：acceptGeneration %d → %d", generationBefore, generationAfter)
	}

	// 新那次自己退出时照常清理。
	cancelNew()
	<-newDone
	server.connMu.RLock()
	generationFinal := server.acceptGeneration
	server.connMu.RUnlock()
	if generationFinal == generationBefore {
		t.Fatal("当前那次 Connect 退出时应当照常 Close()")
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("等待条件超时")
}
