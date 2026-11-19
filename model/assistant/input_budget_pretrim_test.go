package assistant

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/llm"
)

// countingSummaryProvider 接住预算摘要调用，只数次数。
type countingSummaryProvider struct {
	calls int
	fail  bool
}

func (p *countingSummaryProvider) Generate(_ context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	p.calls++
	if p.fail {
		return nil, errors.New("summary unavailable")
	}
	return &llm.GenerateResponse{Provider: llm.ProviderOpenAICompatible, Model: "test", Text: "压缩后的摘要"}, nil
}

func newPretrimTestProvider(t *testing.T, summaries *countingSummaryProvider) (*imageBudgetProvider, *recordingProvider) {
	t.Helper()
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
		return summaries, nil
	})
	upstream := &recordingProvider{}
	return &imageBudgetProvider{runtime: runtime, provider: upstream, group: llm.GroupChat}, upstream
}

// typicalReplyRequest 按线上主回复的形状造一条请求：系统提示、较早摘要、一长串
// 较早历史、最近三轮、记忆、当前消息。historyMessages 条较早历史每条约 300 token，
// 线上超预算的请求正是这种「许多条不大的历史加起来压线」。
func typicalReplyRequest(historyMessages int) llm.GenerateRequest {
	messages := []llm.Message{
		{Role: llm.RoleSystem, Content: "人设与规则 " + strings.Repeat("s", 6000), Priority: llm.MessagePrioritySystem},
		{Role: llm.RoleUser, Content: "【较早上下文压缩摘要】" + strings.Repeat("m", 1500), Priority: llm.MessagePrioritySummary, AtomicText: true},
	}
	for i := 0; i < historyMessages; i++ {
		role := llm.RoleUser
		if i%2 == 1 {
			role = llm.RoleAssistant
		}
		messages = append(messages, llm.Message{Role: role, Content: fmt.Sprintf("历史 %d：", i) + strings.Repeat("h", 900), Priority: llm.MessagePriorityHistory, ContextGroup: fmt.Sprintf("history-turn-%d", i/2)})
	}
	for i := 0; i < 6; i++ {
		messages = append(messages, llm.Message{Role: llm.RoleUser, Content: fmt.Sprintf("最近 %d：", i) + strings.Repeat("r", 600), Priority: llm.MessagePriorityRecentHistory})
	}
	messages = append(messages,
		llm.Message{Role: llm.RoleUser, Content: "记忆 " + strings.Repeat("k", 3000), Priority: llm.MessagePriorityMemory, AtomicText: true},
		llm.Message{Role: llm.RoleSystem, Content: "发言者与时钟", Priority: llm.MessagePrioritySystem},
		llm.Message{Role: llm.RoleUser, Content: "当前问题：这个怎么弄", Priority: llm.MessagePriorityCurrent},
	)
	return llm.GenerateRequest{Messages: messages}
}

// appendAgentStep 模拟 Runner 的一步：一条带工具调用的 assistant 消息加一条工具结果。
func appendAgentStep(req llm.GenerateRequest, step int, resultChars int) llm.GenerateRequest {
	id := fmt.Sprintf("call-%d", step)
	req.Messages = append(append([]llm.Message(nil), req.Messages...),
		llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: id, Name: "web_search"}}},
		llm.Message{Role: llm.RoleTool, ToolCallID: id, ToolName: "web_search", Content: fmt.Sprintf("结果 %d：", step) + strings.Repeat("t", resultChars)},
	)
	return req
}

// legacyBudgetTextCalls 复现改动前的流程：不预裁剪，fitBudgetText 前后各跑一遍，
// 不缓存摘要。只数它会调几次摘要模型。
func legacyBudgetTextCalls(req llm.GenerateRequest, budget int64) (int, bool) {
	calls := 0
	summarize := func(context.Context, string, int64) (string, error) { return "压缩后的摘要", nil }
	req = fitBudgetText(context.Background(), req, budget, &calls, nil, summarize)
	req = fitBudgetText(context.Background(), req, budget, &calls, nil, summarize)
	return calls, llm.PlanInputBudget(req, budget).OverBudget()
}

const pretrimTestBudget int64 = 126848 // 128000 窗口减 1024 输出预留和 128 安全余量，和线上一致

