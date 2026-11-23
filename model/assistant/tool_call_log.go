// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"log"
	"sort"
	"strings"
	"time"

	"github.com/SuInk/diana/model/agent"
)

// 工具调用不跨轮（续接前缀只留 send，见 append_context.go），模型下一轮看不到自己
// 上一轮调过什么。群友追问「刚才那张图是按什么画的」「你搜的什么」，或者模型自己要
// 接着改上一张图时，只能凭回复正文猜。所以每次实际执行的工具调用都按会话落库，
// 由 tasks 工具按需查：参数和成败照实给，结果不存——要现在的数据就重新调。

// AgentToolCallRecord 是一次实际执行过的工具调用。
type AgentToolCallRecord struct {
	ID        int64  `json:"id"`
	Session   string `json:"-"`
	ProfileID string `json:"-"`
	// EventID 是触发这一轮的入站事件，MessageID 是它对应的平台消息。
	EventID   string `json:"-"`
	MessageID string `json:"message_id,omitempty"`
	UserID    string `json:"user_id,omitempty"`
	Tool      string `json:"tool"`
	// Input 是脱敏后的参数 JSON，和调试追踪里记的同一份。
	Input      string    `json:"input,omitempty"`
	Error      string    `json:"error,omitempty"`
	DurationMS int64     `json:"duration_ms"`
	CreatedAt  time.Time `json:"created_at"`
}

// AgentToolCallQuery 按会话倒序翻页查工具调用；BeforeID 是上一页最后一条的 ID。
type AgentToolCallQuery struct {
	Session   string
	ProfileID string
	Tool      string
	BeforeID  int64
	Limit     int
}

// AgentToolCallStore 是可选接口：没实现的存储照常工作，只是 tasks 查不到工具调用。
type AgentToolCallStore interface {
	RecordAgentToolCall(ctx context.Context, record AgentToolCallRecord) error
	ListAgentToolCalls(ctx context.Context, query AgentToolCallQuery) ([]AgentToolCallRecord, error)
}

// SessionSubtaskQuery 按会话倒序翻页查后台任务；BeforeSeq 是上一页最后一条的 Seq。
type SessionSubtaskQuery struct {
	Session   string
	ProfileID string
	BeforeSeq int64
	Limit     int
}

// SessionSubtask 是会话里一个后台任务的落库记录。
type SessionSubtask struct {
	InboundEventSubtask
	// Seq 是翻页游标，MessageID 是触发它的那条消息。
	Seq       int64  `json:"seq"`
	MessageID string `json:"message_id,omitempty"`
}

// SessionSubtaskStore 是可选接口，同 AgentToolCallStore。
type SessionSubtaskStore interface {
	ListSessionSubtasks(ctx context.Context, query SessionSubtaskQuery) ([]SessionSubtask, error)
}

// toolCallLogSkipped 不落库：send、tool_search、tools_execute 不是「做了什么」，
// codemode 里每个子调用各自记一条，外面这层脚本不再记。
func toolCallLogSkipped(tool string) bool {
	return tool == "" || toolCallMemoryIgnored[tool] || tool == agent.CodeModeToolName
}

func (r *Runtime) agentToolCallStore() AgentToolCallStore {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	store, _ := r.messageStore.(AgentToolCallStore)
	return store
}

// recordAgentToolCall 记下一次执行完的工具调用。input 已经过和调试追踪相同的脱敏。
// 失败只记日志，不影响这一轮回复。
func (r *Runtime) recordAgentToolCall(ctx context.Context, event MessageEvent, runEvent agent.RunEvent, input map[string]any, runError string) {
	if runEvent.Phase != agent.RunPhaseToolCompleted || toolCallLogSkipped(strings.TrimSpace(runEvent.Tool)) {
		return
	}
	store := r.agentToolCallStore()
	if store == nil {
		return
	}
	record := AgentToolCallRecord{
		Session:    sessionKey(event),
		ProfileID:  strings.TrimSpace(event.ProfileID),
		MessageID:  strings.TrimSpace(event.MessageID),
		UserID:     strings.TrimSpace(event.UserID),
		Tool:       strings.TrimSpace(runEvent.Tool),
		Error:      strings.TrimSpace(runError),
		DurationMS: runEvent.DurationMS,
		CreatedAt:  time.Now(),
	}
	if turn := outboundTurnFromContext(ctx); turn != nil {
		record.EventID = turn.id
	}
	if len(input) > 0 {
		if raw, err := json.Marshal(input); err == nil {
			record.Input = string(raw)
		}
	}
	writeCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := store.RecordAgentToolCall(writeCtx, record); err != nil {
		log.Printf("diana tool call record failed: %v", err)
	}
}

