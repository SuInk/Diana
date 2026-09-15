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
	// threadStateLockedKeysField 是 state 里记录「锁定字段」的保留键。锁定的字段在任务结束
	// 前不能被改写：线上一局群聊猜谜，谜底存进了发起者的个人范围，别人来问时读不到，
	// 模型就重新出题并一路改写谜底，前后答案自相矛盾。
	threadStateLockedKeysField = "_locked_keys"
	privateThreadStateMarker   = "【临时线程状态，仅用于完成当前多轮任务；scope=user 仅属于当前发言者，scope=session 由当前会话参与者共享；不得复述、泄露或当作长期记忆；当前消息与任务无关时不要使用或提及】"
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
	return "保存、读取和结束 Diana 自己创建的多轮任务状态。scope=user 只属于当前发言者，其他人的轮次读不到；群聊里发起、其他群友可能接着提问或参与的任务（猜谜、棋局、共同计划）必须用 scope=session。你自己出的谜底属于任务状态，放 session，并用 locked_keys 锁住，本局内不能再改。get 不传 task_kind 时列出当前会话和当前发言者全部进行中的状态；回答前先 get，读不到已锁定的谜底时不要重新出题。更新时必须携带 expected_version，完成或取消时及时清理，不得用长期记忆代替。"
}

