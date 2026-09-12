package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

func TestLiveSemanticReplyGate(t *testing.T) {
	client := liveLLMClient(t)
	for _, tc := range []struct{ name, request, previous, candidate, action string }{
		{"duplicate", "茯砖茶是什么", "茯砖茶是黑茶的一种，常见产地有湖南安化和陕西泾阳", "它属于黑茶，湖南安化和陕西泾阳都很有名", "drop"},
		{"partial", "还要说说怎么保存", "茯砖茶是黑茶的一种", "茯砖茶属于黑茶。保存时要避光、防潮、远离异味", "rewrite"},
		{"explicit_repeat", "刚才没看清，请完整再说一遍", "茯砖茶是黑茶的一种", "茯砖茶是黑茶的一种", "keep"},
		{"independent", "标准输出是什么", "茯砖茶是黑茶的一种", "标准输出是程序默认输出结果的数据流", "keep"},
		{"correction", "你确认一下之前说的分类", "茯砖茶是红茶", "刚才说错了，茯砖茶属于黑茶，不是红茶", "keep"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &liveTopicProbe{LLMProvider: client, t: t}
			r := topicTestRuntime(p)
			event := directedGroupMessage("m", "u", tc.request)
			gate, release, err := r.lockSemanticReply(context.Background(), event)
			if err != nil {
				t.Fatal(err)
			}
			defer release()
			gate.remember("之前的问题", tc.previous, "u")
			for i := 0; i < 2; i++ {
				p.raw = ""
				got, err := r.deduplicateReply(context.Background(), event, tc.request, tc.candidate, BotConfig{MaxReplyChars: 300}, gate, true)
				t.Logf("FINAL_OUTPUT=%q ERROR=%v", got, err)
				var result struct {
					Action string `json:"action"`
				}
				if parseErr := json.Unmarshal([]byte(stripJSONCodeFence(p.raw)), &result); parseErr != nil || result.Action != tc.action {
					t.Errorf("sample %d action=%s want=%s parse=%v", i+1, result.Action, tc.action, parseErr)
				}
				if errors.Is(err, errDuplicateReply) != (tc.action == "drop") {
					t.Errorf("unexpected delivery decision: %v", err)
				}
				if tc.action == "rewrite" && (got == "" || got == tc.candidate) {
					t.Error("rewrite not applied")
				}
			}
		})
	}
}
