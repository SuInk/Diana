// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"fmt"
	"log"

	"github.com/SuInk/diana/model/assistant"
)

func (p *RuntimePersistor) SaveModelRole(cfg assistant.BotConfig, role string, next assistant.ModelRole) (assistant.BotConfig, error) {
	if p == nil || p.store == nil {
		return assistant.BotConfig{}, fmt.Errorf("未接入机器人配置存储")
	}
	if store, ok := p.store.(assistant.ModelRoleConfigSaver); ok {
		return store.SaveModelRole(cfg, role, next)
	}
	return assistant.BotConfig{}, fmt.Errorf("配置存储不支持按机器人更新模型分配")
}

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
		log.Printf("persist diana runtime config failed: %v", err)
	}
}