func (*dianaThreadStateTool) InputSchema() map[string]any {
	return toolObjectSchema([]string{"operation"}, map[string]any{
		"operation":   toolEnumParam("操作：set 创建或更新；get 读取；complete 正常结束；cancel 取消。", "set", "get", "complete", "cancel"),
		"task_kind":   toolStringParam("通用任务类型标识，使用小写字母、数字、点、横线或下划线，例如 guess.character、form.onboarding。set、complete、cancel 必填；get 不传时列出全部进行中的状态。不要把具体答案写进 task_kind。"),
		"scope":       toolEnumParam("状态作用域：user 仅当前发言者可见（默认），只用于私聊或明确只和一个人进行的任务；session 供当前会话所有人共享，群聊里其他人可能接着参与的任务和你自己出的谜底都放这里。session 不得存放某个参与者自己提供、不该让别人知道的秘密。get 不传 scope 时不按作用域过滤。", string(ThreadStateScopeUser), string(ThreadStateScopeSession)),
		"locked_keys": toolStringArrayParam("set 可选：state 里需要锁定的顶层字段名，例如谜底字段。锁定后本任务结束前再 set 时这些字段不能改，也不会被省略掉；只能追加锁定，不能解锁。要换题必须先 complete 或 cancel。"),
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
	operation := strings.ToLower(strings.TrimSpace(configToolString(input, "operation")))
	rawTaskKind := strings.TrimSpace(configToolString(input, "task_kind"))
	rawScope := strings.TrimSpace(configToolString(input, "scope"))
	taskKind := ""
	if operation != "get" || rawTaskKind != "" {
		normalized, err := normalizeThreadStateTaskKind(rawTaskKind)
		if err != nil {
			return "", err
		}
		taskKind = normalized
	}
	scope, err := normalizeThreadStateScope(rawScope)
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
		existing, err := t.activeThreadState(ctx, scope, taskKind, userID, now)
		if err != nil {
			return "", err
		}
		lockedKeys, _, code, message := repositoryIssueStringList(input, "locked_keys", 20)
		if code != "" {
			return "", fmt.Errorf("locked_keys 格式不对：%s", message)
		}
		state, err := applyThreadStateLocks(stateValue, existing, lockedKeys)
		if err != nil {
			return "", err
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
		// 不传 task_kind 或 scope 时不按它过滤。以前两者都必须精确命中，模型记不清之前
		// 存成了什么类型，只能一个个猜，猜不中就当作没有、重新出题。
		filtered := items[:0]
		for _, item := range items {
			if (taskKind == "" || item.TaskKind == taskKind) && (rawScope == "" || item.Scope == scope) {
				filtered = append(filtered, item)
			}
		}
		return marshalThreadStateToolResult("get", filtered)
	case "complete", "cancel":
		status := ThreadStateCompleted
		if operation == "cancel" {
			status = ThreadStateCancelled
		}
		// 存储层结束时会清空状态；先把结束前的最终状态读出来放进结果，事后能在调用链里核对。
		final, err := t.activeThreadState(ctx, scope, taskKind, userID, now)
		if err != nil {
			return "", err
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
		if final != nil && len(item.State) == 0 {
			item.State = append(json.RawMessage(nil), final.State...)
		}
		return marshalThreadStateToolResult(operation, []ThreadState{item})
	default:
		return "", fmt.Errorf("不支持的 operation %q", operation)
	}
}

// activeThreadState 读出当前发言者可见、作用域和类型都对得上的那条进行中状态。
func (t *dianaThreadStateTool) activeThreadState(ctx context.Context, scope ThreadStateScope, taskKind, userID string, now time.Time) (*ThreadState, error) {
	items, err := t.runtime.threadStateStore().ListActiveThreadStates(ctx, strings.TrimSpace(t.event.ProfileID), sessionKey(t.event), userID, now, maximumActiveThreadStates)
	if err != nil {
		return nil, err
	}
	for index := range items {
		if items[index].Scope == scope && items[index].TaskKind == taskKind {
			return &items[index], nil
		}
	}
	return nil, nil
}

// applyThreadStateLocks 把锁定规则套到这次 set 上：已锁定的字段不能改，省略了就沿用原值；
// 锁定只增不减。返回编码后的 state。
func applyThreadStateLocks(stateValue any, existing *ThreadState, requested []string) (json.RawMessage, error) {
	encoded, err := json.Marshal(stateValue)
	if err != nil {
		return nil, fmt.Errorf("编码私有状态: %w", err)
	}
	var next map[string]any
	if err := json.Unmarshal(encoded, &next); err != nil || next == nil {
		if len(requested) > 0 || existing != nil && len(threadStateLockedKeys(existing.State)) > 0 {
			return nil, fmt.Errorf("使用 locked_keys 时 state 必须是对象")
		}
		return encoded, nil
	}
	var previous map[string]any
	locked := []string{}
	if existing != nil {
		_ = json.Unmarshal(existing.State, &previous)
		locked = threadStateLockedKeys(existing.State)
	}
	seen := map[string]bool{}
	for _, key := range locked {
		seen[key] = true
	}
	for _, key := range locked {
		oldValue, hadValue := previous[key]
		newValue, hasValue := next[key]
		if !hasValue {
			if hadValue {
				next[key] = oldValue
			}
			continue
		}
		oldJSON, _ := json.Marshal(oldValue)
		newJSON, _ := json.Marshal(newValue)
		if hadValue && string(oldJSON) != string(newJSON) {
			return nil, fmt.Errorf("字段 %s 已锁定，本任务内不能修改（当前值保持不变）；如果确实要换，先 complete 或 cancel 结束这一局再重新 set", key)
		}
	}
	for _, key := range requested {
		key = strings.TrimSpace(key)
		if key == "" || key == threadStateLockedKeysField || seen[key] {
			continue
		}
		if _, ok := next[key]; !ok {
			return nil, fmt.Errorf("locked_keys 里的 %s 不在 state 里", key)
		}
		seen[key] = true
		locked = append(locked, key)
	}
	if len(locked) > 0 {
		next[threadStateLockedKeysField] = locked
	} else {
		delete(next, threadStateLockedKeysField)
	}
	return json.Marshal(next)
}

func threadStateLockedKeys(state json.RawMessage) []string {
	var payload struct {
		Locked []string `json:"_locked_keys"`
	}
	if json.Unmarshal(state, &payload) != nil {
		return nil
	}
	return payload.Locked
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
		ID       string           `json:"id"`
		Scope    ThreadStateScope `json:"scope"`
		TaskKind string           `json:"task_kind"`
		State    json.RawMessage  `json:"state,omitempty"`
		// FinalState 只在 complete/cancel 的结果里出现：结束前最后一版状态，供事后核对。
		FinalState json.RawMessage   `json:"final_state,omitempty"`
		Version    int               `json:"version"`
		Status     ThreadStateStatus `json:"status"`
		ExpiresAt  string            `json:"expires_at,omitempty"`
	}
	result := struct {
		OK        bool   `json:"ok"`
		Operation string `json:"operation"`
		Items     []view `json:"items"`
	}{OK: true, Operation: operation, Items: make([]view, 0, len(items))}
	for _, item := range items {
		state := append(json.RawMessage(nil), item.State...)
		var final json.RawMessage
		if item.Status != ThreadStateActive {
			final = state
			state = nil
		}
		result.Items = append(result.Items, view{
			ID: item.ID, Scope: item.Scope, TaskKind: item.TaskKind, State: state, FinalState: final, Version: item.Version,
			Status: item.Status, ExpiresAt: item.ExpiresAt.Format(time.RFC3339),
		})
	}
	encoded, err := json.Marshal(result)
	return string(encoded), err
}
