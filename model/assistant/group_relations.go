// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import "time"

// 群聊关系图的数据形状放在这里而不是存储层：存储层已经 import 了 assistant
// （它返回 MessageEvent），反过来再 import 就成环了；而渲染和插件都在这一侧。

// GroupRelationNode 是关系图上的一个人。
type GroupRelationNode struct {
	UserID      string `json:"user_id"`
	DisplayName string `json:"display_name,omitempty"`
	// Messages 是这段时间内的发言数，决定节点大小。
	Messages int `json:"messages"`
	// Favorability 取自用户档案，是全局值而不是本群值——好感度本来就不分群。
	Favorability int `json:"favorability"`
	// IsBot 标记中心节点。
	IsBot bool `json:"is_bot,omitempty"`
}

// GroupRelationEdge 是一条互动边，无向：Source 恒小于 Target，避免同一对人
// 出现两条边。
type GroupRelationEdge struct {
	Source string `json:"source"`
	Target string `json:"target"`
	Weight int    `json:"weight"`
}

// GroupRelationGraph 是一个群在某段时间内的关系图。
type GroupRelationGraph struct {
	GroupID  string     `json:"group_id"`
	BotID    string     `json:"bot_id,omitempty"`
	Since    *time.Time `json:"since,omitempty"`
	Messages int        `json:"messages"`
	// Participants 是这段时间实际发过言的人数，Nodes 可能因为上限少于它。
	Participants int                 `json:"participants"`
	Nodes        []GroupRelationNode `json:"nodes"`
	Edges        []GroupRelationEdge `json:"edges"`
	// Truncated 说明扫描撞到了条数上限，图只反映最近这一段。
	Truncated bool `json:"truncated"`
}
