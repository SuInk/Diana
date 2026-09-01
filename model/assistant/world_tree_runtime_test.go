// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"strings"
	"testing"
)

type stubWorldTreeStore struct {
	tree WorldTree
	ok   bool
	err  error
}

func (s stubWorldTreeStore) LoadWorldTree(context.Context) (WorldTree, bool, error) {
	return s.tree, s.ok, s.err
}

func TestRuntimeWorldTreeContextHonorsConfigGate(t *testing.T) {
	tree := WorldTree{Nodes: []WorldTreeNode{
		{ID: "world", Title: "世界", Content: "故事发生在虚构城市枝江。", AlwaysOn: true},
	}}.WithDefaults()
	event := MessageEvent{Kind: EventKindPrivate, UserID: "10001"}

	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	// 没配存储时安静返回空，聊天链路不感知世界树的存在。
	if got := runtime.worldTreeContext(context.Background(), event, "你好"); got != "" {
		t.Fatalf("context without store = %q", got)
	}

	runtime.SetWorldTreeStore(stubWorldTreeStore{tree: tree, ok: true})
	if got := runtime.worldTreeContext(context.Background(), event, "你好"); !strings.Contains(got, "枝江") {
		t.Fatalf("enabled context = %q", got)
	}

	disabled := NewRuntime(BotConfig{WorldTreeEnabled: boolPointer(false)}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	disabled.SetWorldTreeStore(stubWorldTreeStore{tree: tree, ok: true})
	if got := disabled.worldTreeContext(context.Background(), event, "你好"); got != "" {
		t.Fatalf("disabled context = %q", got)
	}
}
