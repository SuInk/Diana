// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package desktopctl

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"
)

type memoryJobStore struct {
	mu   sync.Mutex
	jobs []Job
}

func (s *memoryJobStore) LoadDesktopJobs(context.Context) ([]Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Job, len(s.jobs))
	copy(out, s.jobs)
	return out, nil
}

func (s *memoryJobStore) SaveDesktopJobs(_ context.Context, jobs []Job) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.jobs = append([]Job(nil), jobs...)
	return nil
}

func TestJobPauseResumeRequiresReobserve(t *testing.T) {
	store := &memoryJobStore{}
	jobs := NewJobManager(context.Background(), store)
	hub := NewHub(NewRegistry(context.Background(), &memoryStore{}))
	if _, err := hub.registry.SetPolicy(context.Background(), writePolicy()); err != nil {
		t.Fatal(err)
	}
	hub.SetJobManager(jobs)
	conn := &fakeConn{}
	c, _, err := hub.Register(conn, Hello{ProtocolVersion: ProtocolVersion, HelperID: "mac-1"}, TokenInfo{ID: "t1"})
	if err != nil {
		t.Fatal(err)
	}
	png := []byte("x")
	conn.respond = func(frame Frame) *Frame {
		return &Frame{Type: FrameResult, ID: frame.ID, Data: rawJSON(ScreenshotPayload{WindowID: "w1", Mime: "image/png", Data: "eA=="})}
	}
	conn.deliver = c.HandleFrame
	c.HandleFrame(Frame{Type: FrameWindows, Data: rawJSON(WindowsPayload{Windows: []WindowInfo{
		{ID: "w1", AppName: "Safari", BundleID: "com.apple.Safari", Active: true},
	}})})

	job, err := jobs.Create(context.Background(), "填表", "owner", "", JobBudget{MaxSteps: 10})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := jobs.Start(context.Background(), job.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := jobs.Pause(context.Background(), job.ID, "主人离开"); err != nil {
		t.Fatal(err)
	}
	_, err = hub.Dispatch(context.Background(), Command{Op: OpWindowClick, JobID: job.ID, WindowID: "w1", X: f64(1), Y: f64(1)})
	if ErrorCode(err) != CodeJobBlocked {
		t.Fatalf("暂停应拦截，得到 %v (%s)", err, ErrorCode(err))
	}
	if _, err := jobs.Resume(context.Background(), job.ID); err != nil {
		t.Fatal(err)
	}
	_, err = hub.Dispatch(context.Background(), Command{Op: OpWindowClick, JobID: job.ID, WindowID: "w1", X: f64(1), Y: f64(1)})
	if ErrorCode(err) != CodeJobBlocked {
		t.Fatalf("恢复后未观察应拦截写操作，得到 %v (%s)", err, ErrorCode(err))
	}
	_ = png
	if _, err := hub.Dispatch(context.Background(), Command{Op: OpWindowScreenshot, JobID: job.ID, WindowID: "w1"}); err != nil {
		t.Fatalf("观察截图应成功：%v", err)
	}
	conn.respond = func(frame Frame) *Frame {
		return &Frame{Type: FrameResult, ID: frame.ID, Data: rawJSON(ActionResult{WindowID: "w1", Op: frame.Op, OK: true})}
	}
	if _, err := hub.Dispatch(context.Background(), Command{Op: OpWindowClick, JobID: job.ID, WindowID: "w1", X: f64(1), Y: f64(1)}); err != nil {
		t.Fatalf("重新观察后点击应成功：%v", err)
	}
}

func TestJobCancelStopsDispatch(t *testing.T) {
	jobs := NewJobManager(context.Background(), &memoryJobStore{})
	hub := NewHub(NewRegistry(context.Background(), &memoryStore{}))
	_, _ = hub.registry.SetPolicy(context.Background(), writePolicy())
	hub.SetJobManager(jobs)
	job, _ := jobs.Create(context.Background(), "x", "", "", JobBudget{MaxSteps: 5})
	_, _ = jobs.Start(context.Background(), job.ID)
	_, _ = jobs.Cancel(context.Background(), job.ID, "不要了")
	_, err := hub.Dispatch(context.Background(), Command{Op: OpWindowsList, JobID: job.ID})
	if ErrorCode(err) != CodeJobBlocked {
		t.Fatalf("取消后应拦截，得到 %v (%s)", err, ErrorCode(err))
	}
}

func TestJobIdempotencySkipsCompletedStep(t *testing.T) {
	jobs := NewJobManager(context.Background(), &memoryJobStore{})
	hub := NewHub(NewRegistry(context.Background(), &memoryStore{}))
	_, _ = hub.registry.SetPolicy(context.Background(), writePolicy())
	hub.SetJobManager(jobs)
	conn := &fakeConn{}
	c, _, _ := hub.Register(conn, Hello{ProtocolVersion: ProtocolVersion, HelperID: "m"}, TokenInfo{})
	var calls int
	conn.respond = func(frame Frame) *Frame {
		calls++
		return &Frame{Type: FrameResult, ID: frame.ID, Data: rawJSON(ActionResult{OK: true, Op: frame.Op, WindowID: "w1"})}
	}
	conn.deliver = c.HandleFrame
	c.HandleFrame(Frame{Type: FrameWindows, Data: rawJSON(WindowsPayload{Windows: []WindowInfo{{ID: "w1", AppName: "A", BundleID: "a", Active: true}}})})
	job, _ := jobs.Create(context.Background(), "x", "", "", JobBudget{MaxSteps: 10})
	_, _ = jobs.Start(context.Background(), job.ID)
	cmd := Command{Op: OpWindowType, JobID: job.ID, WindowID: "w1", Text: "hi", IdempotencyKey: "step-submit"}
	if _, err := hub.Dispatch(context.Background(), cmd); err != nil {
		t.Fatal(err)
	}
	if _, err := hub.Dispatch(context.Background(), cmd); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("相同 idempotency_key 不应再次下发，实际调用 %d", calls)
	}
	got, _ := jobs.Get(job.ID)
	if got.Budget.StepsUsed != 1 {
		t.Fatalf("步数应只计一次，得到 %d", got.Budget.StepsUsed)
	}
}

