// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"github.com/SuInk/diana/model/assistant"
)

type RuntimePersistor struct {
	store QQBotProfileStore
}

// NewRuntimePersistor 创建机器人运行态配置持久化器。
func NewRuntimePersistor(store QQBotProfileStore) *RuntimePersistor {
	return &RuntimePersistor{store: store}
}

// SaveBotConfig 保存BotConfig数据。
func (p *RuntimePersistor) SaveBotConfig(cfg assistant.BotConfig) {
	if p == nil || p.store == nil {
		return
	}
	// 这是机器人 owner 指令的轻量落盘通道，失败不阻塞聊天响应。
	p.store.SaveCurrentConfig(cfg)
}
