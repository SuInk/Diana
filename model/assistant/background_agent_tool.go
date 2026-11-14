// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
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
		`status 查某个任务的轮次、各轮记录和结果（主人问「进度怎样」「查到哪了」时用），记录长时按 start_line 续读，不填 id 列出本会话全部；cancel 停止。`+
		`start 之后先简短告诉主人已放到后台和任务号，不要在这一轮里自己再做一遍。`, backgroundAgentMaxRounds)
}

func (t *dianaBackgroundTaskTool) InputSchema() map[string]any {
	return toolObjectSchema([]string{"operation"}, map[string]any{
		"operation":  toolEnumParam("操作。", "start", "status", "cancel"),
		"goal":       toolStringParam("start：完整的任务目标，写清要查什么、交付什么、有什么限制；后台运行时看不到当前对话"),
		"max_rounds": toolIntParam(fmt.Sprintf("start：最多几轮，默认 %d", backgroundAgentMaxRounds), 1, backgroundAgentMaxRounds),
		"id":         toolStringParam("status/cancel：任务号"),
		"start_line": toolIntParam("status 带 id 时续读记录：从第几行开始，传上次返回的 next_line", 1, 100000000),
	})
}

func (t *dianaBackgroundTaskTool) Run(ctx context.Context, input map[string]any) (string, error) {
	switch inputString(input, "operation") {
	case "start":
		id, err := t.runtime.startBackgroundAgent(ctx, t.event, inputString(input, "goal"), chatHistoryCount(input, "max_rounds", backgroundAgentMaxRounds), t.run)
		if err != nil {
			return "", err
		}
		return marshalBackgroundTool(map[string]any{
			"id":     id,
			"status": "started",
			"next":   "告诉主人已放到后台、任务号是什么；每轮进度和最终结果会自动发到这个会话",
		})
	case "status":
		return t.status(ctx, strings.TrimSpace(inputString(input, "id")), intFromAny(input["start_line"]))
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

// status 照 Pi 的做法给完整记录：带 id 时目标、各轮记录和结果合成一份文本按整行
// 分页，放不下的写明用 start_line 续读；不带 id 时列出本会话全部任务和各自最后一轮
// 记录，放不下就去掉记录、写明带 id 单独看。以前记录和结果都截到 600 字、目标截到
// 120 字，结论的后半截谁也看不到。
func (t *dianaBackgroundTaskTool) status(ctx context.Context, id string, startLine int) (string, error) {
	states := t.runtime.backgroundAgentsFor(t.event)
	budget := toolResultBudget(ctx)
	if id == "" {
		items := make([]map[string]any, 0, len(states))
		for _, state := range states {
			item := backgroundAgentStatusItem(state)
			if len(state.Notes) > 0 {
				item["last_note"] = state.Notes[len(state.Notes)-1]
			}
			items = append(items, item)
		}
		body, err := marshalBackgroundTool(map[string]any{"tasks": items})
		if err != nil || utf8.RuneCountInString(body) <= budget {
			return body, err
		}
		for _, item := range items {
			delete(item, "last_note")
		}
		return marshalBackgroundTool(map[string]any{"tasks": items, "next": "各任务的最后一轮记录合起来超出单次上限，这里没有列出；带 id 调 status 单独看完整记录"})
	}
	for _, state := range states {
		if state.ID == id {
			return backgroundAgentStatusPage(state, max(startLine, 1), budget)
		}
	}
	return "", fmt.Errorf("这个会话里没有任务 %s（结束超过一小时的不再保留）", id)
}

func backgroundAgentStatusItem(state backgroundAgentState) map[string]any {
	return map[string]any{
		"id":         state.ID,
		"goal":       state.Goal,
		"phase":      state.Phase,
		"round":      state.Round,
		"max_rounds": state.MaxRounds,
		"elapsed":    formatCodingDuration(time.Since(state.StartedAt)),
	}
}

// backgroundAgentStatusPage 把单个任务的目标、各轮记录和结果合成一份文本，从
// startLine 起按整行装进 budget。
func backgroundAgentStatusPage(state backgroundAgentState, startLine, budget int) (string, error) {
	var builder strings.Builder
	builder.WriteString("目标：" + state.Goal)
	for index, note := range state.Notes {
		fmt.Fprintf(&builder, "\n第 %d 轮记录：%s", index+1, note)
	}
	if state.Result != "" {
		builder.WriteString("\n结果：" + state.Result)
	}
	detail := builder.String()
	reserve := 400
	for {
		item := backgroundAgentStatusItem(state)
		delete(item, "goal")
		window := takeTextLines(detail, startLine, max(0, budget-reserve), jsonStringRunes)
		if window.Start > window.Total {
			return "", fmt.Errorf("任务 %s 的记录共 %d 行，start_line=%d 超出范围", state.ID, window.Total, window.Start)
		}
		item["detail"] = window.Text
		item["total_lines"] = window.Total
		if !window.Whole() {
			item["next"] = "detail 给出了" + window.Range() + "。"
		}
		if !window.Complete() {
			item["next_line"] = window.ResumeLine()
			item["next"] = item["next"].(string) + fmt.Sprintf("还没读完：传 operation=status、id=%s、start_line=%d 接着读。", state.ID, window.ResumeLine())
		}
		body, err := marshalBackgroundTool(map[string]any{"tasks": []map[string]any{item}})
		if err != nil {
			return "", err
		}
		over := utf8.RuneCountInString(body) - budget
		if over <= 0 || reserve >= budget {
			return body, nil
		}
		reserve += over
	}
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
