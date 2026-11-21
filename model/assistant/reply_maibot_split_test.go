// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"strings"
	"testing"
)

// withMaiBotSplit 给单个用例打开 MaiBot 断句（整包默认关着，见 main_test.go）。
func withMaiBotSplit(t *testing.T) {
	t.Helper()
	maiBotSplitDisabled.Store(false)
	t.Cleanup(func() { maiBotSplitDisabled.Store(true) })
}

// 11-20 回放：swe-2 每个回答都写成一整行长句，只按空行和分条标记拆的话全是一条
// 八九十字的大段。照 MaiBot 在标点处断句，最多三条。
func TestMaiBotSplitBreaksLongProseLine(t *testing.T) {
	withMaiBotSplit(t)
	cfg := BotConfig{Platform: PlatformOneBotV11}.WithDefaults()
	reply := "想喝就买，它是常温酸奶里口感最浓的那档，但注意两点：一是偏甜，糖不少，当饮料喝行，天天喝就不太值"
	got := splitEventChatReply(reply, cfg, MessageEvent{Kind: EventKindGroup})
	if len(got) < 2 || len(got) > 3 {
		t.Fatalf("long line should become 2-3 messages: %q", got)
	}
	joined := strings.Join(got, "")
	for _, piece := range []string{"想喝就买", "口感最浓的那档", "当饮料喝行", "天天喝就不太值"} {
		if !strings.Contains(joined, piece) {
			t.Fatalf("lost %q: %q", piece, got)
		}
	}
}

// 汉字之间的空格照 MaiBot 当停顿：「换谁 笑得都僵」这种写法要能拆开。
func TestMaiBotSplitBreaksAtSpaceBetweenHan(t *testing.T) {
	withMaiBotSplit(t)
	cfg := BotConfig{Platform: PlatformOneBotV11}.WithDefaults()
	got := splitEventChatReply("一边敲钟一边看着八百亿进账还要想着上半年亏掉的那些 换谁笑得都僵硬吧大概率是装的", cfg, MessageEvent{Kind: EventKindGroup})
	if len(got) != 2 || got[0] != "一边敲钟一边看着八百亿进账还要想着上半年亏掉的那些" {
		t.Fatalf("space between Han should split: %q", got)
	}
}

func TestMaiBotSplitKeepsMarkupLinksAndLatinWords(t *testing.T) {
	withMaiBotSplit(t)
	cfg := BotConfig{Platform: PlatformOneBotV11}.WithDefaults()
	group := MessageEvent{Kind: EventKindGroup}
	for name, tc := range map[string]struct {
		reply string
		keep  []string
	}{
		"cq code":      {"语音来了，听一下[CQ:record,file=http://127.0.0.1/a.mp3]，听完说说感受怎么样啊", []string{"[CQ:record,file=http://127.0.0.1/a.mp3]"}},
		"mention lead": {"<diana-at id=\"10002\"/> 你问的是 Ice 吧，那个早就停更了，别再翻了", []string{"<diana-at id=\"10002\"/> 你问的是 Ice 吧"}},
		"url":          {"文档在这里https://example.com/a,b?c=1，照着第三节改就行，改完再跑一次", []string{"https://example.com/a,b?c=1"}},
		// 11-20 线上：「+」两边的空格被当成停顿，拆出一条以「+ 考核制」开头的消息。
		"symbol space": {"馒头就是 M-Team（m-team.cc），国内最大的 PT 站之一，邀请制 + 考核制，资源量在全类型 PT 站里算头部", []string{"邀请制 + 考核制"}},
		"digit space":  {"一年回 2600 正好保本，你这融资方案是零息债券，利息全靠群友情谊", []string{"一年回 2600 正好保本"}},
		// 11-21 线上：书名里的逗号被当停顿，拆成「…那本《反派」和「我把女二养成病娇》的…」。
		"book title":  {"搜了搜了，还真有：渔别源是个网文作者，写明末千里行同人和病娇百合那本《反派，我把女二养成病娇》的；城倾山是知乎答主，答过原神4.2剧情评价那条", []string{"《反派，我把女二养成病娇》"}},
		"lenticular":  {"群公告写了，进群先看置顶，【新人须知，第三条】别发广告，不然直接踢，管理员不会提醒第二次", []string{"【新人须知，第三条】"}},
		"angle title": {"这首歌收在专辑里，叫〈夜航，星〉那首，副歌那段特别好听，你可以先听这一首再决定要不要整张听", []string{"〈夜航，星〉"}},
	} {
		got := splitEventChatReply(tc.reply, cfg, group)
		for _, keep := range tc.keep {
			found := false
			for _, bubble := range got {
				found = found || strings.Contains(bubble, keep)
			}
			if !found {
				t.Fatalf("%s: %q was cut apart: %q", name, keep, got)
			}
		}
	}
}

