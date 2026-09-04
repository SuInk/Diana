// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	dianaThreadStateToolName       = "diana.thread_state"
	defaultThreadStateTTL          = 30 * time.Minute
	minimumThreadStateTTL          = time.Minute
	maximumThreadStateTTL          = 24 * time.Hour
	maximumThreadStatePayloadBytes = 8 * 1024
	maximumActiveThreadStates      = 4
	privateThreadStateMarker       = "【临时线程状态，仅用于完成当前多轮任务；scope=user 仅属于当前发言者，scope=session 由当前会话参与者共享；不得复述、泄露或当作长期记忆；当前消息与任务无关时不要使用或提及】"
)

var ErrThreadStateVersionConflict = errors.New("thread state version conflict")

type ThreadStateScope string

const (
	ThreadStateScopeUser    ThreadStateScope = "user"
	ThreadStateScopeSession ThreadStateScope = "session"
)

type ThreadStateStatus string

const (
	ThreadStateActive    ThreadStateStatus = "active"
	ThreadStateCompleted ThreadStateStatus = "completed"
	ThreadStateCancelled ThreadStateStatus = "cancelled"
	ThreadStateExpired   ThreadStateStatus = "expired"
)

// ThreadState 是 Diana 自己创建、只在一个多轮任务内有效的临时状态。
// user 作用域用于私有猜谜、计划和表单；session 作用域用于多人棋局等共同任务。
// 它不是用户画像或长期记忆，任务结束后必须清理。
type ThreadState struct {
	ID              string            `json:"id"`
	ProfileID       string            `json:"profile_id,omitempty"`
	Session         string            `json:"session"`
	UserID          string            `json:"user_id"`
	Scope           ThreadStateScope  `json:"scope"`
	TaskKind        string            `json:"task_kind"`
	State           json.RawMessage   `json:"state"`
	Version         int               `json:"version"`
	Status          ThreadStateStatus `json:"status"`
	SourceMessageID string            `json:"source_message_id,omitempty"`
	CreatedAt       time.Time         `json:"created_at"`
	UpdatedAt       time.Time         `json:"updated_at"`
	ExpiresAt       time.Time         `json:"expires_at"`
}

type ThreadStatePutRequest struct {
	ProfileID       string
	Session         string
	UserID          string
	Scope           ThreadStateScope
	TaskKind        string
	State           json.RawMessage
	ExpectedVersion int
	SourceMessageID string
	Now             time.Time
	ExpiresAt       time.Time
}

type ThreadStateEndRequest struct {
	ProfileID       string
	Session         string
	UserID          string
	Scope           ThreadStateScope
	TaskKind        string
	ExpectedVersion int
	Status          ThreadStateStatus
	Now             time.Time
}

type ThreadStateStore interface {
	PutThreadState(context.Context, ThreadStatePutRequest) (ThreadState, error)
	ListActiveThreadStates(ctx context.Context, profileID, session, userID string, now time.Time, limit int) ([]ThreadState, error)
	EndThreadState(context.Context, ThreadStateEndRequest) (ThreadState, error)
}

func (r *Runtime) SetThreadStateStore(store ThreadStateStore) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.threadStates = store
}

func (r *Runtime) threadStateStore() ThreadStateStore {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.threadStates
}

func (r *Runtime) privateThreadStateContext(ctx context.Context, event MessageEvent) string {
	text, _ := r.privateThreadStateContextDetailed(ctx, event)
	return text
}

// privateThreadStateContextDetailed 同时返回真正注入的状态条目，供管理员事件审计
// 展示。本地状态仍不会进入公开回复或普通操作日志。
func (r *Runtime) privateThreadStateContextDetailed(ctx context.Context, event MessageEvent) (string, []ThreadState) {
	store := r.threadStateStore()
	if store == nil || strings.TrimSpace(event.UserID) == "" {
		return "", nil
	}
	loadCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	items, err := store.ListActiveThreadStates(loadCtx, strings.TrimSpace(event.ProfileID), sessionKey(event), strings.TrimSpace(event.UserID), r.clock(), maximumActiveThreadStates)
	if err != nil {
		return "", nil
	}
	if len(items) == 0 {
		return "", nil
	}
	type privateState struct {
		ID        string           `json:"id"`
		Scope     ThreadStateScope `json:"scope"`
		TaskKind  string           `json:"task_kind"`
		State     json.RawMessage  `json:"state"`
		Version   int              `json:"version"`
		ExpiresAt string           `json:"expires_at"`
	}
	payload := make([]privateState, 0, len(items))
	for _, item := range items {
		payload = append(payload, privateState{
			ID:        item.ID,
			Scope:     item.Scope,
			TaskKind:  item.TaskKind,
			State:     append(json.RawMessage(nil), item.State...),
			Version:   item.Version,
			ExpiresAt: item.ExpiresAt.Format(time.RFC3339),
		})
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", nil
	}
	return privateThreadStateMarker + "\n" + string(encoded), append([]ThreadState(nil), items...)
}

