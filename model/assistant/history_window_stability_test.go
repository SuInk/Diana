package assistant

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/llm"
)

// groupReplyRequest 按线上主回复的形状造一条群聊请求：系统头部、从固定起点开始的
// 近期历史（锚定窗口不动时，起点就是这样固定的）、最近三轮、尾部 system 和当前
// 消息。tail 是尾部那段每轮都在变的内容（检索记忆、时钟），长短随消息浮动。
func groupReplyRequest(history []string, head, tail int) llm.GenerateRequest {
	messages := []llm.Message{{Role: llm.RoleSystem, Content: "人设、规则与工具协议 " + strings.Repeat("规", head), Priority: llm.MessagePrioritySystem}}
	recentFrom := max(0, len(history)-6)
	for i, text := range history {
		priority := llm.MessagePriorityHistory
		if i >= recentFrom {
			priority = llm.MessagePriorityRecentHistory
		}
		role := llm.RoleUser
		if i%7 == 6 {
			role = llm.RoleAssistant
		}
		messages = append(messages, llm.Message{Role: role, Content: text, Priority: priority, ContextGroup: fmt.Sprintf("history-turn-%d", i)})
	}
	messages = append(messages,
		llm.Message{Role: llm.RoleSystem, Content: "群聊场景、记忆与时钟 " + strings.Repeat("记", tail), Priority: llm.MessagePrioritySystem},
		llm.Message{Role: llm.RoleUser, Content: "当前消息：@机器人 看下这个", Priority: llm.MessagePriorityCurrent},
	)
	return llm.GenerateRequest{Messages: messages}
}

// legacyPretrimWindow 复现改动前的裁法：从最旧那头一轮轮丢，丢够就停。返回留下的
// 较早历史 token 和留下的第一条，作为「不影响效果」和「开头挪了几次」的比较基准。
func legacyPretrimWindow(req llm.GenerateRequest, budget int64) (int64, string) {
	plan := llm.PlanInputBudget(req, budget)
	need := plan.TextTokens - plan.TextLimit*budgetPretrimTargetPercent/100
	kept, first := int64(0), ""
	for _, unit := range budgetPretrimHistoryUnits(req.Messages, budgetPretrimCurrentIndex(req.Messages)) {
		if need > 0 {
			need -= unit.cost
			continue
		}
		if first == "" {
			first = req.Messages[unit.indexes[0]].Content
		}
		kept += unit.cost
	}
	return kept, first
}

// pretrimKeptHistory 数裁完的请求里留下的较早历史 token 和第一条历史。
func pretrimKeptHistory(req llm.GenerateRequest) (int64, string) {
	kept, first := int64(0), ""
	for _, unit := range budgetPretrimHistoryUnits(req.Messages, budgetPretrimCurrentIndex(req.Messages)) {
		if first == "" {
			first = req.Messages[unit.indexes[0]].Content
		}
		kept += unit.cost
	}
	return kept, first
}

