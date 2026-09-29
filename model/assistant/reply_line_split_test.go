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
	// 普通话之间的换行没开关也会拆（见 splitPlainProseLines），这里用带列表的一条。
	reply := "说明一句" + notificationLineMarker + "1. 第一项" + notificationLineMarker + "2. 第二项"
	for _, cfg := range []BotConfig{
		BotConfig{}.WithDefaults(),
		// 关掉多条发送时换行分条不生效。
		BotConfig{ReplyLineSplitEnabled: boolPointer(true), NaturalReplySplitEnabled: boolPointer(false)}.WithDefaults(),
	} {
		got := splitEventChatReply(reply, cfg, MessageEvent{Kind: EventKindGroup})
		if len(got) != 1 || got[0] != "说明一句\n1. 第一项\n2. 第二项" {
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

// 没开换行分条时，一条里只有几句普通话、中间被换行隔开的，拆成几条发；
// 排过版的（列表、标签、链接、代码、空行分段）整条留着。
func TestPlainProseLineBreaksBecomeSeparateMessages(t *testing.T) {
	line := notificationLineMarker
	cfg := BotConfig{Platform: PlatformOneBotV11}.WithDefaults()
	group := MessageEvent{Kind: EventKindGroup}
	chatIn := MessageEvent{Kind: EventKindGroup, proactiveReply: true, chatInReply: true}
	for _, event := range []MessageEvent{group, chatIn} {
		got := splitEventChatReply("[diana-at:10002] 你问的是 Ice 吧……"+line+"不过真别再翻这些了", cfg, event)
		if len(got) != 2 || got[0] != "[diana-at:10002] 你问的是 Ice 吧……" || got[1] != "不过真别再翻这些了" {
			t.Fatalf("chatIn=%v prose lines stayed in one bubble: %q", event.chatInReply, got)
		}
	}
	for name, reply := range map[string]string{
		"label": "已提交到 GitHub 啦！" + line + "Issue #920：修复语音时长" + line + "https://github.com/example/demo/issues/920",
		"link":  "看这个" + line + "https://example.com/a",
		"lead":  "蓝标核心就几样：" + line + "权重置顶" + line + "发长推文",
		"quote": "> 原文" + line + "> 保留",
		"blank": "第一段" + line + line + "第二段",
	} {
		if got := splitEventChatReply(reply, cfg, group); len(got) != 1 {
			t.Fatalf("%s: formatted reply was split: %q", name, got)
		}
	}
	// 本轮要求保留换行、配置成收拢换行、合并转发卡片：都不拆。
	prose := "第一句" + line + "第二句"
	if got := splitEventChatReply(prose, cfg, MessageEvent{Kind: EventKindGroup, replyLineBreakMode: replyLinesPreserve}); len(got) != 1 {
		t.Fatalf("turn preserve request was split: %q", got)
	}
	if got := splitEventChatReply(replyLinesPreserveMarker+prose, cfg, group); len(got) != 1 {
		t.Fatalf("preserve marker was split: %q", got)
	}
	compact := BotConfig{Platform: PlatformOneBotV11, ReplyPreserveLineBreaks: boolPointer(false)}.WithDefaults()
	if got := splitEventChatReply(prose, compact, group); len(got) != 1 || got[0] != "第一句，第二句" {
		t.Fatalf("compact mode should join prose: %q", got)
	}
	if got := splitForwardReply(prose, chatSplitLimitsForEvent(cfg, group)); len(got) != 1 {
		t.Fatalf("forward card was split by prose lines: %q", got)
	}
}
