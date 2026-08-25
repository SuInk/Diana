// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"
)

type relationSeed struct {
	userID   string
	name     string
	outbound bool
	toMe     bool
	mentions []string
	quoted   string
}

func seedRelationEvents(t *testing.T, store *SQLiteStore, groupID string, at time.Time, seeds []relationSeed) {
	t.Helper()
	ctx := context.Background()
	for index, seed := range seeds {
		segments := []map[string]any{}
		for _, mention := range seed.mentions {
			segments = append(segments, map[string]any{"type": "at", "data": map[string]string{"qq": mention}})
		}
		payload := map[string]any{
			"user_id": seed.userID, "sender_name": seed.name,
			"outbound": seed.outbound, "to_me": seed.toMe, "segments": segments,
		}
		if seed.quoted != "" {
			payload["quoted"] = map[string]any{"user_id": seed.quoted}
		}
		encoded, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		id := groupID + "-" + seed.userID + "-" + string(rune('a'+index))
		if _, err := store.db.ExecContext(ctx, `
INSERT INTO message_events (id, session, kind, group_id, user_id, message_id, sender_name, event_time, text, payload, created_at)
VALUES (?, ?, 'group', ?, ?, ?, ?, ?, '', ?, ?)
`, id, "group:"+groupID, groupID, seed.userID, id, seed.name, at.Unix(), string(encoded), at.Format(time.RFC3339Nano)); err != nil {
			t.Fatal(err)
		}
	}
}

func TestGroupRelationGraphCountsInteractions(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "relations.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	now := time.Now()

	seedRelationEvents(t, store, "g1", now, []relationSeed{
		// Alice 直接叫机器人两次
		{userID: "1001", name: "Alice", toMe: true},
		{userID: "1001", name: "Alice", toMe: true},
		// Bob @ 了 Alice
		{userID: "1002", name: "Bob", mentions: []string{"1001"}},
		// Carol 引用了 Bob 的消息
		{userID: "1003", name: "Carol", quoted: "1002"},
		// 机器人自己回了一条，@ 了 Alice
		{userID: "42", outbound: true, mentions: []string{"1001"}},
	})
	// 另一个群的消息不该混进来
	seedRelationEvents(t, store, "g2", now, []relationSeed{{userID: "9001", name: "Mallory", toMe: true}})

	graph, err := store.GroupRelationGraphFor(context.Background(), "g1", now.Add(-time.Hour), "42")
	if err != nil {
		t.Fatal(err)
	}
	if graph.BotID != "42" {
		t.Fatalf("bot = %q", graph.BotID)
	}
	if graph.Messages != 5 || graph.Participants != 4 {
		t.Fatalf("messages = %d, participants = %d, want 5 / 4", graph.Messages, graph.Participants)
	}
	for _, node := range graph.Nodes {
		if node.UserID == "9001" {
			t.Fatal("别的群的人混进了这张图")
		}
	}
	// 中心节点排在最前，图从它画起。
	if len(graph.Nodes) == 0 || !graph.Nodes[0].IsBot {
		t.Fatalf("nodes = %#v, want the bot first", graph.Nodes)
	}

	weights := map[string]int{}
	for _, edge := range graph.Edges {
		weights[edge.Source+"-"+edge.Target] = edge.Weight
	}
	// Alice 叫了两次机器人，机器人回 @ 了一次 → 三次互动收在同一条无向边上。
	if weights["1001-42"] != 3 {
		t.Fatalf("Alice↔Diana = %d, want 3；edges=%#v", weights["1001-42"], graph.Edges)
	}
	if weights["1001-1002"] != 1 {
		t.Fatalf("Bob→Alice 的 @ 没记上：%#v", graph.Edges)
	}
	if weights["1002-1003"] != 1 {
		t.Fatalf("Carol 引用 Bob 没记上：%#v", graph.Edges)
	}
	// 边是无向的，同一对人只出现一条。
	seen := map[string]bool{}
	for _, edge := range graph.Edges {
		if edge.Source > edge.Target {
			t.Fatalf("边没有归一化方向：%#v", edge)
		}
		key := edge.Source + "-" + edge.Target
		if seen[key] {
			t.Fatalf("同一对人出现了两条边：%s", key)
		}
		seen[key] = true
	}
}

// 人数上限截掉的成员，连着他的边也要一起去掉，否则图上会有指向空处的线。
func TestGroupRelationGraphDropsEdgesToTrimmedNodes(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "relations-trim.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	now := time.Now()

	seeds := []relationSeed{}
	// 一个话痨，和一个只说过一句、还 @ 了话痨的人。
	for i := 0; i < 5; i++ {
		seeds = append(seeds, relationSeed{userID: "1001", name: "Chatty", toMe: true})
	}
	seeds = append(seeds, relationSeed{userID: "1002", name: "Quiet", mentions: []string{"1001"}})
	seedRelationEvents(t, store, "g3", now, seeds)

	graph, err := store.GroupRelationGraphFor(context.Background(), "g3", now.Add(-time.Hour), "42")
	if err != nil {
		t.Fatal(err)
	}
	present := map[string]bool{}
	for _, node := range graph.Nodes {
		present[node.UserID] = true
	}
	for _, edge := range graph.Edges {
		if !present[edge.Source] || !present[edge.Target] {
			t.Fatalf("边指向了图上没有的节点：%#v，nodes=%#v", edge, graph.Nodes)
		}
	}
}

func TestGroupRelationGraphEmptyGroup(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "relations-empty.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()

	graph, err := store.GroupRelationGraphFor(context.Background(), "nobody", time.Now().Add(-time.Hour), "42")
	if err != nil {
		t.Fatal(err)
	}
	// 空群返回空图而不是 nil：前端不必为「没有数据」单独判一种形状。
	if graph.Nodes == nil || graph.Edges == nil || len(graph.Nodes) != 0 || len(graph.Edges) != 0 {
		t.Fatalf("graph = %#v", graph)
	}
	if _, err := store.GroupRelationGraphFor(context.Background(), "", time.Now(), "42"); err != nil {
		t.Fatalf("空群号不该报错：%v", err)
	}
}
