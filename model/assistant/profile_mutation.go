// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// 运行时里没有「当前机器人」或「主配置」：每条消息、每次调用都按它所属的机器人取配置。
// 以前另有一份 r.cfg，取的是 WebUI 里当前选中的那台，凡是读它的判断都会在切换选中时
// 悄悄换成另一台机器人的设置。
//
// 查找规则：
//   - 按机器人 ID 找到就用它；
//   - 只有一台机器人时就是它（单机器人部署和没带 ProfileID 的老事件）；
//   - 多台机器人又对不上 ID 时用默认配置，不去猜是哪一台。

// profileConfigLocked 返回这台机器人的配置。调用方必须持有 r.mu。
func (r *Runtime) profileConfigLocked(profileID string) BotConfig {
	if profile, ok := r.lookupProfileLocked(profileID); ok {
		return profile.WithDefaults()
	}
	return DefaultBotConfig().WithDefaults()
}

// lookupProfileLocked 按上面的规则找机器人，找不到时 ok 为 false。
func (r *Runtime) lookupProfileLocked(profileID string) (BotConfig, bool) {
	if profileID = strings.TrimSpace(profileID); profileID != "" {
		if profile, ok := r.profileConfigs[profileID]; ok {
			return profile, true
		}
	}
	if len(r.profileConfigs) == 1 {
		for _, profile := range r.profileConfigs {
			return profile, true
		}
	}
	return BotConfig{}, false
}

func (r *Runtime) profileConfig(profileID string) BotConfig {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.profileConfigLocked(profileID)
}

// ProfileConfig 返回指定机器人的配置；profileID 为空且只有一台机器人时返回那一台。
func (r *Runtime) ProfileConfig(profileID string) BotConfig {
	return r.profileConfig(profileID)
}

// ProfileConfigs 按配置集里的顺序返回全部机器人配置。
func (r *Runtime) ProfileConfigs() []BotConfig {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.orderedProfilesLocked()
}

func (r *Runtime) orderedProfilesLocked() []BotConfig {
	out := make([]BotConfig, 0, len(r.profileConfigs))
	seen := make(map[string]bool, len(r.profileConfigs))
	for _, id := range r.profileOrder {
		if profile, ok := r.profileConfigs[id]; ok && !seen[id] {
			seen[id] = true
			out = append(out, profile.WithDefaults())
		}
	}
	for id, profile := range r.profileConfigs {
		if !seen[id] {
			out = append(out, profile.WithDefaults())
		}
	}
	return out
}

func (r *Runtime) enabledProfilesLocked() []BotConfig {
	var out []BotConfig
	for _, profile := range r.orderedProfilesLocked() {
		if profile.Enabled {
			out = append(out, profile)
		}
	}
	return out
}

// eventProfileID 返回事件所属机器人的 ID；老事件没有 ProfileID 且只有一台机器人时归到它。
func (r *Runtime) eventProfileID(event MessageEvent) string {
	if id := strings.TrimSpace(event.ProfileID); id != "" {
		return id
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if profile, ok := r.lookupProfileLocked(""); ok {
		return profile.ID
	}
	return ""
}

// configForContext 返回一次模型调用所属机器人的配置：按 context 里带的机器人或事件找。
// 流式输出、身份脱敏这类开关都挂在机器人配置上，不能读某一台「当前」机器人的。
func (r *Runtime) configForContext(ctx context.Context) BotConfig {
	profileID := ""
	if ctx != nil {
		if id, ok := ctx.Value(modelProfileContextKey{}).(string); ok {
			profileID = id
		}
		if usage := llmUsageFromContext(ctx); profileID == "" && usage != nil {
			profileID = usage.event.ProfileID
		}
	}
	return r.profileConfig(profileID)
}

// commitProfileChange 是聊天里修改机器人配置的统一入口。
//
// 以前每个配置工具各写一遍「落库 → 改 profileConfigs → 同步 r.cfg」，锁的用法也不一样，
// 有的在 r.mu 写锁里做磁盘 I/O，落盘期间所有消息处理都读不到配置。这里统一成：
//   - modelConfigMu 串行化所有配置修改，读改写之间不会丢更新；
//   - 在读锁下取这台机器人的配置，落盘时不持有 r.mu；
//   - 落盘成功后再写回内存。
//
// mutate 只改自己负责的字段，且给切片字段赋新值，不能原地修改。
func (r *Runtime) commitProfileChange(profileID string, mutate func(*BotConfig) error, persist func(BotConfig) error) (BotConfig, error) {
	r.modelConfigMu.Lock()
	defer r.modelConfigMu.Unlock()

	r.mu.RLock()
	profile, exists := r.profileConfigs[profileID]
	r.mu.RUnlock()
	if !exists {
		return BotConfig{}, fmt.Errorf("目标机器人不存在")
	}

	if err := mutate(&profile); err != nil {
		return BotConfig{}, err
	}
	if err := persist(profile); err != nil {
		return BotConfig{}, err
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.profileConfigs == nil {
		r.profileConfigs = map[string]BotConfig{}
	}
	r.profileConfigs[profileID] = profile
	r.updatedAt = time.Now()
	return profile, nil
}
