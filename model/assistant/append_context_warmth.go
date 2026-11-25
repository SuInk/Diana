// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"math"
	"sync"
	"time"

	"github.com/SuInk/diana/model/llm"
)

// 缓存一冷就整份重新组装。
//
// 续接请求比这一轮完整组装长：11-21～25 生产 3142 次主回复首步里，续接请求中位 6.5 万
// token，同一轮完整组装中位 3.7 万。续接值得，是因为上一轮的前缀还在供应商缓存里，命中
// 的部分只按一成计价。隔得久了缓存就冷了，续接反而要按未命中付一份更长的请求：距上一轮
// 1 分钟内续接首步命中 85.5%，30～60 分钟只剩 27.4%，60 分钟以上 7.6%。
//
// 冷得多快因模型和网关而异，同样 30～60 分钟，swe-2 命中 16%、astra 还有 72%，所以不写死
// 阈值：按同一模型、间隔相近（相差两倍以内）的续接首步实测命中估这一次的命中，续接的计费
// token（未命中算 1、命中算 0.1）超过完整组装时就重新组装。样本不够时照旧续接。同样 5 天
// 数据按时间顺序模拟（只用已经发生的样本），首步计费 token 少 3.9%，少发 740 万输入
// token；重组后的前缀更短，之后几轮续接也跟着变短，这部分没算进去。

// cacheWarmthMinIdle 以内不估：几分钟内续接的命中一直在八成以上，不值得冒误判的险。
const cacheWarmthMinIdle = 5 * time.Minute

// cacheWarmthMinSamples 是估一次命中至少要的相近样本数。
const cacheWarmthMinSamples = 5

// cacheWarmthMaxSamples 是每个模型留的样本数，旧的先丢。
const cacheWarmthMaxSamples = 200

// cacheWarmth 记每个模型续接首步「隔了多久、命中多少」，只在内存里，重启后从头攒。
type cacheWarmth struct {
	mu      sync.Mutex
	samples map[string][]cacheWarmthSample
}

type cacheWarmthSample struct {
	idle time.Duration
	hit  float64
}

// observe 记一次续接首步的实测命中。
func (w *cacheWarmth) observe(model string, idle time.Duration, usage llm.Usage) {
	if model == "" || idle <= 0 || usage.InputTokens <= 0 {
		return
	}
	hit := float64(usage.CachedInputTokens) / float64(usage.InputTokens)
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.samples == nil {
		w.samples = map[string][]cacheWarmthSample{}
	}
	list := append(w.samples[model], cacheWarmthSample{idle: idle, hit: min(1, hit)})
	if len(list) > cacheWarmthMaxSamples {
		list = list[len(list)-cacheWarmthMaxSamples:]
	}
	w.samples[model] = list
}

// estimate 返回同一模型、间隔在 idle 两倍以内的续接首步平均命中；样本不够时 ok=false。
func (w *cacheWarmth) estimate(model string, idle time.Duration) (hit float64, ok bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	var sum float64
	count := 0
	for _, sample := range w.samples[model] {
		if math.Abs(math.Log(float64(sample.idle)/float64(idle))) <= math.Ln2 {
			sum += sample.hit
			count++
		}
	}
	if count < cacheWarmthMinSamples {
		return 0, false
	}
	return sum / float64(count), true
}

// cold 判断这一次续接是不是不如重新组装：continued 是续接请求的估算 token，full 是完整
// 组装的估算 token（已含 Runner 自己加的部分）。完整组装按冷缓存算，偏保守。
func (w *cacheWarmth) cold(model string, idle time.Duration, continued, full int64) (hit float64, cold bool) {
	if idle < cacheWarmthMinIdle || continued <= 0 || full <= 0 {
		return 0, false
	}
	hit, ok := w.estimate(model, idle)
	if !ok {
		return 0, false
	}
	return hit, float64(continued)*(1-0.9*hit) > float64(full)
}
