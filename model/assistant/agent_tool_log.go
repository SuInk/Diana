package assistant

import (
	"context"
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
		action := "qqbot.agent_run"
		message := "Agent 运行状态已更新"
		switch runEvent.Phase {
		case agent.RunPhaseStarted:
			message = "Agent 运行开始"
		case agent.RunPhaseModelCompleted:
			action = "qqbot.agent_model"
			message = "Agent 模型轮次完成"
		case agent.RunPhaseProtocolRepair:
			action = "qqbot.agent_protocol"
			message = "Agent 协议已自动修正"
		case agent.RunPhaseToolStarted:
			action = "qqbot.agent_tool"
			message = "Agent 工具调用开始"
		case agent.RunPhaseToolCompleted:
			action = "qqbot.agent_tool"
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
		logCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = writer.AppendLog(logCtx, applog.Entry{
			Kind:    kind,
			Level:   level,
			Action:  action,
			Message: message,
			Detail:  runError,
			Actor:   qqEventActor(event),
			Target:  target,
			Metadata: map[string]any{
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
			},
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
		log.Printf("qqbot agent progress: trace=%s %s %s phase=%s model_turn=%d tool=%s duration_ms=%d",
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

func sanitizeRepositoryIssuesDebugToolCall(input map[string]any, _ string) (map[string]any, string) {
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
	return redactedInput, "[repository issue tool output omitted]"
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
