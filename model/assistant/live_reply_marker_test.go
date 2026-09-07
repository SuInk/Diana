package assistant

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/SuInk/diana/model/agent"
	"github.com/SuInk/diana/model/llm"
)

type markerProbeTool struct{}

func (markerProbeTool) Name() string { return "test.unavailable" }
func (markerProbeTool) Description() string {
	return "测试占位工具，不提供外部能力，无需调用。"
}
func (markerProbeTool) Run(context.Context, map[string]any) (string, error) {
	return "无外部能力，请根据已有对话直接回复", nil
}

type markerProbeClient struct {
	llm.LLMClient
	finalCalls int
}

func (c *markerProbeClient) Generate(ctx context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	resp, err := c.LLMClient.Generate(ctx, req)
	if err == nil && resp != nil {
		for _, call := range resp.ToolCalls {
			if call.Name == "agent.finalize" {
				c.finalCalls++
			}
		}
	}
	return resp, err
}

// Opt-in only: synthetic conversations, no runtime channels or external tools.
func TestLiveReplyMarkersViaFinalize(t *testing.T) {
	runLiveMarkerCases(t, []liveMarkerCase{
		{"ordinary", "我终于修好那个折腾了一天的 bug 了，累死", false, "chat"},
		{"explicit", "先回应我终于修好 bug 这件事，再单独问我是哪一步出了问题，分成两次发言", false, "split"},
		{"short", "只回复：在的", false, "short"},
		{"list", "只给我三步整理桌面的方法，用编号列表，不加开头结尾", false, "list"},
		{"code", "只输出 Python 代码块，包含两行：print(1) 和 print(2)", false, "code"},
		{"history", "我终于修好那个折腾了一天的 bug 了，累死", true, "chat"},
	})
}

type liveMarkerCase struct {
	name, text string
	history    bool
	kind       string
}

func TestLiveReplyMarkersNaturalConversation(t *testing.T) {
	// No user-side requests for formatting, splitting, lists or markers.
	runLiveMarkerCases(t, []liveMarkerCase{
		{"good_news", "我拿到 offer 了！就是要搬去一个完全不熟的城市，有点慌", false, "chat"},
		{"debugging", "我终于修好那个折腾一天的 bug 了，结果就是少写了个等号", false, "chat"},
		{"plans", "周末本来约好出去，朋友突然说不来了，我都收拾好了", false, "chat"},
		{"question", "你觉得我是不是太在意别人怎么看我了", false, "chat"},
		{"topic_change", "刚才那个问题解决了。对了，你平时会想些什么啊", false, "chat"},
		{"greeting", "在吗", false, "chat"},
		{"long_history", "我拿到 offer 了！就是要搬去一个完全不熟的城市，有点慌", true, "chat"},
		{"long_history_question", "你觉得我是不是太在意别人怎么看我了", true, "chat"},
	})
}

func TestLiveConversationalIntent(t *testing.T) {
	runLiveMarkerCases(t, []liveMarkerCase{
		{"unseen_share", "明天第一次上台演出，刚刚试衣服的时候手都在抖", false, "chat"},
		{"unseen_complaint", "做了半天的蛋糕，脱模的时候整个塌了", false, "chat"},
		{"requested_advice", "要搬去新城市入职了，住处和通勤都没定，应该先准备什么？", false, "advice"},
		{"technical_help", "本地服务启动报 address already in use，macOS 上怎么查是谁占着端口？", false, "advice"},
	})
}

func TestLiveTravelConversationIntent(t *testing.T) {
	// Behavior-only probe: no live attraction, booking or transport verification.
	// Advice cases are reviewed from raw outputs, not graded by keyword matching.
	runLiveMarkerCases(t, []liveMarkerCase{
		{"travel_share", "终于能去成都玩了，有点期待", false, "chat"},
		{"travel_plan", "成都周末两天怎么玩？", false, "document"},
		{"travel_constraints", "带老人去成都三天，不想太累，怎么安排？", false, "document"},
		{"travel_unspecified", "帮我安排旅行", false, "chat"},
	})
}

func TestLiveAnswerDepth(t *testing.T) {
	// Natural inputs only; content adequacy is reviewed from the full output.
	runLiveMarkerCases(t, []liveMarkerCase{
		{"broad_travel", "成都周末两天怎么玩？", false, "advice"},
		{"constraints", "带老人去成都三天，不想太累，怎么安排？", false, "advice"},
		{"missing_info", "帮我安排旅行", false, "chat"},
		{"unseen_city", "杭州只有一天可以逛，去哪比较合适？", false, "advice"},
		{"technical_scope", "macOS 上怎么查是谁占着 8080 端口？", false, "advice"},
		{"explicit_detail", "帮我做成都两天的详细行程，第一次去，不自驾，每天上午下午晚上怎么安排，交通和需要提前准备的事也写清楚", false, "document"},
	})
}

