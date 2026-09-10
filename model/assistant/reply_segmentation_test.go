package assistant

import (
	"reflect"
	"strings"
	"testing"
)

func promptTeachesSegmentation(prompt string) bool {
	// 「不许真实换行」现在只由 replyBlankLineRule 说一次，分条规则不再重复一遍。
	for _, want := range []string{notificationSplitMarker, notificationLineMarker, "不得输出真实换行符"} {
		if !strings.Contains(prompt, want) {
			return false
		}
	}
	return true
}

func TestReplyPromptTeachesExplicitLayoutProtocol(t *testing.T) {
	prompt := ReplyStyleAssistant.prompt(true, personaVoice{})
	if !promptTeachesSegmentation(prompt) {
		t.Fatalf("prompt does not teach explicit layout: %q", prompt)
	}
	if strings.Contains(prompt, "意群边界换行") || strings.Contains(prompt, "换行不会分条、只是") {
		t.Fatalf("prompt still teaches literal-newline semantics: %q", prompt)
	}
}

func TestExplicitReplyLayoutMarkers(t *testing.T) {
	reply := "先说结论" + notificationSplitMarker +
		"配置如下：" + notificationLineMarker + "1. 第一项" + notificationLineMarker + "2. 第二项" +
		notificationSplitMarker + "最后补充"
	want := []string{"先说结论", "配置如下：\n1. 第一项\n2. 第二项", "最后补充"}
	for name, split := range map[string]func(string, chatSplitLimits) []string{
		"chat": splitChatReply, "forward": splitForwardReply,
	} {
		if got := split(reply, chatSplitLimits{}); !reflect.DeepEqual(got, want) {
			t.Fatalf("%s got %#v, want %#v", name, got, want)
		}
	}
}

func TestLiteralNewlinesCannotControlReplyLayout(t *testing.T) {
	input := "第一段\r\n第二段\n\n1. 项目"
	want := []string{"第一段，第二段，1. 项目"}
	if got := splitChatReply(input, chatSplitLimits{}); !reflect.DeepEqual(got, want) {
		t.Fatalf("literal newlines affected layout: %#v", got)
	}
}

func TestNotificationUsesMessageMarkerAndKeepsProgrammaticLines(t *testing.T) {
	card := "标题\n明细" + notificationSplitMarker + "跟评"
	got := splitReply(card, notificationChunkSize)
	want := []string{"标题\n明细", "跟评"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("notification split = %#v", got)
	}
}

func TestLengthFallbackCutsAtChinesePunctuation(t *testing.T) {
	long := strings.Repeat("这是一句用来占位的话，长度足够触发兜底切分。", 6)
	for _, chunk := range chunkTextByLength(long, 60) {
		runes := []rune(strings.TrimSpace(chunk))
		last := runes[len(runes)-1]
		if !isSentenceEnd(last) && !isClauseBreak(last) {
			t.Fatalf("length fallback cut mid-word: %q", chunk)
		}
	}
}

func TestChatReplyPreservesTrailingPunctuation(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"知道了。", "知道了。"},
		{"真的吗？", "真的吗？"},
		{"版本是 v1.0.", "版本是 v1.0."},
	} {
		if got := splitChatReply(tc.in, chatSplitLimits{}); len(got) != 1 || got[0] != tc.want {
			t.Fatalf("%q => %#v, want %q", tc.in, got, tc.want)
		}
	}
}

func TestRawSoftNewlinesBecomeNaturalPunctuation(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"前面说得对\n这里只靠提示词不稳定", "前面说得对，这里只靠提示词不稳定"},
		{"Check logs\nRestart service", "Check logs. Restart service"},
		{"已经说完。\n继续下一句", "已经说完。继续下一句"},
	} {
		if got := splitChatReply(tc.in, chatSplitLimits{}); len(got) != 1 || got[0] != tc.want {
			t.Fatalf("%q => %#v, want %q", tc.in, got, tc.want)
		}
	}
}

func TestForwardCardThresholdStillUsesExplicitMessages(t *testing.T) {
	chunks := splitChatReply(strings.Join([]string{"a", "b", "c", "d", "e"}, notificationSplitMarker), chatSplitLimits{})
	if !shouldUseForwardReply("abcde", chunks, 0, 4) {
		t.Fatal("five explicit messages should use a four-node forward threshold")
	}
}

func TestGroupNaturalSplitSettingStillReachesPromptMode(t *testing.T) {
	on := ReplyStyleAssistant.prompt(true, personaVoice{})
	off := ReplyStyleAssistant.prompt(false, personaVoice{})
	if !strings.Contains(on, "当前开启自然分条") || !strings.Contains(off, "默认只发送一条消息") {
		t.Fatalf("split setting did not select prompt mode: on=%q off=%q", on, off)
	}
	for _, prompt := range []string{on, off} {
		if !strings.Contains(prompt, notificationSplitMarker) || !strings.Contains(prompt, notificationLineMarker) {
			t.Fatalf("prompt missing explicit markers: %q", prompt)
		}
	}
}
