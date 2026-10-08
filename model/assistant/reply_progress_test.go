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

// runProgress 用缩短的节奏跑 loop，返回在 loop 退出时关闭的通道。
func runProgress(p *replyProgress, sink *progressSink) chan struct{} {
	exited := make(chan struct{})
	go func() {
		p.loop(context.Background(), sink.send, 20*time.Millisecond, 20*time.Millisecond, 2)
		close(exited)
	}()
	return exited
}

type progressSink struct {
	mu   sync.Mutex
	sent []string
}

func (s *progressSink) send(text string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sent = append(s.sent, text)
}

func (s *progressSink) snapshot() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.sent...)
}

// 跑久了报做到哪，次数封顶；收尾后一条都不再发。
func TestReplyProgressReportsLongRunsAndStops(t *testing.T) {
	progress := &replyProgress{started: time.Now(), counts: map[string]int{}, done: make(chan struct{})}
	sink := &progressSink{}
	exited := runProgress(progress, sink)
	progress.observe(agent.RunEvent{Phase: agent.RunPhaseToolCompleted, Tool: "web_search"})
	progress.observe(agent.RunEvent{Phase: agent.RunPhaseToolCompleted, Tool: "web_search"})
	progress.observe(agent.RunEvent{Phase: agent.RunPhaseToolCompleted, Tool: "browser_render", ToolInput: map[string]any{"urls": []any{"https://a.example", "https://b.example"}}})
	progress.observe(agent.RunEvent{Phase: agent.RunPhaseToolStarted, Tool: "browser_render"})
	<-exited
	sent := sink.snapshot()
	if len(sent) != 2 {
		t.Fatalf("发了 %d 条，want 上限 2: %q", len(sent), sent)
	}
	for _, want := range []string{"搜了 2 次", "读了 2 个网页", "现在在读网页"} {
		if !strings.Contains(sent[0], want) {
			t.Fatalf("进度缺 %q: %s", want, sent[0])
		}
	}
}

func TestReplyProgressSilentForQuickOrToolFreeRuns(t *testing.T) {
	// 很快收尾：一条都不发。
	quick := &replyProgress{started: time.Now(), counts: map[string]int{}, done: make(chan struct{})}
	sink := &progressSink{}
	quickExited := runProgress(quick, sink)
	quick.observe(agent.RunEvent{Phase: agent.RunPhaseToolCompleted, Tool: "web_search"})
	quick.observe(agent.RunEvent{Phase: agent.RunPhaseCompleted})
	// 没调工具：模型只是想得慢，报了也没内容。
	idle := &replyProgress{started: time.Now(), counts: map[string]int{}, done: make(chan struct{})}
	idleExited := runProgress(idle, sink)
	time.Sleep(80 * time.Millisecond)
	idle.stop()
	<-quickExited
	<-idleExited
	if sent := sink.snapshot(); len(sent) != 0 {
		t.Fatalf("不该发进度: %q", sent)
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
