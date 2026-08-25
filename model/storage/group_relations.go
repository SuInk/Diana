// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// 群聊关系图：以机器人为中心，看这个群里谁在跟谁说话。
//
// 数据不用新建表。message_events 的 payload 里存的是整条 MessageEvent 的 JSON，
// outbound、to_me、segments（含 at 段）、quoted 全在里面，边就从这些字段数出来。
// 新开一张统计表意味着历史消息没有边，图一上线是空的。
const (
	// groupRelationScanLimit 限制单次扫描的消息条数。刷屏群一个月能有十几万条，
	// 全量扫一遍既慢又没有额外信息量——边的形状在几千条上就稳定了。
	groupRelationScanLimit = 20000
	// groupRelationMaxNodes 限制返回的成员数。几百人的群全画出来是一团毛线，
	// 看不出任何关系；只保留发言最多的这些人，其余并进「其他」计数。
	groupRelationMaxNodes = 40
)

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

// relationEventPayload 只取关系图用得上的那几个字段。整条 MessageEvent 解出来
// 会带上媒体、工具结果这些大字段，扫两万条就是白烧内存。
type relationEventPayload struct {
	UserID     string `json:"user_id"`
	SenderName string `json:"sender_name"`
	SelfID     string `json:"self_id"`
	Outbound   bool   `json:"outbound"`
	ToMe       bool   `json:"to_me"`
	Segments   []struct {
		Type string            `json:"type"`
		Data map[string]string `json:"data"`
	} `json:"segments"`
	Quoted *struct {
		UserID string `json:"user_id"`
	} `json:"quoted"`
}

// GroupRelationGraphFor 统计一个群的关系图。
func (s *SQLiteStore) GroupRelationGraphFor(ctx context.Context, groupID string, since time.Time, botID string) (GroupRelationGraph, error) {
	graph := GroupRelationGraph{
		GroupID: strings.TrimSpace(groupID),
		BotID:   strings.TrimSpace(botID),
		Nodes:   []GroupRelationNode{},
		Edges:   []GroupRelationEdge{},
	}
	if s == nil || s.db == nil || graph.GroupID == "" {
		return graph, nil
	}
	if !since.IsZero() {
		at := since
		graph.Since = &at
	}
	sinceUnix := int64(0)
	if !since.IsZero() {
		sinceUnix = since.Unix()
	}

	rows, err := s.db.QueryContext(ctx, `
SELECT COALESCE(user_id, ''), COALESCE(sender_name, ''), payload
FROM message_events
WHERE group_id = ? AND event_time >= ?
ORDER BY event_time DESC
LIMIT ?
`, graph.GroupID, sinceUnix, groupRelationScanLimit+1)
	if err != nil {
		return GroupRelationGraph{}, fmt.Errorf("query group relations: %w", err)
	}
	defer func() { _ = rows.Close() }()

	names := map[string]string{}
	messages := map[string]int{}
	weights := map[[2]string]int{}
	addEdge := func(left, right string) {
		left, right = strings.TrimSpace(left), strings.TrimSpace(right)
		if left == "" || right == "" || left == right {
			return
		}
		if left > right {
			left, right = right, left
		}
		weights[[2]string{left, right}]++
	}

	scanned := 0
	for rows.Next() {
		var userID, senderName, payload string
		if err := rows.Scan(&userID, &senderName, &payload); err != nil {
			return GroupRelationGraph{}, fmt.Errorf("scan group relations: %w", err)
		}
		scanned++
		if scanned > groupRelationScanLimit {
			graph.Truncated = true
			break
		}
		var event relationEventPayload
		if strings.TrimSpace(payload) != "" {
			_ = json.Unmarshal([]byte(payload), &event)
		}
		speaker := strings.TrimSpace(firstNonEmptyText(event.UserID, userID))
		if speaker == "" {
			continue
		}
		if event.Outbound {
			// 机器人自己的发言：确定中心节点，并把它 @ 到的人连上。
			if graph.BotID == "" {
				graph.BotID = speaker
			}
		} else if name := strings.TrimSpace(firstNonEmptyText(senderName, event.SenderName)); name != "" {
			names[speaker] = name
		}
		graph.Messages++
		messages[speaker]++

		if event.ToMe && graph.BotID != "" {
			addEdge(speaker, graph.BotID)
		}
		for _, segment := range event.Segments {
			if segment.Type != "at" {
				continue
			}
			if target := strings.TrimSpace(segment.Data["qq"]); target != "" && target != "all" {
				addEdge(speaker, target)
			}
		}
		if event.Quoted != nil {
			addEdge(speaker, event.Quoted.UserID)
		}
	}
	if err := rows.Err(); err != nil {
		return GroupRelationGraph{}, fmt.Errorf("iterate group relations: %w", err)
	}

	graph.Participants = len(messages)
	graph.Nodes = s.buildRelationNodes(ctx, messages, names, graph.BotID)
	graph.Edges = relationEdgesAmong(graph.Nodes, weights)
	return graph, nil
}

