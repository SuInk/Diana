package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/SuInk/diana/model/agent"
	"github.com/SuInk/diana/model/llm"
)

type documentDeliveryProbe struct {
	llm.LLMClient
	remaining int
	requests  []llm.GenerateRequest
	responses []*llm.GenerateResponse
}

func (p *documentDeliveryProbe) Generate(ctx context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	if p.remaining == 0 {
		return nil, fmt.Errorf("live test model-call budget exhausted")
	}
	p.remaining--
	p.requests = append(p.requests, req)
	resp, err := p.LLMClient.Generate(ctx, req)
	p.responses = append(p.responses, resp)
	return resp, err
}

var probeDayHeading = regexp.MustCompile(`(?i)^(?:day[ \t]*([123])|第[ \t]*([一二三123])[ \t]*[天日])`)

func dailyDocumentIssues(bubbles []string, days int) []string {
	issues := replyContractIssues("travel", bubbles)
	if len(bubbles) < days || len(bubbles) > days+2 {
		issues = append(issues, fmt.Sprintf("expected one message per day plus optional intro/preparation, got %d", len(bubbles)))
	}
	seen := map[string]int{}
	for index, bubble := range bubbles {
		local := map[string]bool{}
		for _, line := range strings.Split(bubble, "\n") {
			if !isDocumentSectionLabel(line) {
				continue
			}
			match := probeDayHeading.FindStringSubmatch(strings.Trim(line, " #*_\t"))
			if len(match) == 0 {
				continue
			}
			day := match[1] + match[2]
			day = strings.NewReplacer("一", "1", "二", "2", "三", "3").Replace(day)
			if previous, ok := seen[day]; ok && previous != index {
				issues = append(issues, "day "+day+" is split across messages")
			}
			seen[day], local[day] = index, true
		}
		if len(local) > 1 {
			issues = append(issues, "multiple days collapsed into one message")
		}
	}
	for day := 1; day <= days; day++ {
		if _, ok := seen[fmt.Sprint(day)]; !ok {
			issues = append(issues, fmt.Sprintf("day %d has no identifiable heading", day))
		}
	}
	return issues
}

func TestDailyDocumentProbeRejectsBothBadExtremes(t *testing.T) {
	day1, day2 := "## Day 1\n### Morning\nWalk\n### Evening\nDinner", "## Day 2\n### Morning\nMuseum\n### Evening\nReturn"
	if issues := dailyDocumentIssues([]string{"Intro", day1, day2, "Preparation"}, 2); len(issues) != 0 {
		t.Fatal(issues)
	}
	for _, invalid := range [][]string{{day1 + "\n" + day2}, {"## Day 1", "Morning", "Afternoon", "Evening", day2}} {
		if len(dailyDocumentIssues(invalid, 2)) == 0 {
			t.Fatalf("accepted bad grouping: %q", invalid)
		}
	}
}

