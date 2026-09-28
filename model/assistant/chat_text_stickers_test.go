// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"reflect"
	"testing"
)

func TestStripTrailingTextStickers(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		{"没闭合的括号", "那我就当你没说」（doge", "那我就当你没说」"},
		{"闭合的括号", "好吧好吧(doge)", "好吧好吧"},
		{"方括号连着几个", "你赢了 [狗头][狗头]", "你赢了"},
		{"斜杠前缀", "行吧 /滑稽", "行吧"},
		{"收掉留下的逗号", "我先溜了，（狗头保命）", "我先溜了"},
		{"大小写", "稳了（DOGE）", "稳了"},
		{"行中间的不动", "（doge）这个表情是狗头", "（doge）这个表情是狗头"},
		{"正常括号不动", "明天再说（我先睡了）", "明天再说（我先睡了）"},
		{"不带括号的词不动", "我在旁边吃瓜", "我在旁边吃瓜"},
	} {
		if got := stripTrailingTextStickers(tc.in); got != tc.want {
			t.Errorf("%s: stripTrailingTextStickers(%q) = %q, want %q", tc.name, tc.in, got, tc.want)
		}
	}
}

func TestSplitChatReplyDropsTrailingTextStickers(t *testing.T) {
	got := splitChatReply("那我就当你没说（doge"+notificationSplitMarker+"（doge）"+notificationSplitMarker+"看 https://example.com/doge", chatSplitLimits{})
	want := []string{"那我就当你没说", "看 https://example.com/doge"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("splitChatReply = %q, want %q", got, want)
	}
}
