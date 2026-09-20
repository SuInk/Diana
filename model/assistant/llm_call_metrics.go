// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/SuInk/diana/model/llm"
)

// 模型调用并发和回复并发（active_workers）不是一回事。
//
// 一个 worker 处理一条消息，途中会打好几次模型：路由判断、记忆抽取、正式生成
// 各算一次，开了子任务还会再并出去几条。所以 worker 数说不出模型侧的真实压力，
// 而限流、排队和账单都跟着在飞的请求数走。这里在记账装饰器那一层数——它已经
// 包住了每一次经过 provider 装饰链的调用，新增调用路径不必补登记。口径跟着用量
// 记账走：不走装饰链的 embedding 在它自己的调用点补登记，两个数才对得上。

// LLMConcurrencyStatus 是「此刻有多少次模型调用还没回来」的快照。
type LLMConcurrencyStatus struct {
	Active int `json:"active"`
	// Peak 是本次运行以来的最高并发。瞬时值撞上低谷时它还在，能说明峰值有多高。
	Peak   int                   `json:"peak"`
	Models []LLMConcurrencyModel `json:"models,omitempty"`
}

// LLMConcurrencyModel 是单个模型上的在飞调用。
type LLMConcurrencyModel struct {
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model"`
	Active   int    `json:"active"`
	// StartedAt 是这一组里最早发出、还没回来的那次调用的起点。并发高本身不是
	// 故障，一直卡着不动才是，这个时间戳把两者分开。
	StartedAt time.Time `json:"started_at"`
}

// llmConcurrencyUnknownModel 是识别不出模型名时落的桶。宁可显示一条「未知模型」，
// 也不要让总数和明细对不上。
const llmConcurrencyUnknownModel = "unknown"

type llmConcurrencyCall struct {
	provider  string
	model     string
	startedAt time.Time
}

type llmConcurrencyTracker struct {
	mu    sync.Mutex
	peak  int
	seq   uint64
	calls map[uint64]llmConcurrencyCall
}

// begin 登记一次在飞调用，返回的函数负责销账；调用方用 defer 保证异常路径也销。
func (t *llmConcurrencyTracker) begin(provider, model string, startedAt time.Time) func() {
	model = strings.TrimSpace(model)
	if model == "" {
		model = llmConcurrencyUnknownModel
	}
	t.mu.Lock()
	if t.calls == nil {
		t.calls = make(map[uint64]llmConcurrencyCall)
	}
	t.seq++
	id := t.seq
	t.calls[id] = llmConcurrencyCall{provider: strings.TrimSpace(provider), model: model, startedAt: startedAt}
	if len(t.calls) > t.peak {
		t.peak = len(t.calls)
	}
	t.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			t.mu.Lock()
			delete(t.calls, id)
			t.mu.Unlock()
		})
	}
}

// snapshot 汇总当前在飞调用。明细按并发数降序，同数按模型名排，顺序稳定不跳动。
func (t *llmConcurrencyTracker) snapshot() LLMConcurrencyStatus {
	t.mu.Lock()
	defer t.mu.Unlock()
	status := LLMConcurrencyStatus{Active: len(t.calls), Peak: t.peak}
	if len(t.calls) == 0 {
		return status
	}
	index := make(map[string]int, len(t.calls))
	for _, call := range t.calls {
		key := call.provider + "\x00" + call.model
		position, ok := index[key]
		if !ok {
			index[key] = len(status.Models)
			status.Models = append(status.Models, LLMConcurrencyModel{Provider: call.provider, Model: call.model, Active: 1, StartedAt: call.startedAt})
			continue
		}
		status.Models[position].Active++
		if call.startedAt.Before(status.Models[position].StartedAt) {
			status.Models[position].StartedAt = call.startedAt
		}
	}
	sort.Slice(status.Models, func(i, j int) bool {
		if status.Models[i].Active != status.Models[j].Active {
			return status.Models[i].Active > status.Models[j].Active
		}
		if status.Models[i].Model != status.Models[j].Model {
			return status.Models[i].Model < status.Models[j].Model
		}
		return status.Models[i].Provider < status.Models[j].Provider
	})
	return status
}

