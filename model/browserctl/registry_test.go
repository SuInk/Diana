// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package browserctl

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
)

// memoryStore 是测试用的内存存储，顺手记下保存次数。
type memoryStore struct {
	mu    sync.Mutex
	doc   Document
	saved int
	err   error
}

func (s *memoryStore) LoadBrowserControl(context.Context) (Document, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.doc, true, nil
}

func (s *memoryStore) SaveBrowserControl(_ context.Context, doc Document) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	s.doc = doc
	s.saved++
	return nil
}

func TestRegistryCreateTokenStoresOnlyHash(t *testing.T) {
	store := &memoryStore{}
	registry := NewRegistry(context.Background(), store)
	info, plaintext, err := registry.CreateToken(context.Background(), "我的 Chrome")
	if err != nil {
		t.Fatalf("创建令牌失败：%v", err)
	}
	if !strings.HasPrefix(plaintext, tokenPlaintextPrefix) {
		t.Fatalf("令牌明文应带可识别前缀，得到 %q", plaintext)
	}
	if info.Prefix != plaintext[:tokenPrefixRunes] {
		t.Fatalf("展示前缀与明文不一致：%q vs %q", info.Prefix, plaintext)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.doc.Tokens) != 1 {
		t.Fatalf("应落盘一把令牌，得到 %d", len(store.doc.Tokens))
	}
	saved := store.doc.Tokens[0]
	if saved.TokenHash != HashToken(plaintext) {
		t.Fatal("落盘的应是哈希")
	}
	if strings.Contains(string(mustJSON(t, store.doc)), plaintext) {
		t.Fatal("存储里不能出现令牌明文")
	}
}

func TestRegistryAuthenticatePinsExtensionID(t *testing.T) {
	registry := NewRegistry(context.Background(), &memoryStore{})
	_, plaintext, err := registry.CreateToken(context.Background(), "chrome")
	if err != nil {
		t.Fatalf("创建令牌失败：%v", err)
	}
	info, err := registry.Authenticate(context.Background(), plaintext, "aaaabbbbccccdddd")
	if err != nil {
		t.Fatalf("首次握手应通过：%v", err)
	}
	if info.ExtensionID != "aaaabbbbccccdddd" {
		t.Fatalf("首次握手应钉定扩展 ID，得到 %q", info.ExtensionID)
	}
	if _, err := registry.Authenticate(context.Background(), plaintext, "aaaabbbbccccdddd"); err != nil {
		t.Fatalf("同一扩展重连应通过：%v", err)
	}
	_, err = registry.Authenticate(context.Background(), plaintext, "eeeeffffgggghhhh")
	if !errors.Is(err, ErrExtensionMismatch) {
		t.Fatalf("换扩展用同一把令牌应被拒，得到 %v", err)
	}
}

func TestRegistryAuthenticateRejectsUnknownAndEmpty(t *testing.T) {
	registry := NewRegistry(context.Background(), &memoryStore{})
	if _, err := registry.Authenticate(context.Background(), "dianabx_nope", "abc"); !errors.Is(err, ErrTokenNotFound) {
		t.Fatalf("未知令牌应被拒，得到 %v", err)
	}
	_, plaintext, _ := registry.CreateToken(context.Background(), "chrome")
	if _, err := registry.Authenticate(context.Background(), plaintext, "   "); !errors.Is(err, ErrExtensionIDInvalid) {
		t.Fatalf("缺扩展 ID 应被拒，得到 %v", err)
	}
}

func TestRegistryRevokeToken(t *testing.T) {
	registry := NewRegistry(context.Background(), &memoryStore{})
	info, plaintext, _ := registry.CreateToken(context.Background(), "chrome")
	if _, err := registry.RevokeToken(context.Background(), info.ID); err != nil {
		t.Fatalf("吊销失败：%v", err)
	}
	if _, err := registry.Authenticate(context.Background(), plaintext, "abc"); !errors.Is(err, ErrTokenNotFound) {
		t.Fatalf("吊销后不能再连，得到 %v", err)
	}
	if _, err := registry.RevokeToken(context.Background(), info.ID); !errors.Is(err, ErrTokenNotFound) {
		t.Fatalf("重复吊销应报不存在，得到 %v", err)
	}
}

func TestRegistrySetPolicyRollsBackOnStoreFailure(t *testing.T) {
	store := &memoryStore{err: errors.New("磁盘满了")}
	registry := NewRegistry(context.Background(), store)
	if _, err := registry.SetPolicy(context.Background(), Policy{Enabled: true, WriteEnabled: true}); err == nil {
		t.Fatal("落盘失败时应报错")
	}
	if registry.Policy().Enabled {
		t.Fatal("落盘失败后不能留下只在内存里生效的授权")
	}
}

func TestRegistryReloadsPersistedState(t *testing.T) {
	store := &memoryStore{}
	first := NewRegistry(context.Background(), store)
	if _, err := first.SetPolicy(context.Background(), Policy{
		Enabled:      true,
		AllowedHosts: []string{"example.com"},
	}); err != nil {
		t.Fatalf("保存策略失败：%v", err)
	}
	_, plaintext, _ := first.CreateToken(context.Background(), "chrome")

	second := NewRegistry(context.Background(), store)
	if !second.Policy().HostAllowed("https://example.com/") {
		t.Fatal("重启后站点白名单应保留")
	}
	if _, err := second.Authenticate(context.Background(), plaintext, "abc"); err != nil {
		t.Fatalf("重启后令牌应仍可用：%v", err)
	}
}

func TestRegistryTokenNameValidation(t *testing.T) {
	registry := NewRegistry(context.Background(), &memoryStore{})
	for _, name := range []string{"", "   ", "带\n换行", strings.Repeat("长", tokenNameMaxLen+1)} {
		if _, _, err := registry.CreateToken(context.Background(), name); !errors.Is(err, ErrTokenNameInvalid) {
			t.Errorf("名称 %q 应被拒，得到 %v", name, err)
		}
	}
}
