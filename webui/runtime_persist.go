// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"log"

	"github.com/SuInk/diana/model/assistant"
)

type RuntimePersistor struct {
	store BotProfileStore
}

// NewRuntimePersistor 创建机器人运行态配置持久化器。
func NewRuntimePersistor(store BotProfileStore) *RuntimePersistor {
	return &RuntimePersistor{store: store}
}

// SaveBotConfig 保存BotConfig数据。
func (p *RuntimePersistor) SaveBotConfig(cfg assistant.BotConfig) {
	if p == nil || p.store == nil {
		return
	}
	// 这是机器人 owner 指令的轻量落盘通道，失败不阻塞聊天响应；
	// 但至少要留一行日志，否则改完配置重启又变回去时没有任何线索。
	if err := p.store.SaveCurrentConfig(cfg); err != nil {
		log.Printf("persist chatbot runtime config failed: %v", err)
	}
}