// beginLLMCall 在记账装饰器里登记一次模型调用。
func (r *Runtime) beginLLMCall(provider LLMProvider, req llm.GenerateRequest) func() {
	if r == nil {
		return func() {}
	}
	providerName, model := llmCallTarget(provider, req)
	return r.llmConcurrency.begin(providerName, model, r.clock())
}

// llmConcurrencyStatus 返回运行时状态里的并发快照。
func (r *Runtime) llmConcurrencyStatus() LLMConcurrencyStatus {
	if r == nil {
		return LLMConcurrencyStatus{}
	}
	return r.llmConcurrency.snapshot()
}

// llmCallTarget 取这次调用打的是哪个模型。装饰链上握着选择的那一层最准；它认不出
// 时退回请求里写死的模型名，两边都没有就记成未知——统计不该因为认不出名字而漏数。
func llmCallTarget(provider LLMProvider, req llm.GenerateRequest) (string, string) {
	if identity, err := llmProviderIdentity(provider, ""); err == nil {
		if model := strings.TrimSpace(identity.ModelID); model != "" {
			return strings.TrimSpace(identity.Provider), model
		}
	}
	return "", strings.TrimSpace(req.Model)
}

// 并发说明此刻压力有多大，token 说明这些调用花掉了什么。两个数出自同一批调用，
// 所以记在一起：并发在装饰器进出时增减，用量在 recordLLMUsage 那一个汇合点累加
// ——embedding 不走装饰链，但它也会走到那里，两个数的口径因此是一致的。

// LLMUsageCounters 是一段时间里模型调用的花费。
type LLMUsageCounters struct {
	Calls             int64 `json:"calls"`
	InputTokens       int64 `json:"input_tokens"`
	OutputTokens      int64 `json:"output_tokens"`
	CachedInputTokens int64 `json:"cached_input_tokens"`
	TotalTokens       int64 `json:"total_tokens"`
	// MissingUsageCalls 是上游没报用量的调用数。不为 0 时 token 合计只会偏少，
	// 不标出来就会被当成「这几次没花钱」。
	MissingUsageCalls int64 `json:"missing_usage_calls"`
}

func (c *LLMUsageCounters) add(usage llm.Usage) {
	c.Calls++
	c.InputTokens += usage.InputTokens
	c.OutputTokens += usage.OutputTokens
	c.CachedInputTokens += usage.CachedInputTokens
	c.TotalTokens += usage.TotalTokens
	if usage.InputTokens == 0 && usage.OutputTokens == 0 && usage.TotalTokens == 0 {
		c.MissingUsageCalls++
	}
}

// LLMUsageTotals 是运行期的 token 用量。两个桶都只从本次启动算起，重启清零——
// 要按天回溯历史用量得查记录页，那是另一份数据。
type LLMUsageTotals struct {
	Today   LLMUsageCounters `json:"today"`
	Session LLMUsageCounters `json:"session"`
}

type llmUsageTracker struct {
	mu     sync.Mutex
	day    string
	totals LLMUsageTotals
}

// observe 累加一次调用的用量。跨日时先把今天那一桶清零：日期一换，「今日」就该
// 从头数，而不是把昨天的量一直挂在上面。
func (t *llmUsageTracker) observe(usage llm.Usage, now time.Time) {
	day := now.Format(time.DateOnly)
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.day != day {
		t.day = day
		t.totals.Today = LLMUsageCounters{}
	}
	t.totals.Today.add(usage)
	t.totals.Session.add(usage)
}

func (t *llmUsageTracker) snapshot() LLMUsageTotals {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.totals
}

// recordLLMUsageTotals 把一次调用的用量计入运行期合计。
func (r *Runtime) recordLLMUsageTotals(usage llm.Usage) {
	if r == nil {
		return
	}
	r.llmUsage.observe(usage, r.clock())
}

// llmUsageTotals 返回运行时状态里的用量合计。
func (r *Runtime) llmUsageTotals() LLMUsageTotals {
	if r == nil {
		return LLMUsageTotals{}
	}
	return r.llmUsage.snapshot()
}
