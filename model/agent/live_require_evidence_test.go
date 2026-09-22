// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/SuInk/diana/model/llm"
)

// 用真实模型回放线上那次答错的提问，验证 RequireEvidence 确实把「不搜就收口」
// 挡住了。默认跳过：
//
//	DIANA_LIVE_LLM=1 DIANA_TEST_LLM_API_KEY=... \
//	DIANA_TEST_LLM_BASE_URL=https://sub2api.earlyso.com/v1 \
//	DIANA_TEST_LLM_MODEL=gpt-5.6-terra \
//	go test ./model/agent/ -run TestLiveRequireEvidence -v
const liveEvidenceQuestion = "pi 有支持 subagent 的非阻塞创建原语吗？生态里有没有现成的插件？"

func liveAgentClient(t *testing.T) llm.LLMClient {
	t.Helper()
	if os.Getenv("DIANA_LIVE_LLM") != "1" {
		t.Skip("set DIANA_LIVE_LLM=1 and DIANA_TEST_LLM_API_KEY to run this against a real model")
	}
	apiKey := strings.TrimSpace(os.Getenv("DIANA_TEST_LLM_API_KEY"))
	if apiKey == "" {
		t.Skip("DIANA_TEST_LLM_API_KEY is empty")
	}
	model := strings.TrimSpace(os.Getenv("DIANA_TEST_LLM_MODEL"))
	if model == "" {
		t.Skip("DIANA_TEST_LLM_MODEL is empty")
	}
	provider := llm.Provider(strings.TrimSpace(os.Getenv("DIANA_TEST_LLM_PROVIDER")))
	if provider == "" {
		provider = llm.ProviderOpenAICompatible
	}
	client, err := llm.NewClient(llm.ProviderConfig{
		Provider: provider,
		APIKey:   apiKey,
		BaseURL:  strings.TrimSpace(os.Getenv("DIANA_TEST_LLM_BASE_URL")),
		Model:    model,
		APIStyle: llm.APIStyle(strings.TrimSpace(os.Getenv("DIANA_TEST_LLM_API_STYLE"))),
		Timeout:  120 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

// liveEvidenceRun 跑一轮，返回模型是否检索过以及最终正文。
func liveEvidenceRun(t *testing.T, client llm.LLMClient, require bool) (int, string) {
	t.Helper()
	return liveEvidenceRunWith(t, client, []llm.Message{
		// 还原线上那一轮的关键条件：上下文里已经聊过这个项目。模型上次
		// 就是据此判定「当前上下文已经足够」而没有检索。
		{Role: llm.RoleUser, Content: "我们刚才一直在聊 pi 这个终端编程 agent 的循环设计。"},
		{Role: llm.RoleAssistant, Content: "嗯，它的循环是单主线同步阻塞式的工具调用。"},
		{Role: llm.RoleUser, Content: liveEvidenceQuestion},
	}, require)
}

func liveEvidenceRunWith(t *testing.T, client llm.LLMClient, messages []llm.Message, require bool) (int, string) {
	t.Helper()
	// 桩里用真实域名：.example 是保留的占位域名，模型会据此判定来源不可信并
	// 改变行为，那样测出来的就不是门控的效果，而是它对占位域名的警觉。
	payload, _ := json.Marshal(webSearchResult{
		Status:     "ok",
		StopReason: "sufficient_evidence",
		Sources: []string{
			"https://github.com/badlogic/pi-mono",
			"https://www.npmjs.com/package/pi-better-subagents",
		},
		Content: "检索结果：社区已发布 pi-better-subagents、pi-subagents、pi-subagentura、@pi9/subagent 四个扩展，" +
			"均提供在主循环之外派生子代理、稍后回收结果的能力。",
	})
	tool := &recordingSearchTool{output: string(payload)}
	runner, err := NewRunner(client, Config{MaxSteps: 4, ProtocolRepairLimit: 3}, NewToolRegistry(tool))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	resp, err := runner.Run(ctx, Request{Messages: messages, RequireEvidence: require})
	if err != nil {
		t.Fatalf("require=%v run failed: %v", require, err)
	}
	// 搜索工具是桩：只看模型有没有真的发起调用，返回内容是固定的。把查询词和
	// claims 一并打出来，免得把「模型自己编的来源」当成检索到的证据。
	t.Logf("  require=%v finish=%q 模型轮数=%d 步骤=%d 最后一次查询=%#v claims=%+v",
		require, resp.FinishReason, resp.ModelTurns, len(resp.Steps), tool.input, resp.Claims)
	return tool.calls, resp.Text
}

// 上面那个用例回放的是线上原题，但强模型本来就会去搜，证明不了门控在起作用。
// 这个用例换一个模型笃定到不会去查的问题：基线必须零检索，开了门控必须去检索。
// 真正区分「门控生效」和「模型自己就会搜」的是这一组。
func TestLiveRequireEvidenceFlipsAConfidentAnswer(t *testing.T) {
	client := liveAgentClient(t)
	const settled = "Go 里的 defer 是在函数返回之前执行的吗？一句话回答就行。"

	settledMessages := []llm.Message{{Role: llm.RoleUser, Content: settled}}
	baselineCalls, baselineText := liveEvidenceRunWith(t, client, settledMessages, false)
	t.Logf("RequireEvidence=false: 检索 %d 次，正文=%q", baselineCalls, baselineText)
	if baselineCalls != 0 {
		t.Skipf("这个模型连稳定知识都要查（检索 %d 次），换不出对照组", baselineCalls)
	}

	gatedCalls, gatedText := liveEvidenceRunWith(t, client, settledMessages, true)
	t.Logf("RequireEvidence=true: 检索 %d 次，正文=%q", gatedCalls, gatedText)
	if gatedCalls < 1 {
		t.Fatalf("开启 RequireEvidence 后模型仍然没有检索就收口：%q", gatedText)
	}
	if strings.TrimSpace(gatedText) == "" {
		t.Fatal("门控把回复卡掉了")
	}
}

func TestLiveRequireEvidenceForcesSearch(t *testing.T) {
	client := liveAgentClient(t)

	baselineCalls, baselineText := liveEvidenceRun(t, client, false)
	t.Logf("RequireEvidence=false: 检索 %d 次，正文=%q", baselineCalls, baselineText)

	gatedCalls, gatedText := liveEvidenceRun(t, client, true)
	t.Logf("RequireEvidence=true: 检索 %d 次，正文=%q", gatedCalls, gatedText)

	if gatedCalls < 1 {
		t.Fatalf("开启 RequireEvidence 后模型仍然没有检索就收口：%q", gatedText)
	}
}
