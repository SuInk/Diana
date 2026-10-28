// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"fmt"
	"strings"
	"time"
)

// 跨机器人寻址：一个实例里的多台机器人分布在不同平台上，主人在一个平台上说话，
// 要读另一个平台的群、往那边发消息、在那边做群管，都得先把「哪台机器人的哪个会话」
// 落成一条事件，后面的读历史、发送、群管接口才能原样复用。
//
// 这里只造「目的地」，不带当前这轮的消息体，理由和 crossSessionGroupEvent 一样：
// 带过去会让引用、@ 和媒体索引指向另一条会话里的消息。

// crossBotTarget 是另一台机器人上的一个会话。
type crossBotTarget struct {
	profile BotConfig
	event   MessageEvent
}

// runnableProfile 取一台在跑的机器人：配置存在、启用着、平台在当前版本里可用。
func (r *Runtime) runnableProfile(profileID string) (BotConfig, error) {
	profileID = strings.TrimSpace(profileID)
	if profileID == "" {
		return BotConfig{}, fmt.Errorf("需要 profile_id：先用 bots 查有哪些机器人")
	}
	r.mu.RLock()
	cfg, ok := r.profileConfigs[profileID]
	r.mu.RUnlock()
	if !ok {
		return BotConfig{}, fmt.Errorf("没有 ID 为 %s 的机器人：先用 bots 查", profileID)
	}
	if !cfg.Enabled || r.profileDisabled(profileID) {
		return BotConfig{}, fmt.Errorf("机器人 %s 已停用", profileID)
	}
	if !profileRunnable(cfg) {
		return BotConfig{}, fmt.Errorf("%s", UnsupportedPlatformMessage(cfg.Platform))
	}
	return cfg.WithDefaults(), nil
}

// runnableProfiles 按配置顺序列出在跑的机器人。
func (r *Runtime) runnableProfiles() []BotConfig {
	var out []BotConfig
	for _, cfg := range r.ProfileConfigs() {
		if cfg.Enabled && !r.profileDisabled(cfg.ID) && profileRunnable(cfg) {
			out = append(out, cfg)
		}
	}
	return out
}

// profileChannelStatus 取这台机器人通道的状态；单通道运行时直接取那一条。
func (r *Runtime) profileChannelStatus(cfg BotConfig) (ChannelStatus, bool) {
	r.mu.RLock()
	channel := r.channel
	r.mu.RUnlock()
	if channel == nil {
		return ChannelStatus{}, false
	}
	if multi, ok := channel.(*MultiChannel); ok {
		binding, err := multi.bindingFor(cfg.ID, cfg.Platform)
		if err != nil || binding.Channel == nil {
			return ChannelStatus{}, false
		}
		return binding.Channel.Status(), true
	}
	return channel.Status(), true
}

// crossBotBaseEvent 只定位到机器人本身，不落到具体会话：查群列表、好友列表这类
// 账号层面的操作用它路由到那台机器人的通道。
func (r *Runtime) crossBotBaseEvent(profileID string) (crossBotTarget, error) {
	cfg, err := r.runnableProfile(profileID)
	if err != nil {
		return crossBotTarget{}, err
	}
	event := MessageEvent{
		Platform:  NormalizePlatformID(cfg.Platform),
		ProfileID: cfg.ID,
		SelfID:    strings.TrimSpace(cfg.BotAccount),
		Time:      time.Now().Unix(),
	}
	if status, ok := r.profileChannelStatus(cfg); ok && strings.TrimSpace(status.SelfID) != "" {
		event.SelfID = strings.TrimSpace(status.SelfID)
	}
	// 命名空间的规则和入站一致：多机器人运行时按机器人分，单台时不分。
	r.mu.RLock()
	_, multi := r.channel.(*MultiChannel)
	r.mu.RUnlock()
	if multi {
		event.ContextNamespace = cfg.ID
	}
	return crossBotTarget{profile: cfg, event: event}, nil
}

// crossBotEvent 把「profileID 上的某个群或某个人」落成一条事件。kind 是
// EventKindGroup 或 EventKindPrivate。
func (r *Runtime) crossBotEvent(profileID string, kind EventKind, targetID string) (crossBotTarget, error) {
	targetID = strings.TrimSpace(targetID)
	if targetID == "" {
		return crossBotTarget{}, fmt.Errorf("需要 group_id 或 user_id")
	}
	if err := validatePlatformTargetID(targetID); err != nil {
		return crossBotTarget{}, fmt.Errorf("目标必须是群或账号 ID，不能使用名称")
	}
	target, err := r.crossBotBaseEvent(profileID)
	if err != nil {
		return crossBotTarget{}, err
	}
	event := target.event
	event.Kind = kind
	if kind == EventKindGroup {
		event.MessageType = "group"
		event.GroupID = targetID
	} else {
		event.MessageType = "private"
		event.UserID = targetID
	}
	target.event = event
	return target, nil
}

// requesterOwnsProfile 判断发起人是不是目标机器人的主人。一个实例里的几台机器人
// 可能各有各的主人：在 A 上是主人，不等于能动 B 的群、读 B 的私聊。规则和
// linkedOwnerFor 一致：
//
//   - 目标配了主人：发起人自己的号，或和他关联的号里有目标的主人账号。
//   - 目标没配主人：实例里所有配了主人的机器人，主人账号都在发起人这组号里。
//
// WebUI 聊天页是控制台管理员，本来就能看、改所有机器人。
func (r *Runtime) requesterOwnsProfile(event MessageEvent, target BotConfig) bool {
	if event.webChat {
		return true
	}
	self := r.identityAccountForEvent(event)
	accounts := []IdentityAccount{self}
	for _, account := range r.identityGroup(self) {
		if account.key() != self.key() {
			accounts = append(accounts, account)
		}
	}
	owns := func(profile BotConfig) bool {
		for _, account := range accounts {
			if r.accountIsProfileOwner(profile, account) {
				return true
			}
		}
		return false
	}
	if strings.TrimSpace(target.OwnerID) != "" {
		return owns(target)
	}
	owned := 0
	for _, profile := range r.ProfileConfigs() {
		if strings.TrimSpace(profile.OwnerID) == "" {
			continue
		}
		owned++
		if !owns(profile) {
			return false
		}
	}
	return owned > 0
}

// ownedCrossBotTarget 是 crossBotEvent 加上主人核对，跨机器人读写都走它。
func (r *Runtime) ownedCrossBotTarget(requester MessageEvent, profileID string, kind EventKind, targetID string) (crossBotTarget, error) {
	var target crossBotTarget
	var err error
	if targetID == "" && kind == "" {
		target, err = r.crossBotBaseEvent(profileID)
	} else {
		target, err = r.crossBotEvent(profileID, kind, targetID)
	}
	if err != nil {
		return crossBotTarget{}, err
	}
	if !r.requesterOwnsProfile(requester, target.profile) {
		return crossBotTarget{}, fmt.Errorf("你不是机器人 %s 的主人，不能操作它那边的会话", target.profile.ID)
	}
	return target, nil
}
