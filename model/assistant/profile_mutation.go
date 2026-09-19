// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"fmt"
	"strings"
	"time"
)

// profileConfigLocked 返回这台机器人的配置；profileID 为空或查不到时退回主配置。
// 调用方必须持有 r.mu。多机器人时凡是「这台机器人」的判定都要走这里或
// effectiveConfigForEvent，不能直接读 r.cfg——r.cfg 只是当前激活的那一台。
func (r *Runtime) profileConfigLocked(profileID string) BotConfig {
	if profileID = strings.TrimSpace(profileID); profileID != "" {
		if profile, ok := r.profileConfigs[profileID]; ok {
			return profile.WithDefaults()
		}
	}
	return r.cfg.WithDefaults()
}

func (r *Runtime) profileConfig(profileID string) BotConfig {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.profileConfigLocked(profileID)
}

// eventProfileID 返回事件所属机器人的 ID；单机器人的老事件没有 ProfileID，归到主配置。
func (r *Runtime) eventProfileID(event MessageEvent) string {
	if id := strings.TrimSpace(event.ProfileID); id != "" {
		return id
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.cfg.ID
}

// commitProfileChange 是聊天里修改机器人配置的统一入口。
//
// 以前每个配置工具各写一遍「落库 → 改 profileConfigs → 同步 r.cfg」，锁的用法也不一样，
// 有的在 r.mu 写锁里做磁盘 I/O，落盘期间所有消息处理都读不到配置。这里统一成：
//   - modelConfigMu 串行化所有配置修改，读改写之间不会丢更新；
//   - 在读锁下取当前配置，落盘时不持有 r.mu；
//   - 落盘成功后再把同一个修改应用到内存里的这台机器人（以及它恰好是主配置时的 r.cfg）。
//
// mutate 必须是确定的、只改自己负责的字段，且给切片字段赋新值，不能原地修改。
func (r *Runtime) commitProfileChange(profileID string, mutate func(*BotConfig) error, persist func(BotConfig) error) (BotConfig, error) {
	r.modelConfigMu.Lock()
	defer r.modelConfigMu.Unlock()

	r.mu.RLock()
	profile, exists := r.profileConfigs[profileID]
	if !exists {
		if r.cfg.ID != profileID {
			r.mu.RUnlock()
			return BotConfig{}, fmt.Errorf("目标机器人不存在")
		}
		profile = r.cfg
	}
	r.mu.RUnlock()

	if err := mutate(&profile); err != nil {
		return BotConfig{}, err
	}
	if err := persist(profile); err != nil {
		return BotConfig{}, err
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if exists {
		if r.profileConfigs == nil {
			r.profileConfigs = map[string]BotConfig{}
		}
		r.profileConfigs[profileID] = profile
	}
	if r.cfg.ID == profileID {
		main := r.cfg
		if err := mutate(&main); err == nil {
			r.cfg = main
		}
	}
	r.updatedAt = time.Now()
	return profile, nil
}
