// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"strings"
	"testing"
)

type stubWorldBookStore struct {
	tree WorldBook
	ok   bool
	err  error
}

func (s stubWorldBookStore) LoadWorldBook(context.Context) (WorldBook, bool, error) {
	return s.tree, s.ok, s.err
}

func TestRuntimeWorldBookContextHonorsConfigGate(t *testing.T) {
	tree := WorldBook{Nodes: []WorldBookNode{
		{ID: "world", Title: "世界", Content: "故事发生在虚构城市枝江。", AlwaysOn: true},
	}}.WithDefaults()
	event := MessageEvent{Kind: EventKindPrivate, UserID: "10001"}

	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	// 没配存储时安静返回空，聊天链路不感知世界书的存在。
	if got := runtime.worldBookContext(context.Background(), event, "你好"); got != "" {
		t.Fatalf("context without store = %q", got)
	}

	runtime.SetWorldBookStore(stubWorldBookStore{tree: tree, ok: true})
	if got := runtime.worldBookContext(context.Background(), event, "你好"); !strings.Contains(got, "枝江") {
		t.Fatalf("enabled context = %q", got)
	}

	disabled := NewRuntime(BotConfig{WorldBookEnabled: boolPointer(false)}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	disabled.SetWorldBookStore(stubWorldBookStore{tree: tree, ok: true})
	if got := disabled.worldBookContext(context.Background(), event, "你好"); got != "" {
		t.Fatalf("disabled context = %q", got)
	}
}
