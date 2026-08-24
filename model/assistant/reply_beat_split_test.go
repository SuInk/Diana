// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"strings"
	"testing"
)

// 截图里的原话：两句短的写成两行，却挤在同一个气泡里发了出去。
func TestSplitChatReplySendsShortBeatsSeparately(t *testing.T) {
	reply := strings.Join([]string{
		"好家伙，u8 电脑前肝的是 Yuki，醒来发现已经肝到公元前 8 世纪了喵",
		"但 event ID 必须单调递增，谁把 Yuki 的时间线 Rollup 回 8BC 了喵，哈！",
	}, "\n")
	got := splitChatReply(reply, groupmateReplyChunkSize, ReplyStyleGroupmate)
	if len(got) != 2 {
		t.Fatalf("groupmate short beats: got %d chunks %q, want 2", len(got), got)
	}
	for i, want := range strings.Split(reply, "\n") {
		if got[i] != want {
			t.Fatalf("chunk %d = %q, want %q", i, got[i], want)
		}
	}
}

// 其余风格不受影响：换行在那里仍然只是排版，提示词也没承诺过分条。
func TestSplitChatReplyKeepsOtherStylesIntact(t *testing.T) {
	reply := "第一句短的\n第二句短的"
	for _, style := range []ReplyStyle{ReplyStyleAssistant, ReplyStyleGentle, ReplyStyleLively, ReplyStyleConcise, ReplyStyleCatgirl} {
		got := splitChatReply(reply, 400, style)
		if len(got) != 1 {
			t.Fatalf("style %s: got %d chunks %q, want 1", style, len(got), got)
		}
	}
}

func TestSplitShortBeatsRejectsNonBeatShapes(t *testing.T) {
	longLine := strings.Repeat("长", chatBeatMaxRunes+1)
	cases := []struct {
		name  string
		input string
	}{
		{"清单不拆", "先说结论\n1. 打开设置\n2. 关掉那个开关"},
		{"项目符号不拆", "两条路\n- 直接重启\n- 或者先看日志"},
		{"步骤不拆", "照着做\n第一步 停服务\n第二步 换配置"},
		{"有长行就不拆", "短的一句\n" + longLine},
		{"行数太多不拆", "一\n二\n三\n四\n五"},
		{"代码块不拆", "这样写\n```go\nfmt.Println(1)\n```"},
		{"代码行不拆", "改成这样\nif (a) { b(); }"},
		{"单行原样", "就一句话"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := splitChatReply(tc.input, groupmateReplyChunkSize, ReplyStyleGroupmate)
			if len(got) != 1 {
				t.Fatalf("got %d chunks %q, want 1", len(got), got)
			}
		})
	}
}

// <dianabr> 和长度兜底仍然先生效，短句分条只在它们切出来的块里再看一眼。
func TestSplitChatReplyComposesWithExistingRules(t *testing.T) {
	got := splitChatReply("刚看到\n有点离谱<dianabr>回头细说", groupmateReplyChunkSize, ReplyStyleGroupmate)
	want := []string{"刚看到", "有点离谱", "回头细说"}
	if len(got) != len(want) {
		t.Fatalf("got %d chunks %q, want %v", len(got), got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("chunk %d = %q, want %q", i, got[i], want[i])
		}
	}
}

// 通知不能被这条规则波及：事实卡片的每一行都短，但它是一张卡片。
func TestNotificationChunkingUnaffectedByBeatSplit(t *testing.T) {
	card := "仓库有新 PR\n#12 修复登录\n作者 someone"
	if chunks := splitReply(card, notificationChunkSize); len(chunks) != 1 {
		t.Fatalf("notification split into %d chunks %q, want 1", len(chunks), chunks)
	}
}
