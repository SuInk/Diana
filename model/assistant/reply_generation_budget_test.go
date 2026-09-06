package assistant

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/agent"
	"github.com/SuInk/diana/model/llm"
)

func TestReplyGenerationBudgetDoesNotMutateMessages(t *testing.T) {
	messages := make([]llm.Message, 3, 8)
	messages[0] = llm.Message{Role: llm.RoleSystem, Content: "Stable prefix", CacheBreakpoint: true}
	messages[1] = llm.Message{Role: llm.RoleAssistant, Content: "History"}
	messages[2] = llm.Message{Role: llm.RoleUser, Content: "Question"}
	want := append([]llm.Message(nil), messages...)
	for _, limit := range []int{300, 500} {
		got := withReplyGenerationBudget(messages, limit)
		if len(got) != 4 || !reflect.DeepEqual(got[:2], want[:2]) || !reflect.DeepEqual(got[3], want[2]) {
			t.Fatalf("message order or prefix changed: %#v", got)
		}
		if got[2].Content != replyGenerationBudgetPrompt(limit) || got[2].Role != llm.RoleSystem || !got[2].AtomicText || got[2].Priority != llm.MessagePrioritySystem {
			t.Fatal("budget is not an intact system instruction")
		}
		if !reflect.DeepEqual(messages, want) {
			t.Fatal("caller messages mutated")
		}
	}
	for _, limit := range []int{0, -1} {
		if !reflect.DeepEqual(withReplyGenerationBudget(messages, limit), want) || replyGenerationBudgetPrompt(limit) != "" {
			t.Fatal("disabled limit added an instruction")
		}
	}
}

func assertGenerationBudget(t *testing.T, req llm.GenerateRequest, limit int) {
	t.Helper()
	count := 0
	for _, message := range req.Messages {
		if message.Role == llm.RoleSystem && strings.Contains(message.Content, replyGenerationBudgetPrompt(limit)) {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("expected one %d-character budget, found %d", limit, count)
	}
}

func TestReplyGenerationBudgetReachesFirstCallWithoutCompression(t *testing.T) {
	for _, entry := range []string{"plain", "agent", "follow_up"} {
		t.Run(entry, func(t *testing.T) {
			p := &compressionTestProvider{capturingLLMProvider: capturingLLMProvider{reply: "完整简洁的答复"}}
			rt := NewRuntime(BotConfig{MaxReplyChars: 500}, nilChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) { return p, nil })
			rt.SetGroupConfigStore(&stubGroupConfigStore{configs: map[string]GroupConfig{"123456": {GroupID: "123456", MaxReplyChars: 80}}})
			event := MessageEvent{Kind: EventKindGroup, GroupID: "123456", UserID: "10001"}
			cfg := rt.effectiveConfigForEvent(event)
			if cfg.MaxReplyChars != 80 {
				t.Fatal("group budget did not override bot budget")
			}
			messages := []llm.Message{{Role: llm.RoleUser, Content: "请用一条消息简洁回答"}}
			var reply string
			var err error
			switch entry {
			case "plain":
				cfg.AgentEnabled = false
				reply, err = rt.generateReply(context.Background(), cfg, event, RelationshipPolicy{}, messages, nil)
			case "agent":
				cfg.AgentEnabled = false
				reply, err = rt.generateReplyWithAgentTools(context.Background(), cfg, messages, []agent.Tool{markerProbeTool{}})
			case "follow_up":
				reply = rt.followUpComment(context.Background(), followUpKindPlugin, event, "已发送正文")
			}
			if err != nil || reply != "完整简洁的答复" || p.generationCalls != 1 || len(p.requests) != 0 {
				t.Fatalf("reply=%q error=%v generation=%d compression=%d", reply, err, p.generationCalls, len(p.requests))
			}
			assertGenerationBudget(t, p.requestSnapshot(), 80)
		})
	}
}