// 群里一条条来消息，每次主回复都从完整历史重新裁。顶满以后，省略提示后面的第一条
// 历史要连续很多轮保持不变，只在跨过格子线时整段后移；每轮都不超限额的 85%，
// 留下的历史和旧裁法比平均不少于 85%、最少不少于 75%。
//
// 尾部（检索记忆、时钟、当前消息）每轮长短不一时，要丢的量会在格子线附近来回
// 跨，开头在相邻两个格子之间来回跳。这两个前缀都刚用过、都在供应商缓存里，真正
// 付全价的只有第一次出现的开头，所以浮动场景另数「出现过几种开头」。
func TestPretrimMovesHistoryStartInChunksAcrossReplies(t *testing.T) {
	cases := []struct {
		name               string
		budget             int64
		head, tail, jitter int
		messages           int
	}{
		// 线上那种：128K 窗口，头部很大，历史几百条。
		{name: "128K 尾部不变", budget: pretrimTestBudget, head: 30000, tail: 4000, messages: 2500},
		{name: "128K 尾部浮动", budget: pretrimTestBudget, head: 30000, tail: 3000, jitter: 3000, messages: 2500},
		// 窗口小、头部占比大时格子会自动缩窄。
		{name: "32K 尾部不变", budget: llm.InputTokenBudget(32000, 1024), head: 8000, tail: 1500, messages: 1200},
		{name: "32K 尾部浮动", budget: llm.InputTokenBudget(32000, 1024), head: 8000, tail: 1000, jitter: 1000, messages: 1200},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			random := rand.New(rand.NewSource(20260928))
			var history []string
			var previousStart, legacyPreviousStart string
			over, starts, legacyStarts := 0, 0, 0
			ratioSum, minRatio, fillSum, minFill := 0.0, 1.0, 0.0, 1.0
			seen := map[string]bool{}
			for i := 0; len(history) < tc.messages; i++ {
				// 两次回复之间来一到四条群消息，长短不一：多数一两句，偶尔一大段。
				for n := 1 + random.Intn(4); n > 0; n-- {
					length := 10 + random.Intn(60)
					if random.Intn(10) == 0 {
						length += random.Intn(400)
					}
					history = append(history, fmt.Sprintf("[群友%05d] 第 %d 条：", 10000+random.Intn(90), len(history))+strings.Repeat("聊", length))
				}
				req := groupReplyRequest(history, tc.head, tc.tail+random.Intn(tc.jitter+1))
				if llm.PlanInputBudget(req, tc.budget).TextExcess <= 0 {
					continue
				}
				got, _ := pretrimBudgetText(req, tc.budget, nil)
				again, _ := pretrimBudgetText(req, tc.budget, nil)
				if !sameMessageContents(got.Messages, again.Messages) {
					t.Fatalf("第 %d 条：同样的请求裁出了不同的窗口", i)
				}
				plan := llm.PlanInputBudget(got, tc.budget)
				if plan.TextTokens > plan.TextLimit*budgetPretrimTargetPercent/100 {
					t.Fatalf("第 %d 条：裁完超出限额的 85%%：text=%d limit=%d", i, plan.TextTokens, plan.TextLimit)
				}
				fill := float64(plan.TextTokens) / float64(plan.TextLimit)
				fillSum += fill
				minFill = min(minFill, fill)
				kept, start := pretrimKeptHistory(got)
				legacyKept, legacyStart := legacyPretrimWindow(req, tc.budget)
				if kept > legacyKept {
					t.Fatalf("第 %d 条：留下的历史比丢够就停还多：%d > %d", i, kept, legacyKept)
				}
				ratio := float64(kept) / float64(legacyKept)
				over++
				ratioSum += ratio
				minRatio = min(minRatio, ratio)
				if start != previousStart {
					starts++
				}
				seen[start] = true
				if legacyStart != legacyPreviousStart {
					legacyStarts++
				}
				previousStart, legacyPreviousStart = start, legacyStart
			}
			if over < 300 {
				t.Fatalf("模拟里顶满的轮次太少：%d", over)
			}
			average := ratioSum / float64(over)
			t.Logf("顶满 %d 轮：历史开头和上一轮不同 %d 次（旧裁法 %d 次），出现过 %d 种开头；留下的历史占旧裁法的平均 %.1f%%、最少 %.1f%%；整个请求占文字限额平均 %.1f%%、最少 %.1f%%（旧裁法约 85%%）", over, starts, legacyStarts, len(seen), average*100, minRatio*100, fillSum/float64(over)*100, minFill*100)
			// 旧裁法只有新消息比一整轮还小的时候开头才不动。
			if legacyStarts*4 < over*3 {
				t.Fatalf("旧裁法应当绝大多数轮都挪开头：%d/%d", legacyStarts, over)
			}
			// 窗口小时格子窄，跨格子也更勤；和旧裁法比仍是一个数量级的差别。
			if tc.jitter == 0 && (starts != len(seen) || starts*8 > over) {
				t.Fatalf("尾部不变时开头应当只在跨格子时前移：变了 %d 次、%d 种开头、%d 轮", starts, len(seen), over)
			}
			if tc.jitter > 0 && (len(seen)*8 > over || starts*5 > over) {
				t.Fatalf("开头挪得太勤：变了 %d 次、%d 种开头、%d 轮", starts, len(seen), over)
			}
			if average < 0.85 || minRatio < 0.75 {
				t.Fatalf("历史保留量不够：平均 %.3f，最少 %.3f", average, minRatio)
			}
		})
	}
}

