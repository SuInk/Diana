// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/SuInk/diana/model/agent"
	"github.com/google/uuid"
)

const dianaRoomToolName = "room"

type RoomIntent struct {
	Pose      string    `json:"pose"`
	UpdatedAt time.Time `json:"updated_at"`
}

type RoomStore interface {
	LoadRoomIntent(context.Context, string) (RoomIntent, error)
	SaveRoomIntent(context.Context, string, RoomIntent) error
}

// RoomState separates a persistent scenic choice from transient reply execution.
// It never includes conversation content, tool arguments or platform account IDs.
type RoomState struct {
	ProfileID   string     `json:"profile_id"`
	Epoch       string     `json:"epoch"`
	Revision    uint64     `json:"revision"`
	Enabled     bool       `json:"enabled"`
	Pose        string     `json:"pose"`
	Source      string     `json:"source"`
	Phase       string     `json:"phase"`
	ActiveTasks int        `json:"active_tasks"`
	TaskID      string     `json:"task_id,omitempty"`
	ExpiresAt   *time.Time `json:"expires_at,omitempty"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

type roomTask struct {
	id      string
	order   uint64
	phase   string
	tools   int
	expires time.Time
}

type roomProfile struct {
	intent         RoomIntent
	intentRevision uint64
	revision       uint64
	updated        time.Time
	phase          string
	tasks          map[string]*roomTask
}

type roomTracker struct {
	mu       sync.Mutex
	store    RoomStore
	renderer RoomSnapshotRenderer
	epoch    string
	profiles map[string]*roomProfile
}

type roomRunKey struct{}
type roomRunRef struct{ profile, id string }

func ValidRoomPose(pose string) bool { return pose == "rest" || pose == "phone" || pose == "desk" }

func (r *Runtime) SetRoomStore(store RoomStore) {
	r.rooms.mu.Lock()
	defer r.rooms.mu.Unlock()
	r.rooms.store = store
}

func (p *roomProfile) change(now time.Time) { p.revision++; p.updated = now }

// The room lock serializes persistence with live snapshots so a late write cannot
// restore an earlier intent. The store operation has a short independent timeout.
func (r *Runtime) loadRoomLocked(ctx context.Context, profile string) (*roomProfile, error) {
	if p := r.rooms.profiles[profile]; p != nil {
		return p, nil
	}
	intent := RoomIntent{Pose: "rest"}
	if r.rooms.store != nil {
		loadCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		var err error
		intent, err = r.rooms.store.LoadRoomIntent(loadCtx, profile)
		if err != nil {
			return nil, err
		}
	}
	if !ValidRoomPose(intent.Pose) {
		intent.Pose = "rest"
	}
	if r.rooms.profiles == nil {
		r.rooms.profiles = make(map[string]*roomProfile)
		r.rooms.epoch = uuid.NewString()
	}
	p := &roomProfile{intent: intent, revision: 1, phase: "idle", updated: r.clock(), tasks: make(map[string]*roomTask)}
	if !intent.UpdatedAt.IsZero() {
		p.intentRevision = 1
	}
	r.rooms.profiles[profile] = p
	return p, nil
}

func (p *roomProfile) expire(now time.Time) {
	for id, task := range p.tasks {
		if !now.Before(task.expires) {
			delete(p.tasks, id)
			p.phase = "expired"
			p.change(now)
		}
	}
}

func (r *Runtime) roomSnapshotLocked(profile string, p *roomProfile, enabled bool) RoomState {
	now := r.clock()
	p.expire(now)
	if !enabled && len(p.tasks) > 0 {
		clear(p.tasks)
		p.phase = "disabled"
		p.change(now)
	}
	state := RoomState{ProfileID: profile, Epoch: r.rooms.epoch, Revision: p.revision, Enabled: enabled,
		Pose: p.intent.Pose, Source: "idle", Phase: p.phase, ActiveTasks: len(p.tasks), UpdatedAt: p.updated}
	if p.intentRevision > 0 {
		state.Source = "agent"
	}
	var latest *roomTask
	for _, task := range p.tasks {
		if latest == nil || task.order > latest.order {
			latest = task
		}
	}
	if latest != nil {
		state.TaskID, state.Phase = latest.id, latest.phase
		expires := latest.expires
		state.ExpiresAt = &expires
		if p.intentRevision < latest.order {
			state.Pose, state.Source = "desk", "runtime"
		}
	}
	if !enabled {
		state.Pose, state.Phase, state.Source = "rest", "disabled", "idle"
	}
	return state
}

func (r *Runtime) RoomSnapshot(ctx context.Context, profile string) (RoomState, error) {
	profile = strings.TrimSpace(profile)
	if profile == "" {
		return RoomState{}, fmt.Errorf("room requires a robot profile")
	}
	cfg := r.ProfileConfig(profile)
	if cfg.ID != profile {
		return RoomState{}, fmt.Errorf("unknown room profile")
	}
	enabled := OwnSpaceEnabled(cfg)
	r.rooms.mu.Lock()
	defer r.rooms.mu.Unlock()
	p, err := r.loadRoomLocked(ctx, profile)
	if err != nil {
		return RoomState{}, err
	}
	return r.roomSnapshotLocked(profile, p, enabled), nil
}

func (r *Runtime) roomSnapshots() []RoomState {
	// Never acquire the runtime config lock while holding the room lock.
	configs := r.ProfileConfigs()
	enabled := make(map[string]bool, len(configs))
	for _, cfg := range configs {
		enabled[cfg.ID] = OwnSpaceEnabled(cfg)
	}
	r.rooms.mu.Lock()
	defer r.rooms.mu.Unlock()
	states := make([]RoomState, 0, len(r.rooms.profiles))
	for id, p := range r.rooms.profiles {
		if _, exists := enabled[id]; !exists {
			delete(r.rooms.profiles, id)
			continue
		}
		states = append(states, r.roomSnapshotLocked(id, p, enabled[id]))
	}
	sort.Slice(states, func(i, j int) bool { return states[i].ProfileID < states[j].ProfileID })
	return states
}

func (r *Runtime) beginRoomReply(ctx context.Context, cfg BotConfig, event MessageEvent) (context.Context, func(error)) {
	if !OwnSpaceEnabled(cfg) || strings.TrimSpace(event.ProfileID) == "" || ctx.Err() != nil {
		return ctx, func(error) {}
	}
	r.rooms.mu.Lock()
	p, err := r.loadRoomLocked(ctx, event.ProfileID)
	if err != nil {
		r.rooms.mu.Unlock()
		return ctx, func(error) {}
	}
	now := r.clock()
	p.expire(now)
	deadline := now.Add(30 * time.Minute)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	id := uuid.NewString()
	p.change(now)
	p.tasks[id] = &roomTask{id: id, order: p.revision, phase: "thinking", expires: deadline}
	r.rooms.mu.Unlock()
	ctx = context.WithValue(ctx, roomRunKey{}, roomRunRef{profile: event.ProfileID, id: id})
	return ctx, func(runErr error) {
		r.rooms.mu.Lock()
		defer r.rooms.mu.Unlock()
		p.expire(r.clock())
		if p.tasks[id] == nil {
			return
		}
		delete(p.tasks, id)
		p.phase = "completed"
		if runErr != nil && !errors.Is(runErr, errModelSilentFinish) {
			p.phase = "error"
		}
		if ctx.Err() != nil || errors.Is(runErr, context.Canceled) || errors.Is(runErr, context.DeadlineExceeded) {
			p.phase = "cancelled"
		}
		p.change(r.clock())
	}
}

func (r *Runtime) observeRoomRun(ctx context.Context, event agent.RunEvent) {
	ref, ok := ctx.Value(roomRunKey{}).(roomRunRef)
	if !ok {
		return
	}
	r.rooms.mu.Lock()
	defer r.rooms.mu.Unlock()
	p := r.rooms.profiles[ref.profile]
	if p == nil {
		return
	}
	p.expire(r.clock())
	task := p.tasks[ref.id]
	if task == nil {
		return
	}
	switch event.Phase {
	case agent.RunPhaseToolStarted:
		task.tools++
	case agent.RunPhaseToolCompleted:
		if task.tools > 0 {
			task.tools--
		}
	default:
		return
	}
	task.phase = "thinking"
	if task.tools > 0 {
		task.phase = "tool"
	}
	p.change(r.clock())
}

type dianaRoomTool struct {
	runtime *Runtime
	event   MessageEvent
}

func (*dianaRoomTool) Name() string { return dianaRoomToolName }
func (*dianaRoomTool) Description() string {
	return "查看或选择你自己小窝里的活动。get 查看当前状态供你回答；set 选择 rest 休息、phone 看手机、desk 去书桌；report 向当前会话发送状态文字；snapshot 向当前会话发送默认房间状态图。" +
		"只在想改变房间活动或用户问到小窝时使用，不必每次回复都调用。动作会保存，前端负责连续过渡。" +
		"report/snapshot 仅在用户明确要求发状态或看房间画面时使用，工具负责发送，不要重复发送。snapshot 是服务端默认插画合成，不是摄像头、电脑截图或用户浏览器实拍，不包含浏览器本地自定义。普通询问用 get 后自然回复。房间动作不操作真实电脑或花钱。"
}
func (*dianaRoomTool) InputSchema() map[string]any {
	return toolObjectSchema([]string{"operation"}, map[string]any{
		"operation": toolEnumParam("get 查看；set 选择活动；report 发状态；snapshot 发默认房间状态图", "get", "set", "report", "snapshot"),
		"pose":      toolEnumParam("set 必填，只选择当前房间支持的活动", "rest", "phone", "desk"),
	})
}
func (t *dianaRoomTool) Run(ctx context.Context, input map[string]any) (string, error) {
	if t == nil || t.runtime == nil || strings.TrimSpace(t.event.ProfileID) == "" {
		return "", fmt.Errorf("room requires a robot profile")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	r := t.runtime
	if r.ProfileConfig(t.event.ProfileID).ID != t.event.ProfileID {
		return "", fmt.Errorf("unknown room profile")
	}
	if !OwnSpaceEnabled(r.effectiveConfigForEvent(t.event)) {
		return "", fmt.Errorf("这台机器人没有开启小窝")
	}
	for key := range input {
		if key != "operation" && key != "pose" {
			return "", fmt.Errorf("room 不接受参数 %s", key)
		}
	}
	op, _ := input["operation"].(string)
	pose, _ := input["pose"].(string)
	if op != "get" && op != "set" && op != "report" && op != "snapshot" {
		return "", fmt.Errorf("operation 只能为 get、set、report 或 snapshot")
	}
	if (op == "set" || pose != "") && !ValidRoomPose(pose) {
		return "", fmt.Errorf("pose 只能为 rest、phone 或 desk")
	}
	if op != "set" && pose != "" {
		return "", fmt.Errorf("pose 只用于 set；截图和状态不能伪造姿势")
	}
	state, err := t.state(ctx, op, pose)
	if err != nil {
		return "", err
	}
	if op == "snapshot" || op == "report" {
		return t.sendState(ctx, op, state)
	}
	result, err := json.Marshal(map[string]any{"ok": true, "room": state,
		"notice": "虚拟房间状态；客户端播放进度未确认，没有执行真实电脑、发消息或支付操作。"})
	return string(result), err
}

func (t *dianaRoomTool) state(ctx context.Context, op, pose string) (RoomState, error) {
	r := t.runtime
	r.rooms.mu.Lock()
	defer r.rooms.mu.Unlock()
	p, err := r.loadRoomLocked(ctx, t.event.ProfileID)
	if err != nil {
		return RoomState{}, err
	}
	p.expire(r.clock())
	if ref, ok := ctx.Value(roomRunKey{}).(roomRunRef); ok && (ref.profile != t.event.ProfileID || p.tasks[ref.id] == nil) {
		return RoomState{}, fmt.Errorf("本次房间活动所属任务已结束或过期")
	}
	if op == "set" {
		if r.rooms.store == nil {
			return RoomState{}, fmt.Errorf("房间存储尚未配置，未改变活动")
		}
		intent := RoomIntent{Pose: pose, UpdatedAt: r.clock()}
		writeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		if err := r.rooms.store.SaveRoomIntent(writeCtx, t.event.ProfileID, intent); err != nil {
			return RoomState{}, err
		}
		p.intent = intent
		p.change(intent.UpdatedAt)
		p.intentRevision = p.revision
	}
	return r.roomSnapshotLocked(t.event.ProfileID, p, true), nil
}
