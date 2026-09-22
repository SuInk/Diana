// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
	"errors"
	"sync"
	"testing"
)

// 没注册安装器时必须是空操作：库使用者和测试不该因为渲染一次就被拖去下载浏览器。
func TestEnsureBrowserNoopWithoutInstaller(t *testing.T) {
	resetBrowserInstallStateForTest()
	EnsureBrowser(context.Background()) // 不应 panic，也不应做任何事
}

// 装不上就别每次渲染都重试：几十 MB 的下载失败重试会把带宽和日志一起打满。
func TestEnsureBrowserInstallsAtMostOnce(t *testing.T) {
	resetBrowserInstallStateForTest()
	var mu sync.Mutex
	calls := 0
	SetBrowserInstaller(func(context.Context) error {
		mu.Lock()
		defer mu.Unlock()
		calls++
		return errors.New("装不上")
	})
	defer SetBrowserInstaller(nil)
	for i := 0; i < 3; i++ {
		EnsureBrowser(context.Background())
	}
	mu.Lock()
	defer mu.Unlock()
	if calls > 1 {
		t.Fatalf("安装器应当最多被调用一次，实际 %d 次", calls)
	}
}
