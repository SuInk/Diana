package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/llm"
)

func TestLiveReplyLayoutContract(t *testing.T) {
	client := liveLLMClient(t)
	for _, tc := range []struct {
		name, input     string
		multi, preserve bool
		count, lines    int
	}{
		{"math", "已知从2到500有95个质数、404个合数，f(1)=1，每遇质数加1、合数减1，f(500)是多少？简单说明理由", true, false, 1, 1},
		{"list", "用1. 2. 3.列出检查端口占用的三个步骤，只发一条，每项一行", true, false, 1, 3},
		{"three_lines", "只发一条，严格分三行：第一行甲，第二行乙，第三行丙，不要其他文字", true, false, 1, 3},
		{"two_inline", "发两条，每条不换行。第一条写甲和乙，第二条写丙和丁，不要其他文字", false, true, 2, 1},
		{"keep_paragraphs", "不要分条，但要保留换行。只输出两行：第一行检查配置，第二行重启服务", true, false, 1, 2},
		{"inline", "不要换行也不要分条，用一句话说明质数是什么", true, true, 1, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &liveTopicProbe{LLMProvider: client, t: t}
			cfg := BotConfig{BotAccount: "42", MaxReplyChars: 500, NaturalReplySplitEnabled: boolPointer(tc.multi), ReplyPreserveLineBreaks: boolPointer(tc.preserve)}.WithDefaults()
			r := NewRuntime(cfg, &recordingChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) { return p, nil })
			event := MessageEvent{Kind: EventKindPrivate, UserID: "user"}
			req := llm.GenerateRequest{Messages: withReplyGenerationBudget([]llm.Message{{Role: llm.RoleSystem, Content: r.systemPromptWithMode(event, nil, false)}, {Role: llm.RoleUser, Content: tc.input}}, 500)}
			response, err := p.Generate(context.Background(), req)
			if err != nil {
				t.Fatal("live request failed; see redacted model error")
			}
			prepared, err := r.prepareGeneratedReply(context.Background(), cfg, response.Text, event)
			if err != nil {
				t.Fatal(err)
			}
			parts := splitEventChatReply(prepared, cfg, event)
			output, _ := json.Marshal(parts)
			t.Logf("FINAL_MESSAGES=%s", output)
			if len(parts) != tc.count {
				t.Errorf("messages=%d want=%d", len(parts), tc.count)
			}
			for _, part := range parts {
				if strings.Count(part, "\n")+1 != tc.lines {
					t.Errorf("lines=%d want=%d", strings.Count(part, "\n")+1, tc.lines)
				}
			}
		})
	}
}
