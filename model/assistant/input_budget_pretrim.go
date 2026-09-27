package assistant

import (
	"context"
	"strings"
	"sync"

	"github.com/SuInk/diana/model/llm"
)

// 预算预裁剪：文字超出限额时，先按确定性规则腾地方，调模型摘要只做兜底。
//
// 线上 9/25–9/27 抓到 523 条超预算请求，调了摘要模型的有 518 条（347 条两次、171 条
// 一次），同期 context_summary_compaction 约 830 次，p50 3–8 秒，全串在回复前面。
// 超出的文字几乎都在两处：
//
//   - Agent 循环。Runner 每一步都把完整的消息列表重新发一遍，只往后追加工具结果，
//     从不回收。预算层改写的只是发出去的那份副本，Runner 手里的原件不变，于是一旦
//     越过限额，此后每一步都超，每一步都把同一批大消息重新摘要一遍。
//   - 较早的聊天历史。各层编排各有上限，但没有谁为 Agent 后续的工具结果留地方，
//     第一步就压着线的请求随后必然越线。
//
// 所以超额时先丢最旧的聊天历史（MessagePriorityHistory，从旧往新整轮丢），不够
// 再把 Agent 较早的大段工具结果截成开头加结尾。一次裁到限额的 85%，而不是刚好
// 压线：留出来的余量够后面两三步工具结果用，这几步既不用再裁，前缀也不动。
//
// 不碰的：系统提示、当前触发消息及之后的同轮补充、最近三轮历史（RecentHistory）、
// 记忆和摘要层、带工具调用的消息，以及 Agent 最新的两条大工具结果。
// 被丢掉的历史换成一句固定的省略提示；已有的较早摘要、会话便签照常在请求里。
const (
	// budgetPretrimTargetPercent 是预裁剪一次裁到的文字限额比例。
	budgetPretrimTargetPercent int64 = 85
	// budgetPretrimKeepObservations 是原样保留的 Agent 最新大工具结果条数。
	budgetPretrimKeepObservations = 2
	// budgetPretrimClipMinTokens 以下的工具结果不值得截，截了也省不出多少。
	budgetPretrimClipMinTokens int64 = 1024
	// budgetPretrimClipRunes 是较早工具结果截短后保留的字数（开头加结尾）。
	budgetPretrimClipRunes = 1200
)

// budgetPretrimMarker 替换被丢掉的那段历史。内容固定不带条数：同一轮里第二次
// 预裁剪、Agent 的下一步都能认出它，不会重复插入，也不会因为条数变了打断前缀缓存。
const budgetPretrimMarker = "【此处省略了更早的聊天记录（超出输入预算，未发送）；不要当作它们不存在，也不要猜测其内容】"

type inputBudgetRunKey struct{}

// inputBudgetRun 记住同一次回复（一整个 Agent 循环）里做过的预算决定。
//
// 预算层每次拿到的都是 Runner 的完整原件，不记的话每一步都要从头裁：裁剪点随着
// 新工具结果一步步前移，前缀每步都变；摘要也每步重算。记下来之后：丢过的历史、
// 截过的工具结果下一步照样处理（前缀稳定），摘要过的文字直接复用，摘要失败的
// 也不再重试——重试正是那几秒延迟的来源，装不下的交给供应商裁剪兜底。
//
// 按内容指纹记，不按下标：同一个 ctx 里可能有别的请求流（子任务、重试），下标
// 对不上，指纹对得上才算同一条。
type inputBudgetRun struct {
	mu        sync.Mutex
	dropped   map[string]bool
	clipped   map[string]bool
	summaries map[string]string
}

func newInputBudgetRun() *inputBudgetRun {
	return &inputBudgetRun{dropped: map[string]bool{}, clipped: map[string]bool{}, summaries: map[string]string{}}
}

// withInputBudgetRun 给一次回复挂上预算记录；已经挂过就沿用。
func withInputBudgetRun(ctx context.Context) context.Context {
	if ctx == nil || inputBudgetRunFromContext(ctx) != nil {
		return ctx
	}
	return context.WithValue(ctx, inputBudgetRunKey{}, newInputBudgetRun())
}

func inputBudgetRunFromContext(ctx context.Context) *inputBudgetRun {
	if ctx == nil {
		return nil
	}
	run, _ := ctx.Value(inputBudgetRunKey{}).(*inputBudgetRun)
	return run
}

// empty 说明这次回复还没做过任何裁剪决定，装得下的请求可以直接放行。
func (r *inputBudgetRun) empty() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.dropped) == 0 && len(r.clipped) == 0
}

func (r *inputBudgetRun) wasDropped(key string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.dropped[key]
}