func TestPretrimResolvesTypicalOverageWithoutSummaryCalls(t *testing.T) {
	req := typicalReplyRequest(400)
	before := llm.PlanInputBudget(req, pretrimTestBudget)
	if before.TextExcess <= 0 || before.TextTokens > pretrimTestBudget+8000 {
		t.Fatalf("构造的请求应当刚好压线超出：%+v", before)
	}
	legacyCalls, legacyOver := legacyBudgetTextCalls(req, pretrimTestBudget)
	if legacyCalls != 2 || !legacyOver {
		t.Fatalf("旧流程应当花满两次摘要还超：calls=%d over=%v", legacyCalls, legacyOver)
	}

	summaries := &countingSummaryProvider{}
	client, upstream := newPretrimTestProvider(t, summaries)
	if _, err := client.Generate(withInputBudgetRun(context.Background()), req); err != nil {
		t.Fatal(err)
	}
	if summaries.calls != 0 {
		t.Fatalf("常规超额不该调摘要模型，调了 %d 次", summaries.calls)
	}
	got := upstream.req
	plan := llm.PlanInputBudget(got, pretrimTestBudget)
	if plan.TextTokens > plan.TextLimit*budgetPretrimTargetPercent/100 {
		t.Fatalf("没有裁到留余量的位置：text=%d limit=%d", plan.TextTokens, plan.TextLimit)
	}
	// 系统提示、摘要、最近三轮、记忆和当前消息原样保留，被丢的历史换成一句提示。
	if got.Messages[0].Content != req.Messages[0].Content || got.Messages[1].Content != req.Messages[1].Content || got.Messages[2].Content != budgetPretrimMarker {
		t.Fatalf("开头结构不对：%q / %q", got.Messages[1].Content[:20], got.Messages[2].Content)
	}
	tail := req.Messages[len(req.Messages)-9:]
	gotTail := got.Messages[len(got.Messages)-9:]
	for i := range tail {
		if tail[i].Content != gotTail[i].Content {
			t.Fatalf("最近历史、记忆或当前消息被改动：%q", tail[i].Content[:12])
		}
	}
	// 从最旧那头整轮丢：留下的第一条历史是某一轮的开头（用户那条）。
	first := got.Messages[3]
	if first.Priority != llm.MessagePriorityHistory || first.Role != llm.RoleUser || strings.HasPrefix(first.Content, "历史 0：") {
		t.Fatalf("留下的第一条历史不对：role=%s %q", first.Role, first.Content[:12])
	}
	if strings.Contains(req.Messages[2].Content, budgetPretrimMarker) || len(req.Messages) != 400+11 {
		t.Fatal("调用方的请求被就地改了")
	}
}

// Agent 循环：Runner 每一步都带着完整原件重发。旧流程一旦越线，此后每一步都要
// 重新摘要；现在越线那一步丢到限额的 85%，后面几步照搬同一个裁剪点，前缀不动，
// 余量用完才再裁一次，全程不调摘要模型。
func TestPretrimAgentLoopKeepsCutPointStableAndSkipsSummaries(t *testing.T) {
	summaries := &countingSummaryProvider{}
	client, upstream := newPretrimTestProvider(t, summaries)
	ctx := withInputBudgetRun(context.Background())
	// 起步约 11 万 token，每步一条约 6700 token 的工具结果（接近 20000 字的上限），
	// 第三步起越线。
	req := typicalReplyRequest(350)
	legacyTotal, overSteps, stableSteps := 0, 0, 0
	var previous []llm.Message
	for step := 1; step <= 10; step++ {
		req = appendAgentStep(req, step, 20000)
		if llm.PlanInputBudget(req, pretrimTestBudget).OverBudget() {
			overSteps++
		}
		calls, _ := legacyBudgetTextCalls(req, pretrimTestBudget)
		legacyTotal += calls
		if _, err := client.Generate(ctx, req); err != nil {
			t.Fatal(err)
		}
		sent := upstream.req.Messages
		if plan := llm.PlanInputBudget(upstream.req, pretrimTestBudget); plan.OverBudget() {
			t.Fatalf("第 %d 步发出去的请求仍超预算：%+v", step, plan)
		}
		if previous != nil && len(sent) >= len(previous) && sameMessageContents(sent[:len(previous)], previous) {
			stableSteps++
		}
		previous = sent
	}
	if summaries.calls != 0 {
		t.Fatalf("Agent 循环调了 %d 次摘要模型", summaries.calls)
	}
	if overSteps < 6 || legacyTotal < overSteps {
		t.Fatalf("旧流程应当每个越线步都摘要：越线 %d 步，摘要 %d 次", overSteps, legacyTotal)
	}
	// 大多数步只在尾部追加，不动前缀：裁剪点只在余量用完时才挪。
	if stableSteps < 6 {
		t.Fatalf("裁剪点来回挪动，只有 %d 步保持前缀不变", stableSteps)
	}
	t.Logf("10 步 Agent 循环，越线 %d 步：旧流程摘要 %d 次，现在 0 次；%d 步前缀不变", overSteps, legacyTotal, stableSteps)
}

func sameMessageContents(a, b []llm.Message) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Role != b[i].Role || a[i].Content != b[i].Content || a[i].ToolCallID != b[i].ToolCallID {
			return false
		}
	}
	return true
}

