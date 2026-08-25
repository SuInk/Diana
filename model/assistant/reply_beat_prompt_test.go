// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"strings"
	"testing"
)

// 群友风格靠提示词分条，不靠投递侧改写换行的含义。
//
// 「单个换行是同一条消息里的排版、<dianabr> 才是下一次发言」是已经和模型约好的
// 契约。运行时如果自己把换行重新解释成分条，分条位置就又变成看模型的排版习惯——
// splitReply 的注释里记着这个坑。所以这条能力只能长在提示词上：告诉群友风格
// 「连着说的两三句短话本来就是两三次发言」，让它照原契约写 <dianabr>。
func TestGroupmatePromptTeachesSplitMarker(t *testing.T) {
	prompt := ReplyStyleGroupmate.prompt(personaVoice{})
	if !strings.Contains(prompt, notificationSplitMarker) {
		t.Fatalf("群友风格没教分条标记 %s：\n%s", notificationSplitMarker, prompt)
	}
	// 规则之外还要有个示例：这个提示词里的语气和格式都是靠示例教会的。
	if !strings.Contains(prompt, "你：又来"+notificationSplitMarker) {
		t.Fatalf("群友风格缺少连发示例：\n%s", prompt)
	}
	// 反过来的一半同样要说清楚，否则模型会给每个列表项都加一个标记。
	if !strings.Contains(prompt, "清单") || !strings.Contains(prompt, "单个换行") {
		t.Fatalf("群友风格没说清单和连续论述仍用单个换行：\n%s", prompt)
	}
}

// 其余风格不带这条规则：它们本来就是一条消息说完一件事。
func TestOtherStylesDoNotTeachSplitMarker(t *testing.T) {
	for _, style := range []ReplyStyle{ReplyStyleAssistant, ReplyStyleGentle, ReplyStyleLively, ReplyStyleConcise, ReplyStyleCatgirl} {
		if strings.Contains(style.prompt(personaVoice{}), notificationSplitMarker) {
			t.Fatalf("风格 %s 不该教分条标记", style)
		}
	}
}

// 模型照做之后，投递侧原样按标记分条——这条链路本来就通，不需要额外规则。
func TestGroupmateConsecutiveBeatsSplitOnMarker(t *testing.T) {
	reply := "又来" + notificationSplitMarker + "先看 dmesg，多半是被 OOM 掉了"
	got := splitReply(reply, chatReplyChunkSize)
	want := []string{"又来", "先看 dmesg，多半是被 OOM 掉了"}
	if len(got) != len(want) {
		t.Fatalf("got %d chunks %q, want %v", len(got), got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("chunk %d = %q, want %q", i, got[i], want[i])
		}
	}
}

// 单个换行仍然是同一条消息里的排版，运行时不碰它。
func TestSingleNewlineStaysInOneMessage(t *testing.T) {
	reply := "端口被占了\n先 lsof -i:8080 看看是谁占着"
	if got := splitReply(reply, chatReplyChunkSize); len(got) != 1 {
		t.Fatalf("单个换行被拆成了 %d 条：%q", len(got), got)
	}
}
