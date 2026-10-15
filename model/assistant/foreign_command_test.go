package assistant

import (
	"context"
	"fmt"
	"testing"
)

func TestLooksLikeForeignCommand(t *testing.T) {
	for text, want := range map[string]bool{
		"/ai":                      true,
		"/ai off":                  true,
		"/查谱":                      true,
		"/草 yjx":                   true,
		"/tarot@linverseHelperBot": true,
		"  /status@Shio0721_bot\n": true,
		"#签到":                      true,
		".r 1d100":                 true,
		"!help":                    true,
		"/etc/hosts 报错了":           false,
		"/ 你好":                     false,
		"// ==UserScript==":        false,
		"ai off":                   false,
		"#diana":                   false,
		"#周末去哪 有推荐吗":               false,
		".NET 9 怎么样":               false,
		"看看 /ai 是谁的命令":             false,
		"https://example.com/ai":   false,
		"/":                        false,
	} {
		if got := looksLikeForeignCommand(text); got != want {
			t.Errorf("looksLikeForeignCommand(%q) = %v, want %v", text, got, want)
		}
	}
}

func TestForeignCommandSkipsReplyFlow(t *testing.T) {
	runtime := NewRuntime(BotConfig{BotAccount: "10000"}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	for index, command := range []string{"/ai off", "#签到", ".r 1d100", "!help"} {
		event := MessageEvent{Kind: EventKindGroup, GroupID: "20001", UserID: "10001", MessageID: fmt.Sprint(30001 + index), RawMessage: command,
			Segments: []MessageSegment{{Type: "text", Data: map[string]string{"text": command}}}}
		_, _, handled, outcome := runtime.prepareMessageEvent(context.Background(), event)
		if handled || outcome != "ignored_foreign_command" {
			t.Fatalf("%q: handled=%v outcome=%q", event.RawMessage, handled, outcome)
		}
	}
}

func TestForeignCommandLeavesOrdinaryAndPrivateMessages(t *testing.T) {
	runtime := NewRuntime(BotConfig{BotAccount: "10000"}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	if runtime.foreignCommand(MessageEvent{Kind: EventKindPrivate, UserID: "10001"}, "/start") {
		t.Fatal("private slash command treated as foreign")
	}
	if runtime.foreignCommand(MessageEvent{Kind: EventKindGroup, GroupID: "20001", UserID: "10001"}, "/etc/hosts 报错了") {
		t.Fatal("path treated as command")
	}
	if runtime.foreignCommand(MessageEvent{Kind: EventKindGroup, GroupID: "20001", UserID: "10001", SelfID: "10000", ToMe: true}, "/查谱") {
		t.Fatal("command addressed to this bot treated as foreign")
	}
	if runtime.foreignCommand(MessageEvent{Kind: EventKindGroup, GroupID: "20001", UserID: "10001", SelfID: "10000",
		Segments: []MessageSegment{{Type: "at", Data: map[string]string{"qq": "10002"}}}}, "你看这个") {
		t.Fatal("ordinary message mentioning another member treated as foreign")
	}
}
