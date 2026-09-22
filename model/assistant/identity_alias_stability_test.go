// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"strings"
	"sync"
	"testing"
)

type memoryAliasSaltStore struct {
	mu    sync.Mutex
	salt  string
	loads int
	saves int
}

func (s *memoryAliasSaltStore) LoadIdentityAliasSalt(context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loads++
	return s.salt, nil
}

func (s *memoryAliasSaltStore) SaveIdentityAliasSalt(_ context.Context, salt string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.saves++
	if s.salt == "" { // 与 INSERT OR IGNORE 同语义：没有才写
		s.salt = salt
	}
	return nil
}

// 同一个账号的别名必须跨轮、跨重启稳定。
//
// 别名遍布每一条历史行（线上单条请求出现 982 处）。盐一变，整段历史文本逐字不同，
// 供应商的前缀缓存在历史这一段永远命不中。改之前每个 scope 一个随机盐，而 scope 每轮
// 对话新建一次，本机实测同一个人换一轮就换别名。
func TestIdentityAliasStaysStableAcrossTurns(t *testing.T) {
	const realID = "30007"
	store := &memoryAliasSaltStore{}

	aliasFor := func(r *Runtime) string {
		scope := newIdentityPrivacyScopeWithSalt(r.identityAliasSalt(context.Background()))
		return scope.register(realID, "bot_owner")
	}

	r1 := newRuntimeWithAliasStore(t, store)
	first := aliasFor(r1)
	if first == "" || !strings.HasPrefix(first, identityAlias("bot_owner")) {
		t.Fatalf("别名格式不对: %q", first)
	}
	// 同一进程内换一轮。
	if again := aliasFor(r1); again != first {
		t.Fatalf("同进程换轮后别名变了: %q -> %q", first, again)
	}
	// 模拟重启：新的 Runtime，盐从存储里读回。
	r2 := newRuntimeWithAliasStore(t, store)
	if afterRestart := aliasFor(r2); afterRestart != first {
		t.Fatalf("重启后别名变了: %q -> %q（盐没有从存储恢复）", first, afterRestart)
	}
	if store.saves == 0 {
		t.Fatal("盐没有落库，重启后必然失效")
	}
}

// 没有存储时至少要保证单进程内稳定，不能每轮一个盐。
func TestIdentityAliasStableWithoutStore(t *testing.T) {
	r := newRuntimeWithAliasStore(t, nil)
	a := newIdentityPrivacyScopeWithSalt(r.identityAliasSalt(context.Background())).register("100001", "bot_owner")
	b := newIdentityPrivacyScopeWithSalt(r.identityAliasSalt(context.Background())).register("100001", "bot_owner")
	if a != b || a == "" {
		t.Fatalf("无存储时同进程内别名应当稳定: %q vs %q", a, b)
	}
}

// aliasSaltHistoryStore 同时实现历史存储和盐存储：运行时是从 messageStore 上做
// 类型断言取盐存储的，测试替身要走同一条路才算验到真实接线。
type aliasSaltHistoryStore struct {
	*memoryAliasSaltStore
}

func (aliasSaltHistoryStore) AppendMessageEvent(context.Context, string, MessageEvent) error {
	return nil
}

func (aliasSaltHistoryStore) ListRecentMessageEvents(context.Context, string, int) ([]MessageEvent, error) {
	return nil, nil
}

func newRuntimeWithAliasStore(t *testing.T, store *memoryAliasSaltStore) *Runtime {
	t.Helper()
	r := NewRuntime(BotConfig{OwnerID: "100001", BotAccount: "200002"}, nilChannel{},
		NewDefaultPluginManager(), nil, nil, nil, nil)
	if store != nil {
		r.SetMessageHistoryStore(aliasSaltHistoryStore{memoryAliasSaltStore: store})
	}
	return r
}