type dianaThreadStateTool struct {
	runtime *Runtime
	event   MessageEvent
}

func newDianaThreadStateTool(runtime *Runtime, event MessageEvent) *dianaThreadStateTool {
	return &dianaThreadStateTool{runtime: runtime, event: event}
}

func (*dianaThreadStateTool) Name() string { return dianaThreadStateToolName }

func (*dianaThreadStateTool) Description() string {
	return "保存、读取和结束 Diana 自己创建的多轮任务状态。默认 scope=user，只属于当前发言者；多人棋局、共同计划等需要群内参与者共享同一 canonical 状态时必须用 scope=session。更新时必须携带 expected_version，完成或取消时及时清理，不得用长期记忆代替。"
}

func (*dianaThreadStateTool) InputSchema() map[string]any {
	return toolObjectSchema([]string{"operation", "task_kind"}, map[string]any{
		"operation": toolEnumParam("操作：set 创建或更新；get 读取；complete 正常结束；cancel 取消。", "set", "get", "complete", "cancel"),
		"task_kind": toolStringParam("通用任务类型标识，使用小写字母、数字、点、横线或下划线，例如 guess.character、form.onboarding。不要把具体答案写进 task_kind。"),
		"scope":     toolEnumParam("状态作用域：user 仅当前发言者可见（默认）；session 供当前私聊或群会话共享，适用于多人棋局和共同任务，不得存放任何参与者的秘密。", string(ThreadStateScopeUser), string(ThreadStateScopeSession)),
		"state": map[string]any{
			"type":                 "object",
			"description":          "set 时必填的结构化状态。保存完成任务所需的 canonical target、约束和进度；session 作用域不得放秘密；最多 8 KiB。",
			"additionalProperties": true,
		},
		"expected_version": toolIntParam("更新或结束时可传当前版本，避免并发覆盖；首次创建和不做并发校验时省略。", 1, 1_000_000),
		"ttl_seconds":      toolIntParam("set 后闲置有效期，默认 1800 秒，范围 60 到 86400 秒。", int(minimumThreadStateTTL/time.Second), int(maximumThreadStateTTL/time.Second)),
	})
}

