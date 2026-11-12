// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"sync"
	"time"

	"github.com/SuInk/diana/model/llm"
)

// 续接是拿「请求越来越长」换上游的前缀缓存命中。缓存过期以后这笔交换就不成立了：
// 长请求整份按未命中算，既贵又慢。11-10～11-12 生产 swe-2 每轮第一步：距同一会话
// 上一轮 5 分钟以内的续接命中中位 92%、4.8 秒出字；超过 5 分钟的命中 0%、6.8 秒，
// 攒到 7 万 token 的要 8～12 秒，而同样未命中的 3～4.5 万请求是 7.3 秒。过期后按预算
// 重新组装，请求回到完整组装的大小。
//
// 缓存留多久各家不一样（swe-2 约 5 分钟，terra 约 1 小时），也没有接口可查，所以按
// 模型从实际命中情况里学：记下「隔了多久还命中过」和「隔了多久没命中」，只有间隔
// 超过前者、达到后者才算过期。

// appendContextCacheFloor 是判过期的最短间隔：已知的上游缓存至少留 5 分钟，比它短的
// 没命中当作偶发，不拿来学。
const appendContextCacheFloor = 5 * time.Minute

// appendContextCacheProbeEvery 是每多少次判过期就照常续接一次。判过期以后那一轮
// 重新组装，看不到「其实还能命中」；隔几次探一下，学错了（比如把一次偶发未命中当成
// 过期）能自己改回来。
const appendContextCacheProbeEvery = 8

type appendContextCacheLife struct {
	mu      sync.Mutex
	byModel map[string]*appendContextModelCache
}

type appendContextModelCache struct {
	// warmUpTo 是见过仍然命中的最长间隔，coldFrom 是见过没命中的最短间隔（0 表示还没见过）。
	warmUpTo time.Duration
	coldFrom time.Duration
	expired  int
}

// expired 报告上一轮用 model 发的请求隔了 gap 之后，缓存是不是已经过期。
func (c *appendContextCacheLife) expired(model string, gap time.Duration) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	life := c.byModel[model]
	if life == nil || life.coldFrom == 0 || gap < appendContextCacheFloor || gap <= life.warmUpTo || gap < life.coldFrom {
		return false
	}
	life.expired++
	return life.expired%appendContextCacheProbeEvery != 0
}

// observe 记下一轮续接的实际命中情况。usage 是整轮的用量：只有一两步的轮次里，第一步
// 没命中时整轮命中比例到不了八成；判没命中只看单步的轮次，免得被后面几步的命中冲淡。
func (c *appendContextCacheLife) observe(model string, gap time.Duration, usage llm.Usage, modelTurns int) {
	if model == "" || gap <= 0 || usage.InputTokens <= 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.byModel == nil {
		c.byModel = map[string]*appendContextModelCache{}
	}
	life := c.byModel[model]
	if life == nil {
		life = &appendContextModelCache{}
		c.byModel[model] = life
	}
	switch {
	case modelTurns <= 2 && usage.CachedInputTokens*10 >= usage.InputTokens*8:
		life.warmUpTo = max(life.warmUpTo, gap)
		if life.coldFrom != 0 && life.coldFrom <= life.warmUpTo {
			// 比「没命中过」更长的间隔命中了：之前那次是偶发。
			life.coldFrom = 0
		}
	case modelTurns == 1 && usage.CachedInputTokens*10 < usage.InputTokens && gap >= appendContextCacheFloor && gap > life.warmUpTo:
		if life.coldFrom == 0 || gap < life.coldFrom {
			life.coldFrom = gap
		}
	}
}
