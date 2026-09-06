package assistant

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/llm"
)

type liveTopicProbe struct {
	LLMProvider
	t   *testing.T
	raw string
}

func (p *liveTopicProbe) Generate(ctx context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	input, _ := json.Marshal(req.Messages)
	p.t.Logf("RAW_INPUT=%s", input)
	response, err := p.LLMProvider.Generate(ctx, req)
	if err != nil {
		p.t.Logf("MODEL_ERROR=%s", strings.ReplaceAll(err.Error(), os.Getenv("DIANA_TEST_LLM_API_KEY"), "[redacted]"))
	}
	if response != nil {
		p.raw = response.Text
		output, _ := json.Marshal(response)
		p.t.Logf("RAW_OUTPUT=%s", output)
	}
	return response, err
}

func TestLiveReplyTopicRelations(t *testing.T) {
	client := liveLLMClient(t)
	for _, tc := range []struct {
		name, original, follow, want string
		recalled                     bool
	}{
		{"tea_repost", "茯砖茶是啥", "茯砖茶是啥", "repeat", true},
		{"paraphrase", "解释标准输出的作用", "能说说 stdout 是用来干什么的吗", "repeat", false},
		{"supplement", "安排两天行程", "还要带老人，少走路", "supplement", false},
		{"correction", "安排两天行程", "改成三天，其他不变", "correction", false},
		{"new_answer", "安排两天行程", "另外再写一份完全不同的方案，两份都保留", "independent", false},
		{"different_topic", "解释标准输出的作用", "茯砖茶是什么", "independent", false},
		{"recalled_new_topic", "解释标准输出的作用", "先不问这个，茯砖茶是什么", "independent", true},
		{"missing_reference", "解释这段报错", "按刚才那个改", "uncertain", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &liveTopicProbe{LLMProvider: client, t: t}
			r := topicTestRuntime(p)
			root := directedGroupMessage("root", "user", tc.original)
			root.ToMe = false
			root.RawMessage = tc.original
			root.Segments = []MessageSegment{{Type: "text", Data: map[string]string{"text": tc.original}}}
			follow := directedGroupMessage("follow", "user", tc.follow)
			if tc.recalled {
				r.noteRecalledInbound(root)
			}
			for sample := 0; sample < 2; sample++ {
				p.raw = ""
				got := r.classifyDirectReplyTopic(context.Background(), root, nil, follow, follow.RawMessage)
				var decision struct {
					Relation string `json:"relation"`
				}
				if err := json.Unmarshal([]byte(stripJSONCodeFence(p.raw)), &decision); err != nil {
					t.Errorf("sample %d: no valid response", sample+1)
					continue
				}
				if decision.Relation != tc.want {
					t.Errorf("sample %d: relation=%s want=%s", sample+1, decision.Relation, tc.want)
				}
				if (tc.want == "repeat" || tc.want == "supplement" || tc.want == "correction") && got != tc.want {
					t.Errorf("sample %d: runtime rejected relation %s", sample+1, got)
				}
			}
		})
	}
}
