// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package desktopctl

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
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

// jobStep runs one command through the job's dispatch gate and records a
// successful result whose scene digest is scene.
func jobStep(t *testing.T, jobs *JobManager, cmd Command, scene string) error {
	t.Helper()
	_, _, stepID, err := jobs.AuthorizeDispatch(context.Background(), cmd)
	if err != nil {
		return err
	}
	data := rawJSON(ScreenshotPayload{WindowID: cmd.WindowID, Data: scene, Observation: 1})
	if IsWriteOp(cmd.Op) {
		data = rawJSON(ActionResult{OK: true, Op: cmd.Op, WindowID: cmd.WindowID, Evidence: &ScreenshotPayload{Data: scene}, Observation: 1})
	}
	jobs.RecordDispatch(context.Background(), cmd.JobID, stepID, cmd, Result{OK: true, Data: data}, nil)
	return nil
}

func runningJob(t *testing.T, budget JobBudget) (*JobManager, Job) {
	t.Helper()
	jobs := NewJobManager(context.Background(), &memoryJobStore{})
	job, err := jobs.Create(context.Background(), "x", "", "", budget)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := jobs.Start(context.Background(), job.ID); err != nil {
		t.Fatal(err)
	}
	return jobs, job
}

// 任务不设总步数和总时长上限：旧记录里存的预算也不再生效，有进展就一直跑。
func TestJobHasNoStepOrDurationCap(t *testing.T) {
	jobs, job := runningJob(t, JobBudget{MaxSteps: 1, MaxDurationMS: 1})
	start := time.Now()
	jobs.now = func() time.Time { return start }
	for i := 0; i < 300; i++ {
		jobs.now = func() time.Time { return start.Add(time.Duration(i) * time.Hour) }
		x := float64(i)
		if err := jobStep(t, jobs, Command{Op: OpWindowClick, JobID: job.ID, WindowID: "w", X: &x, Y: &x}, fmt.Sprintf("scene-%d", i)); err != nil {
			t.Fatalf("step %d blocked: %v", i, err)
		}
	}
	got, _ := jobs.Get(job.ID)
	if got.Status != JobRunning || got.Budget.StepsUsed != 300 {
		t.Fatalf("job = %s steps=%d", got.Status, got.Budget.StepsUsed)
	}
}

// 同一画面下第三次做同一操作就暂停等主人确认；确认后重新观察，计数从头开始。
func TestJobLoopGuardPausesRepeatedActionOnSameScene(t *testing.T) {
	jobs, job := runningJob(t, JobBudget{})
	x, y := 10.0, 20.0
	click := Command{Op: OpWindowClick, JobID: job.ID, WindowID: "w", X: &x, Y: &y}
	if err := jobStep(t, jobs, Command{Op: OpWindowScreenshot, JobID: job.ID, WindowID: "w"}, "same"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		click.Observation = int64(100 + i) // 观察版本每次都变，不能让指纹跟着变
		if err := jobStep(t, jobs, click, "same"); err != nil {
			t.Fatalf("click %d: %v", i, err)
		}
	}
	err := jobStep(t, jobs, click, "same")
	if ErrorCode(err) != CodeJobBlocked || !strings.Contains(err.Error(), "原地打转") {
		t.Fatalf("third identical click should be stopped, got %v", err)
	}
	got, _ := jobs.Get(job.ID)
	if got.Status != JobWaitingConfirm || !strings.Contains(got.WaitReason, "第 3 次") || !got.NeedsReobserve {
		t.Fatalf("job = %+v", got)
	}
	// 换个地方点不受影响的前提是主人先确认继续。
	if _, err := jobs.Confirm(context.Background(), job.ID); err != nil {
		t.Fatal(err)
	}
	if err := jobStep(t, jobs, Command{Op: OpWindowScreenshot, JobID: job.ID, WindowID: "w"}, "same"); err != nil {
		t.Fatal(err)
	}
	if err := jobStep(t, jobs, click, "same"); err != nil {
		t.Fatalf("after the owner confirmed, counting restarts: %v", err)
	}
}

// 光标闪烁让每张截图的哈希都不同，靠不看画面的兜底规则认出重复操作。
func TestJobLoopGuardCatchesRepeatsDespiteChangingScenes(t *testing.T) {
	jobs, job := runningJob(t, JobBudget{})
	typeHello := Command{Op: OpWindowType, JobID: job.ID, WindowID: "w", Text: "hello"}
	for i := 0; i < sameTextLimit-1; i++ {
		if err := jobStep(t, jobs, typeHello, fmt.Sprintf("caret-%d", i)); err != nil {
			t.Fatalf("type %d: %v", i, err)
		}
	}
	if err := jobStep(t, jobs, typeHello, "caret-x"); ErrorCode(err) != CodeJobBlocked {
		t.Fatalf("repeated typing should be stopped, got %v", err)
	}
}

// 连点「下一页」这类参数相同但画面一直在变的操作是正常进展，不能被当成打转。
func TestJobLoopGuardAllowsRepeatedActionThatMakesProgress(t *testing.T) {
	jobs, job := runningJob(t, JobBudget{})
	x, y := 300.0, 500.0
	next := Command{Op: OpWindowClick, JobID: job.ID, WindowID: "w", X: &x, Y: &y}
	scroll := Command{Op: OpWindowScroll, JobID: job.ID, WindowID: "w", X: &x, Y: &y, DeltaY: 400}
	for i := 0; i < 40; i++ {
		cmd := next
		if i%2 == 1 {
			cmd = scroll
		}
		if err := jobStep(t, jobs, cmd, fmt.Sprintf("page-%d", i)); err != nil {
			t.Fatalf("step %d blocked though the scene keeps changing: %v", i, err)
		}
	}
}

// 一直截图不动手同样算卡住。
func TestJobLoopGuardPausesEndlessObservation(t *testing.T) {
	jobs, job := runningJob(t, JobBudget{})
	shot := Command{Op: OpWindowScreenshot, JobID: job.ID, WindowID: "w"}
	for i := 0; i < observeStreakLimit; i++ {
		if err := jobStep(t, jobs, shot, fmt.Sprintf("scene-%d", i)); err != nil {
			t.Fatalf("observation %d: %v", i, err)
		}
	}
	if err := jobStep(t, jobs, shot, "scene-x"); ErrorCode(err) != CodeJobBlocked || !strings.Contains(err.Error(), "卡住") {
		t.Fatalf("endless observation should be stopped, got %v", err)
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
