// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package desktopctl

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// 持久电脑任务状态。终态：succeeded / failed / cancelled。
const (
	JobQueued         = "queued"
	JobRunning        = "running"
	JobWaitingConfirm = "waiting_confirm"
	JobPaused         = "paused"
	JobSucceeded      = "succeeded"
	JobFailed         = "failed"
	JobCancelled      = "cancelled"
)

const (
	StepPending    = "pending"
	StepDispatched = "dispatched"
	StepCompleted  = "completed"
	StepFailed     = "failed"
	StepSkipped    = "skipped"
)

const (
	DefaultJobMaxSteps      = 32
	MaxJobMaxSteps          = 200
	DefaultJobMaxDurationMS = 30 * 60 * 1000
	MaxJobMaxDurationMS     = 24 * 60 * 60 * 1000
	CodeJobBlocked          = "job_blocked"
	CodeJobBudget           = "job_budget"
	CodeJobNotFound         = "job_not_found"
	CodeJobConflict         = "job_conflict"
)

// JobBudget 限制一步任务能跑多远。
type JobBudget struct {
	MaxSteps      int `json:"max_steps"`
	MaxDurationMS int `json:"max_duration_ms"`
	StepsUsed     int `json:"steps_used"`
}

// JobStep 是任务里已记录的一步。Completed 的步骤重启后不会再下发。
type JobStep struct {
	ID                string          `json:"id"`
	Op                string          `json:"op"`
	WindowID          string          `json:"window_id,omitempty"`
	Params            json.RawMessage `json:"params,omitempty"`
	Status            string          `json:"status"`
	ObservationBefore int64           `json:"observation_before,omitempty"`
	ObservationAfter  int64           `json:"observation_after,omitempty"`
	Result            json.RawMessage `json:"result,omitempty"`
	Error             string          `json:"error,omitempty"`
	IdempotencyKey    string          `json:"idempotency_key,omitempty"`
	CompletedAt       time.Time       `json:"completed_at,omitempty"`
}