// listBackground 列当前会话的后台任务。第一页先放运行时里还在排队、运行的（排队中的
// 还没落库），再接库里的记录；同一个任务以运行时的阶段为准。
func (t *dianaTasksTool) listBackground(ctx context.Context, before int64, limit int) ([]dianaBackgroundTaskItem, int64, error) {
	active := map[string]SubagentTaskStatus{}
	var activeOrder []string
	if before == 0 {
		prefix := sessionKey(t.event) + "\x00"
		t.runtime.subagentMu.Lock()
		for key, task := range t.runtime.subagentTasks {
			if strings.HasPrefix(key, prefix) {
				active[task.status.ID] = task.status
				activeOrder = append(activeOrder, task.status.ID)
			}
		}
		t.runtime.subagentMu.Unlock()
		sort.SliceStable(activeOrder, func(i, j int) bool {
			return active[activeOrder[i]].StartedAt.After(active[activeOrder[j]].StartedAt)
		})
	}
	var stored []SessionSubtask
	t.runtime.mu.RLock()
	store, _ := t.runtime.messageStore.(SessionSubtaskStore)
	t.runtime.mu.RUnlock()
	if store != nil {
		var err error
		stored, err = store.ListSessionSubtasks(ctx, SessionSubtaskQuery{Session: sessionKey(t.event), ProfileID: strings.TrimSpace(t.event.ProfileID), BeforeSeq: before, Limit: limit + 1})
		if err != nil {
			return nil, 0, err
		}
	}
	var next int64
	if len(stored) > limit {
		stored = stored[:limit]
		next = stored[len(stored)-1].Seq
	}
	items := make([]dianaBackgroundTaskItem, 0, len(stored)+len(activeOrder))
	seen := map[string]bool{}
	for _, record := range stored {
		item := dianaBackgroundTaskItem{
			ID: record.TaskID, Kind: record.Kind, Name: record.Name, Request: record.Request, Phase: record.Phase,
			Completed: record.Completed, Total: record.Total, Detail: record.Detail, Error: record.Error,
			MessageID: record.MessageID, StartedAt: record.StartedAt, FinishedAt: record.FinishedAt,
		}
		if status, ok := active[record.TaskID]; ok {
			item.Active, item.Phase = true, status.Phase
			item.Completed, item.Total = status.Completed, status.Total
			seen[record.TaskID] = true
		}
		items = append(items, item)
	}
	queued := make([]dianaBackgroundTaskItem, 0, len(activeOrder))
	for _, id := range activeOrder {
		if seen[id] {
			continue
		}
		status := active[id]
		queued = append(queued, dianaBackgroundTaskItem{
			ID: status.ID, Kind: status.Kind, Name: status.Name, Request: status.Request, Phase: status.Phase, Active: true,
			Completed: status.Completed, Total: status.Total, StartedAt: status.StartedAt,
		})
	}
	return append(queued, items...), next, nil
}

// listToolCalls 列当前会话之前执行过的工具调用。当前发言者用不了的工具（主人专属的
// 命令、文件写入、配置）只给名字和成败，参数不给看。
func (t *dianaTasksTool) listToolCalls(ctx context.Context, tool string, before int64, limit int) ([]dianaToolCallItem, int64, error) {
	store := t.runtime.agentToolCallStore()
	if store == nil {
		return nil, 0, nil
	}
	records, err := store.ListAgentToolCalls(ctx, AgentToolCallQuery{Session: sessionKey(t.event), ProfileID: strings.TrimSpace(t.event.ProfileID), Tool: tool, BeforeID: before, Limit: limit + 1})
	if err != nil {
		return nil, 0, err
	}
	var next int64
	if len(records) > limit {
		records = records[:limit]
		next = records[len(records)-1].ID
	}
	allowed := t.runtime.relationshipPolicy(ctx, t.event).toolScope().allowedAgentToolNames()
	items := make([]dianaToolCallItem, 0, len(records))
	for _, record := range records {
		item := dianaToolCallItem{
			ID: record.ID, Tool: record.Tool, Failed: record.Error != "", Error: record.Error,
			MessageID: record.MessageID, UserID: record.UserID, DurationMS: record.DurationMS, CreatedAt: record.CreatedAt,
		}
		switch {
		case allowed != nil && !allowed[record.Tool]:
			item.InputHidden, item.Error = true, ""
		case record.Input != "" && json.Valid([]byte(record.Input)):
			item.Input = json.RawMessage(record.Input)
		}
		items = append(items, item)
	}
	return items, next, nil
}
