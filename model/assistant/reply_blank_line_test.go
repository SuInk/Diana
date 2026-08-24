// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"fmt"
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

// 空行只是排版，不是消息边界。以前运行时按空行分条，于是清单会被拆碎，又反过来
// 加了一套「看起来像清单就别拆」的识别去救；识别顺手把长度上限从 160 顶到 900，
// 清单因此能发成一条 400 多字的宽气泡。现在空行统一收掉、不再分条，那套识别连同
// 它的三个阈值一起删掉了。
func TestBlankLinesAreLayoutNotMessageBoundaries(t *testing.T) {
	for name, reply := range map[string]string{
		"清单":   structuredReplyWithBlankLines,
		"普通对话": "先说结论，端口被占了。\n\n完整报错贴一下我看看。",
	} {
		t.Run(name, func(t *testing.T) {
			for _, part := range splitReply(reply, 900) {
				for _, line := range strings.Split(part, "\n") {
					if strings.TrimSpace(line) == "" {
						t.Fatalf("发出去的文本里残留空行：\n%s", part)
					}
				}
			}
		})
	}
}

// 清单不再享受被抬高的长度上限：该按配置的长度切就切。这正是宽气泡的来源——
// 识别命中后阈值被顶到 900，一份 400 多字的清单会整块发成一条，QQ 再按最长行把
// 气泡横向撑开。
func TestStructuredReplyRespectsConfiguredChunkSize(t *testing.T) {
	// 复刻真实场景：一份明显超过群友风格上限的清单。
	var builder strings.Builder
	builder.WriteString("附近这么走顺路：\n")
	for index := 1; index <= 20; index++ {
		fmt.Fprintf(&builder, "%d. 第 %d 个去处\n", index, index)
		builder.WriteString("这里写一句三十来个字的说明，交代为什么值得去、什么时候去比较合适。\n\n")
	}
	reply := builder.String()
	if total := len([]rune(reply)); total <= chatReplyChunkSize {
		t.Fatalf("用例本身要长过上限才有意义，实际 %d 字", total)
	}

	parts := splitReply(reply, chatReplyChunkSize)
	if len(parts) < 2 {
		t.Fatalf("清单应按配置的 %d 字上限切分，实际 %d 条", chatReplyChunkSize, len(parts))
	}
	for _, part := range parts {
		if got := len([]rune(part)); got > chatReplyChunkSize {
			t.Fatalf("切分后仍有 %d 字的分片，超过上限 %d：%q", got, chatReplyChunkSize, part)
		}
	}
	// 内容不能被顺带吃掉。
	joined := strings.Join(parts, "\n")
	for _, keep := range []string{"1. 第 1 个去处", "20. 第 20 个去处", "什么时候去比较合适"} {
		if !strings.Contains(joined, keep) {
			t.Fatalf("清单内容丢失 %q：\n%s", keep, joined)
		}
	}
}

// <dianabr> 是模型显式要求的分条，任何情况下都保留；收空行不能把它一起吃掉。
func TestExplicitBreakStillSplitsReply(t *testing.T) {
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