// Job 是一条持久电脑任务。落盘后重启可接回；恢复时必须先重新观察，不盲目重放。
type Job struct {
	ID              string    `json:"id"`
	OwnerID         string    `json:"owner_id,omitempty"`
	Goal            string    `json:"goal,omitempty"`
	Status          string    `json:"status"`
	WaitReason      string    `json:"wait_reason,omitempty"`
	Budget          JobBudget `json:"budget"`
	Steps           []JobStep `json:"steps,omitempty"`
	LastObservation int64     `json:"last_observation,omitempty"`
	// NeedsReobserve 在 pause/等待确认后、或重启接回时置位：下一次写操作前必须先截图观察。
	NeedsReobserve bool      `json:"needs_reobserve,omitempty"`
	Connection     string    `json:"connection,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
	FinishedAt     time.Time `json:"finished_at,omitempty"`
	StartedAt      time.Time `json:"started_at,omitempty"`
	Error          string    `json:"error,omitempty"`
}

func (j Job) Terminal() bool {
	switch j.Status {
	case JobSucceeded, JobFailed, JobCancelled:
		return true
	default:
		return false
	}
}

func (j Job) clone() Job {
	out := j
	out.Steps = append([]JobStep(nil), j.Steps...)
	for i := range out.Steps {
		out.Steps[i].Params = append(json.RawMessage(nil), j.Steps[i].Params...)
		out.Steps[i].Result = append(json.RawMessage(nil), j.Steps[i].Result...)
	}
	return out
}

// JobStore 持久化电脑任务。为 nil 时 JobManager 只留在内存（测试可用）。
type JobStore interface {
	LoadDesktopJobs(ctx context.Context) ([]Job, error)
	SaveDesktopJobs(ctx context.Context, jobs []Job) error
}

// JobManager 保管持久任务，并在下发前核对状态与预算。
type JobManager struct {
	store   JobStore
	loadErr error
	now     func() time.Time

	mu   sync.Mutex
	jobs map[string]*Job
	// cancelFns 让 Cancel 能打断正在等待回执的下发。
	cancelFns   map[string]context.CancelFunc
	cancelSteps map[string]string
}

// NewJobManager 从存储加载任务。读失败时拒绝保存，避免覆盖未读取的任务。
func NewJobManager(ctx context.Context, store JobStore) *JobManager {
	m := &JobManager{
		store:       store,
		now:         time.Now,
		jobs:        map[string]*Job{},
		cancelFns:   map[string]context.CancelFunc{},
		cancelSteps: map[string]string{},
	}
	if store == nil {
		return m
	}
	jobs, err := store.LoadDesktopJobs(ctx)
	if err != nil {
		m.loadErr = err
		return m
	}
	for i := range jobs {
		job := jobs[i]
		job = job.clone()
		for i := range job.Steps {
			if job.Steps[i].Status == StepDispatched {
				job.Steps[i].Status = StepFailed
				job.Steps[i].Error = "重启时结果未确认，须重新观察；同一幂等键禁止重放"
			}
		}
		// 重启接回：非终态任务先标成 paused，并要求重新观察，避免盲目重放下一步。
		if !job.Terminal() && job.Status != JobQueued {
			if job.Status == JobRunning {
				job.Status = JobPaused
				job.WaitReason = "进程重启后接回，已暂停；恢复前须重新观察桌面"
			}
			job.NeedsReobserve = true
		}
		m.jobs[job.ID] = &job
	}
	_ = m.persistLocked(ctx)
	return m
}

func newJobID() string {
	raw := make([]byte, 8)
	_, _ = rand.Read(raw)
	return "dj-" + hex.EncodeToString(raw)
}

func newStepID() string {
	raw := make([]byte, 6)
	_, _ = rand.Read(raw)
	return "ds-" + hex.EncodeToString(raw)
}

func (b JobBudget) withDefaults() JobBudget {
	if b.MaxSteps <= 0 {
		b.MaxSteps = DefaultJobMaxSteps
	}
	if b.MaxSteps > MaxJobMaxSteps {
		b.MaxSteps = MaxJobMaxSteps
	}
	if b.MaxDurationMS <= 0 {
		b.MaxDurationMS = DefaultJobMaxDurationMS
	}
	if b.MaxDurationMS > MaxJobMaxDurationMS {
		b.MaxDurationMS = MaxJobMaxDurationMS
	}
	return b
}

// Create 创建一条排队中的任务。
func (m *JobManager) Create(ctx context.Context, goal, ownerID, connection string, budget JobBudget) (Job, error) {
	if m == nil {
		return Job{}, errors.New("desktop job manager 未初始化")
	}
	now := m.now()
	job := Job{
		ID:         newJobID(),
		OwnerID:    strings.TrimSpace(ownerID),
		Goal:       strings.TrimSpace(goal),
		Status:     JobQueued,
		Budget:     budget.withDefaults(),
		Connection: strings.TrimSpace(connection),
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.jobs[job.ID] = &job
	if err := m.persistLocked(ctx); err != nil {
		delete(m.jobs, job.ID)
		return Job{}, err
	}
	return job.clone(), nil
}

// Get 返回任务副本。
func (m *JobManager) Get(id string) (Job, bool) {
	if m == nil {
		return Job{}, false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	job, ok := m.jobs[strings.TrimSpace(id)]
	if !ok {
		return Job{}, false
	}
	return job.clone(), true
}

// List 按更新时间倒序返回任务。
func (m *JobManager) List(ownerID string) []Job {
	if m == nil {
		return nil
	}
	ownerID = strings.TrimSpace(ownerID)
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Job, 0, len(m.jobs))
	for _, job := range m.jobs {
		if ownerID != "" && job.OwnerID != "" && job.OwnerID != ownerID {
			continue
		}
		out = append(out, job.clone())
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].UpdatedAt.Equal(out[j].UpdatedAt) {
			return out[i].UpdatedAt.After(out[j].UpdatedAt)
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// Start 把 queued 任务标成 running。
func (m *JobManager) Start(ctx context.Context, id string) (Job, error) {
	return m.updateStatus(ctx, id, func(job *Job) error {
		if job.Status != JobQueued && job.Status != JobPaused {
			return commandError(CodeJobConflict, "任务 %s 当前是 %s，不能 Start", job.ID, job.Status)
		}
		job.Status = JobRunning
		job.WaitReason = ""
		if job.StartedAt.IsZero() {
			job.StartedAt = m.now()
		}
		return nil
	})
}

// Pause 暂停任务：之后不再下发。
func (m *JobManager) Pause(ctx context.Context, id, reason string) (Job, error) {
	return m.updateStatus(ctx, id, func(job *Job) error {
		if job.Status == JobWaitingConfirm {
			return commandError(CodeJobConflict, "等待确认的任务只能确认或取消")
		}
		if job.Terminal() {
			return commandError(CodeJobConflict, "任务 %s 已结束（%s）", job.ID, job.Status)
		}
		job.Status = JobPaused
		job.WaitReason = strings.TrimSpace(reason)
		job.NeedsReobserve = true
		m.abortInFlightLocked(job.ID)
		return nil
	})
}

// WaitConfirm 进入等待确认：不派发写操作，直到 Confirm。
func (m *JobManager) WaitConfirm(ctx context.Context, id, reason string) (Job, error) {
	return m.updateStatus(ctx, id, func(job *Job) error {
		if job.Terminal() {
			return commandError(CodeJobConflict, "任务 %s 已结束（%s）", job.ID, job.Status)
		}
		job.Status = JobWaitingConfirm
		job.WaitReason = strings.TrimSpace(firstNonEmpty(reason, "等待主人确认"))
		job.NeedsReobserve = true
		m.abortInFlightLocked(job.ID)
		return nil
	})
}

// Confirm 确认后回到 running，并要求重新观察。
func (m *JobManager) Confirm(ctx context.Context, id string) (Job, error) {
	return m.updateStatus(ctx, id, func(job *Job) error {
		if job.Status != JobWaitingConfirm {
			return commandError(CodeJobConflict, "任务 %s 不在等待确认（%s）", job.ID, job.Status)
		}
		job.Status = JobRunning
		job.WaitReason = ""
		job.NeedsReobserve = true
		return nil
	})
}

// Resume 从 paused 恢复；必须重新观察现场，不重放已完成步骤。
func (m *JobManager) Resume(ctx context.Context, id string) (Job, error) {
	return m.updateStatus(ctx, id, func(job *Job) error {
		if job.Status != JobPaused {
			return commandError(CodeJobConflict, "任务 %s 不在暂停（%s）", job.ID, job.Status)
		}
		job.Status = JobRunning
		job.WaitReason = ""
		job.NeedsReobserve = true
		return nil
	})
}

// Cancel 取消任务并停止后续下发；不宣称能撤销已发生的操作。
func (m *JobManager) Cancel(ctx context.Context, id, reason string) (Job, error) {
	return m.updateStatus(ctx, id, func(job *Job) error {
		if job.Terminal() {
			return commandError(CodeJobConflict, "任务 %s 已经是 %s", job.ID, job.Status)
		}
		job.Status = JobCancelled
		job.WaitReason = ""
		job.Error = strings.TrimSpace(firstNonEmpty(reason, "已取消"))
		job.FinishedAt = m.now()
		m.abortInFlightLocked(job.ID)
		return nil
	})
}

// Succeed / Fail 结束任务。
func (m *JobManager) Succeed(ctx context.Context, id, note string) (Job, error) {
	return m.updateStatus(ctx, id, func(job *Job) error {
		if job.Status != JobRunning {
			return commandError(CodeJobConflict, "只能完成执行中的任务")
		}
		for _, step := range job.Steps {
			if step.Status == StepDispatched {
				return commandError(CodeJobConflict, "仍有指令等待回执")
			}
		}
		if job.Terminal() {
			return commandError(CodeJobConflict, "任务 %s 已经是 %s", job.ID, job.Status)
		}
		job.Status = JobSucceeded
		job.WaitReason = ""
		if note != "" {
			job.Error = "" // clear
		}
		job.FinishedAt = m.now()
		m.abortInFlightLocked(job.ID)
		return nil
	})
}

func (m *JobManager) Fail(ctx context.Context, id, reason string) (Job, error) {
	return m.updateStatus(ctx, id, func(job *Job) error {
		if job.Terminal() {
			return commandError(CodeJobConflict, "任务 %s 已经是 %s", job.ID, job.Status)
		}
		job.Status = JobFailed
		job.Error = strings.TrimSpace(reason)
		job.FinishedAt = m.now()
		m.abortInFlightLocked(job.ID)
		return nil
	})
}

func (m *JobManager) updateStatus(ctx context.Context, id string, fn func(*Job) error) (Job, error) {
	if m == nil {
		return Job{}, errors.New("desktop job manager 未初始化")
	}
	id = strings.TrimSpace(id)
	m.mu.Lock()
	defer m.mu.Unlock()
	job, ok := m.jobs[id]
	if !ok {
		return Job{}, commandError(CodeJobNotFound, "桌面任务 %s 不存在", id)
	}
	before := job.clone()
	if err := fn(job); err != nil {
		return Job{}, err
	}
	job.UpdatedAt = m.now()
	if err := m.persistLocked(ctx); err != nil {
		*job = before
		return Job{}, err
	}
	return job.clone(), nil
}

func (m *JobManager) abortInFlightLocked(id string) {
	if cancel, ok := m.cancelFns[id]; ok {
		cancel()
		delete(m.cancelFns, id)
	}
}

// BindCancel 在下发期间登记可取消的 context。
func (m *JobManager) BindCancel(jobID, stepID string, cancel context.CancelFunc) {
	if m == nil || jobID == "" || cancel == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if job, ok := m.jobs[jobID]; !ok || job.Status != JobRunning {
		cancel()
		return
	}
	if old, ok := m.cancelFns[jobID]; ok {
		old()
	}
	m.cancelFns[jobID] = cancel
	m.cancelSteps[jobID] = stepID
}

// UnbindCancel 下发结束后解除。
func (m *JobManager) UnbindCancel(jobID, stepID string) {
	if m == nil || jobID == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cancelSteps[jobID] == stepID {
		delete(m.cancelFns, jobID)
		delete(m.cancelSteps, jobID)
	}
}

// AuthorizeDispatch 在真正下发前核对任务状态、预算与「须重新观察」。
// 对已完成且带相同 idempotency_key 的步骤直接返回已完成结果，避免重复提交。
func (m *JobManager) AuthorizeDispatch(ctx context.Context, cmd Command) (skip bool, cached Result, stepID string, err error) {
	if m == nil || strings.TrimSpace(cmd.JobID) == "" {
		return false, Result{}, "", nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	job, ok := m.jobs[strings.TrimSpace(cmd.JobID)]
	if !ok {
		return false, Result{}, "", commandError(CodeJobNotFound, "桌面任务 %s 不存在", cmd.JobID)
	}
	if job.Status == JobCancelled {
		return false, Result{}, "", commandError(CodeJobBlocked, "任务已取消，不再下发任何操作")
	}
	if job.Terminal() {
		return false, Result{}, "", commandError(CodeJobBlocked, "任务已结束（%s），不再下发", job.Status)
	}
	before := job.clone()
	switch job.Status {
	case JobPaused:
		return false, Result{}, "", commandError(CodeJobBlocked, "任务已暂停：%s", firstNonEmpty(job.WaitReason, "请先 resume"))
	case JobWaitingConfirm:
		return false, Result{}, "", commandError(CodeJobBlocked, "任务等待确认：%s", firstNonEmpty(job.WaitReason, "请先 confirm"))
	case JobQueued:
		job.Status = JobRunning
		if job.StartedAt.IsZero() {
			job.StartedAt = m.now()
		}
	case JobRunning:
	default:
		return false, Result{}, "", commandError(CodeJobBlocked, "任务状态 %s 不允许下发", job.Status)
	}
	if key := strings.TrimSpace(cmd.IdempotencyKey); key != "" {
		for _, step := range job.Steps {
			if step.IdempotencyKey != key {
				continue
			}
			if step.Status == StepCompleted {
				return true, Result{OK: true, Data: append(json.RawMessage(nil), step.Result...)}, step.ID, nil
			}
			return false, Result{}, "", commandError(CodeJobBlocked, "同一幂等键已有下发记录，结果未确认，不得重复执行")
		}
	}
	if err := m.checkBudgetLocked(job); err != nil {
		if saveErr := m.persistLocked(ctx); saveErr != nil {
			*job = before
			return false, Result{}, "", saveErr
		}
		return false, Result{}, "", err
	}
	if _, busy := m.cancelFns[job.ID]; busy {
		return false, Result{}, "", commandError(CodeJobBlocked, "上一条指令仍在结束处理中")
	}
	for _, step := range job.Steps {
		if step.Status == StepDispatched {
			return false, Result{}, "", commandError(CodeJobBlocked, "当前步骤尚未完成，请等待回执或核实现场")
		}
	}
	// 写操作在 NeedsReobserve 时必须先做一次截图观察。
	if IsWriteOp(cmd.Op) && job.NeedsReobserve {
		return false, Result{}, "", commandError(CodeJobBlocked,
			"恢复或确认后须先重新观察桌面：请先调用 window.screenshot（同一 job_id），再执行写操作")
	}
	if cmd.Op == OpWindowScreenshot {
		// 允许用截图清除重新观察标记（在 Record 里做）。
	}
	step := JobStep{
		ID:                newStepID(),
		Op:                cmd.Op,
		WindowID:          cmd.WindowID,
		Params:            rawJSON(cmd),
		Status:            StepDispatched,
		ObservationBefore: job.LastObservation,
		IdempotencyKey:    strings.TrimSpace(cmd.IdempotencyKey),
	}
	job.Steps = append(job.Steps, step)
	// 下发前在同一把锁下占用预算并落盘，失败回执也不退回已经尝试的步数。
	job.Budget.StepsUsed++
	job.UpdatedAt = m.now()
	if err := m.persistLocked(ctx); err != nil {
		*job = before
		return false, Result{}, "", fmt.Errorf("保存桌面任务下发记录失败: %w", err)
	}
	return false, Result{}, step.ID, nil
}

func (m *JobManager) checkBudgetLocked(job *Job) error {
	if job.Budget.StepsUsed >= job.Budget.MaxSteps {
		job.Status = JobFailed
		job.Error = fmt.Sprintf("已用完步数预算 %d", job.Budget.MaxSteps)
		job.FinishedAt = m.now()
		return commandError(CodeJobBudget, "%s", job.Error)
	}
	if !job.StartedAt.IsZero() {
		elapsed := m.now().Sub(job.StartedAt)
		if elapsed >= time.Duration(job.Budget.MaxDurationMS)*time.Millisecond {
			job.Status = JobFailed
			job.Error = fmt.Sprintf("已超过时长预算 %d ms", job.Budget.MaxDurationMS)
			job.FinishedAt = m.now()
			return commandError(CodeJobBudget, "%s", job.Error)
		}
	}
	return nil
}

// RecordDispatch 记下回执；截图成功会推进 observation 并清除 NeedsReobserve。
func (m *JobManager) RecordDispatch(ctx context.Context, jobID, stepID string, cmd Command, result Result, dispatchErr error) {
	if m == nil || strings.TrimSpace(jobID) == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	job, ok := m.jobs[strings.TrimSpace(jobID)]
	if !ok {
		return
	}
	for i := range job.Steps {
		if job.Steps[i].ID != stepID {
			continue
		}
		if dispatchErr != nil {
			job.Steps[i].Status = StepFailed
			job.Steps[i].Error = dispatchErr.Error()
		} else {
			job.Steps[i].Status = StepCompleted
			job.Steps[i].Result = append(json.RawMessage(nil), result.Data...)
			job.Steps[i].CompletedAt = m.now()
			if cmd.Op == OpWindowScreenshot {
				job.LastObservation++
				job.Steps[i].ObservationAfter = job.LastObservation
				job.NeedsReobserve = false
			} else if result.OK {
				job.Steps[i].ObservationAfter = job.LastObservation
			}
		}
		break
	}
	job.UpdatedAt = m.now()
	_ = m.persistLocked(context.Background())
}

func (m *JobManager) persistLocked(ctx context.Context) error {
	if m.loadErr != nil {
		return fmt.Errorf("读取持久任务失败，拒绝覆盖: %w", m.loadErr)
	}
	if m.store == nil {
		return nil
	}
	jobs := make([]Job, 0, len(m.jobs))
	for _, job := range m.jobs {
		jobs = append(jobs, job.clone())
	}
	sort.Slice(jobs, func(i, j int) bool { return jobs[i].ID < jobs[j].ID })
	return m.store.SaveDesktopJobs(ctx, jobs)
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