// 没有较早历史可丢时截 Agent 较早的大工具结果：最新两条原样保留，工具调用和结果
// 的配对不动。
func TestPretrimClipsOlderToolResultsKeepingNewestAndPairs(t *testing.T) {
	req := llm.GenerateRequest{Messages: []llm.Message{
		{Role: llm.RoleSystem, Content: "规则", Priority: llm.MessagePrioritySystem},
		{Role: llm.RoleUser, Content: "当前问题", Priority: llm.MessagePriorityCurrent},
	}}
	for step := 1; step <= 6; step++ {
		req = appendAgentStep(req, step, 72000)
	}
	run := newInputBudgetRun()
	got, stats := pretrimBudgetText(req, pretrimTestBudget, run)
	if stats.Dropped != 0 || stats.Clipped == 0 {
		t.Fatalf("stats=%+v", stats)
	}
	if llm.PlanInputBudget(got, pretrimTestBudget).OverBudget() {
		t.Fatal("截完仍超预算")
	}
	if len(got.Messages) != len(req.Messages) {
		t.Fatal("截工具结果不该增删消息")
	}
	for i, message := range got.Messages {
		if message.Role != req.Messages[i].Role || message.ToolCallID != req.Messages[i].ToolCallID {
			t.Fatalf("第 %d 条的角色或配对变了", i)
		}
	}
	last := len(req.Messages) - 1
	if got.Messages[last].Content != req.Messages[last].Content || got.Messages[last-2].Content != req.Messages[last-2].Content {
		t.Fatal("最新两条工具结果被截了")
	}
	if got.Messages[3].Content == req.Messages[3].Content || !strings.Contains(got.Messages[3].Content, "中间省略") {
		t.Fatal("最旧的工具结果没有截")
	}
	// 下一步带着同样的原件回来，照搬同样的截法，不需要再判断预算。
	again, againStats := pretrimBudgetText(req, pretrimTestBudget, run)
	if againStats != stats || !sameMessageContents(again.Messages, got.Messages) {
		t.Fatalf("同一轮第二次结果不一致：%+v vs %+v", againStats, stats)
	}
}

// 预裁剪只认明确标成较早历史的消息。没标优先级的旁路请求、最近三轮都不丢，
// 这些还是走摘要兜底。
func TestPretrimLeavesUnmarkedAndRecentHistoryAlone(t *testing.T) {
	req := llm.GenerateRequest{Messages: []llm.Message{
		{Role: llm.RoleUser, Content: strings.Repeat("old", 60000)},
		{Role: llm.RoleUser, Content: strings.Repeat("recent", 40000), Priority: llm.MessagePriorityRecentHistory},
		{Role: llm.RoleUser, Content: "current question"},
	}}
	got, stats := pretrimBudgetText(req, 20000, nil)
	if stats != (budgetPretrimStats{}) || !sameMessageContents(got.Messages, req.Messages) {
		t.Fatalf("不该动的消息被裁了：%+v", stats)
	}
}

// 兜底摘要按原文缓存：Agent 下一步带着同一段装不下的资料回来，不再重新摘要；
// 摘要失败过的也不再重试。
func TestBudgetSummaryIsReusedAcrossAgentSteps(t *testing.T) {
	for _, fail := range []bool{false, true} {
		summaries := &countingSummaryProvider{fail: fail}
		client, _ := newPretrimTestProvider(t, summaries)
		ctx := withInputBudgetRun(context.Background())
		// 插件资料不是较早历史，预裁剪不动它，只能摘要。
		req := llm.GenerateRequest{Messages: []llm.Message{
			{Role: llm.RoleSystem, Content: "规则", Priority: llm.MessagePrioritySystem},
			{Role: llm.RoleUser, Content: strings.Repeat("plugin evidence ", 30000), Priority: llm.MessagePriorityPlugin},
			{Role: llm.RoleUser, Content: "当前问题", Priority: llm.MessagePriorityCurrent},
		}}
		for step := 1; step <= 3; step++ {
			req = appendAgentStep(req, step, 300)
			if _, err := client.Generate(ctx, req); err != nil {
				t.Fatal(err)
			}
		}
		if summaries.calls != 1 {
			t.Fatalf("fail=%v：三步里摘要调了 %d 次，想要 1 次", fail, summaries.calls)
		}
	}
}

// 同一次请求里第二遍文字压缩不再把第一遍摘不动的那条重新送去摘要。
func TestBudgetTextSecondPassSkipsFailedCandidates(t *testing.T) {
	req := llm.GenerateRequest{Messages: []llm.Message{
		{Role: llm.RoleUser, Content: strings.Repeat("old result ", 9000)},
		{Role: llm.RoleUser, Content: "current question"},
	}}
	run := newInputBudgetRun()
	calls := 0
	fail := func(context.Context, string, int64) (string, error) { return "", errors.New("summary unavailable") }
	req = fitBudgetText(context.Background(), req, 10000, &calls, run, fail)
	req = fitBudgetText(context.Background(), req, 10000, &calls, run, fail)
	if calls != 1 {
		t.Fatalf("两遍一共调了 %d 次，想要 1 次", calls)
	}
	legacy := 0
	for i := 0; i < 2; i++ {
		fitBudgetText(context.Background(), req, 10000, &legacy, nil, fail)
	}
	if legacy != 2 {
		t.Fatalf("不带缓存时应当重试一次，实际 %d 次", legacy)
	}
}
