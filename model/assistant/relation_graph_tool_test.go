// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"strings"
	"testing"
	"time"
)

type relationToolStore struct {
	MessageHistoryStore
	groupID, botID, centerID string
	since                    time.Time
	graph                    GroupRelationGraph
}

func (s *relationToolStore) GroupRelationGraphFor(_ context.Context, groupID string, since time.Time, botID string) (GroupRelationGraph, error) {
	s.groupID, s.botID, s.since = groupID, botID, since
	return s.graph, nil
}

type centeredRelationToolStore struct{ relationToolStore }

func (s *centeredRelationToolStore) GroupRelationGraphCenteredOn(_ context.Context, groupID string, since time.Time, botID, centerID string) (GroupRelationGraph, error) {
	s.groupID, s.botID, s.centerID, s.since = groupID, botID, centerID, since
	return s.graph, nil
}

func TestGroupRelationsToolCenterSchema(t *testing.T) {
	tool := &dianaGroupRelationsTool{}
	props := tool.InputSchema()["properties"].(map[string]any)
	if prop, ok := props["center_id"].(map[string]any); !ok || prop["type"] != "string" {
		t.Fatalf("missing center_id schema: %#v", props)
	}
}

func TestLoadGroupRelationGraphCenter(t *testing.T) {
	since := time.Now().Add(-time.Hour)
	for _, center := range []string{"", "bot", "member"} {
		t.Run(center, func(t *testing.T) {
			store := &centeredRelationToolStore{}
			if _, err := loadGroupRelationGraph(context.Background(), store, "current-group", since, "bot", center); err != nil {
				t.Fatal(err)
			}
			want := ""
			if center == "member" {
				want = center
			}
			if store.groupID != "current-group" || store.botID != "bot" || store.centerID != want || !store.since.Equal(since) {
				t.Fatalf("incorrect query: %#v", store)
			}
		})
	}
	legacy := &relationToolStore{}
	if _, err := loadGroupRelationGraph(context.Background(), legacy, "g", since, "bot", "member"); err == nil {
		t.Fatal("unsupported store silently used bot center")
	}
	if _, err := loadGroupRelationGraph(context.Background(), legacy, "g", since, "bot", ""); err != nil {
		t.Fatal(err)
	}
}

func TestGroupRelationsToolRejectsMissingCenter(t *testing.T) {
	runtime := NewRuntime(BotConfig{BotAccount: "bot"}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	store := &centeredRelationToolStore{relationToolStore: relationToolStore{graph: GroupRelationGraph{CenterID: "bot", Participants: 2}}}
	runtime.SetMessageHistoryStore(store)
	tool := newDianaGroupRelationsTool(runtime, MessageEvent{Kind: EventKindGroup, GroupID: "g", SelfID: "bot"}, SettingValues{})
	output, err := tool.Run(context.Background(), map[string]any{"center_id": " member ", "range": "7d"})
	if err != nil {
		t.Fatal(err)
	}
	if store.centerID != "member" || store.groupID != "g" || !strings.Contains(output, `"ok":false`) || !strings.Contains(output, "没有这位成员的发言记录") {
		t.Fatalf("center = %q, output = %s", store.centerID, output)
	}
}

func TestRelationCircleUsesSelectedMember(t *testing.T) {
	graph := GroupRelationGraph{
		BotID: "bot", CenterID: "member",
		Nodes: []GroupRelationNode{{UserID: "bot", IsBot: true}, {UserID: "member"}, {UserID: "friend"}},
		Edges: []GroupRelationEdge{{Source: "member", Target: "friend", Weight: 10}, {Source: "bot", Target: "member", Weight: 1}},
	}
	layout := layoutRelationCircle(graph, relationImageDefaultSeats)
	if layout.center.UserID != "member" || len(layout.members) != 2 || layout.members[0].node.UserID != "friend" || layout.members[0].weight != 10 {
		t.Fatalf("wrong selected-member layout: %#v", layout)
	}
}

func TestGroupRelationTitlePrefersGroupName(t *testing.T) {
	tests := []struct {
		name, groupID, groupName, want string
	}{
		{name: "group name", groupID: "123", groupName: "产品讨论", want: "产品讨论 · 互动圈"},
		{name: "trim name", groupID: "123", groupName: "  产品讨论  ", want: "产品讨论 · 互动圈"},
		{name: "fallback id", groupID: "123", groupName: " ", want: "群 123 · 互动圈"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := groupRelationTitle(tt.groupID, tt.groupName); got != tt.want {
				t.Fatalf("groupRelationTitle(%q, %q) = %q, want %q", tt.groupID, tt.groupName, got, tt.want)
			}
		})
	}
}
