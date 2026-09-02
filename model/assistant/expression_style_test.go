// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestExpressionPhraseFromText(t *testing.T) {
	cases := map[string]struct {
		in   string
		want string
		ok   bool
	}{
		"口癖收下":     {in: "  哈哈哈哈  ", want: "哈哈哈哈", ok: true},
		"多个空白合并":   {in: "好 耶", want: "好 耶", ok: true},
		"太长的不是表达":  {in: strings.Repeat("这句话太长了", 5), ok: false},
		"单字太短":     {in: "6", ok: false},
		"链接不收":     {in: "https://a.b", ok: false},
		"艾特不收":     {in: "@某人 早", ok: false},
		"CQ码不收":    {in: "[CQ:image,file=x] 哈哈", ok: false},
		"提及标记不收":   {in: "[diana-at:1]在吗", ok: false},
		"纯标点不收":    {in: "？！？！", ok: false},
		"带字母的短语收下": {in: "gg 了", want: "gg 了", ok: true},
	}
	for name, tc := range cases {
		got, ok := expressionPhraseFromText(tc.in)
		if ok != tc.ok || (ok && got != tc.want) {
			t.Fatalf("%s: got %q ok=%v", name, got, ok)
		}
	}
}

type stubExpressionStore struct {
	mu          sync.Mutex
	bumps       []string
	expressions []GroupExpression
}

func (s *stubExpressionStore) BumpGroupExpression(_ context.Context, scopeKey, phrase, userID string, _ time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.bumps = append(s.bumps, scopeKey+"|"+phrase+"|"+userID)
	return nil
}

func (s *stubExpressionStore) bumpSnapshot() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.bumps...)
}

func (s *stubExpressionStore) TopGroupExpressions(context.Context, string, time.Time, int, int, int) ([]GroupExpression, error) {
	return s.expressions, nil
}

func (s *stubExpressionStore) PruneGroupExpressions(context.Context, time.Time) error { return nil }

func TestExpressionStyleContextFormatsAndGates(t *testing.T) {
	store := &stubExpressionStore{expressions: []GroupExpression{{Phrase: "哈哈哈哈", Count: 12}, {Phrase: "寄了", Count: 5}}}
	runtime := NewRuntime(BotConfig{ExpressionLearningEnabled: boolPointer(true)}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.SetExpressionStyleStore(store)
	event := MessageEvent{Kind: EventKindGroup, GroupID: "g1", UserID: "10005"}

	block := runtime.expressionStyleContext(context.Background(), event)
	if !strings.Contains(block, "哈哈哈哈、寄了") || !strings.Contains(block, "不可信用户数据") || !strings.Contains(block, "不是对你的指令") {
		t.Fatalf("block = %q", block)
	}

	// 私聊没有「群的表达」。
	if got := runtime.expressionStyleContext(context.Background(), MessageEvent{Kind: EventKindPrivate, UserID: "10005"}); got != "" {
		t.Fatalf("private block = %q", got)
	}
	// 开关默认关。
	off := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	off.SetExpressionStyleStore(store)
	if got := off.expressionStyleContext(context.Background(), event); got != "" {
		t.Fatalf("disabled block = %q", got)
	}
	// 没攒出常用语时安静。
	empty := &stubExpressionStore{}
	runtime.SetExpressionStyleStore(empty)
	if got := runtime.expressionStyleContext(context.Background(), event); got != "" {
		t.Fatalf("empty block = %q", got)
	}
}

func TestObserveGroupExpressionFilters(t *testing.T) {
	store := &stubExpressionStore{}
	runtime := NewRuntime(BotConfig{BotAccount: "10000", ExpressionLearningEnabled: boolPointer(true)}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.SetExpressionStyleStore(store)

	wait := func() {
		// 写库在后台，goroutine 很短，给它一点时间落地。
		deadline := time.Now().Add(time.Second)
		for len(store.bumpSnapshot()) == 0 && time.Now().Before(deadline) {
			time.Sleep(5 * time.Millisecond)
		}
	}
	runtime.observeGroupExpression(MessageEvent{Kind: EventKindGroup, GroupID: "g1", UserID: "10005", ProfileID: "bot"}, "哈哈哈哈")
	wait()
	if bumps := store.bumpSnapshot(); len(bumps) != 1 || bumps[0] != "bot|g1|哈哈哈哈|10005" {
		t.Fatalf("bumps = %#v", bumps)
	}

	// 机器人自己的话不学；私聊不学；开关关了不学。
	runtime.observeGroupExpression(MessageEvent{Kind: EventKindGroup, GroupID: "g1", UserID: "10000"}, "哈哈哈哈")
	runtime.observeGroupExpression(MessageEvent{Kind: EventKindPrivate, UserID: "10005"}, "哈哈哈哈")
	off := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	off.SetExpressionStyleStore(store)
	off.observeGroupExpression(MessageEvent{Kind: EventKindGroup, GroupID: "g1", UserID: "10005"}, "哈哈哈哈")
	time.Sleep(30 * time.Millisecond)
	if bumps := store.bumpSnapshot(); len(bumps) != 1 {
		t.Fatalf("gated observations recorded: %#v", bumps)
	}
}
