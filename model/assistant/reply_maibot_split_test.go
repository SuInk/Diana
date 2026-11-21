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
// 八九十字的大段。照 MaiBot 在句号、分号处断句，最多三条。
func TestMaiBotSplitBreaksLongProseLine(t *testing.T) {
	withMaiBotSplit(t)
	cfg := BotConfig{Platform: PlatformOneBotV11}.WithDefaults()
	reply := "想喝就买，它是常温酸奶里口感最浓的那档。但注意两点：一是偏甜，糖不少；当饮料喝行，天天喝就不太值"
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
// 用户 11-21：「分号这种才要分条，逗号不用」。只有逗号的长句整条发；分号处拆。
func TestMaiBotSplitKeepsCommasSplitsSemicolons(t *testing.T) {
	withMaiBotSplit(t)
	cfg := BotConfig{Platform: PlatformOneBotV11}.WithDefaults()
	group := MessageEvent{Kind: EventKindGroup}
	comma := "想喝就买，它是常温酸奶里口感最浓的那档，但注意偏甜，当饮料喝行，天天喝就不太值"
	if got := splitEventChatReply(comma, cfg, group); len(got) != 1 {
		t.Fatalf("commas should not split: %q", got)
	}
	semicolon := "想喝就买，它是常温酸奶里口感最浓的那档；但注意偏甜，当饮料喝行，天天喝就不太值"
	got := splitEventChatReply(semicolon, cfg, group)
	if len(got) != 2 || got[0] != "想喝就买，它是常温酸奶里口感最浓的那档" {
		t.Fatalf("semicolon should split: %q", got)
	}
}

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

// 断句只能落在成对符号、代码、链接、提及、数字之外。逐句检查（不经合并），
// 保证这些片段在任何一种合并结果里都不会被劈开。
func TestMaiBotSplitSentencesNeverCutsInsideUnits(t *testing.T) {
	for name, tc := range map[string]struct{ reply, keep string }{
		"book title":         {"写病娇百合那本《反派；我把女二养成病娇》的；城倾山是知乎答主；答过原神4.2剧情评价那条", "《反派；我把女二养成病娇》"},
		"lenticular":         {"群公告写了；【新人须知；第三条】别发广告；不然直接踢", "【新人须知；第三条】"},
		"corner quote":       {"他说「好的；明天见」然后就走了；留下我们几个面面相觑；不知道怎么办才好", "「好的；明天见」"},
		"white corner quote": {"书里写『风起了；该走了』这句很出名；后来被很多人引用；现在都快成梗了", "『风起了；该走了』"},
		"double quote":       {"他回了一句“行；你说了算”就下线了；后来再也没上过线；群里都在猜怎么了", "“行；你说了算”"},
		"single quote":       {"标题叫‘慢一点；再慢一点’的那篇；写得挺好的；推荐你有空去看看", "‘慢一点；再慢一点’"},
		"ascii quote":        {"他说 \"ok; fine\" 然后走了；大家都没反应过来；过了好一会儿才有人说话", "\"ok; fine\""},
		"full paren":         {"这个函数（见上面；第二段）有问题；调用的时候会空指针；你先别用它", "（见上面；第二段）"},
		"ascii paren":        {"直接写 max(a; b) 就行；不用自己比较；标准库里早就有了；放心用", "max(a; b)"},
		"inline code":        {"把 `a; b := f()` 改成先判断错误；不然会吞掉报错；排查的时候很难找", "`a; b := f()`"},
		"markdown link":      {"详情看[发布说明；第三节](https://example.com/notes)那里写清楚了；照着升级就行", "[发布说明；第三节]"},
		"url":                {"文档在这里https://example.com/a,b?c=1;d=2；照着第三节改就行；改完再跑一次", "https://example.com/a,b?c=1;d=2"},
		"email":              {"有问题发邮件到 help@example.com 就行；一般当天回复；周末可能慢一点", "help@example.com"},
		"plain mention":      {"@嘉然 你刚才说的那个网站打不开了；能不能再发一次链接；我这边一直报错", "@嘉然 你刚才说的"},
		"decimal":            {"原神4.2的剧情评价两极分化；有人说是神作；有人说一般；看个人口味吧", "原神4.2的剧情"},
		"version":            {"升到 v0.9.11 以后就好了；旧版本那个问题已经修掉了；记得重启一下", "v0.9.11"},
		"ellipsis":           {"这个嘛……我也不太确定；你最好去问问群主；他应该知道得比较清楚", "这个嘛……我也不太确定"},
		"dash":               {"结论就一句——别买；等降价再说；现在这个价格明显是在割韭菜", "结论就一句——别买"},
		"emoji":              {"笑死我了😂😂你这操作太秀了；我直接截图发朋友圈了；不介意吧", "笑死我了😂😂你这操作太秀了"},
		"kaomoji":            {"好耶(๑•̀ㅂ•́)و✧今天终于下班了；晚上可以好好打游戏了；谁来一起", "好耶(๑•̀ㅂ•́)و✧今天终于下班了"},
		"kaomoji comma":      {"别催了啦(；´Д`)；马上就写完了；再给我十分钟就好；真的就十分钟", "(；´Д`)"},
	} {
		var texts []string
		found := false
		for _, segment := range maiBotSplitSentences(tc.reply) {
			texts = append(texts, segment.Text)
			found = found || strings.Contains(segment.Text, tc.keep)
		}
		if !found {
			t.Errorf("%s: %q was cut apart: %q", name, tc.keep, texts)
		}
	}
}
