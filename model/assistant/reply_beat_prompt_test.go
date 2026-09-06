// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"strings"
	"testing"
)

// 「连发短句」任何风格都不再教，「长回答按意群分条」所有风格都教。
//
// 这两件事都写 [diana-br]，但不是一回事：前者是把一次发言拆成两三条短消息，是语气；
// 后者是一段长回答别糊成一个气泡，是投递，跟风格无关。早先只有一档风格提到过标记，
// 别的风格全靠可编辑的「纯文本规则」文本框兜着——那份文案被改掉或停在旧版默认值
// 上，分条就彻底不发生了。
func TestConsecutiveBeatsNotTaughtWhileSegmentationIsUniversal(t *testing.T) {
	for _, style := range []ReplyStyle{ReplyStyleAssistant, ReplyStyleGentle, ReplyStyleLively, ReplyStyleConcise, ReplyStyleCatgirl, ReplyStyleHuman} {
		prompt := style.prompt(true, personaVoice{})
		if strings.Contains(prompt, "两三次独立发言") {
			t.Fatalf("风格 %s 不该教连发短句", style)
		}
		if !promptTeachesSegmentation(prompt) {
			t.Fatalf("风格 %s 缺少长回答分条规则：\n%s", style, prompt)
		}
	}
}

// 模型照做之后，投递侧原样按标记分条——这条链路本来就通，不需要额外规则。
func TestConsecutiveBeatsSplitOnMarker(t *testing.T) {
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