func (r *inputBudgetRun) markDropped(key string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.dropped[key] = true
}

func (r *inputBudgetRun) wasClipped(key string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.clipped[key]
}

func (r *inputBudgetRun) markClipped(key string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.clipped[key] = true
}

// summary 返回这段文字之前的摘要结果；空串表示之前摘要失败或不值得保留。
func (r *inputBudgetRun) summary(key string) (string, bool) {
	if r == nil {
		return "", false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	summary, ok := r.summaries[key]
	return summary, ok
}

func (r *inputBudgetRun) rememberSummary(key, summary string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.summaries[key] = summary
}

type budgetPretrimStats struct {
	// Dropped 是这次丢掉的历史消息条数，Clipped 是截短的工具结果条数。
	Dropped, Clipped int
}

func (s budgetPretrimStats) add(other budgetPretrimStats) budgetPretrimStats {
	return budgetPretrimStats{Dropped: s.Dropped + other.Dropped, Clipped: s.Clipped + other.Clipped}
}

type budgetPretrimUnit struct {
	indexes []int
	key     string
	cost    int64
}

// pretrimBudgetText 按确定性规则把文字裁进预算，不调模型。先复用本轮之前的决定，
// 仍超限才继续裁，一次裁到限额的 budgetPretrimTargetPercent。
func pretrimBudgetText(req llm.GenerateRequest, budget int64, run *inputBudgetRun) (llm.GenerateRequest, budgetPretrimStats) {
	if run == nil {
		run = newInputBudgetRun()
	}
	current := budgetPretrimCurrentIndex(req.Messages)
	if current < 0 {
		return req, budgetPretrimStats{}
	}
	history := budgetPretrimHistoryUnits(req.Messages, current)
	observations := budgetPretrimObservationUnits(req.Messages, current)

	drop := map[int]bool{}
	clip := map[int]bool{}
	stats := budgetPretrimStats{}
	// 先照搬之前的决定。历史只从最旧那头连续地丢，碰到没丢过的就停。
	next := 0
	for ; next < len(history) && run.wasDropped(history[next].key); next++ {
		for _, index := range history[next].indexes {
			drop[index] = true
		}
		stats.Dropped += len(history[next].indexes)
	}
	for _, unit := range observations {
		if run.wasClipped(unit.key) {
			clip[unit.indexes[0]] = true
			stats.Clipped++
		}
	}
	if len(drop) > 0 || len(clip) > 0 {
		req = applyBudgetPretrim(req, drop, clip)
		current = budgetPretrimCurrentIndex(req.Messages)
		history = budgetPretrimHistoryUnits(req.Messages, current)
		observations = budgetPretrimObservationUnits(req.Messages, current)
		next = 0
		drop = map[int]bool{}
		clip = map[int]bool{}
	}

	plan := llm.PlanInputBudget(req, budget)
	if plan.TextExcess <= 0 {
		return req, stats
	}
	need := plan.TextTokens - plan.TextLimit*budgetPretrimTargetPercent/100
	for ; next < len(history) && need > 0; next++ {
		unit := history[next]
		for _, index := range unit.indexes {
			drop[index] = true
		}
		run.markDropped(unit.key)
		stats.Dropped += len(unit.indexes)
		need -= unit.cost
	}
	for _, unit := range observations {
		if need <= 0 {
			break
		}
		index := unit.indexes[0]
		clipped := clipHeadTail(req.Messages[index].Content, budgetPretrimClipRunes)
		saved := unit.cost - llm.EstimateTextTokens(clipped)
		if saved <= 0 {
			continue
		}
		clip[index] = true
		run.markClipped(unit.key)
		stats.Clipped++
		need -= saved
	}
	if len(drop) == 0 && len(clip) == 0 {
		return req, stats
	}
	return applyBudgetPretrim(req, drop, clip), stats
}

// budgetPretrimCurrentIndex 找当前触发消息：调用方标了 MessagePriorityCurrent 的
// 最后一条；没标的请求（旁路调用）退回最后一条用户或工具消息。它及之后的内容一概
// 不丢，之后的工具结果另按 Agent 观察处理。
func budgetPretrimCurrentIndex(messages []llm.Message) int {
	fallback := -1
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Priority >= llm.MessagePriorityCurrent {
			return i
		}
		if fallback < 0 && (messages[i].Role == llm.RoleUser || messages[i].Role == llm.RoleTool) {
			fallback = i
		}
	}
	return fallback
}

