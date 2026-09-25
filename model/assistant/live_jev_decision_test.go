// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/SuInk/diana/model/llm"
)

// 真实打一次 TypeSafe 的 System One 接口，确认判断模型这条链路整条通：题目表发得出去、
// 答案回填出来的 JSON 能被接话评分解析器吃下、明显该回和明显不该回的消息分得开。
// 默认跳过，需要：
//
//	DIANA_LIVE_LLM=1 DIANA_TEST_JEV_API_KEY=...
//	DIANA_TEST_JEV_BASE_URL  可选，默认 https://api.typesafe.ai
//	DIANA_TEST_JEV_MODEL     可选，默认 jev-latest
func TestLiveJevParticipationRatings(t *testing.T) {
	client := liveJevClient(t)
	cases := []struct {
		name         string
		current      string
		recent       []string
		wantDirected bool
		maxChatIn    float64
	}{
		{
			name:         "叫了名字",
			current:      "diana 这个报错应该怎么修",
			recent:       []string{"Alice：编译又挂了", "Bob：贴一下日志"},
			wantDirected: true,
		},
		{
			name:         "在问别的群友",
			current:      "@Bob 你明天到底去不去",
			recent:       []string{"Alice：周末那个活动还有人去吗"},
			wantDirected: false,
		},
		{
			name:         "纯反应",
			current:      "草",
			recent:       []string{"Alice：刚把生产库删了", "Bob：？？？"},
			wantDirected: false,
			maxChatIn:    0.35,
		},
		{
			name:         "接机器人刚才的话",
			current:      "你刚说的那个参数是哪个",
			recent:       []string{"Diana（机器人）：把超时调大一点就行", "Alice：好"},
			wantDirected: true,
		},
	}
	prefs := ParticipationPreferences{RelevanceLevel: "on", ChatLevel: "medium", Desire: 50}
	spec := participationDecisionSpec(nil)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// 和线上一样喂对话稿；机器人的称呼要在，缺了它「叫没叫它」这道题无从判起。
			payload := proactiveReplyPayload{CurrentText: tc.current, CurrentSender: "Carol", BotAliases: []string{"Diana", "diana", "小 D"}}
			for i := len(tc.recent) - 1; i >= 0; i-- {
				sender, text, _ := strings.Cut(tc.recent[i], "：")
				isBot := strings.HasSuffix(sender, "（机器人）")
				payload.RecentMessages = append(payload.RecentMessages, proactiveReplyHistoryItem{Sender: strings.TrimSuffix(sender, "（机器人）"), Text: text, IsBot: isBot})
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			resp, err := client.Generate(ctx, llm.GenerateRequest{
				Messages: []llm.Message{
					{Role: llm.RoleSystem, Content: prefs.prompt()},
					{Role: llm.RoleUser, Content: "Intent Recognition：请判断当前消息是不是在跟机器人说话（directed 与 reason），并给出闲聊适合度（score 与 reason）。上下文：\n" + proactiveReplyTranscript(payload)},
				},
				Decision: spec,
			})
			if err != nil {
				t.Fatalf("live call failed: %v", err)
			}
			ratings, err := parseParticipationRatings(resp.Text)
			if err != nil {
				t.Fatalf("live output did not parse: %v (%s)", err, resp.Text)
			}
			t.Logf("directed=%v chat_in=%.2f reason=%s usage=%+v", *ratings.Relevance.Directed, *ratings.ChatIn.Score, strings.TrimSpace(ratings.Relevance.Reason), resp.Usage)
			if *ratings.Relevance.Directed != tc.wantDirected {
				t.Errorf("directed = %v, want %v (%s)", *ratings.Relevance.Directed, tc.wantDirected, resp.Text)
			}
			if tc.maxChatIn > 0 && *ratings.ChatIn.Score > tc.maxChatIn {
				t.Errorf("chat_in = %.2f, want <= %.2f (%s)", *ratings.ChatIn.Score, tc.maxChatIn, resp.Text)
			}
		})
	}
}

// 绑错用途时必须当场报错，而不是把空文本当成模型输出。
func TestLiveJevRefusesTextWork(t *testing.T) {
	client := liveJevClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err := client.Generate(ctx, llm.GenerateRequest{
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "把这段对话压缩成三句话"}},
	})
	if err == nil {
		t.Fatal("expected a refusal for text-only work")
	}
	t.Logf("refused as expected: %v", err)
}

func liveJevClient(t *testing.T) llm.LLMClient {
	t.Helper()
	if os.Getenv("DIANA_LIVE_LLM") != "1" {
		t.Skip("set DIANA_LIVE_LLM=1 to run live decision-model tests")
	}
	key := strings.TrimSpace(os.Getenv("DIANA_TEST_JEV_API_KEY"))
	if key == "" {
		t.Skip("set DIANA_TEST_JEV_API_KEY to run live decision-model tests")
	}
	model := strings.TrimSpace(os.Getenv("DIANA_TEST_JEV_MODEL"))
	if model == "" {
		model = "jev-latest"
	}
	client, err := llm.NewClient(llm.ProviderConfig{
		Provider: llm.ProviderTypeSafe,
		APIKey:   key,
		BaseURL:  strings.TrimSpace(os.Getenv("DIANA_TEST_JEV_BASE_URL")),
		Model:    model,
	})
	if err != nil {
		t.Fatalf("live client: %v", err)
	}
	return client
}
