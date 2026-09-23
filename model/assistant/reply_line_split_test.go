package assistant

import (
	"strings"
	"testing"

	"github.com/SuInk/diana/model/llm"
)

func TestLineSplitSendsEachLineButKeepsListsWhole(t *testing.T) {
	cfg := BotConfig{ReplyLineSplitEnabled: boolPointer(true)}.WithDefaults()
	reply := strings.Join([]string{
		"在呢",
		"这事分三步：",
		"1. 检查连接",
		"2. 查看日志",
		"   日志在 data/logs 下",
		"3. 重启服务",
		"弄完跟我说一声",
	}, notificationLineMarker)
	got := splitEventChatReply(reply, cfg, MessageEvent{Kind: EventKindGroup})
	want := []string{
		"在呢",
		"这事分三步：\n1. 检查连接\n2. 查看日志\n   日志在 data/logs 下\n3. 重启服务",
		"弄完跟我说一声",
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("line split = %q, want %q", got, want)
	}
}

func TestLineSplitKeepsCodeFenceWithItsLeadIn(t *testing.T) {
	cfg := BotConfig{ReplyLineSplitEnabled: boolPointer(true)}.WithDefaults()
	reply := strings.Join([]string{"改成这样：", "```go", "a := 1", "", "b := 2", "```", "再跑一次"}, notificationLineMarker)
	got := splitEventChatReply(reply, cfg, MessageEvent{Kind: EventKindPrivate})
	if len(got) != 2 || got[0] != "改成这样：\n```go\na := 1\n\nb := 2\n```" || got[1] != "再跑一次" {
		t.Fatalf("code fence was split: %q", got)
	}
}

func TestLineSplitOffKeepsLinesInOneMessage(t *testing.T) {
	reply := "第一行" + notificationLineMarker + "第二行"
	for _, cfg := range []BotConfig{
		BotConfig{}.WithDefaults(),
		// 关掉多条发送时换行分条不生效。
		BotConfig{ReplyLineSplitEnabled: boolPointer(true), NaturalReplySplitEnabled: boolPointer(false)}.WithDefaults(),
	} {
		got := splitEventChatReply(reply, cfg, MessageEvent{Kind: EventKindGroup})
		if len(got) != 1 || got[0] != "第一行\n第二行" {
			t.Fatalf("lines were split without the toggle: %q", got)
		}
	}
	// 闲聊插话只认显式标记。
	cfg := BotConfig{ReplyLineSplitEnabled: boolPointer(true)}.WithDefaults()
	got := splitEventChatReply(reply, cfg, MessageEvent{Kind: EventKindGroup, proactiveReply: true, chatInReply: true})
	if len(got) != 1 {
		t.Fatalf("casual interjection was split by lines: %q", got)
	}
	// 本轮要求一条发送优先。
	got = splitEventChatReply(replySingleMarker+reply, cfg, MessageEvent{Kind: EventKindGroup})
	if len(got) != 1 {
		t.Fatalf("single-message request was split by lines: %q", got)
	}
}

func TestLineSplitGroupOverride(t *testing.T) {
	cfg := BotConfig{ReplyLineSplitEnabled: boolPointer(true)}.WithDefaults()
	r := NewRuntime(cfg, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	r.SetGroupConfigStore(&stubGroupConfigStore{configs: map[string]GroupConfig{
		"100": {GroupID: "100", Enabled: true, ReplyLineSplitEnabled: boolPointer(false)},
	}})
	effective := r.effectiveConfigForEvent(MessageEvent{Kind: EventKindGroup, GroupID: "100"})
	if boolValue(effective.ReplyLineSplitEnabled, false) {
		t.Fatal("group override did not disable line split")
	}
}

func TestLineSplitPromptMatchesDelivery(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		cfg := BotConfig{ReplyLineSplitEnabled: boolPointer(enabled)}.WithDefaults()
		r := NewRuntime(cfg, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
		for _, event := range []MessageEvent{
			{Kind: EventKindPrivate},
			{Kind: EventKindGroup, proactiveReply: true, chatInReply: true},
		} {
			want := enabled && !event.chatInReply
			mainPrompt := r.systemPromptWithMode(event, nil, event.proactiveReply)
			persona := r.withUserFacingPersona(event, []llm.Message{{Role: llm.RoleUser, Content: "test"}})
			for _, prompt := range []string{mainPrompt, persona[0].Content} {
				if strings.Contains(prompt, "当前开启换行分条") != want {
					t.Fatalf("enabled=%v casual=%v prompt disagrees with delivery", enabled, event.chatInReply)
				}
			}
		}
	}
}
