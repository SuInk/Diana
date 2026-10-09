// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const dianaBackgroundTaskToolName = "background_task"

type dianaBackgroundTaskTool struct {
	runtime *Runtime
	event   MessageEvent
	// run 留给测试替换每一轮的执行。
	run backgroundRoundRunner
}

func newDianaBackgroundTaskTool(runtime *Runtime, event MessageEvent) *dianaBackgroundTaskTool {
	return &dianaBackgroundTaskTool{runtime: runtime, event: event}
}

func (t *dianaBackgroundTaskTool) Name() string { return dianaBackgroundTaskToolName }

func (t *dianaBackgroundTaskTool) Description() string {
	return fmt.Sprintf(`把需要多轮查证或较长时间的任务放到后台分轮执行，仅主人可用。适合深度调研、多来源交叉核对、批量处理文件等一轮回复做不完的活。`+
		`start 开始，后台最多 %d 轮、每轮一次完整工具调用，每轮结束把进度发到当前会话，完成后发结果；`+
		`status 查某个任务的轮次、各轮记录和结果（主人问「进度怎样」「查到哪了」时用），不填 id 列出本会话全部；cancel 停止。`+
		`start 之后先简短告诉主人已放到后台和任务号，不要在这一轮里自己再做一遍。`, backgroundAgentMaxRounds)
}

func (t *dianaBackgroundTaskTool) InputSchema() map[string]any {
	return toolObjectSchema([]string{"operation"}, map[string]any{
		"operation":  toolEnumParam("操作。", "start", "status", "cancel"),
		"goal":       toolStringParam("start：完整的任务目标，写清要查什么、交付什么、有什么限制；后台运行时看不到当前对话"),
		"max_rounds": toolIntParam(fmt.Sprintf("start：最多几轮，默认 %d", backgroundAgentMaxRounds), 1, backgroundAgentMaxRounds),
		"id":         toolStringParam("status/cancel：任务号"),
	})
}

func (t *dianaBackgroundTaskTool) Run(ctx context.Context, input map[string]any) (string, error) {
	switch inputString(input, "operation") {
	case "start":
		id, err := t.runtime.startBackgroundAgent(ctx, t.event, inputString(input, "goal"), chatHistoryBoundedInt(input, "max_rounds", backgroundAgentMaxRounds, backgroundAgentMaxRounds), t.run)
		if err != nil {
			return "", err
		}
		return marshalBackgroundTool(map[string]any{
			"id":     id,
			"status": "started",
			"next":   "告诉主人已放到后台、任务号是什么；每轮进度和最终结果会自动发到这个会话",
		})
	case "status":
		return t.status(strings.TrimSpace(inputString(input, "id")))
	case "cancel":
		id := strings.TrimSpace(inputString(input, "id"))
		if id == "" {
			return "", fmt.Errorf("cancel 需要 id")
		}
		if !t.runtime.cancelSessionSubagentTask(t.event, id) {
			return "", fmt.Errorf("这个会话里没有在跑的任务 %s", id)
		}
		// 排队中被直接摘掉的任务不会再跑到收尾，这里补上状态。
		t.runtime.markBackgroundAgentCancelled(t.event, id)
		return marshalBackgroundTool(map[string]any{"id": id, "status": "cancelling", "next": "停下后会把已有进度发到会话"})
	}
	return "", fmt.Errorf("operation 必须是 start、status 或 cancel")
}

func (t *dianaBackgroundTaskTool) status(id string) (string, error) {
	states := t.runtime.backgroundAgentsFor(t.event)
	items := make([]map[string]any, 0, len(states))
	for _, state := range states {
		if id != "" && state.ID != id {
			continue
		}
		item := map[string]any{
			"id":         state.ID,
			"goal":       truncateRunes(state.Goal, 120),
			"phase":      state.Phase,
			"round":      state.Round,
			"max_rounds": state.MaxRounds,
			"elapsed":    formatCodingDuration(time.Since(state.StartedAt)),
		}
		if id != "" {
			item["notes"] = state.Notes
			if state.Result != "" {
				item["result"] = state.Result
			}
		} else if len(state.Notes) > 0 {
			item["last_note"] = state.Notes[len(state.Notes)-1]
		}
		items = append(items, item)
	}
	if id != "" && len(items) == 0 {
		return "", fmt.Errorf("这个会话里没有任务 %s（结束超过一小时的不再保留）", id)
	}
	return marshalBackgroundTool(map[string]any{"tasks": items})
}

func (r *Runtime) markBackgroundAgentCancelled(event MessageEvent, id string) {
	r.subagentMu.Lock()
	defer r.subagentMu.Unlock()
	if state, ok := r.backgroundAgents[id]; ok && state.Session == sessionKey(event) && state.Phase == "queued" {
		state.Phase = "cancelled"
		state.UpdatedAt = time.Now()
	}
}

func marshalBackgroundTool(payload map[string]any) (string, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return string(body), nil
}
