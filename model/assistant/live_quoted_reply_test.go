package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/llm"
)

func TestLiveQuotedReplyContext(t *testing.T) {
	client := liveLLMClient(t)
	for _, mode := range []string{"original_quote_correction", "accepted_quote_repeat", "quoted_restatement"} {
		t.Run(mode, func(t *testing.T) {
			for sample := 0; sample < 2; sample++ {
				p := &liveTopicProbe{LLMProvider: client, t: t}
				r := topicTestRuntime(p)
				root := quotedPowerEvent("root", "45")
				follow := quotedPowerEvent("next", "80")
				var prior []proactiveReplyCandidate
				want := "correction"
				if mode == "accepted_quote_repeat" {
					prior = []proactiveReplyCandidate{{Event: follow}}
					follow = directedGroupMessage("continue", "user", "就按刚才的新参数继续算")
					want = "repeat"
				}
				if mode == "quoted_restatement" {
					follow.Quoted.RawMessage = "请完整重复一次"
					gate, release, _ := r.lockSemanticReply(context.Background(), follow)
					gate.rememberRequest(requestContextForReply(root, ""), nil, "45W工作一小时耗电0.045度")
					got, err := r.deduplicateReply(context.Background(), follow, "", "45W工作一小时耗电0.045度", BotConfig{}, gate, true)
					release()
					t.Logf("FINAL_REPLY=%q ERROR=%v", got, err)
					var decision struct {
						Action string `json:"action"`
					}
					if json.Unmarshal([]byte(stripJSONCodeFence(p.raw)), &decision) != nil || decision.Action != "keep" || err != nil || got == "" {
						t.Errorf("sample %d: repeated request not kept", sample+1)
					}
				} else {
					got := r.classifyDirectReplyTopic(context.Background(), root, prior, follow, follow.RawMessage)
					t.Logf("FINAL_RELATION=%s", got)
					if got != want {
						t.Errorf("sample %d relation=%s want=%s", sample+1, got, want)
					}
				}
			}
		})
	}
	t.Run("regenerated_answer", func(t *testing.T) {
		p := &liveTopicProbe{LLMProvider: client, t: t}
		root := directedGroupMessage("root", "user", "45W设备工作1小时，耗电多少度")
		correction := quotedPowerEvent("correction", "80")
		request := llm.GenerateRequest{Messages: []llm.Message{
			{Role: llm.RoleSystem, Content: "根据本轮问题和较晚的纠正给出最终答案。只输出一句耗电量，不要附加解释。"},
			{Role: llm.RoleUser, Content: proactiveTurnPromptTextAt(correction, "", 0)},
			{Role: llm.RoleUser, Content: updatedReplyRequestText(currentPromptText(root, root.RawMessage), replyRequestContexts([]proactiveReplyCandidate{{Event: correction}}))},
		}}
		response, err := p.Generate(context.Background(), request)
		if err != nil {
			t.Fatal("real generation failed; see redacted model error")
		}
		if !strings.Contains(response.Text, "0.08") || strings.Contains(response.Text, "0.045") {
			t.Errorf("old parameter used: %q", response.Text)
		}
	})
}