// Natural inputs only, no formatting or splitting requests, no assertions:
// the model decides whether a reply is one message or several, and the
// delivered bubbles are recorded for review.
func TestLiveSplitAcrossStyles(t *testing.T) {
	cases := []liveMarkerCase{
		{"greeting", "在吗", false, "free"},
		{"done", "我搞定了！", false, "free"},
		{"interview", "今天面试挂了", false, "free"},
		{"server", "服务器又炸了", false, "free"},
		{"still_broken", "我改完了还是不行", false, "free"},
		{"good_news", "我拿到 offer 了！就是要搬去一个完全不熟的城市，有点慌", false, "free"},
		{"question", "你觉得我是不是太在意别人怎么看我了", false, "free"},
		{"desk", "桌面乱得没法看了，怎么整理比较好", false, "free"},
		{"technical", "本地服务启动报 address already in use，macOS 上怎么查是谁占着端口？", false, "free"},
	}
	for _, style := range []ReplyStyle{ReplyStyleCatgirl, ReplyStyleHuman} {
		t.Run(string(style), func(t *testing.T) {
			runLiveMarkerCasesWithStyle(t, style, cases)
		})
	}
}

func runLiveMarkerCases(t *testing.T, cases []liveMarkerCase) {
	t.Helper()
	runLiveMarkerCasesWithStyle(t, ReplyStyleCatgirl, cases)
}

func runLiveMarkerCasesWithStyle(t *testing.T, style ReplyStyle, cases []liveMarkerCase) {
	t.Helper()
	client := liveLLMClient(t)
	finalCalls, total, marked, chatNewlines, multiReplies := 0, 0, 0, 0, 0
	bubbleCounts := map[int]int{}
	for _, tc := range cases {
		for sample := 0; sample < 2; sample++ {
			t.Run(fmt.Sprintf("%s_%d", tc.name, sample+1), func(t *testing.T) {
				probe := &markerProbeClient{LLMClient: client}
				runner, err := agent.NewRunner(probe, agent.Config{MaxSteps: 2}, agent.NewToolRegistry(markerProbeTool{}))
				if err != nil {
					t.Fatal(err)
				}
				defer runner.Close()
				prompt := defaultSystemPrompt + "\n" + style.prompt(true, personaVoice{}) + "\n" + style.closingAnchor()
				messages := []llm.Message{{Role: llm.RoleSystem, Content: prompt}}
				if tc.history {
					messages = append(messages, livePaddingTurns(25)...)
				}
				messages = append(messages, llm.Message{Role: llm.RoleUser, Content: tc.text})
				ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
				defer cancel()
				resp, err := runner.Run(ctx, agent.Request{Messages: messages})
				if err != nil {
					t.Fatal(strings.ReplaceAll(err.Error(), os.Getenv("DIANA_TEST_LLM_API_KEY"), "[redacted]"))
				}
				total++
				finalCalls += probe.finalCalls
				text := strings.TrimSpace(resp.Text)
				hasMarker := strings.Contains(text, notificationSplitMarker)
				hasNewline := strings.Contains(text, "\n")
				if hasMarker {
					marked++
				}
				parts := splitChatReply(text, chatSplitLimits{})
				bubbleCounts[len(parts)]++
				if len(parts) > 1 {
					multiReplies++
				}
				remainingNewline := false
				for _, part := range parts {
					remainingNewline = remainingNewline || strings.Contains(part, "\n")
				}
				if (tc.kind == "chat" || tc.kind == "split") && remainingNewline {
					chatNewlines++
				}
				t.Logf("input=%q chars=%d finalize=%d marker=%v newline=%v output=%q", tc.text, len([]rune(text)), probe.finalCalls, hasMarker, hasNewline, text)
				for i, part := range parts {
					t.Logf("bubble[%d/%d] chars=%d text=%q", i+1, len(parts), len([]rune(part)), part)
				}
				joined := strings.Join(parts, "\n")
				if strings.Contains(joined, notificationSplitMarker) || strings.Contains(joined, notificationLineMarker) {
					t.Error("layout marker leaked into delivered text")
				}
				switch tc.kind {
				case "document":
					// There is no product length/count quota. Check the observed
					// regression instead: automatic splitting must not orphan a title.
					for i, part := range parts {
						if hasMarker || i == len(parts)-1 {
							continue
						}
						onlyHeadings := true
						for _, line := range strings.Split(part, "\n") {
							onlyHeadings = onlyHeadings && isDocumentSectionLabel(line)
						}
						if onlyHeadings {
							t.Errorf("bubble %d contains headings without their body: %q", i+1, part)
						}
					}
				case "chat":
					if remainingNewline {
						t.Error("ordinary chat used bubble-internal newlines")
					}
				case "split":
					if len(parts) < 2 || remainingNewline {
						t.Error("explicit split did not produce separate bubbles")
					}
				case "short":
					if len(parts) != 1 || hasNewline {
						t.Error("short reply was split")
					}
				case "list":
					if len(parts) != 1 || !remainingNewline {
						t.Error("list structure was split or flattened")
					}
				case "code":
					if len(parts) != 1 || !strings.Contains(parts[0], "print(1)\nprint(2)") {
						t.Error("code structure was changed")
					}
				}
			})
		}
	}
	t.Logf("SUMMARY samples=%d finalize_calls=%d replies_with_marker=%d multi_bubble_replies=%d delivered_chat_newline_violations=%d", total, finalCalls, marked, multiReplies, chatNewlines)
	t.Logf("BUBBLE_COUNTS %v", bubbleCounts)
}
