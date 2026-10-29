// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import "testing"

func TestStripLeakedControlMarkers(t *testing.T) {
	cases := map[string]string{
		"[reply:...] 念旧说的应该是我那句回得慢":                            "念旧说的应该是我那句回得慢",
		"嗯，那就好[DIANA_REPLY_SINGLE]":                            "嗯，那就好",
		"好[[DIANA_SOMETHING_NEW]]吧":                            "好吧",
		"【quote：12345】看这条":                                     "看这条",
		"先 @ 一下 [diana-at:12345] 再说":                           "先 @ 一下 [diana-at:12345] 再说",
		"[CQ:face,id=1]好":                                      "[CQ:face,id=1]好",
		"[Note: 记得带伞] 明天下雨":                                    "[Note: 记得带伞] 明天下雨",
		"看 [文档](https://x.test) 第 [1] 条":                       "看 [文档](https://x.test) 第 [1] 条",
		"写法是 `[reply:123]`，别照抄":                                "写法是 `[reply:123]`，别照抄",
		"[https://x.test/p/1](https://x.test/p/1) 去重了":         "[https://x.test/p/1](https://x.test/p/1) 去重了",
		"见 [https://x.test/a] 这页":                              "见 [https://x.test/a] 这页",
		"[agent_finalize:竹荪是种菌菇]":                              "竹荪是种菌菇",
		"[agent_finalize:竹荪是种菌菇，煮汤脆脆的":                         "竹荪是种菌菇，煮汤脆脆的",
		"[合并转发:abc] 看这个":                                       "[合并转发:abc] 看这个",
		"执行失败：Post \"[REDACTED_URL]\": lookup [REDACTED_HOST]": "执行失败：Post \"[REDACTED_URL]\": lookup [REDACTED_HOST]",
		"更正[[/DIANA_REPLY_SINGLE]]":                            "更正",
		"[DAIAN_REPLY_SINGLE]你问得挺快":                            "你问得挺快",
		"已根据『一个fufu [MSG_ID:113244033]』生成":                     "已根据『一个fufu』生成",
		"[reply_target: 540036259] 我看岔了":                       "我看岔了",
		"[dianda-at:37941] 你好":                                 "你好",
		"括号照发（":                                                "括号照发（",
		"```\n[[DIANA_REPLY_AUTO]]\n```\n好":                    "```\n[[DIANA_REPLY_AUTO]]\n```\n好",
	}
	for in, want := range cases {
		if got := stripLeakedControlMarkers(in); got != want {
			t.Errorf("stripLeakedControlMarkers(%q) = %q, want %q", in, got, want)
		}
	}
}
