// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	goruntime "runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// 全匹配就是全匹配：口令前后可以有空白、大小写随意，但夹在一句话里不算。
func TestStatusCommandMatchesOnlyExactCommand(t *testing.T) {
	for _, text := range []string{"#diana", "  #diana  ", "#Diana", "#DIANA", "\n#diana\n"} {
		if !isStatusCommand(text) {
			t.Fatalf("%q 应当被认成口令", text)
		}
	}
	for _, text := range []string{
		"#diana 怎么样了",
		"帮我看看 #diana",
		"#diana2",
		"##diana",
		"diana",
		"#dian",
		"",
	} {
		if isStatusCommand(text) {
			t.Fatalf("%q 不该被认成口令", text)
		}
	}
}

// 插件开着的时候，每条消息都会进 Handle 一次。不是口令就必须原样放过，
// 否则一开插件机器人就对所有消息回状态卡片。
func TestStatusCommandIgnoresOtherMessages(t *testing.T) {
	plugin := NewStatusCommandPlugin()
	resp, err := plugin.Handle(context.Background(), PluginRequest{Text: "今天天气怎么样"})
	if err != nil {
		t.Fatal(err)
	}
	if resp != nil {
		t.Fatalf("普通消息不该被接管：%#v", resp)
	}
}

func TestStatusCommandReply(t *testing.T) {
	plugin := NewStatusCommandPlugin()
	started := time.Now().Add(-(3*24*time.Hour + 11*time.Hour + 51*time.Minute))
	resp, err := plugin.Handle(context.Background(), PluginRequest{
		Text:      " #diana ",
		BuildInfo: BuildInfo{Version: "v0.8.68", StartedAt: started},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp == nil || !resp.Handled {
		t.Fatalf("口令没有被接管：%#v", resp)
	}
	lines := strings.Split(resp.Reply, "\n")
	if len(lines) != 4 {
		t.Fatalf("卡片行数 = %d：%q", len(lines), resp.Reply)
	}
	if lines[0] != "Diana 状态" {
		t.Fatalf("首行 = %q", lines[0])
	}
	if lines[1] != "版本: 0.8.68" {
		t.Fatalf("版本行 = %q", lines[1])
	}
	if want := "平台: " + goruntime.GOOS + "-" + goruntime.GOARCH; lines[2] != want {
		t.Fatalf("平台行 = %q，期望 %q", lines[2], want)
	}
	if lines[3] != "运行时长: 3天 11小时 51分钟" {
		t.Fatalf("运行时长行 = %q", lines[3])
	}
}

// 没注入版本号时如实说未知，不拿源码基线或者训练记忆糊一个上去。
// v 前缀只在真的是 vX.Y.Z 时去掉。dev 这类版本串砍掉首字母就成了 ev。
func TestTrimVersionPrefix(t *testing.T) {
	cases := map[string]string{
		"v0.8.68":     "0.8.68",
		"V0.8.68":     "0.8.68",
		"v0.8.68-dev": "0.8.68-dev",
		" v1.0.0 ":    "1.0.0",
		"0.8.68":      "0.8.68",
		"dev":         "dev",
		"version":     "version",
		"v":           "v",
		"":            "",
	}
	for in, want := range cases {
		if got := trimVersionPrefix(in); got != want {
			t.Fatalf("trimVersionPrefix(%q) = %q，期望 %q", in, got, want)
		}
	}
}

func TestStatusCommandReportsUnknownVersion(t *testing.T) {
	card := statusCardText(BuildInfo{}, time.Now())
	if !strings.Contains(card, "版本: 未知") {
		t.Fatalf("卡片 = %q", card)
	}
	// StartedAt 为零值时退回进程启动时刻，运行时长这一行照样有内容。
	if strings.Contains(card, "运行时长: \n") || strings.HasSuffix(card, "运行时长: ") {
		t.Fatalf("运行时长为空：%q", card)
	}
}

func TestFormatStatusUptime(t *testing.T) {
	cases := []struct {
		in   time.Duration
		want string
	}{
		{3*24*time.Hour + 11*time.Hour + 51*time.Minute, "3天 11小时 51分钟"},
		// 中间的零也照写：格式固定，扫一眼就知道哪个数字变了。
		{2 * 24 * time.Hour, "2天 0小时 0分钟"},
		{5*time.Hour + 7*time.Minute, "5小时 7分钟"},
		{42 * time.Minute, "42分钟"},
		{30 * time.Second, "不到1分钟"},
		{-time.Hour, "不到1分钟"},
	}
	for _, tc := range cases {
		if got := formatStatusUptime(tc.in); got != tc.want {
			t.Fatalf("formatStatusUptime(%v) = %q，期望 %q", tc.in, got, tc.want)
		}
	}
}

// 默认关闭：装好但不启用，也不因为「内置」就被 Restore 顺手打开。
func TestStatusCommandPluginDefaultsToDisabled(t *testing.T) {
	manager := NewDefaultPluginManager()
	state, ok := pluginStateByID(manager.List(), statusCommandPluginID)
	if !ok {
		t.Fatal("插件没有注册进默认插件管理器")
	}
	if !state.Installed {
		t.Fatal("内置插件应当是装好的")
	}
	if state.Enabled {
		t.Fatal("这个插件默认应当是关闭的")
	}

	// 库里没有它的记录时（老版本升上来就是这样），Restore 不能把它打开。
	manager.Restore(map[string]PluginState{})
	if state, _ := pluginStateByID(manager.List(), statusCommandPluginID); state.Enabled {
		t.Fatal("没有存过状态时 Restore 把默认关闭的插件打开了")
	}

	// 用户开过之后要记住。
	if _, err := manager.SetEnabled(statusCommandPluginID, true); err != nil {
		t.Fatal(err)
	}
	manager.Restore(map[string]PluginState{
		statusCommandPluginID: {Installed: true, Enabled: true},
	})
	if state, _ := pluginStateByID(manager.List(), statusCommandPluginID); !state.Enabled {
		t.Fatal("用户开过的状态没有被 Restore 保留")
	}

	// 再关回去也要记住。
	manager.Restore(map[string]PluginState{
		statusCommandPluginID: {Installed: true, Enabled: false},
	})
	if state, _ := pluginStateByID(manager.List(), statusCommandPluginID); state.Enabled {
		t.Fatal("用户关掉的状态没有被 Restore 保留")
	}
}

// 默认开启的内置插件不受这次改动影响。
func TestBuiltInPluginsStillDefaultToEnabled(t *testing.T) {
	manager := NewDefaultPluginManager()
	manager.Restore(map[string]PluginState{})
	for _, state := range manager.List() {
		if !state.Manifest.BuiltIn || state.Manifest.DefaultDisabled {
			continue
		}
		if !state.Enabled {
			t.Fatalf("内置插件 %s 本该默认启用", state.Manifest.ID)
		}
	}
}

// 关着的时候不该被口令叫醒。
func TestStatusCommandNotTriggeredWhileDisabled(t *testing.T) {
	manager := NewDefaultPluginManager()
	event := MessageEvent{Kind: EventKindGroup, GroupID: "100200301", UserID: "10086"}
	if manager.ShouldHandleWithOverrides(event, statusCommandTrigger, nil) {
		t.Fatal("插件默认关闭，口令不该叫醒机器人")
	}
	if _, err := manager.SetEnabled(statusCommandPluginID, true); err != nil {
		t.Fatal(err)
	}
	if !manager.ShouldHandleWithOverrides(event, statusCommandTrigger, nil) {
		t.Fatal("插件开启后口令应当叫醒机器人")
	}
	if manager.ShouldHandleWithOverrides(event, "今天天气怎么样", nil) {
		t.Fatal("普通消息不该叫醒机器人")
	}
}

func pluginStateByID(states []PluginState, id string) (PluginState, bool) {
	for _, state := range states {
		if state.Manifest.ID == id {
			return state, true
		}
	}
	return PluginState{}, false
}

// 端到端：群里发一句 #diana，没有触发词也没有 @，机器人照样回一张卡片，
// 而且完全不喊模型。
func TestRuntimeStatusCommandRepliesWithoutLLM(t *testing.T) {
	manager := NewPluginManager(NewStatusCommandPlugin())
	if _, err := manager.SetEnabled(statusCommandPluginID, true); err != nil {
		t.Fatal(err)
	}
	channel := &recordingChannel{}
	var llmCalls atomic.Int32
	botRuntime := NewRuntime(BotConfig{GroupTriggers: []string{"Diana"}, BotAccount: "42"}, channel, manager, nil, nil, nil, func() (LLMProvider, error) {
		llmCalls.Add(1)
		return &capturingLLMProvider{reply: "不应该调用"}, nil
	})
	botRuntime.SetBuildInfo(BuildInfo{
		Version:   "v0.8.68",
		BuildType: "release",
		StartedAt: time.Now().Add(-(3*24*time.Hour + 11*time.Hour + 51*time.Minute)),
	})

	event := MessageEvent{
		Kind:        EventKindGroup,
		SelfID:      "42",
		GroupID:     "123456",
		UserID:      "10001",
		MessageID:   "status-1",
		SenderLevel: 40, SenderLevelLabel: "LV40",
		RawMessage: statusCommandTrigger,
		Segments:   []MessageSegment{{Type: "text", Data: map[string]string{"text": statusCommandTrigger}}},
	}
	if err := botRuntime.HandleEvent(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	waitForCondition(t, time.Second, func() bool {
		return len(channel.sentSnapshot()) > 0
	})
	if got := llmCalls.Load(); got != 0 {
		t.Fatalf("状态卡片不该经过模型，llm calls = %d", got)
	}
	sent := channel.sentSnapshot()
	if len(sent) != 1 {
		t.Fatalf("发送次数 = %d：%#v", len(sent), sent)
	}
	if !strings.Contains(sent[0].Text, "版本: 0.8.68") || !strings.Contains(sent[0].Text, "运行时长: 3天 11小时 51分钟") {
		t.Fatalf("回复内容 = %q", sent[0].Text)
	}
}

// 插件关着的时候，#diana 就是一条普通的、没有触发词的群消息：不发任何东西，
// 走的路径也和随便一句闲聊完全一样。
//
// 这里拿一条普通消息当对照，而不是直接断言「模型调用 0 次」——没触发词的群消息
// 本来就会过一次主动回复判断，那是既有行为，跟这个插件无关。断言两者相等才说明
// 插件关着时确实什么都没多做。
func TestRuntimeStatusCommandSilentWhileDisabled(t *testing.T) {
	run := func(text string) (sent int, llmCalls int32) {
		manager := NewPluginManager(NewStatusCommandPlugin())
		channel := &recordingChannel{}
		var calls atomic.Int32
		botRuntime := NewRuntime(BotConfig{GroupTriggers: []string{"Diana"}, BotAccount: "42"}, channel, manager, nil, nil, nil, func() (LLMProvider, error) {
			calls.Add(1)
			return &capturingLLMProvider{reply: "不应该发出来"}, nil
		})
		event := MessageEvent{
			Kind:        EventKindGroup,
			SelfID:      "42",
			GroupID:     "123456",
			UserID:      "10001",
			MessageID:   "status-2",
			SenderLevel: 40, SenderLevelLabel: "LV40",
			RawMessage: text,
			Segments:   []MessageSegment{{Type: "text", Data: map[string]string{"text": text}}},
		}
		if err := botRuntime.HandleEvent(context.Background(), event); err != nil {
			t.Fatal(err)
		}
		return len(channel.sentSnapshot()), calls.Load()
	}

	commandSent, commandCalls := run(statusCommandTrigger)
	if commandSent != 0 {
		t.Fatalf("插件关着还发了 %d 条消息", commandSent)
	}
	controlSent, controlCalls := run("今天天气怎么样")
	if commandSent != controlSent || commandCalls != controlCalls {
		t.Fatalf("关着的口令和普通消息走了不同的路径：口令 sent=%d llm=%d，对照 sent=%d llm=%d",
			commandSent, commandCalls, controlSent, controlCalls)
	}
}
