// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import "testing"

func TestStripChatPeriods(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		{"行尾", "好的，我去看看。", "好的，我去看看"},
		{"句中换空格", "不一定会扣他钱。关键看有没有揽收。", "不一定会扣他钱 关键看有没有揽收"},
		{"分条标记前后", "晚到快一小时了。" + notificationSplitMarker + "先跟他说一声。", "晚到快一小时了" + notificationSplitMarker + "先跟他说一声"},
		{"消息内换行", "第一句。" + notificationLineMarker + "第二句。", "第一句" + notificationLineMarker + "第二句"},
		{"问号感叹号不动", "真的吗？太好了！", "真的吗？太好了！"},
		{"拖长的语气不动", "这个嘛。。。", "这个嘛。。。"},
		{"引号里的不动", "他说「今天不去了。」就走了。", "他说「今天不去了。」就走了"},
		{"网址那行不动", "看这里 https://example.com/a。", "看这里 https://example.com/a。"},
		{"代码块整条不动", "```\nfmt.Println(\"。\")\n```\n改好了。", "```\nfmt.Println(\"。\")\n```\n改好了。"},
		{"没有句号", "嘿嘿，收到夸奖啦～", "嘿嘿，收到夸奖啦～"},
	} {
		if got := stripChatPeriods(tc.in); got != tc.want {
			t.Errorf("%s: stripChatPeriods(%q) = %q, want %q", tc.name, tc.in, got, tc.want)
		}
	}
}

// 去句号在切好每条之后：长度兜底照旧按句号断句，发出去的每条都不带句号。
func TestSplitChatReplyDropsPeriodsAfterSplitting(t *testing.T) {
	got := splitChatReply("先核对配置是否生效。然后查看服务启动日志。"+notificationSplitMarker+"最后确认端口是否被占用。", chatSplitLimits{})
	want := []string{"先核对配置是否生效 然后查看服务启动日志", "最后确认端口是否被占用"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("splitChatReply = %q, want %q", got, want)
	}
}