// buildRelationNodes 按发言数取前 N 个人，并补上好感度。中心节点无论发言多少
// 都要在——它是这张图的原点。
func (s *SQLiteStore) buildRelationNodes(ctx context.Context, messages map[string]int, names map[string]string, botID string) []GroupRelationNode {
	nodes := make([]GroupRelationNode, 0, len(messages))
	for userID, count := range messages {
		nodes = append(nodes, GroupRelationNode{UserID: userID, DisplayName: names[userID], Messages: count, IsBot: userID == botID && botID != ""})
	}
	sort.Slice(nodes, func(left, right int) bool {
		if nodes[left].IsBot != nodes[right].IsBot {
			return nodes[left].IsBot
		}
		if nodes[left].Messages == nodes[right].Messages {
			return nodes[left].UserID < nodes[right].UserID
		}
		return nodes[left].Messages > nodes[right].Messages
	})
	if len(nodes) > groupRelationMaxNodes {
		nodes = nodes[:groupRelationMaxNodes]
	}
	s.fillRelationFavorability(ctx, nodes)
	return nodes
}

// fillRelationFavorability 一次查完这些人的好感度。逐个查会在四十个人的图上
// 打四十次库。
func (s *SQLiteStore) fillRelationFavorability(ctx context.Context, nodes []GroupRelationNode) {
	if len(nodes) == 0 {
		return
	}
	placeholders := make([]string, 0, len(nodes))
	args := make([]any, 0, len(nodes))
	for _, node := range nodes {
		placeholders = append(placeholders, "?")
		args = append(args, node.UserID)
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT user_id, MAX(favorability), COALESCE(MAX(display_name), '')
FROM user_profiles
WHERE user_id IN (`+strings.Join(placeholders, ",")+`)
GROUP BY user_id
`, args...)
	if err != nil {
		// 好感度只是装饰，查不到不该让整张图失败。
		return
	}
	defer func() { _ = rows.Close() }()
	favorability := map[string]int{}
	profileNames := map[string]string{}
	for rows.Next() {
		var userID, name string
		var score int
		if err := rows.Scan(&userID, &score, &name); err != nil {
			return
		}
		favorability[userID] = score
		profileNames[userID] = name
	}
	for index := range nodes {
		nodes[index].Favorability = favorability[nodes[index].UserID]
		if strings.TrimSpace(nodes[index].DisplayName) == "" {
			nodes[index].DisplayName = strings.TrimSpace(profileNames[nodes[index].UserID])
		}
	}
}

// relationEdgesAmong 只保留两端都在图上的边。一端被人数上限截掉之后，那条边
// 会指向一个画不出来的节点。
func relationEdgesAmong(nodes []GroupRelationNode, weights map[[2]string]int) []GroupRelationEdge {
	present := make(map[string]struct{}, len(nodes))
	for _, node := range nodes {
		present[node.UserID] = struct{}{}
	}
	edges := make([]GroupRelationEdge, 0, len(weights))
	for pair, weight := range weights {
		if _, ok := present[pair[0]]; !ok {
			continue
		}
		if _, ok := present[pair[1]]; !ok {
			continue
		}
		edges = append(edges, GroupRelationEdge{Source: pair[0], Target: pair[1], Weight: weight})
	}
	sort.Slice(edges, func(left, right int) bool {
		if edges[left].Weight == edges[right].Weight {
			if edges[left].Source == edges[right].Source {
				return edges[left].Target < edges[right].Target
			}
			return edges[left].Source < edges[right].Source
		}
		return edges[left].Weight > edges[right].Weight
	})
	return edges
}

func firstNonEmptyText(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
