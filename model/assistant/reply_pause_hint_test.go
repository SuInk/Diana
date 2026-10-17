package assistant

import (
	"strings"
	"testing"
)

// 收声提示必须读起来像人随口说的。后台词汇漏一个就打回，宁可不发也不退回模板。
func TestSanitizeReplyPauseHintRejectsSystemVoice(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
		want string
	}{
		{"natural", "那我先去忙点别的啦，晚点再聊喵", "那我先去忙点别的啦，晚点再聊喵"},
		{"quoted", "「先歇会儿，一会儿回来喵」", "先歇会儿，一会儿回来喵"},
		{"pause_word", "我先暂停一下喵", ""},
		{"account_word", "已暂停响应此账号", ""},
		{"loop_word", "为避免循环我先不说话了", ""},
		{"mention", "@某人 我先去忙啦", ""},
		{"cq_code", "[CQ:at,qq=20002] 先不聊了", ""},
		{"account_number", "账号 20002 先歇着", ""},
		{"too_long", strings.Repeat("先", 31), ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := sanitizeReplyPauseHint(tc.raw); got != tc.want {
				t.Fatalf("sanitize(%q) = %q，want %q", tc.raw, got, tc.want)
			}
		})
	}
}
