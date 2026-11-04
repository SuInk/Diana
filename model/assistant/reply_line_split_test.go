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
	// 没开关时普通话之间的换行也会拆（见 splitPlainProseLines），这里用「一句 + 链接」：
	// 换行分条会把链接单独拆出去，按块切则让它跟着上一句。
	reply := "说明一句" + notificationLineMarker + "https://example.com/a"
	for _, cfg := range []BotConfig{
		BotConfig{}.WithDefaults(),
		// 关掉多条发送时换行分条不生效。
		BotConfig{ReplyLineSplitEnabled: boolPointer(true), NaturalReplySplitEnabled: boolPointer(false)}.WithDefaults(),
	} {
		got := splitEventChatReply(reply, cfg, MessageEvent{Kind: EventKindGroup})
		if len(got) != 1 || got[0] != "说明一句\nhttps://example.com/a" {
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

// 没开换行分条时按块切：普通的话一行一条，排过版的部分（清单、小节、代码、链接、引用）
// 各自整块。以前是全拆或全不拆，一行像标签就整条挤成一个气泡。
func TestPlainProseLineBreaksBecomeSeparateMessages(t *testing.T) {
	line := notificationLineMarker
	cfg := BotConfig{Platform: PlatformOneBotV11}.WithDefaults()
	group := MessageEvent{Kind: EventKindGroup}
	chatIn := MessageEvent{Kind: EventKindGroup, proactiveReply: true, chatInReply: true}
	for _, event := range []MessageEvent{group, chatIn} {
		got := splitEventChatReply("<diana-at id=\"10002\"/> 你问的是 Ice 吧……"+line+"不过真别再翻这些了", cfg, event)
		if len(got) != 2 || got[0] != "<diana-at id=\"10002\"/> 你问的是 Ice 吧……" || got[1] != "不过真别再翻这些了" {
			t.Fatalf("chatIn=%v prose lines stayed in one bubble: %q", event.chatInReply, got)
		}
	}
	for name, tc := range map[string]struct {
		reply string
		want  []string
	}{
		"lone label is prose": {
			"你今晚的状态不太对" + line + "先回答你：没人能验证" + line + "但你现在该操心的不是这个",
			[]string{"你今晚的状态不太对", "先回答你：没人能验证", "但你现在该操心的不是这个"},
		},
		"colon inside a title": {
			"看完了，原来是《赛博朋克：边缘行者2》的角色" + line + "这分析够细的",
			[]string{"看完了，原来是《赛博朋克：边缘行者2》的角色", "这分析够细的"},
		},
		"lead-in with labels then prose": {
			"预报如下：" + line + "11月4日：小雨" + line + "11月5日：多云" + line + "这两天回暖。来源：https://example.com/w",
			[]string{"预报如下：\n11月4日：小雨\n11月5日：多云", "这两天回暖。来源：https://example.com/w"},
		},
		"label run": {
			"双打：两人组队" + line + "酒馆法术：刷新时出法术",
			[]string{"双打：两人组队\n酒馆法术：刷新时出法术"},
		},
		"numbered list between prose": {
			"结论先说" + line + "配置如下：" + line + "1. 第一项" + line + "2. 第二项" + line + "最后补充",
			[]string{"结论先说", "配置如下：\n1. 第一项\n2. 第二项", "最后补充"},
		},
		"loose items after lead-in": {
			"蓝标核心就几样：" + line + "权重置顶" + line + "发长推文" + line + "不过要不要买看你自己，我觉得没必要",
			[]string{"蓝标核心就几样：\n权重置顶\n发长推文", "不过要不要买看你自己，我觉得没必要"},
		},
		"lead-in takes the next sentence": {
			"我推荐这个：" + line + "《近月少女的礼仪》，入坑最合适。" + line + "别的以后再说",
			[]string{"我推荐这个：\n《近月少女的礼仪》，入坑最合适", "别的以后再说"},
		},
		"link follows its sentence": {
			"已提交到 GitHub 啦！" + line + "Issue #920：修复语音时长" + line + "https://github.com/example/demo/issues/920",
			[]string{"已提交到 GitHub 啦！", "Issue #920：修复语音时长\nhttps://github.com/example/demo/issues/920"},
		},
		"labeled link": {
			"有这一部" + line + "商店页：https://example.com/a",
			[]string{"有这一部\n商店页：https://example.com/a"},
		},
		"quote with comment": {
			"> 原文" + line + "> 保留" + line + "这句说得对" + line + "另一件事",
			[]string{"> 原文\n> 保留\n这句说得对", "另一件事"},
		},
		"sections keep their body": {
			"## 第一天" + line + "上午去 A" + line + "下午去 B" + line + "## 第二天" + line + "全天在 C",
			[]string{"## 第一天\n上午去 A\n下午去 B", "## 第二天\n全天在 C"},
		},
		"blank line": {
			"第一段" + line + line + "第二段",
			[]string{"第一段", "第二段"},
		},
		"code fence with its intro": {
			"我改了这里" + line + "```go" + line + "a := 1" + line + "```",
			[]string{"我改了这里\n```go\na := 1\n```"},
		},
		"code fence with lead-in": {
			"改成这样：" + line + "```go" + line + "a := 1" + line + "```" + line + "再跑一次",
			[]string{"改成这样：\n```go\na := 1\n```", "再跑一次"},
		},
	} {
		got := splitEventChatReply(tc.reply, cfg, group)
		if strings.Join(got, "|") != strings.Join(tc.want, "|") {
			t.Fatalf("%s:\n got %q\nwant %q", name, got, tc.want)
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

// 换行分条关着时，提示词也要照实说普通话之间的换行会另起一条；不拆的几种情况不说。
func TestPlainProseSplitPromptMatchesDelivery(t *testing.T) {
	const marker = "两句普通的话之间换行，会拆成两条发出"
	cfg := BotConfig{Platform: PlatformOneBotV11}.WithDefaults()
	for name, tc := range map[string]struct {
		limits chatSplitLimits
		want   bool
	}{
		"default":        {chatSplitLimitsForEvent(cfg, MessageEvent{Kind: EventKindGroup}), true},
		"chat in":        {chatSplitLimitsForEvent(cfg, MessageEvent{Kind: EventKindGroup, chatInReply: true}), true},
		"turn preserve":  {chatSplitLimitsForEvent(cfg, MessageEvent{Kind: EventKindGroup, replyLineBreakMode: replyLinesPreserve}), false},
		"compact config": {chatSplitLimitsFrom(BotConfig{ReplyPreserveLineBreaks: boolPointer(false)}.WithDefaults()), false},
		"rich text":      {chatSplitLimitsFrom(BotConfig{Platform: PlatformTelegram}.WithDefaults()), false},
		"single message": {chatSplitLimitsFrom(BotConfig{NaturalReplySplitEnabled: boolPointer(false)}.WithDefaults()), false},
	} {
		if got := strings.Contains(replyLineSplitPrompt(tc.limits), marker); got != tc.want {
			t.Fatalf("%s: prompt mentions prose split = %v, want %v", name, got, tc.want)
		}
	}
}