// 多行的块（清单、带链接的说明）、富文本平台、本轮要求一条发：都不按句拆。
func TestMaiBotSplitLeavesBlocksAndRichTextAlone(t *testing.T) {
	withMaiBotSplit(t)
	line := notificationLineMarker
	plain := BotConfig{Platform: PlatformOneBotV11}.WithDefaults()
	group := MessageEvent{Kind: EventKindGroup}
	list := "配置如下：" + line + "1. 第一项，先装依赖" + line + "2. 第二项，再跑测试"
	if got := splitEventChatReply(list, plain, group); len(got) != 1 {
		t.Fatalf("list block was split: %q", got)
	}
	long := "想喝就买，它是常温酸奶里口感最浓的那档，但注意两点：一是偏甜，糖不少，当饮料喝行，天天喝就不太值"
	telegram := BotConfig{Platform: PlatformTelegram}.WithDefaults()
	if got := splitEventChatReply(long, telegram, group); len(got) != 1 {
		t.Fatalf("rich text platform was split by sentence: %q", got)
	}
	single := MessageEvent{Kind: EventKindGroup, replyDeliveryMode: replyDeliverySingle}
	if got := splitEventChatReply(long, plain, single); len(got) != 1 {
		t.Fatalf("single-message turn was split: %q", got)
	}
}

// 11-20 线上：一条讲 LCS 的四行回复先按行切成四块、每块各自断句，发了九条，
// max(dp[i-1][j], dp[i][j-1]) 也从逗号处断开。MaiBot 对整条回复计数：超过 8 句就整条
// 不拆，否则最多三条；括号里不断。
func TestMaiBotSplitCountsSentencesAcrossLines(t *testing.T) {
	withMaiBotSplit(t)
	cfg := BotConfig{Platform: PlatformOneBotV11}.WithDefaults()
	reply := "最长公共子序列，经典动态规划题：两个序列里按原有顺序都出现、但位置不要求连续的最长公共部分，比如 \"ABCBDAB\" 和 \"BDCABA\" 的 LCS 是 \"BCBA\"，长度4。\n核心是二维 dp 表，dp[i][j] 表示 s1 前 i 个字符和 s2 前 j 个字符的 LCS 长度：\ns1[i-1] == s2[j-1] 时 dp[i][j] = dp[i-1][j-1] + 1\n否则 dp[i][j] = max(dp[i-1][j], dp[i][j-1])\n时间空间都是 O(m*n)，要还原具体序列就从 dp[m][n] 往回走，相等就斜着走并收下那个字符。Python 十几行就能写完，要代码我给你"
	got := splitEventChatReply(reply, cfg, MessageEvent{Kind: EventKindGroup})
	if len(got) > 3 {
		t.Fatalf("one reply should become at most 3 messages, got %d: %q", len(got), got)
	}
	if joined := strings.Join(got, "\n"); !strings.Contains(joined, "max(dp[i-1][j], dp[i][j-1])") {
		t.Fatalf("split inside parentheses: %q", got)
	}
}

func TestMaiBotSplitMergesShortLinesToThree(t *testing.T) {
	withMaiBotSplit(t)
	cfg := BotConfig{Platform: PlatformOneBotV11}.WithDefaults()
	got := splitEventChatReply("先说结论能用\n价格也还行\n就是有点吵\n晚上别开", cfg, MessageEvent{Kind: EventKindGroup})
	if len(got) < 2 || len(got) > 3 {
		t.Fatalf("four short lines should merge to 2-3 messages: %q", got)
	}
	if joined := strings.Join(got, "\n"); !strings.Contains(joined, "价格也还行") || !strings.Contains(joined, "晚上别开") {
		t.Fatalf("lost text: %q", got)
	}
}

// 模型自己用 <diana-message/> 分了条就照着发：11-20 线上两段各被再拆，发成了五条。
func TestMaiBotSplitRespectsExplicitMessageMarkers(t *testing.T) {
	withMaiBotSplit(t)
	cfg := BotConfig{Platform: PlatformOneBotV11}.WithDefaults()
	reply := "这视频说的是真的，原文我读了：有人发推夸新模型的对话体验，说只要订阅能在任何工具里用就回来了，官方回了一句十二月二号迈出第一步<diana-message/>意思是十二月二号开始把订阅额度开放给第三方工具用，但具体支持哪些、怎么开没说。底下已经有人求别只给一家了，等十二月二号看落地范围"
	got := splitEventChatReply(reply, cfg, MessageEvent{Kind: EventKindGroup})
	if len(got) != 2 {
		t.Fatalf("explicit markers should give exactly 2 messages, got %d: %q", len(got), got)
	}
}
