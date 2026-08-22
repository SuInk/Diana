// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"strings"
	"testing"
)

const structuredReplyWithBlankLines = `附近这么走顺路：
1. 玉岩书院＋萝峰寺
岭南古建和碑刻比非花季的梅林好看，17:00 左右闭园。
2. 后山短线
不怕热就往后山走一段，到视野开阔处折返。
3. 创业公园
想看湖散步就去，雪浪湖一带舒服。
4. 黄埔区图书馆
太晒就进去吹空调躲最热那阵。

我最推荐：书院→萝峰寺→后山短走→万达吃饭。`

func TestStructuredReplyDropsBlankLines(t *testing.T) {
	// 清单型回复整块发送，空行不再是消息边界，会原样渲染成一整行空白。
	parts := splitReply(structuredReplyWithBlankLines, 160)
	if len(parts) != 1 {
		t.Fatalf("清单应整块发送，实际拆成 %d 条", len(parts))
	}
	for _, line := range strings.Split(parts[0], "\n") {
		if strings.TrimSpace(line) == "" {
			t.Fatalf("整块发送的清单里仍然残留空行：\n%s", parts[0])
		}
	}
	// 内容不能被顺带吃掉。
	for _, keep := range []string{"1. 玉岩书院＋萝峰寺", "岭南古建和碑刻", "我最推荐"} {
		if !strings.Contains(parts[0], keep) {
			t.Fatalf("清单内容丢失 %q：\n%s", keep, parts[0])
		}
	}
}

func TestOrdinaryReplyStillSplitsOnBlankLines(t *testing.T) {
	// 普通聊天按空行分条的行为不变：那里空行本来就不会显示出来。
	parts := splitReply("先说结论，端口被占了。\n\n完整报错贴一下我看看。", 900)
	if len(parts) != 2 {
		t.Fatalf("普通回复应按空行分成两条，实际 %d 条：%#v", len(parts), parts)
	}
}

func TestExplicitBotBreakStillSplitsStructuredReply(t *testing.T) {
	// <botbr> 是模型显式要求的分条，去空行不能把它一起吃掉。
	reply := "1. 甲\n2. 乙\n3. 丙\n" + notificationSplitMarker + "\n对了，你带伞了吗。"
	parts := splitReply(reply, 900)
	if len(parts) != 2 {
		t.Fatalf("显式分条应保留，实际 %d 条：%#v", len(parts), parts)
	}
	if !strings.Contains(parts[1], "带伞") {
		t.Fatalf("分条后的第二段不对：%#v", parts)
	}
}

func TestCollapseBlankLinesTrimsTrailingSpaces(t *testing.T) {
	got := collapseBlankLines("甲   \n\n\n乙\t\n")
	if got != "甲\n乙" {
		t.Fatalf("collapseBlankLines = %q", got)
	}
}