func (t *dianaThreadStateTool) Run(ctx context.Context, input map[string]any) (string, error) {
	if t == nil || t.runtime == nil || t.runtime.threadStateStore() == nil {
		return "", fmt.Errorf("diana thread state: store is not configured")
	}
	userID := strings.TrimSpace(t.event.UserID)
	if userID == "" {
		return "", fmt.Errorf("无法识别当前发言者，不能操作临时线程状态")
	}
	taskKind, err := normalizeThreadStateTaskKind(configToolString(input, "task_kind"))
	if err != nil {
		return "", err
	}
	operation := strings.ToLower(strings.TrimSpace(configToolString(input, "operation")))
	scope, err := normalizeThreadStateScope(configToolString(input, "scope"))
	if err != nil {
		return "", err
	}
	stateUserID := threadStateScopeUserID(scope, userID)
	expectedVersion := threadStateInputInt(input, "expected_version")
	now := t.runtime.clock()
	store := t.runtime.threadStateStore()
	switch operation {
	case "set":
		stateValue, ok := input["state"]
		if !ok || stateValue == nil {
			return "", fmt.Errorf("set 必须提供 state")
		}
		state, err := json.Marshal(stateValue)
		if err != nil {
			return "", fmt.Errorf("编码私有状态: %w", err)
		}
		if len(state) == 0 || len(state) > maximumThreadStatePayloadBytes {
			return "", fmt.Errorf("state 大小必须在 1 到 %d 字节之间", maximumThreadStatePayloadBytes)
		}
		ttl := time.Duration(threadStateInputInt(input, "ttl_seconds")) * time.Second
		if ttl == 0 {
			ttl = defaultThreadStateTTL
		}
		if ttl < minimumThreadStateTTL || ttl > maximumThreadStateTTL {
			return "", fmt.Errorf("ttl_seconds 必须在 %d 到 %d 之间", int(minimumThreadStateTTL/time.Second), int(maximumThreadStateTTL/time.Second))
		}
		item, err := store.PutThreadState(ctx, ThreadStatePutRequest{
			ProfileID:       strings.TrimSpace(t.event.ProfileID),
			Session:         sessionKey(t.event),
			UserID:          stateUserID,
			Scope:           scope,
			TaskKind:        taskKind,
			State:           state,
			ExpectedVersion: expectedVersion,
			SourceMessageID: strings.TrimSpace(t.event.MessageID),
			Now:             now,
			ExpiresAt:       now.Add(ttl),
		})
		if err != nil {
			return "", err
		}
		return marshalThreadStateToolResult("set", []ThreadState{item})
	case "get":
		items, err := store.ListActiveThreadStates(ctx, strings.TrimSpace(t.event.ProfileID), sessionKey(t.event), userID, now, maximumActiveThreadStates)
		if err != nil {
			return "", err
		}
		filtered := items[:0]
		for _, item := range items {
			if item.TaskKind == taskKind && item.Scope == scope {
				filtered = append(filtered, item)
			}
		}
		return marshalThreadStateToolResult("get", filtered)
	case "complete", "cancel":
		status := ThreadStateCompleted
		if operation == "cancel" {
			status = ThreadStateCancelled
		}
		item, err := store.EndThreadState(ctx, ThreadStateEndRequest{
			ProfileID:       strings.TrimSpace(t.event.ProfileID),
			Session:         sessionKey(t.event),
			UserID:          stateUserID,
			Scope:           scope,
			TaskKind:        taskKind,
			ExpectedVersion: expectedVersion,
			Status:          status,
			Now:             now,
		})
		if err != nil {
			return "", err
		}
		return marshalThreadStateToolResult(operation, []ThreadState{item})
	default:
		return "", fmt.Errorf("不支持的 operation %q", operation)
	}
}

func normalizeThreadStateScope(value string) (ThreadStateScope, error) {
	scope := ThreadStateScope(strings.ToLower(strings.TrimSpace(value)))
	if scope == "" {
		return ThreadStateScopeUser, nil
	}
	if scope != ThreadStateScopeUser && scope != ThreadStateScopeSession {
		return "", fmt.Errorf("scope 只能是 user 或 session")
	}
	return scope, nil
}

func threadStateScopeUserID(scope ThreadStateScope, userID string) string {
	if scope == ThreadStateScopeSession {
		return ""
	}
	return strings.TrimSpace(userID)
}

func normalizeThreadStateTaskKind(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" || len(value) > 64 {
		return "", fmt.Errorf("task_kind 长度必须在 1 到 64 之间")
	}
	for _, char := range value {
		if char >= 'a' && char <= 'z' || char >= '0' && char <= '9' || char == '.' || char == '-' || char == '_' {
			continue
		}
		return "", fmt.Errorf("task_kind 只能包含小写字母、数字、点、横线和下划线")
	}
	return value, nil
}

func threadStateInputInt(input map[string]any, key string) int {
	switch value := input[key].(type) {
	case int:
		return value
	case int64:
		return int(value)
	case float64:
		return int(value)
	case json.Number:
		parsed, _ := value.Int64()
		return int(parsed)
	}
	return 0
}

func marshalThreadStateToolResult(operation string, items []ThreadState) (string, error) {
	type view struct {
		ID        string            `json:"id"`
		Scope     ThreadStateScope  `json:"scope"`
		TaskKind  string            `json:"task_kind"`
		State     json.RawMessage   `json:"state,omitempty"`
		Version   int               `json:"version"`
		Status    ThreadStateStatus `json:"status"`
		ExpiresAt string            `json:"expires_at,omitempty"`
	}
	result := struct {
		OK        bool   `json:"ok"`
		Operation string `json:"operation"`
		Items     []view `json:"items"`
	}{OK: true, Operation: operation, Items: make([]view, 0, len(items))}
	for _, item := range items {
		state := append(json.RawMessage(nil), item.State...)
		if item.Status != ThreadStateActive {
			state = nil
		}
		result.Items = append(result.Items, view{
			ID: item.ID, Scope: item.Scope, TaskKind: item.TaskKind, State: state, Version: item.Version,
			Status: item.Status, ExpiresAt: item.ExpiresAt.Format(time.RFC3339),
		})
	}
	encoded, err := json.Marshal(result)
	return string(encoded), err
}
