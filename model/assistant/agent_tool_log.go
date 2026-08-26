// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/SuInk/diana/model/agent"
	"github.com/SuInk/diana/model/applog"
)

func (r *Runtime) agentRunObserver(event MessageEvent) agent.RunObserver {
	return func(ctx context.Context, runEvent agent.RunEvent) {
		writer := r.appLogWriter()
		if writer == nil {
			return
		}
		runError := runEvent.Error
		if runError != "" {
			switch runEvent.Tool {
			case dianaOneBotV11ToolName:
				runError = "[OneBot v11 tool error omitted]"
			case dianaRepositoryIssuesToolName:
				runError = "[repository issue tool error omitted]"
			}
		}
		kind := applog.KindOperation
		level := applog.LevelInfo
		action := "chatbot.agent_run"
		message := "Agent 运行状态已更新"
		switch runEvent.Phase {
		case agent.RunPhaseStarted:
			message = "Agent 运行开始"
		case agent.RunPhaseModelCompleted:
			action = "chatbot.agent_model"
			message = "Agent 模型轮次完成"
		case agent.RunPhaseProtocolRepair:
			action = "chatbot.agent_protocol"
			message = "Agent 协议已自动修正"
		case agent.RunPhaseToolStarted:
			action = "chatbot.agent_tool"
			message = "Agent 工具调用开始"
		case agent.RunPhaseToolCompleted:
			action = "chatbot.agent_tool"
			message = "Agent 工具调用完成"
			if runError != "" {
				kind = applog.KindError
				level = applog.LevelError
				message = "Agent 工具调用失败"
			}
		case agent.RunPhaseCompleted:
			message = "Agent 运行完成"
		case agent.RunPhaseFailed:
			kind = applog.KindError
			level = applog.LevelError
			message = "Agent 运行失败"
		}
		progressBar, progressLabel, progressCurrent, progressTotal, progressPercent := formatAgentProgress(runEvent)
		message = message + " " + progressBar + " " + progressLabel
		target := strings.TrimSpace(event.MessageID)
		if strings.TrimSpace(runEvent.Tool) != "" {
			target = runEvent.Tool
		}
		metadata := map[string]any{
			"trace_id":         runEvent.TraceID,
			"phase":            runEvent.Phase,
			"group_id":         event.GroupID,
			"user_id":          event.UserID,
			"message_id":       event.MessageID,
			"model_turn":       runEvent.ModelTurn,
			"tool_call":        runEvent.ToolCall,
			"max_tool_calls":   runEvent.MaxToolCalls,
			"tool":             runEvent.Tool,
			"input_keys":       runEvent.InputKeys,
			"output_chars":     runEvent.OutputChars,
			"duration_ms":      runEvent.DurationMS,
			"finish_reason":    runEvent.FinishReason,
			"input_tokens":     runEvent.Usage.InputTokens,
			"output_tokens":    runEvent.Usage.OutputTokens,
			"total_tokens":     runEvent.Usage.TotalTokens,
			"progress_bar":     progressBar,
			"progress_current": progressCurrent,
			"progress_total":   progressTotal,
			"progress_percent": progressPercent,
		}
		if len(runEvent.Metadata) > 0 {
			metadata["tool_metadata"] = runEvent.Metadata
		}
		actor := oneBotEventActor(event)
		if runEvent.Tool == agent.WebSearchToolName {
			// Search operation logs remain useful without retaining the person or
			// conversation that produced a potentially private query. Raw queries
			// are available only through the explicitly enabled debug trace.
			actor = ""
			delete(metadata, "group_id")
			delete(metadata, "user_id")
			delete(metadata, "message_id")
		}
		logCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = writer.AppendLog(logCtx, applog.Entry{
			Kind:     kind,
			Level:    level,
			Action:   action,
			Message:  message,
			Detail:   runError,
			Actor:    actor,
			Target:   target,
			Metadata: metadata,
		})
		if runEvent.Phase != agent.RunPhaseModelCompleted {
			toolOutput := runEvent.ToolOutput
			toolInput := runEvent.ToolInput
			if runEvent.Tool == dianaOneBotV11ToolName {
				toolInput, toolOutput = sanitizeOneBotV11DebugToolCall(toolInput, toolOutput)
			} else if runEvent.Tool == dianaRepositoryIssuesToolName {
				toolInput, toolOutput = sanitizeRepositoryIssuesDebugToolCall(toolInput, toolOutput)
			}
			// 报错文本要补在脱敏之后：上面那层挡的是工具载荷，而错误信息不是载荷，
			// 挡掉它只会让追踪里少一行、多一句「输出已省略」。
			if runError != "" && strings.TrimSpace(toolOutput) == "" {
				toolOutput = "ERROR: " + runError
			}
			r.recordDebugTrace(debugTraceFromContext(ctx), "Agent 调用链更新", map[string]any{
				"phase":           "agent_" + string(runEvent.Phase),
				"trace_id":        runEvent.TraceID,
				"model_turn":      runEvent.ModelTurn,
				"tool_call":       runEvent.ToolCall,
				"max_tool_calls":  runEvent.MaxToolCalls,
				"tool":            runEvent.Tool,
				"tool_input":      toolInput,
				"tool_output":     toolOutput,
				"available_tools": runEvent.AvailableTools,
				"duration_ms":     runEvent.DurationMS,
				"error":           runError,
				"finish_reason":   runEvent.FinishReason,
				"usage":           runEvent.Usage,
			})
		}
		log.Printf("chatbot agent progress: trace=%s %s %s phase=%s model_turn=%d tool=%s duration_ms=%d",
			runEvent.TraceID, progressBar, progressLabel, runEvent.Phase, runEvent.ModelTurn, runEvent.Tool, runEvent.DurationMS)
	}
}