func TestJobBudgetExhaustion(t *testing.T) {
	jobs := NewJobManager(context.Background(), &memoryJobStore{})
	hub := NewHub(NewRegistry(context.Background(), &memoryStore{}))
	_, _ = hub.registry.SetPolicy(context.Background(), enabledPolicy())
	hub.SetJobManager(jobs)
	conn := &fakeConn{}
	c, _, _ := hub.Register(conn, Hello{ProtocolVersion: ProtocolVersion, HelperID: "m"}, TokenInfo{})
	conn.respond = func(frame Frame) *Frame {
		return &Frame{Type: FrameResult, ID: frame.ID, Data: rawJSON(WindowsPayload{})}
	}
	conn.deliver = c.HandleFrame
	job, _ := jobs.Create(context.Background(), "x", "", "", JobBudget{MaxSteps: 1})
	_, _ = jobs.Start(context.Background(), job.ID)
	if _, err := hub.Dispatch(context.Background(), Command{Op: OpWindowsList, JobID: job.ID}); err != nil {
		t.Fatal(err)
	}
	_, err := hub.Dispatch(context.Background(), Command{Op: OpWindowsList, JobID: job.ID})
	if ErrorCode(err) != CodeJobBudget {
		t.Fatalf("应耗尽预算，得到 %v (%s)", err, ErrorCode(err))
	}
}

func TestJobSurvivesRestartAndRequiresReobserve(t *testing.T) {
	store := &memoryJobStore{}
	jobs := NewJobManager(context.Background(), store)
	job, _ := jobs.Create(context.Background(), "goal", "o", "", JobBudget{MaxSteps: 8})
	_, _ = jobs.Start(context.Background(), job.ID)
	// 模拟运行中落盘
	jobs.mu.Lock()
	jobs.jobs[job.ID].Status = JobRunning
	jobs.jobs[job.ID].NeedsReobserve = false
	_ = jobs.persistLocked(context.Background())
	jobs.mu.Unlock()

	reloaded := NewJobManager(context.Background(), store)
	got, ok := reloaded.Get(job.ID)
	if !ok {
		t.Fatal("重启后应能读到任务")
	}
	if got.Status != JobPaused {
		t.Fatalf("运行中任务重启后应变 paused，得到 %s", got.Status)
	}
	if !got.NeedsReobserve {
		t.Fatal("重启后须重新观察")
	}
}

func TestWaitConfirmBlocksUntilConfirm(t *testing.T) {
	jobs := NewJobManager(context.Background(), &memoryJobStore{})
	hub := NewHub(NewRegistry(context.Background(), &memoryStore{}))
	_, _ = hub.registry.SetPolicy(context.Background(), writePolicy())
	hub.SetJobManager(jobs)
	conn := &fakeConn{}
	c, _, _ := hub.Register(conn, Hello{ProtocolVersion: ProtocolVersion, HelperID: "m"}, TokenInfo{})
	conn.respond = func(frame Frame) *Frame {
		return &Frame{Type: FrameResult, ID: frame.ID, Data: rawJSON(map[string]any{"ok": true})}
	}
	conn.deliver = c.HandleFrame
	c.HandleFrame(Frame{Type: FrameWindows, Data: rawJSON(WindowsPayload{Windows: []WindowInfo{{ID: "w1", AppName: "A", BundleID: "a", Active: true}}})})
	job, _ := jobs.Create(context.Background(), "x", "", "", JobBudget{MaxSteps: 10})
	_, _ = jobs.Start(context.Background(), job.ID)
	_, _ = jobs.WaitConfirm(context.Background(), job.ID, "确认提交？")
	_, err := hub.Dispatch(context.Background(), Command{Op: OpWindowClick, JobID: job.ID, WindowID: "w1", X: f64(1), Y: f64(1)})
	if ErrorCode(err) != CodeJobBlocked {
		t.Fatalf("等待确认应拦截，得到 %v", err)
	}
	_, _ = jobs.Confirm(context.Background(), job.ID)
	// still need reobserve
	_, err = hub.Dispatch(context.Background(), Command{Op: OpWindowClick, JobID: job.ID, WindowID: "w1", X: f64(1), Y: f64(1)})
	if ErrorCode(err) != CodeJobBlocked {
		t.Fatalf("确认后仍须观察，得到 %v", err)
	}
	_ = time.Second
	_ = json.RawMessage{}
}