func TestBudgetPretrimChunkShrinksWithKeptHistory(t *testing.T) {
	if got := budgetPretrimChunk(pretrimTestBudget, 80000); got != pretrimTestBudget/8 {
		t.Fatalf("历史充足时格子应当是预算的 1/8：%d", got)
	}
	if got := budgetPretrimChunk(pretrimTestBudget, 30000); got*budgetPretrimChunkMaxShare > 30000 || got*2*budgetPretrimChunkMaxShare <= 30000 {
		t.Fatalf("格子应当缩到不超过留下历史 1/5 的最大一档：%d", got)
	}
	if got := budgetPretrimChunk(pretrimTestBudget, 0); got != 1 {
		t.Fatalf("没有可留的历史时退回逐轮丢：%d", got)
	}
}

// 近期历史预算 20000 的活跃群：每分钟三条左右、每条几十 token。这时总输入离输入
// 预算很远，裁历史的是锚定窗口（anchoredHistoryWindow）而不是预裁剪。走完整的
// remember → promptContextHistory，确认窗口开头连续上百条消息不动、每次都不超
// 预算，留下的历史平均不少于预算的 85%、最少约 75%。
func TestActiveGroupHistoryWindowMovesInChunksAtTwentyThousandBudget(t *testing.T) {
	const budget = 20000
	runtime := NewRuntime(BotConfig{RecentHistoryTokenBudget: budget, ContextSummaryThreshold: 100000, RecentContextLimit: 50000}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	random := rand.New(rand.NewSource(20260928))
	now := int64(1790000000)
	previousStart, previousMove := "", 0
	var gaps []int
	requests, moves := 0, 0
	ratioSum, minRatio := 0.0, 1.0
	filled := false
	for i := 0; i < 3000; i++ {
		// 平均 20 秒一条，每条十几到六十字。
		now += int64(5 + random.Intn(30))
		event := MessageEvent{Kind: EventKindGroup, GroupID: "20001", UserID: fmt.Sprintf("%05d", 10000+random.Intn(20)), SenderName: "群友", MessageID: fmt.Sprintf("m-%d", i), Time: now, RawMessage: strings.Repeat("聊", 10+random.Intn(50))}
		runtime.remember(event)
		current := event
		current.MessageID = fmt.Sprintf("current-%d", i)
		current.Time = now + 1
		cfg := runtime.effectiveConfigForEvent(current)
		window := runtime.promptContextHistory(current, cfg)
		if len(window) == 0 || window[len(window)-1].MessageID != event.MessageID {
			t.Fatalf("第 %d 条：窗口必须以最新一条结尾", i)
		}
		used := int64(0)
		for _, turn := range groupHistoryContextTurns(window, current.Time, cfg.BotAccount) {
			used += turn.estimated
		}
		if used > budget {
			t.Fatalf("第 %d 条：窗口 %d token 超出预算 %d", i, used, budget)
		}
		start := window[0].MessageID
		if !filled {
			// 第一次顶满之前窗口从头开始，开头本来就不动，不计入统计。
			filled = start != "m-0"
			previousStart, previousMove = start, i
			continue
		}
		requests++
		ratio := float64(used) / budget
		ratioSum += ratio
		minRatio = min(minRatio, ratio)
		if start != previousStart {
			moves++
			gaps = append(gaps, i-previousMove)
			previousMove = i
		}
		previousStart = start
	}
	if requests < 2000 || moves < 10 {
		t.Fatalf("模拟没有覆盖足够多次回落：%d 次请求、%d 次前移", requests, moves)
	}
	shortest := gaps[0]
	for _, gap := range gaps {
		shortest = min(shortest, gap)
	}
	average := ratioSum / float64(requests)
	t.Logf("顶满后 %d 次请求：开头前移 %d 次，平均每 %d 条消息（约 %d 分钟）一次、最短隔 %d 条；留下的历史占预算平均 %.1f%%、最少 %.1f%%", requests, moves, requests/moves, requests/moves/3, shortest, average*100, minRatio*100)
	if shortest < 50 {
		t.Fatalf("开头前移太勤：最短只隔 %d 条", shortest)
	}
	if average < 0.85 || minRatio < 0.74 {
		t.Fatalf("历史保留量不够：平均 %.3f，最少 %.3f", average, minRatio)
	}
}