func sanitizeOneBotV11DebugToolCall(input map[string]any, output string) (map[string]any, string) {
	action := strings.TrimSpace(configToolString(input, "action"))
	params, _ := input["params"].(map[string]any)
	redactedInput := map[string]any{
		"action":     action,
		"param_keys": sortedMapKeys(params),
	}
	// 还没有输出就别说「输出已省略」。调用开始和调用完成是同一次调用的两条记录，
	// 开始那条的输出必然是空的，写上占位串会让人以为结果被挡掉了——实际是还没有。
	if strings.TrimSpace(output) == "" {
		return redactedInput, ""
	}
	return redactedInput, "[OneBot v11 tool output omitted]"
}

func sanitizeRepositoryIssuesDebugToolCall(input map[string]any, output string) (map[string]any, string) {
	redactedInput := map[string]any{
		"operation":  strings.TrimSpace(configToolString(input, "operation")),
		"repository": strings.TrimSpace(configToolString(input, "repository")),
		"input_keys": sortedMapKeys(input),
	}
	if number := repositoryIssueNumber(input); number > 0 {
		redactedInput["number"] = number
	}
	if state := strings.TrimSpace(configToolString(input, "state")); state != "" {
		redactedInput["state"] = state
	}
	return redactedInput, repositoryIssueDebugOutcome(output)
}

// repositoryIssueDebugBodyRunes 是草稿正文在追踪里露出的长度。够认出这是哪一条，
// 不至于把整篇正文抄进日志。
const repositoryIssueDebugBodyRunes = 120

// repositoryIssueDebugMaxItems 限制列出来的条数。一次列几十条 Issue 是常事，全抄
// 进追踪会把这一步的记录撑得没法读。
const repositoryIssueDebugMaxItems = 5

// repositoryIssueDebugOutcome 把结果整理成一份能看懂的摘要透出到调试追踪。
//
// 这里原先是「除了状态字段全挡」，理由是 Issue 标题和正文可能含敏感内容。但挡到
// 只剩 outcome 的结果是「共找到 2 条草稿」——看得见有两条，看不出是哪两条，排查
// 时等于没记。而这整份追踪只在「调试追踪」开着时才存在，那个开关自己的说明就写着
// 会记录完整模型上下文（聊天历史全在里面）和工具结果；单独把这一个工具挡死，挡不
// 住任何东西，只是让它比旁边那份上下文更难读。
//
// 所以改成给摘要：编号、标题、状态、链接照出，草稿正文截前 120 字并标出总字数，
// 条数超过 5 条只列前 5 条。
func repositoryIssueDebugOutcome(output string) string {
	// 同上：调用开始那条还没有输出，占位串会被误读成「结果被挡掉了」。
	if strings.TrimSpace(output) == "" {
		return ""
	}
	var result struct {
		OK                   bool                       `json:"ok"`
		Operation            string                     `json:"operation"`
		Outcome              string                     `json:"outcome"`
		FailureCode          string                     `json:"failure_code"`
		Message              string                     `json:"message"`
		RequiresConfirmation bool                       `json:"requires_confirmation"`
		RequiresApproval     bool                       `json:"requires_approval"`
		Idempotent           bool                       `json:"idempotent"`
		Issue                *repositoryIssueSummary    `json:"issue"`
		Items                []repositoryIssueSummary   `json:"items"`
		Draft                *repositoryIssueDraftView  `json:"draft"`
		Drafts               []repositoryIssueDraftView `json:"drafts"`
	}
	if json.Unmarshal([]byte(output), &result) != nil {
		return "[repository issue tool output omitted]"
	}
	status := map[string]any{"ok": result.OK}
	if summaries, dropped := repositoryIssueDebugSummaries(result.Items, result.Issue); len(summaries) > 0 {
		status["items"] = summaries
		if dropped > 0 {
			status["items_not_listed"] = dropped
		}
	}
	if drafts, dropped := repositoryIssueDebugDrafts(result.Drafts, result.Draft); len(drafts) > 0 {
		status["drafts"] = drafts
		if dropped > 0 {
			status["drafts_not_listed"] = dropped
		}
	}
	for key, value := range map[string]string{
		"operation":    result.Operation,
		"outcome":      result.Outcome,
		"failure_code": result.FailureCode,
	} {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			status[key] = trimmed
		}
	}
	// 失败文案取自固定文案表，不含用户内容；成功文案同理。
	if message := strings.TrimSpace(result.Message); message != "" {
		status["message"] = message
	}
	for key, value := range map[string]bool{
		"requires_confirmation": result.RequiresConfirmation,
		"requires_approval":     result.RequiresApproval,
		"idempotent":            result.Idempotent,
	} {
		if value {
			status[key] = true
		}
	}
	encoded, err := json.Marshal(status)
	if err != nil {
		return "[repository issue tool output omitted]"
	}
	return string(encoded)
}