// budgetPretrimHistoryUnits 列出当前消息之前可以整轮丢的历史，从旧到新。只认
// 调用方明确标成 MessagePriorityHistory 的：最近三轮（RecentHistory）、没标优先级
// 的消息来源不明，都留给后面的摘要和供应商裁剪处理。同一 ContextGroup 的消息是
// 一轮问答，一起丢，免得留下没有问题的回答。
func budgetPretrimHistoryUnits(messages []llm.Message, current int) []budgetPretrimUnit {
	var units []budgetPretrimUnit
	lastGroup := ""
	for i := 0; i < current; i++ {
		message := messages[i]
		if message.Priority != llm.MessagePriorityHistory || message.Role == llm.RoleSystem || message.Role == llm.RoleTool || len(message.ToolCalls) > 0 || len(message.ResponsesOutput) > 0 || message.Content == budgetPretrimMarker {
			lastGroup = ""
			continue
		}
		fingerprint := promptCacheHash(promptCacheCanonicalMessage(message))
		cost := llm.PlanInputBudget(llm.GenerateRequest{Messages: []llm.Message{message}}, 0).TextTokens
		if n := len(units); n > 0 && message.ContextGroup != "" && message.ContextGroup == lastGroup && units[n-1].indexes[len(units[n-1].indexes)-1] == i-1 {
			units[n-1].indexes = append(units[n-1].indexes, i)
			units[n-1].key += fingerprint
			units[n-1].cost += cost
			continue
		}
		units = append(units, budgetPretrimUnit{indexes: []int{i}, key: fingerprint, cost: cost})
		lastGroup = message.ContextGroup
	}
	return units
}

// budgetPretrimObservationUnits 列出当前消息之后、Agent 较早拿到的大段工具结果，
// 从旧到新，最新的 budgetPretrimKeepObservations 条不列：模型下一步多半就靠它们。
// 带图片的结果不截，工具调用那半边不动，工具调用和结果的配对保持完整。
func budgetPretrimObservationUnits(messages []llm.Message, current int) []budgetPretrimUnit {
	var units []budgetPretrimUnit
	for i := current + 1; i < len(messages); i++ {
		message := messages[i]
		// AtomicText 的要么是调用方定型的语义块，要么是这里已经截过的，都不再截。
		// 插件级以上的（上一轮预算耗尽留下的工具观察存档、带图的工具结果）也不截。
		if len(message.Parts) > 0 || len(message.ToolCalls) > 0 || message.AtomicText || message.Priority >= llm.MessagePriorityPlugin {
			continue
		}
		if message.Role != llm.RoleTool && message.Role != llm.RoleUser {
			continue
		}
		cost := llm.EstimateTextTokens(message.Content)
		if cost <= budgetPretrimClipMinTokens {
			continue
		}
		units = append(units, budgetPretrimUnit{indexes: []int{i}, key: promptCacheHash(promptCacheCanonicalMessage(message)), cost: cost})
	}
	if len(units) <= budgetPretrimKeepObservations {
		return nil
	}
	return units[:len(units)-budgetPretrimKeepObservations]
}

// applyBudgetPretrim 生成裁剪后的请求，不改调用方的切片。被丢的那段换成一条固定
// 提示；请求里已经有这条提示（同一轮第二次裁）就不再插。
func applyBudgetPretrim(req llm.GenerateRequest, drop, clip map[int]bool) llm.GenerateRequest {
	hasMarker := false
	for _, message := range req.Messages {
		hasMarker = hasMarker || message.Content == budgetPretrimMarker
	}
	out := make([]llm.Message, 0, len(req.Messages)+1)
	markerIndex := -1
	for i, message := range req.Messages {
		if message.Content == budgetPretrimMarker {
			markerIndex = len(out)
		}
		if drop[i] {
			if !hasMarker {
				hasMarker = true
				markerIndex = len(out)
				out = append(out, llm.Message{Role: llm.RoleUser, Content: budgetPretrimMarker, Priority: llm.MessagePriorityHistory, AtomicText: true})
			}
			// 缓存断点落在被丢的消息上时挪到提示上，别让稳定前缀的标记跟着消失。
			if message.CacheBreakpoint && markerIndex >= 0 {
				out[markerIndex].CacheBreakpoint = true
			}
			continue
		}
		if clip[i] {
			message.Content = clipHeadTail(message.Content, budgetPretrimClipRunes)
			message.AtomicText = true
		}
		out = append(out, message)
	}
	req.Messages = out
	return req
}

// budgetSummaryKey 是摘要缓存的键：同一段文字在同一次回复里只摘要一次。
func budgetSummaryKey(text string) string {
	return promptCacheHash(text)
}

// isBudgetSummaryText 认出已经是摘要的文字，免得同一轮第二遍把摘要再摘要一次。
func isBudgetSummaryText(text string) bool {
	return strings.HasPrefix(strings.TrimSpace(text), budgetSummaryPrefix)
}
