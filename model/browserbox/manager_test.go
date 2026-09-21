// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package browserbox

import (
	"context"
	"sync"
	"testing"
)

type memoryStore struct {
	mu  sync.Mutex
	doc Document
	ok  bool
}

func (s *memoryStore) LoadBrowserBox(context.Context) (Document, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.doc, s.ok, nil
}

func (s *memoryStore) SaveBrowserBox(_ context.Context, doc Document) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.doc = doc
	s.ok = true
	return nil
}

// 关着的时候不该有进程，也不该给模型任何地址。
func TestManagerStartsDisabled(t *testing.T) {
	manager := New(context.Background(), &memoryStore{}, t.TempDir())
	status := manager.Status()
	if status.Running || status.Settings.Enabled {
		t.Fatalf("默认应是关着的，实际 %+v", status)
	}
	if manager.AgentCDPURL() != "" {
		t.Fatal("没启用时不该给出 CDP 地址")
	}
	if manager.Unavailable() != "" {
		t.Fatal("没启用时应让位给外部 CDP 地址，不该报不可用")
	}
}

// 配置要落盘，进程起不起来是另一回事——测试环境里没有浏览器也不能丢配置。
func TestManagerPersistsSettings(t *testing.T) {
	store := &memoryStore{}
	dir := t.TempDir()
	manager := New(context.Background(), store, dir)
	saved, err := manager.SetSettings(context.Background(), Settings{DeniedHosts: []string{"blocked.example.com"}})
	if err != nil {
		t.Fatalf("保存配置失败：%v", err)
	}
	if len(saved.DeniedHosts) != 1 {
		t.Fatalf("黑名单没存住：%+v", saved)
	}
	reloaded := New(context.Background(), store, dir)
	if got := reloaded.Settings(); len(got.DeniedHosts) != 1 || got.DeniedHosts[0] != "blocked.example.com" {
		t.Fatalf("重新加载后配置对不上：%+v", got)
	}
}

// 接管打开时模型那一侧必须当场失效，且理由要说清楚是谁在占着。
func TestTakeoverHidesCDPURLFromAgent(t *testing.T) {
	manager := New(context.Background(), &memoryStore{}, t.TempDir())
	manager.mu.Lock()
	manager.settings.Enabled = true
	manager.cdpURL = "http://127.0.0.1:12345"
	manager.mu.Unlock()

	if manager.AgentCDPURL() == "" {
		t.Fatal("正常状态下应给出地址")
	}
	manager.SetTakeover(true)
	if manager.AgentCDPURL() != "" {
		t.Fatal("接管时不该再给模型地址")
	}
	if reason := manager.Unavailable(); reason == "" {
		t.Fatal("接管时要给模型一句能看懂的理由")
	}
	manager.SetTakeover(false)
	if manager.AgentCDPURL() == "" {
		t.Fatal("交还控制权后应恢复")
	}
}

// 调试地址要能从 Chrome 的输出里解析出来，端口是随机的。
func TestDebugHTTPBase(t *testing.T) {
	got, err := debugHTTPBase("ws://127.0.0.1:53201/devtools/browser/8f2c")
	if err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	if got != "http://127.0.0.1:53201" {
		t.Fatalf("解析结果不对：%s", got)
	}
	if _, err := debugHTTPBase("nonsense"); err == nil {
		t.Fatal("看不懂的地址应该报错")
	}
}
