// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"testing"
	"time"

	"github.com/SuInk/diana/model/applog"
)

func appLogActions(entries []applog.Entry) []string {
	actions := make([]string, 0, len(entries))
	for _, entry := range entries {
		actions = append(actions, entry.Action)
	}
	return actions
}

// 协调器只记第一个 OneBot 连接。第二个 OneBot 账号和其他平台的上线、掉线、账号
// 异常以前在运行日志里一条都没有，这里固定它们都能看到，且只在转折时各记一次。
func TestChannelWatchLogsEveryConnectionTransition(t *testing.T) {
	primary := &multiChannelProbe{status: ChannelStatus{Connected: true}}
	second := &multiChannelProbe{}
	telegram := &multiChannelProbe{}
	channel := NewMultiChannel([]ChannelBinding{
		{ProfileID: "qq-main", Platform: PlatformOneBotV11, Name: "主号", Channel: primary},
		{ProfileID: "qq-alt", Platform: PlatformOneBotV11, Name: "小号", Channel: second},
		{ProfileID: "tg", Platform: PlatformTelegram, Name: "电报", Channel: telegram},
	})
	r := NewRuntime(BotConfig{}, channel, NewPluginManager(), nil, nil, nil, nil)
	r.SetInboundEventStore(newMemoryInboundEventStore())
	logs := &captureAppLogs{}
	r.SetAppLogWriter(logs)
	states := map[string]*channelWatchState{}
	now := time.Now()

	// 刚启动都还没连上：不报断开。
	r.observeChannels(t.Context(), states, now)
	if entries := logs.entriesSnapshot(); len(entries) != 0 {
		t.Fatalf("startup logged %v", appLogActions(entries))
	}

	second.status = ChannelStatus{Connected: true}
	telegram.status = ChannelStatus{Connected: true}
	r.observeChannels(t.Context(), states, now)
	r.observeChannels(t.Context(), states, now)
	if got := countAppLogAction(logs.entriesSnapshot(), "channel_connected"); got != 2 {
		t.Fatalf("connected logs = %d, want 2 (primary belongs to the coordinator): %v", got, appLogActions(logs.entriesSnapshot()))
	}

	second.status = ChannelStatus{Connected: true, AccountStatusKnown: true, AccountOnline: false, AccountStatusMessage: "账号被风控"}
	telegram.status = ChannelStatus{}
	r.observeChannels(t.Context(), states, now)
	entries := logs.entriesSnapshot()
	if !hasAppLogAction(entries, "channel_account_offline") || !hasAppLogAction(entries, "channel_disconnected") {
		t.Fatalf("missing transitions: %v", appLogActions(entries))
	}
	for _, entry := range entries {
		if entry.Action == "channel_disconnected" && (entry.Target != "tg" || entry.Level != applog.LevelError) {
			t.Fatalf("disconnect entry = %#v", entry)
		}
		if entry.Action == "channel_account_offline" && entry.Detail != "账号被风控" {
			t.Fatalf("account entry = %#v", entry)
		}
	}

	second.status = ChannelStatus{Connected: true, AccountStatusKnown: true, AccountOnline: true, AccountGood: true}
	r.observeChannels(t.Context(), states, now)
	if !hasAppLogAction(logs.entriesSnapshot(), "channel_account_recovered") {
		t.Fatalf("missing recovery: %v", appLogActions(logs.entriesSnapshot()))
	}
	for _, entry := range logs.entriesSnapshot() {
		if entry.Target == "qq-main" && entry.Action != "channel_error" {
			t.Fatalf("primary connection logged twice: %#v", entry)
		}
	}
}

// 没有入站队列时协调器不跑，第一个连接也得有人记。
func TestChannelWatchCoversPrimaryWithoutInboundStore(t *testing.T) {
	primary := &multiChannelProbe{status: ChannelStatus{Connected: true}}
	channel := NewMultiChannel([]ChannelBinding{{ProfileID: "qq-main", Platform: PlatformOneBotV11, Channel: primary}})
	r := NewRuntime(BotConfig{}, channel, NewPluginManager(), nil, nil, nil, nil)
	logs := &captureAppLogs{}
	r.SetAppLogWriter(logs)
	r.observeChannels(t.Context(), map[string]*channelWatchState{}, time.Now())
	if !hasAppLogAction(logs.entriesSnapshot(), "channel_connected") {
		t.Fatalf("primary without store not logged: %v", appLogActions(logs.entriesSnapshot()))
	}
}

// 复用同一条连接的几台机器人只算一条连接，一次掉线不能按机器人数记好几遍。
func TestChannelWatchCountsSharedConnectionOnce(t *testing.T) {
	shared := &multiChannelProbe{status: ChannelStatus{Connected: true}}
	channel := NewMultiChannel([]ChannelBinding{
		{ConnectionID: "qq", ProfileID: "qq", Platform: PlatformOneBotV11, Channel: shared},
		{ConnectionID: "qq", ProfileID: "qq-persona", Platform: PlatformOneBotV11, Channel: shared},
	})
	r := NewRuntime(BotConfig{}, channel, NewPluginManager(), nil, nil, nil, nil)
	logs := &captureAppLogs{}
	r.SetAppLogWriter(logs)
	r.observeChannels(t.Context(), map[string]*channelWatchState{}, time.Now())
	if got := countAppLogAction(logs.entriesSnapshot(), "channel_connected"); got != 1 {
		t.Fatalf("shared connection logged %d times", got)
	}
}

// 连不上时通道几秒重试一次，错误文本还可能每次都不同；一分钟内同一条连接只记一条。
func TestChannelWatchThrottlesConnectionErrors(t *testing.T) {
	probe := &multiChannelProbe{status: ChannelStatus{LastError: "dial tcp: connection refused"}}
	channel := NewMultiChannel([]ChannelBinding{{ProfileID: "tg", Platform: PlatformTelegram, Channel: probe}})
	r := NewRuntime(BotConfig{}, channel, NewPluginManager(), nil, nil, nil, nil)
	logs := &captureAppLogs{}
	r.SetAppLogWriter(logs)
	states := map[string]*channelWatchState{}
	now := time.Now()
	r.observeChannels(t.Context(), states, now)
	r.observeChannels(t.Context(), states, now.Add(time.Second))
	probe.status.LastError = "dial tcp: i/o timeout"
	r.observeChannels(t.Context(), states, now.Add(10*time.Second))
	if got := countAppLogAction(logs.entriesSnapshot(), "channel_error"); got != 1 {
		t.Fatalf("error logs within a minute = %d, want 1", got)
	}
	r.observeChannels(t.Context(), states, now.Add(2*time.Minute))
	entries := logs.entriesSnapshot()
	if got := countAppLogAction(entries, "channel_error"); got != 2 || entries[len(entries)-1].Detail != "dial tcp: i/o timeout" {
		t.Fatalf("error logs after the interval = %d, last = %#v", got, entries[len(entries)-1])
	}
}
