// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"strings"
	"time"

	"github.com/SuInk/diana/model/llm"
)

const (
	// llmCapabilityProbeInterval 是两轮探测的间隔。结论的保质期是 7 天，一天探
	// 一次意味着过期前还有好几次补救机会。
	llmCapabilityProbeInterval = 24 * time.Hour
	// llmCapabilityProbeStartDelay 让进程先把连接和配置都拉起来再探，避免启动
	// 高峰上再压一批请求。
	llmCapabilityProbeStartDelay = 3 * time.Minute
	// llmCapabilityProbeIdlePoll 是「前台正忙」时的重试间隔。探测是纯后台工作，
	// 不能和正在进行的对话抢配额。
	llmCapabilityProbeIdlePoll = 1 * time.Minute
	// llmCapabilityProbeIdleWait 封顶等待前台空闲的时间。一直不空就这轮跳过，
	// 留给下一轮。
	llmCapabilityProbeIdleWait = 30 * time.Minute
	llmCapabilityProbeTimeout  = 60 * time.Second
)

// LLMCapabilityStore 持久化探测结论。SQLite 实现有，测试的内存实现可以没有——
// 没有就只在内存里记，功能安静降级。
type LLMCapabilityStore interface {
	LoadLLMParamDowngrades(ctx context.Context) ([]llm.DowngradeRecord, error)
	SaveLLMParamDowngrades(ctx context.Context, records []llm.DowngradeRecord) error
}

// SetLLMCapabilityStore 注入探测结论的存储。
func (r *Runtime) SetLLMCapabilityStore(store LLMCapabilityStore) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.llmCapability = store
}

// runLLMCapabilityProbeLoop 后台定期探测各配置档收不收强制具名工具。
//
// 为什么要探：收尾和记忆门控会强制模型调用指定工具，而带思考模式的模型（DeepSeek
// 一类）只认 auto，会直接 400 掉整轮对话。请求路径上有降级梯子兜底，但那是「撞一次
// 再学」；提前探好，真实对话第一条就不用吃这一次失败。
//
// 默认没有机器人开这个开关，循环就只是按间隔醒来看一眼目标为空然后接着睡——探测
// 是会计费的真实调用，得由部署者明确打开。
func (r *Runtime) runLLMCapabilityProbeLoop(ctx context.Context) {
	r.restoreLLMCapabilityRecords(ctx)
	select {
	case <-ctx.Done():
		return
	case <-time.After(llmCapabilityProbeStartDelay):
	}
	ticker := time.NewTicker(llmCapabilityProbeInterval)
	defer ticker.Stop()
	for {
		r.probeLLMCapabilitiesWhenIdle(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// restoreLLMCapabilityRecords 把上次落盘的结论装回内存，重启后不必重新学。
func (r *Runtime) restoreLLMCapabilityRecords(ctx context.Context) {
	store := r.llmCapabilityStoreRef()
	if store == nil {
		return
	}
	records, err := store.LoadLLMParamDowngrades(ctx)
	if err != nil || len(records) == 0 {
		return
	}
	llm.RestoreDowngradeRecords(records)
}

func (r *Runtime) llmCapabilityStoreRef() LLMCapabilityStore {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.llmCapability
}

// probeLLMCapabilitiesWhenIdle 等前台空下来再探一轮；等太久就放弃这轮。
func (r *Runtime) probeLLMCapabilitiesWhenIdle(ctx context.Context) {
	if !r.waitRuntimeIdle(ctx, llmCapabilityProbeIdleWait) {
		return
	}
	targets := r.llmCapabilityProbeTargets()
	if len(targets) == 0 {
		return
	}
	probed := false
	for _, target := range targets {
		if ctx.Err() != nil {
			return
		}
		if r.probeLLMCapability(ctx, target) {
			probed = true
		}
	}
	if probed {
		r.persistLLMCapabilityRecords(ctx)
	}
}

// waitRuntimeIdle 等到没有正在处理的对话。后台补图注用的是同一条判据。
func (r *Runtime) waitRuntimeIdle(ctx context.Context, limit time.Duration) bool {
	deadline := time.Now().Add(limit)
	ticker := time.NewTicker(llmCapabilityProbeIdlePoll)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return false
		}
		if r.activeCount() == 0 {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		select {
		case <-ctx.Done():
			return false
		case <-ticker.C:
		}
	}
}

// probeLLMCapability 探一个配置档，返回是否真的发出了探测请求。
func (r *Runtime) probeLLMCapability(ctx context.Context, cfg llm.ProviderConfig) bool {
	client, err := r.newLLMProviderForConfig(cfg)
	if err != nil || client == nil {
		return false
	}
	prober, ok := client.(llm.ForcedToolChoiceProber)
	if !ok {
		return false
	}
	probeCtx, cancel := context.WithTimeout(ctx, llmCapabilityProbeTimeout)
	defer cancel()
	started := time.Now()
	// 探测失败（鉴权、限流、网络）不改结论，也不值得打断整轮：下一轮再来。
	result, _ := prober.ProbeForcedToolChoice(probeCtx)
	// 探测的回复不发给任何人，钱是真花了，按普通调用记进用量。
	r.recordLLMUsage(ctx, MessageEvent{}, result.Provider, result.Model, result.Usage, "llm_capability_probe", time.Since(started), 0)
	return true
}

func (r *Runtime) persistLLMCapabilityRecords(ctx context.Context) {
	store := r.llmCapabilityStoreRef()
	if store == nil {
		return
	}
	_ = store.SaveLLMParamDowngrades(ctx, llm.DowngradeRecords())
}

func (r *Runtime) newLLMProviderForConfig(cfg llm.ProviderConfig) (LLMProvider, error) {
	r.mu.RLock()
	factory := r.llmCfgFactory
	r.mu.RUnlock()
	if factory != nil {
		return factory(cfg)
	}
	return llm.NewClient(cfg)
}

// llmCapabilityProbeTargets 列出值得探的配置档：开了探测开关的机器人、其角色真正
// 绑着的那些，按「端点 + 模型」去重。生图角色不发工具，跳过。没有机器人开着开关
// 就返回空，整个探测循环等于没开销。
func (r *Runtime) llmCapabilityProbeTargets() []llm.ProviderConfig {
	store := r.llmStoreRef()
	if store == nil {
		return nil
	}
	set := store.Profiles().WithDefaults()
	seen := map[string]bool{}
	targets := make([]llm.ProviderConfig, 0, 4)
	for _, botCfg := range r.orderedProfiles() {
		roles := normalizeModelRoles(botCfg.ModelRoles)
		if !boolValue(botCfg.LLMCapabilityProbeEnabled, false) {
			continue
		}
		for roleKey := range roles {
			if roleKey == llmConfigRoleImage {
				continue
			}
			profile, model, ok := profilesForRole(set, roles, roleKey)
			if !ok {
				continue
			}
			cfg := profile.Config.WithDefaults()
			if strings.TrimSpace(model) != "" {
				cfg.Model = model
			}
			key := cfg.BaseURL + "|" + cfg.Model + "|" + string(cfg.Provider)
			if seen[key] {
				continue
			}
			seen[key] = true
			targets = append(targets, cfg)
		}
	}
	return targets
}

func (r *Runtime) llmStoreRef() LLMProfileStore {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.llmStore
}

func (r *Runtime) orderedProfiles() []BotConfig {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.orderedProfilesLocked()
}
