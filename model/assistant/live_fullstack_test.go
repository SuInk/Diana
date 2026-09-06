package assistant

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SuInk/diana/model/llm"
)

type fullstackProbeClient struct {
	llm.LLMClient
	mu        sync.Mutex
	calls     int
	toolCalls []string
}

func (c *fullstackProbeClient) Generate(ctx context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	resp, err := c.LLMClient.Generate(ctx, req)
	c.mu.Lock()
	c.calls++
	if err == nil && resp != nil {
		for _, call := range resp.ToolCalls {
			c.toolCalls = append(c.toolCalls, call.Name)
		}
	}
	c.mu.Unlock()
	return resp, err
}

// Full pipeline: real Runtime, real system prompt with tool rules, default
// plugins (web search via keyless Exa), agent loop, delivery splitting.
func TestLiveFullStackResearchQuestions(t *testing.T) {
	client := liveLLMClient(t)
	withFastSendTiming(t)
	cases := []struct{ name, text string }{
		{"travel_broad", "Diana 成都周末两天怎么玩？"},
		{"research_agent", "Diana 帮我调查一下 AI 里说的 agent 到底是什么"},
		{"travel_detailed", "Diana 帮我做成都两天的详细行程，第一次去，不自驾"},
	}
	for _, style := range []ReplyStyle{ReplyStyleCatgirl, ReplyStyleHuman} {
		for _, tc := range cases {
			t.Run(fmt.Sprintf("%s/%s", style, tc.name), func(t *testing.T) {
				probe := &fullstackProbeClient{LLMClient: client}
				channel := &recordingChannel{}
				cfg := BotConfig{GroupTriggers: []string{"Diana"}, BotAccount: "42", ReplyStyle: style}.WithDefaults()
				rt := NewRuntime(cfg, channel, NewDefaultPluginManager(), nil, nil, nil, func() (LLMProvider, error) { return probe, nil })
				event := MessageEvent{
					Kind: EventKindGroup, SelfID: "42", GroupID: "123456", UserID: "10001", MessageID: "fs-" + tc.name,
					SenderLevel: 40, SenderLevelLabel: "LV40",
					RawMessage: tc.text,
					Segments:   []MessageSegment{{Type: "text", Data: map[string]string{"text": tc.text}}},
				}
				prompt := rt.systemPrompt(event, nil)
				t.Logf("system_prompt_chars=%d has_style=%v has_split_rule=%v", len([]rune(prompt)), strings.Contains(prompt, "默认表达风格"), strings.Contains(prompt, notificationSplitMarker))
				ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
				defer cancel()
				start := time.Now()
				if err := rt.HandleEvent(ctx, event); err != nil {
					t.Fatalf("HandleEvent: %v", err)
				}
				last := -1
				stable := 0
				// Human-style delivery pauses between bubbles; wait until the
				// outbound count has been stable for a few seconds.
				waitForCondition(t, 150*time.Second, func() bool {
					n := len(channel.sentSnapshot()) + len(channel.callsSnapshot())
					if n == last && n > 0 {
						stable++
					} else {
						stable = 0
					}
					last = n
					return stable >= 800
				})
				probe.mu.Lock()
				t.Logf("elapsed=%s llm_calls=%d tool_calls=%q", time.Since(start).Round(time.Second), probe.calls, probe.toolCalls)
				probe.mu.Unlock()
				for i, call := range channel.callsSnapshot() {
					t.Logf("api_call[%d]=%s", i, call.action)
				}
				for i, msg := range channel.sentSnapshot() {
					t.Logf("sent[%d] chars=%d text=%q", i, len([]rune(msg.Text)), msg.Text)
				}
				if len(channel.sentSnapshot())+len(channel.callsSnapshot()) == 0 {
					t.Error("nothing was sent")
				}
			})
		}
	}
}
