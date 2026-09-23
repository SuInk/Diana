// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"reflect"
	"time"

	"github.com/SuInk/diana/model/llm"
)

// llmDowngradePersistInterval 是把降级结论落盘的间隔。结论只在真实请求撞上 400
// 并靠降级救回来之后才会变，变化很少，落盘本身也只是一次本地写。
const llmDowngradePersistInterval = 10 * time.Minute

// LLMDowngradeStore 持久化「哪个端点的哪个模型拒过哪些请求字段」。SQLite 实现有，
// 测试的内存实现可以没有——没有就只在内存里记，重启后重新学。
type LLMDowngradeStore interface {
	LoadLLMParamDowngrades(ctx context.Context) ([]llm.DowngradeRecord, error)
	SaveLLMParamDowngrades(ctx context.Context, records []llm.DowngradeRecord) error
}

// SetLLMDowngradeStore 注入降级结论的存储。
func (r *Runtime) SetLLMDowngradeStore(store LLMDowngradeStore) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.llmDowngrades = store
}

func (r *Runtime) llmDowngradeStoreRef() LLMDowngradeStore {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.llmDowngrades
}

// runLLMDowngradeMemoLoop 启动时装回上次落盘的结论，之后定期把新学到的落盘。
//
// 结论全部来自真实请求：带思考模式的模型拒绝强制具名工具时，请求路径上的降级梯子
// 退回 auto 重发，救回来之后记住。以前还有一个后台探测开关，空闲时专门花钱发一条
// 请求去问；它默认关着，而落盘又只挂在探测后面，结果是默认配置下学到的结论从来
// 没存下来，重启就忘。探测去掉了，落盘改成独立的定时任务，不发任何模型请求。
func (r *Runtime) runLLMDowngradeMemoLoop(ctx context.Context) {
	store := r.llmDowngradeStoreRef()
	if store == nil {
		return
	}
	if records, err := store.LoadLLMParamDowngrades(ctx); err == nil && len(records) > 0 {
		llm.RestoreDowngradeRecords(records)
	}
	last := llm.DowngradeRecords()
	ticker := time.NewTicker(llmDowngradePersistInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			// 退出前再存一次，最近一段学到的不丢。ctx 已经取消，换一个短超时的。
			saveCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			r.persistLLMDowngrades(saveCtx, store, &last)
			cancel()
			return
		case <-ticker.C:
			r.persistLLMDowngrades(ctx, store, &last)
		}
	}
}

// persistLLMDowngrades 只在结论有变化时写。过期的结论 DowngradeRecords 已经滤掉，
// 所以过期本身也会触发一次写，把它从库里清出去。
func (r *Runtime) persistLLMDowngrades(ctx context.Context, store LLMDowngradeStore, last *[]llm.DowngradeRecord) {
	records := llm.DowngradeRecords()
	if reflect.DeepEqual(records, *last) {
		return
	}
	if err := store.SaveLLMParamDowngrades(ctx, records); err == nil {
		*last = records
	}
}
