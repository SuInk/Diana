// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package main

import (
	"bytes"
	"context"
	"log"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SuInk/diana/model/assistant"
	"github.com/SuInk/diana/model/storage"
)

// idleChannel 一直连着、什么也不收，直到运行时取消它。
type idleChannel struct{}

func (idleChannel) Connect(ctx context.Context, _ assistant.EventHandler) error {
	<-ctx.Done()
	return nil
}
func (idleChannel) Send(context.Context, assistant.OutgoingMessage) error { return nil }
func (idleChannel) CallAPI(context.Context, string, map[string]any) (map[string]any, error) {
	return nil, nil
}
func (idleChannel) Status() assistant.ChannelStatus { return assistant.ChannelStatus{} }
func (idleChannel) Close() error                    { return nil }

// lockedBuffer 让后台 goroutine 写日志时不和断言读缓冲区打架。
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// 按 main 的关机顺序走一遍：根 ctx 取消 → 停运行时 → 关库。入站队列和记忆任务的
// worker 收尾时要释放租约，以前库先关，日志里每次重启都是一串 database is closed。
func TestStopBotRuntimeLetsWorkersReleaseLeasesBeforeStoreCloses(t *testing.T) {
	store, err := storage.NewSQLiteStore(filepath.Join(t.TempDir(), "diana.db"))
	if err != nil {
		t.Fatal(err)
	}
	var output lockedBuffer
	writer, flags := log.Writer(), log.Flags()
	log.SetOutput(&output)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(writer)
		log.SetFlags(flags)
	})

	runtime := assistant.NewRuntime(assistant.BotConfig{Enabled: true, BotAccount: "42", OneBotAccessToken: "test-token"}, idleChannel{}, assistant.NewPluginManager(), nil, nil, nil, nil)
	runtime.SetInboundEventStore(store)
	runtime.SetStructuredMemoryStore(store)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := runtime.Start(ctx); err != nil {
		t.Fatal(err)
	}
	// 等两组协调器都起来、做完启动时的租约回收，再模拟收到 SIGTERM。
	time.Sleep(300 * time.Millisecond)
	cancel()
	stopBotRuntime(runtime)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	logs := output.String()
	if strings.Contains(logs, "database is closed") || strings.Contains(logs, "lease release failed") {
		t.Fatalf("workers still touched the store after it closed:\n%s", logs)
	}
	if !strings.Contains(logs, "assistant runtime stopped in") {
		t.Fatalf("stop was not logged:\n%s", logs)
	}
}