func TestCapturedDocumentDeliveryReplay(t *testing.T) {
	withFastSendTiming(t)
	raw, err := os.ReadFile("testdata/document_delivery_live_20260906.json")
	if err != nil {
		t.Fatal(err)
	}
	var samples []struct {
		Case      string `json:"case"`
		Input     string `json:"input"`
		Output    string `json:"raw_output"`
		Days      int    `json:"days"`
		WantCount int    `json:"want_count"`
	}
	if err := json.Unmarshal(raw, &samples); err != nil {
		t.Fatal(err)
	}
	for _, sample := range samples {
		t.Run(sample.Case, func(t *testing.T) {
			bubbles := splitChatReply(sample.Output, chatSplitLimits{})
			issues := replyContractIssues(sample.Case, bubbles)
			if sample.Days > 0 {
				issues = dailyDocumentIssues(bubbles, sample.Days)
			}
			if len(issues) != 0 || len(bubbles) != sample.WantCount {
				t.Fatalf("messages=%d want=%d issues=%v", len(bubbles), sample.WantCount, issues)
			}
			source := strings.ReplaceAll(sample.Output, notificationSplitMarker, "\n")
			if !reflect.DeepEqual(strings.Fields(source), strings.Fields(strings.Join(bubbles, "\n"))) {
				t.Fatal("document content changed or lost")
			}
			for _, mode := range []string{"plain", "forward", "forward_failed"} {
				t.Run(mode, func(t *testing.T) {
					base := &recordingChannel{}
					var channel Channel = base
					if mode == "forward_failed" {
						reject := &documentForwardRejectChannel{}
						base, channel = &reject.recordingChannel, reject
					}
					threshold := 0
					if mode != "plain" {
						threshold = 1
					}
					rt := NewRuntime(BotConfig{BotAccount: "42", ForwardReplyThreshold: threshold, SendChunkIntervalMS: 1}.WithDefaults(), channel, NewPluginManager(), nil, nil, nil, nil)
					_, err := rt.sendDecorated(context.Background(), MessageEvent{Kind: EventKindGroup, GroupID: "123456", SelfID: "42", UserID: "10001"}, sample.Output, outboundDecoration{})
					if err != nil {
						t.Fatal(err)
					}
					var sent []string
					for _, message := range base.sentSnapshot() {
						sent = append(sent, message.Text)
					}
					if mode == "forward" {
						calls := recordedCallsByAction(base.callsSnapshot(), "send_group_forward_msg")
						if len(calls) != 1 || len(sent) != 0 {
							t.Fatalf("forward delivery: cards=%d plain=%d calls=%v", len(calls), len(sent), base.callsSnapshot())
						}
						nodes, ok := calls[0].params["messages"].([]map[string]any)
						if !ok || len(nodes) != len(bubbles) {
							t.Fatal("forward node count differs from message count")
						}
						for i, node := range nodes {
							call := recordingAPICall{params: map[string]any{"messages": []map[string]any{node}}}
							if !forwardCallContainsText(call, bubbles[i]) {
								t.Fatalf("forward node %d lost content or changed order", i)
							}
						}
					} else if !reflect.DeepEqual(sent, bubbles) {
						t.Fatalf("recorded sends differ: %q", sent)
					}
					if mode == "plain" {
						record, err := json.Marshal(map[string]any{"case": sample.Case, "input": sample.Input, "raw_output": sample.Output, "bubbles": bubbles, "sent": sent})
						if err != nil {
							t.Fatal(err)
						}
						t.Logf("REPLAY_SAMPLE %s", record)
					}
				})
			}
		})
	}
}

// Four calls maximum, through the real Agent runner and recording-only delivery.
func TestLiveDocumentDeliveryAcceptance(t *testing.T) {
	probe := &documentDeliveryProbe{LLMClient: liveLLMClient(t), remaining: 4}
	withFastSendTiming(t)
	cases := []struct {
		name, input string
		days        int
	}{
		{"chengdu", "帮我做成都两天的详细行程，第一次去，不自驾，每天上午下午晚上怎么安排，交通和需要提前准备的事也写清楚", 2},
		{"hangzhou_unseen", "杭州三天怎么详细安排？带爸妈，不自驾，每天上午下午晚上做什么、怎么坐车，以及要提前准备什么都写清楚，不想太累", 3},
		{"explicit", "先回应我终于修好 bug 这件事，再单独问我是哪一步出了问题，分成两次发言", 0},
		{"short", "只回复：在的", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			start := len(probe.requests)
			runner, err := agent.NewRunner(probe, agent.Config{MaxSteps: 2}, agent.NewToolRegistry(markerProbeTool{}))
			if err != nil {
				t.Fatal(err)
			}
			defer runner.Close()
			prompt := defaultSystemPrompt + "\n" + ReplyStyleHuman.prompt(true, personaVoice{}) + "\n" + ReplyStyleHuman.closingAnchor()
			ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
			defer cancel()
			response, runErr := runner.Run(ctx, agent.Request{Messages: []llm.Message{
				{Role: llm.RoleSystem, Content: prompt}, {Role: llm.RoleUser, Content: tc.input},
			}})
			var output string
			var bubbles, sent, issues []string
			if runErr == nil {
				output = response.Text
				bubbles = splitChatReply(output, chatSplitLimits{})
				if tc.days > 0 {
					issues = dailyDocumentIssues(bubbles, tc.days)
					if !strings.Contains(output, notificationSplitMarker) {
						issues = append(issues, "model did not specify document message boundaries")
					}
				} else {
					issues = replyContractIssues(tc.name, bubbles)
				}
				channel := &recordingChannel{}
				rt := NewRuntime(BotConfig{BotAccount: "42", SendChunkIntervalMS: 1}.WithDefaults(), channel, NewPluginManager(), nil, nil, nil, nil)
				_, runErr = rt.sendDecorated(ctx, MessageEvent{Kind: EventKindGroup, GroupID: "123456", SelfID: "42", UserID: "10001"}, output, outboundDecoration{})
				for _, message := range channel.sentSnapshot() {
					sent = append(sent, message.Text)
				}
				if !reflect.DeepEqual(sent, bubbles) {
					issues = append(issues, "recorded delivery differs from splitter output")
				}
			}
			errorText := ""
			if runErr != nil {
				errorText = strings.ReplaceAll(runErr.Error(), os.Getenv("DIANA_TEST_LLM_API_KEY"), "[redacted]")
			}
			raw, marshalErr := json.Marshal(map[string]any{
				"case": tc.name, "input": tc.input, "raw_output": output, "bubbles": bubbles, "sent": sent,
				"requests": probe.requests[start:], "responses": probe.responses[start:], "issues": issues, "error": errorText,
			})
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			t.Logf("DELIVERY_SAMPLE %s", raw)
			if errorText != "" || len(issues) > 0 {
				t.Errorf("acceptance failed: %s %v", errorText, issues)
			}
		})
	}
	t.Logf("MODEL_CALLS %d/4", 4-probe.remaining)
}

