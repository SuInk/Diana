// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SuInk/diana/model/agent"
)

func TestReplyProgressTracksToolsWithoutSending(t *testing.T) {
	channel := &recordingChannel{}
	runtime := NewRuntime(BotConfig{}, channel, NewPluginManager(), nil, nil, nil, nil)
	event := MessageEvent{Kind: EventKindGroup, GroupID: "group", UserID: "owner", MessageID: "first"}
	calls := 0
	observer, stop := runtime.startReplyProgress(withOutboundTurn(context.Background(), "turn"), event, func(context.Context, agent.RunEvent) { calls++ })
	defer stop()
	progress := runtime.earlierReplyProgress(MessageEvent{Kind: EventKindGroup, GroupID: "group", UserID: "owner", MessageID: "next"})
	if progress == nil {
		t.Fatal("missing running-reply context")
	}
	observer(context.Background(), agent.RunEvent{Phase: agent.RunPhaseToolCompleted, Tool: "web_search"})
	observer(context.Background(), agent.RunEvent{Phase: agent.RunPhaseToolCompleted, Tool: "browser_render", ToolInput: map[string]any{"urls": []string{"https://a.example", "https://b.example"}}})
	observer(context.Background(), agent.RunEvent{Phase: agent.RunPhaseToolStarted, Tool: "browser_render"})
	for _, want := range []string{"搜了 1 次", "读了 2 个网页", "现在在读网页"} {
		if !strings.Contains(progress.describe(time.Now()), want) {
			t.Fatal("missing", want)
		}
	}
	observer(context.Background(), agent.RunEvent{Phase: agent.RunPhaseCompleted})
	if calls != 4 {
		t.Fatal("inner observer lost events")
	}
	if sent := channel.sentSnapshot(); len(sent) != 0 {
		t.Fatalf("unexpected progress messages: %+v", sent)
	}
	stop()
	if runtime.earlierReplyProgress(MessageEvent{Kind: EventKindGroup, GroupID: "group", UserID: "owner", MessageID: "next"}) != nil {
		t.Fatal("tracker not removed")
	}
}

func TestCodingHeartbeatReportsProgressAndStall(t *testing.T) {
	first, every, limit := codingHeartbeatFirst, codingHeartbeatEvery, codingHeartbeatMax
	codingHeartbeatFirst, codingHeartbeatEvery, codingHeartbeatMax = 10*time.Millisecond, 30*time.Millisecond, 2
	t.Cleanup(func() { codingHeartbeatFirst, codingHeartbeatEvery, codingHeartbeatMax = first, every, limit })
	logPath := filepath.Join(t.TempDir(), "job.log")
	if err := os.WriteFile(logPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	job := CodingJob{ID: "job-1", LogPath: logPath, StartedAt: time.Now().Add(-20 * time.Minute)}
	var mu sync.Mutex
	var sent []string
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		runCodingHeartbeat(ctx, job, func(_ context.Context, text string) error {
			mu.Lock()
			sent = append(sent, text)
			mu.Unlock()
			return nil
		})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("心跳没有在上限后停下")
	}
	if len(sent) != 2 || !strings.Contains(sent[0], "job-1 还在跑") {
		t.Fatalf("心跳 = %q", sent)
	}
	// 日志 30ms 没动就超过了缩短后的间隔：第二条要提示可能卡住、怎么取消。
	if !strings.Contains(sent[1], "可能卡住") || !strings.Contains(sent[1], "取消任务 job-1") {
		t.Fatalf("没有卡住提示: %s", sent[1])
	}
}

// 定时查询、后台任务等没有入站轮次的运行不报进度。
func TestReplyProgressOnlyForLiveTurns(t *testing.T) {
	runtime := &Runtime{}
	called := 0
	inner := func(context.Context, agent.RunEvent) { called++ }
	observer, stop := runtime.startReplyProgress(context.Background(), MessageEvent{}, inner)
	defer stop()
	observer(context.Background(), agent.RunEvent{Phase: agent.RunPhaseStarted})
	if called != 1 {
		t.Fatalf("原观察者被调用 %d 次", called)
	}
}
