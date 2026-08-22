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
			if runError != "" && toolOutput == "" {
				toolOutput = "ERROR: " + runError
			}
			if runEvent.Tool == dianaOneBotV11ToolName {
				toolInput, toolOutput = sanitizeOneBotV11DebugToolCall(toolInput, toolOutput)
			} else if runEvent.Tool == dianaRepositoryIssuesToolName {
				toolInput, toolOutput = sanitizeRepositoryIssuesDebugToolCall(toolInput, toolOutput)
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

// repositoryIssueDebugOutcome 只把结果的状态字段透出到调试追踪。
// Issue 标题和正文可能含敏感内容，必须挡住；但 ok/outcome/failure_code 和那句
// 固定失败文案不含任何用户内容，全挡掉的结果是排查时一片空白——写操作被闸门
// 拒绝时，追踪里连「为什么被拒」都看不到。
func repositoryIssueDebugOutcome(output string) string {
	var result struct {
		OK                   bool   `json:"ok"`
		Operation            string `json:"operation"`
		Outcome              string `json:"outcome"`
		FailureCode          string `json:"failure_code"`
		Message              string `json:"message"`
		RequiresConfirmation bool   `json:"requires_confirmation"`
		RequiresApproval     bool   `json:"requires_approval"`
		Idempotent           bool   `json:"idempotent"`
	}
	if json.Unmarshal([]byte(output), &result) != nil {
		return "[repository issue tool output omitted]"
	}
	status := map[string]any{"ok": result.OK}
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
	return string(encoded) + " [issue 正文已省略]"
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
