// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"fmt"
	"strings"
)

// 每台机器人各自的 NoneBot 桥接。
//
// 以前运行时只有一个桥接，配置取自 WebUI 里当前选中的那台机器人，却把所有机器人的
// 事件都转发过去；NoneBot 回来的调用也不分机器人。现在按机器人配置各建一个：
// 只转发自己那台的事件，NoneBot 的 API 调用也只走自己那台的连接。

// bridgeAPIChannel 把桥接收到的 NoneBot 调用路由回所属机器人的连接。
// 桥接只用得到 CallAPI，其余方法不会被调用。
type bridgeAPIChannel struct {
	runtime   *Runtime
	profileID string
}

func (c bridgeAPIChannel) Connect(context.Context, EventHandler) error { return nil }
func (c bridgeAPIChannel) Send(context.Context, OutgoingMessage) error {
	return fmt.Errorf("diana: NoneBot 桥接不直接发送消息")
}
func (c bridgeAPIChannel) CallAPI(ctx context.Context, action string, params map[string]any) (map[string]any, error) {
	return c.runtime.CallOneBotAPIForProfile(ctx, c.profileID, action, params)
}
func (c bridgeAPIChannel) Status() ChannelStatus { return ChannelStatus{ProfileID: c.profileID} }
func (c bridgeAPIChannel) Close() error          { return nil }

// reconcileBridges 让桥接和机器人配置保持一致：开了桥接的机器人各有一个，关掉或删掉的
// 停掉，配置变了的换配置并在运行中重连。
func (r *Runtime) reconcileBridges() {
	r.mu.Lock()
	if r.bridges == nil {
		r.bridges = map[string]*NoneBotBridge{}
	}
	desired := map[string]NoneBotBridgeConfig{}
	for _, profile := range r.orderedProfilesLocked() {
		cfg := bridgeConfigFromBotConfig(profile)
		if profile.Enabled && cfg.Enabled && strings.TrimSpace(cfg.Endpoint) != "" {
			desired[profile.ID] = cfg
		}
	}
	var stop, start []*NoneBotBridge
	for id, bridge := range r.bridges {
		next, ok := desired[id]
		if !ok {
			stop = append(stop, bridge)
			delete(r.bridges, id)
			continue
		}
		if bridge.config() != next {
			bridge.UpdateConfig(next, nil)
			stop = append(stop, bridge)
			start = append(start, bridge)
		}
	}
	for id, cfg := range desired {
		if _, ok := r.bridges[id]; !ok {
			bridge := NewNoneBotBridge(cfg, bridgeAPIChannel{runtime: r, profileID: id})
			r.bridges[id] = bridge
			start = append(start, bridge)
		}
	}
	running, runCtx := r.running, r.runCtx
	r.mu.Unlock()

	for _, bridge := range stop {
		bridge.Stop()
	}
	if running && runCtx != nil {
		for _, bridge := range start {
			bridge.Start(runCtx)
		}
	}
}

func (r *Runtime) startBridges(ctx context.Context) {
	r.mu.RLock()
	bridges := make([]*NoneBotBridge, 0, len(r.bridges))
	for _, bridge := range r.bridges {
		bridges = append(bridges, bridge)
	}
	r.mu.RUnlock()
	for _, bridge := range bridges {
		bridge.Start(ctx)
	}
}

func (r *Runtime) stopBridges() {
	r.mu.RLock()
	bridges := make([]*NoneBotBridge, 0, len(r.bridges))
	for _, bridge := range r.bridges {
		bridges = append(bridges, bridge)
	}
	r.mu.RUnlock()
	for _, bridge := range bridges {
		bridge.Stop()
	}
}

// forwardToBridge 把事件交给它所属机器人的桥接；没开桥接就什么都不做。
func (r *Runtime) forwardToBridge(event MessageEvent) {
	profileID := r.eventProfileID(event)
	r.mu.RLock()
	bridge := r.bridges[profileID]
	r.mu.RUnlock()
	if bridge != nil {
		// NoneBot 桥只做旁路转发，不影响本地插件和 LLM 回复流程。
		bridge.ForwardEvent(event)
	}
}

func (r *Runtime) bridgeStatuses() map[string]NoneBotBridgeStatus {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make(map[string]NoneBotBridgeStatus, len(r.bridges))
	for id, bridge := range r.bridges {
		out[id] = bridge.Status()
	}
	return out
}

// BridgeSummary 汇总各机器人的桥接：有任一台开了桥接即 enabled，开了的全部连上才算 connected。
func (s RuntimeStatus) BridgeSummary() (enabled, connected bool) {
	connected = true
	for _, bridge := range s.NoneBotBridges {
		if !bridge.Enabled {
			continue
		}
		enabled = true
		connected = connected && bridge.Connected
	}
	return enabled, enabled && connected
}
