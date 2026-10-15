package assistant

import (
	"context"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/llm"
)

type noSemanticDedupProvider struct {
	t      *testing.T
	audits int
}

func (p *noSemanticDedupProvider) Generate(_ context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	for _, m := range req.Messages {
		if strings.Contains(m.Content, "你是回复发送前的语义去重编辑器") {
			p.t.Error("unexpected semantic dedup call")
		}
		if strings.Contains(m.Content, "你是机器人回复的发送前审核器") {
			p.audits++
			return &llm.GenerateResponse{Text: `{"send_confidence":0.99,"account_safe":true}`}, nil
		}
	}
	return &llm.GenerateResponse{Text: "这是相同的正常答复"}, nil
}

func TestRepeatedReplyAfterCompletedTurnIsDeliveredUnchanged(t *testing.T) {
	for _, proactive := range []bool{false, true} {
		name := "direct"
		if proactive {
			name = "proactive"
		}
		t.Run(name, func(t *testing.T) {
			p := &noSemanticDedupProvider{t: t}
			r := topicTestRuntime(p)
			for _, id := range []string{"first", "second"} {
				event := directedGroupMessage(id, "user", "再说一次")
				if proactive {
					event.ToMe = false
					event.proactiveReply = true
				}
				outcome, err := r.replyAndRecord(context.Background(), event, event.RawMessage, "replied")
				if err != nil || outcome != "replied" {
					t.Fatalf("outcome=%s err=%v", outcome, err)
				}
			}
			sent := r.channel.(*recordingChannel).sentSnapshot()
			if len(sent) != 2 {
				t.Fatalf("sent=%+v", sent)
			}
			for _, message := range sent {
				if message.Text != "这是相同的正常答复" {
					t.Fatalf("reply changed: %q", message.Text)
				}
			}
			if proactive && p.audits < 2 {
				t.Fatalf("reply audit bypassed: %d", p.audits)
			}
		})
	}
}