func TestLiveUserDeliveryChoice(t *testing.T) {
	probe := &documentDeliveryProbe{LLMClient: liveLLMClient(t), remaining: 2}
	withFastSendTiming(t)
	for _, tc := range []struct {
		name, input string
		mode        replyDeliveryMode
		want        int
	}{
		{"single", "帮我安排杭州两天的行程，每天上午、下午、晚上以及交通和准备事项都写清楚。这次不要分条，整份放在一条消息里一次发完。", replyDeliverySingle, 1},
		{"auto", "这次可以分条：先回应我终于修好 bug 了，再单独问我是哪一步出了问题，分成两条消息发。", replyDeliveryAuto, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			start := len(probe.requests)
			runner, err := agent.NewRunner(probe, agent.Config{MaxSteps: 2}, agent.NewToolRegistry(markerProbeTool{}))
			if err != nil {
				t.Fatal(err)
			}
			defer runner.Close()
			cfg := BotConfig{BotAccount: "42", NaturalReplySplitEnabled: boolPointer(tc.mode == replyDeliverySingle)}.WithDefaults()
			if tc.mode == replyDeliverySingle {
				cfg.ForwardReplyThreshold = 1
			}
			prompt := defaultSystemPrompt + "\n" + ReplyStyleHuman.prompt(boolValue(cfg.NaturalReplySplitEnabled, true), personaVoice{}) + "\n" + ReplyStyleHuman.closingAnchor()
			ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
			defer cancel()
			response, runErr := runner.Run(ctx, agent.Request{Messages: []llm.Message{{Role: llm.RoleSystem, Content: prompt}, {Role: llm.RoleUser, Content: tc.input}}})
			var output string
			var sent, issues []string
			var mode replyDeliveryMode
			if runErr == nil {
				output = response.Text
				normalized := normalizeReplyPreservingControlIntent(output, 0)
				body, intent := consumeReplyControlIntent(normalized)
				mode = intent.DeliveryMode
				channel := &recordingChannel{}
				rt := NewRuntime(cfg, channel, NewPluginManager(), nil, nil, nil, nil)
				event := MessageEvent{Kind: EventKindGroup, GroupID: "123456", UserID: "10001", SelfID: "42", replyDeliveryMode: mode}
				_, runErr = rt.sendDecorated(ctx, event, body, outboundDecoration{})
				for _, msg := range channel.sentSnapshot() {
					sent = append(sent, msg.Text)
				}
				if mode != tc.mode || len(sent) != tc.want {
					issues = append(issues, fmt.Sprintf("mode=%s want=%s messages=%d want=%d", mode, tc.mode, len(sent), tc.want))
				}
				if len(channel.callsSnapshot()) != 0 {
					issues = append(issues, "unexpected automatic forward")
				}
				for _, msg := range sent {
					if strings.Contains(msg, replySingleMarker) || strings.Contains(msg, replyAutoMarker) || strings.Contains(msg, notificationSplitMarker) {
						issues = append(issues, "control metadata leaked")
					}
				}
			}
			errorText := ""
			if runErr != nil {
				errorText = strings.ReplaceAll(runErr.Error(), os.Getenv("DIANA_TEST_LLM_API_KEY"), "[redacted]")
			}
			raw, err := json.Marshal(map[string]any{"case": tc.name, "input": tc.input, "raw_output": output, "mode": mode, "sent": sent, "issues": issues, "error": errorText, "requests": probe.requests[start:], "responses": probe.responses[start:]})
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("CHOICE_SAMPLE %s", raw)
			if errorText != "" || len(issues) > 0 {
				t.Errorf("choice failed: %s %v", errorText, issues)
			}
		})
	}
	t.Logf("MODEL_CALLS %d/2", 2-probe.remaining)
}
