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