// repositoryIssueDebugSummaries 摘出已有 Issue 的编号、标题、状态和链接。这些是
// 仓库里公开的东西，挡它没有意义，而少了标题就认不出是哪一条。
func repositoryIssueDebugSummaries(items []repositoryIssueSummary, single *repositoryIssueSummary) ([]map[string]any, int) {
	if single != nil {
		items = append([]repositoryIssueSummary{*single}, items...)
	}
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		if len(out) >= repositoryIssueDebugMaxItems {
			break
		}
		entry := map[string]any{}
		if item.Number > 0 {
			entry["number"] = item.Number
		}
		for key, value := range map[string]string{
			"title": item.Title,
			"state": item.State,
			"url":   item.URL,
		} {
			if trimmed := strings.TrimSpace(value); trimmed != "" {
				entry[key] = trimmed
			}
		}
		if len(entry) > 0 {
			out = append(out, entry)
		}
	}
	return out, max(len(items)-len(out), 0)
}

// repositoryIssueDebugDrafts 摘出草稿。草稿是从群聊里攒出来的，正文截前 120 字，
// 另外标出总字数——被截掉多少也是排查时想知道的。
func repositoryIssueDebugDrafts(drafts []repositoryIssueDraftView, single *repositoryIssueDraftView) ([]map[string]any, int) {
	if single != nil {
		drafts = append([]repositoryIssueDraftView{*single}, drafts...)
	}
	out := make([]map[string]any, 0, len(drafts))
	for _, draft := range drafts {
		if len(out) >= repositoryIssueDebugMaxItems {
			break
		}
		entry := map[string]any{}
		for key, value := range map[string]string{
			"id":        draft.ID,
			"title":     draft.Title,
			"status":    draft.Status,
			"operation": draft.Operation,
		} {
			if trimmed := strings.TrimSpace(value); trimmed != "" {
				entry[key] = trimmed
			}
		}
		if draft.IssueNumber > 0 {
			entry["issue_number"] = draft.IssueNumber
		}
		if body := strings.TrimSpace(draft.Body); body != "" {
			entry["body"] = truncateRunes(body, repositoryIssueDebugBodyRunes)
			entry["body_chars"] = len([]rune(body))
		}
		if len(entry) > 0 {
			out = append(out, entry)
		}
	}
	return out, max(len(drafts)-len(out), 0)
}

func formatAgentProgress(event agent.RunEvent) (bar, label string, current, total, percent int) {
	total = event.MaxToolCalls
	if total <= 0 {
		total = agent.DefaultMaxSteps
	}
	current = min(max(event.ToolCall, 0), total)
	if event.Phase == agent.RunPhaseCompleted {
		current = total
	}
	percent = current * 100 / total
	const width = 8
	filled := current * width / total
	if current > 0 && filled == 0 {
		filled = 1
	}
	if event.Phase == agent.RunPhaseCompleted {
		filled = width
		percent = 100
	}
	bar = "[" + strings.Repeat("#", filled) + strings.Repeat("-", width-filled) + "]"
	switch event.Phase {
	case agent.RunPhaseCompleted:
		label = "done"
	case agent.RunPhaseFailed:
		label = fmt.Sprintf("failed %d/%d", current, total)
	default:
		label = fmt.Sprintf("%d/%d", current, total)
	}
	return bar, label, current, total, percent
}
