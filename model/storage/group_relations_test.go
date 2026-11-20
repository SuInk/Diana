// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"context"
	"encoding/json"
	"fmt"
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
	seedRelationEventsIn(t, store, "group:"+groupID, groupID, at, seeds)
}

// seedRelationEventsIn 把消息记到指定会话下，模拟同一个群里另一台机器人记的那份。
func seedRelationEventsIn(t *testing.T, store *SQLiteStore, session, groupID string, at time.Time, seeds []relationSeed) {
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
		id := session + "-" + seed.userID + "-" + string(rune('a'+index))
		if _, err := store.db.ExecContext(ctx, `
INSERT INTO message_events (id, session, kind, group_id, user_id, message_id, sender_name, event_time, text, payload, created_at)
VALUES (?, ?, 'group', ?, ?, ?, ?, ?, '', ?, ?)
`, id, session, groupID, seed.userID, id, seed.name, at.Unix(), string(encoded), at.Format(time.RFC3339Nano)); err != nil {
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
	// 同一个群里另一台机器人记的那份也不该混进来，否则每条都算两遍
	seedRelationEventsIn(t, store, "other:group:g1", "g1", now, []relationSeed{
		{userID: "1001", name: "Alice", toMe: true},
		{userID: "9002", name: "Eve", toMe: true},
	})
	// 带本机器人命名空间的当前写法要算进来
	seedRelationEventsIn(t, store, "bot:group:g1", "g1", now, []relationSeed{{userID: "1004", name: "Dave"}})

	graph, err := store.GroupRelationGraphFor(context.Background(), "bot", "g1", now.Add(-time.Hour), "42")
	if err != nil {
		t.Fatal(err)
	}
	if graph.BotID != "42" {
		t.Fatalf("bot = %q", graph.BotID)
	}
	if graph.Messages != 6 || graph.Participants != 5 {
		t.Fatalf("messages = %d, participants = %d, want 6 / 5", graph.Messages, graph.Participants)
	}
	for _, node := range graph.Nodes {
		if node.UserID == "9001" || node.UserID == "9002" {
			t.Fatalf("别的群或别的机器人记的人混进了这张图：%s", node.UserID)
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

	graph, err := store.GroupRelationGraphFor(context.Background(), "bot", "g3", now.Add(-time.Hour), "42")
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

	graph, err := store.GroupRelationGraphFor(context.Background(), "bot", "nobody", time.Now().Add(-time.Hour), "42")
	if err != nil {
		t.Fatal(err)
	}
	// 空群返回空图而不是 nil：前端不必为「没有数据」单独判一种形状。
	if graph.Nodes == nil || graph.Edges == nil || len(graph.Nodes) != 0 || len(graph.Edges) != 0 {
		t.Fatalf("graph = %#v", graph)
	}
	if _, err := store.GroupRelationGraphFor(context.Background(), "bot", "", time.Now(), "42"); err != nil {
		t.Fatalf("空群号不该报错：%v", err)
	}
}

// 扫到条数上限 break 出来时，结果集要先关掉再查好感度。内存库只有一条连接，
// 没关的话好感度查询会一直等到上下文超时，好感度也就丢了。
func TestGroupRelationGraphReleasesScanBeforeFavorability(t *testing.T) {
	store, err := NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	if store.readDB != nil {
		t.Fatal("in-memory store should share its single connection")
	}
	now := time.Now().Unix()
	if _, err := store.db.Exec(`WITH RECURSIVE seq(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM seq WHERE x < ?)
INSERT INTO message_events (id, session, kind, group_id, user_id, message_id, sender_name, event_time, text, payload, created_at)
SELECT printf('m%d', x), 'group:g1', 'group', 'g1', '1001', printf('m%d', x), 'Alice', ? - x, '', '{"user_id":"1001"}', '2026-01-01T00:00:00Z' FROM seq`,
		groupRelationScanLimit+5, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`INSERT INTO user_profiles (bot_profile_id, user_id, display_name, favorability, message_count, memories, updated_at)
VALUES ('', '1001', 'Alice', 37, 1, '[]', '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	started := time.Now()
	graph, err := store.GroupRelationGraphFor(ctx, "bot", "g1", time.Time{}, "42")
	if err != nil {
		t.Fatal(err)
	}
	if !graph.Truncated || graph.Messages != groupRelationScanLimit {
		t.Fatalf("truncated=%v messages=%d", graph.Truncated, graph.Messages)
	}
	if elapsed := time.Since(started); elapsed > 3*time.Second || ctx.Err() != nil {
		t.Fatalf("favorability query waited for the scan's connection: %v", elapsed)
	}
	var favorability int
	for _, node := range graph.Nodes {
		if node.UserID == "1001" {
			favorability = node.Favorability
		}
	}
	if favorability != 37 {
		t.Fatalf("favorability = %d, want 37; nodes=%#v", favorability, graph.Nodes)
	}
}

// 好感度按机器人分开：别的机器人对这个人的好感不能冒充成这台的；自己那台有记录时
// 优先用它，没有时才用多机器人之前写的空命名空间旧画像。
func TestGroupRelationGraphFavorabilityIsPerBot(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "relations-favor.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	now := time.Now()
	seedRelationEventsIn(t, store, "bot:group:g1", "g1", now, []relationSeed{{userID: "1001", name: "Alice"}, {userID: "1002", name: "Bob"}})
	for _, row := range []struct {
		bot, user string
		score     int
	}{{"", "1001", 37}, {"other", "1001", 90}, {"other", "1002", 80}, {"bot", "1002", 12}, {"", "1002", 60}} {
		if _, err := store.db.Exec(`INSERT INTO user_profiles (bot_profile_id, user_id, display_name, favorability, message_count, memories, updated_at)
VALUES (?, ?, '', ?, 1, '[]', '2026-01-01T00:00:00Z')`, row.bot, row.user, row.score); err != nil {
			t.Fatal(err)
		}
	}
	graph, err := store.GroupRelationGraphFor(context.Background(), "bot", "g1", now.Add(-time.Hour), "42")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]int{}
	for _, node := range graph.Nodes {
		got[node.UserID] = node.Favorability
	}
	if got["1001"] != 37 || got["1002"] != 12 {
		t.Fatalf("favorability = %v, want 1001→37（旧画像兜底）1002→12（本机器人）", got)
	}
}

// 换中心时，中心这个人就算发言最少也要留在图上，没发过言的人则退回机器人。
func TestGroupRelationGraphCenteredOnMember(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "relations-center.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	now := time.Now()

	seeds := []relationSeed{}
	for index := 0; index < groupRelationMaxNodes+5; index++ {
		userID := fmt.Sprintf("%d", 20000+index)
		seeds = append(seeds, relationSeed{userID: userID, name: userID}, relationSeed{userID: userID, name: userID})
	}
	// 只说过一句的人，@ 了第一个话痨。
	seeds = append(seeds, relationSeed{userID: "19999", name: "Quiet", mentions: []string{"20000"}})
	seedRelationEvents(t, store, "g5", now, seeds)

	graph, err := store.GroupRelationGraphCenteredOn(context.Background(), "bot", "g5", now.Add(-time.Hour), "42", "19999")
	if err != nil {
		t.Fatal(err)
	}
	if graph.CenterID != "19999" {
		t.Fatalf("center = %q, want 19999", graph.CenterID)
	}
	found := false
	for _, node := range graph.Nodes {
		found = found || node.UserID == "19999"
	}
	if !found {
		t.Fatalf("中心被人数上限截掉了：%d 个节点", len(graph.Nodes))
	}
	if len(graph.Edges) == 0 || graph.Edges[0].Source != "19999" && graph.Edges[0].Target != "19999" {
		t.Fatalf("中心的边丢了：%#v", graph.Edges)
	}

	fallback, err := store.GroupRelationGraphCenteredOn(context.Background(), "bot", "g5", now.Add(-time.Hour), "42", "88888")
	if err != nil {
		t.Fatal(err)
	}
	if fallback.CenterID != "42" {
		t.Fatalf("没发过言的人不该当中心：center = %q", fallback.CenterID)
	}
}
