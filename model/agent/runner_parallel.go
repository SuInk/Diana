// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/SuInk/diana/internal/secretmask"
	"github.com/SuInk/diana/model/llm"
)

// 一步只执行一个工具时，模型并行发来的几次检索只跑第一个，读完一页就收尾很常见。
// 通用 agent（opencode、Codex）的做法是同一条回复里的独立调用并行执行。这里只对
// 只读检索放开并行，顺序规则：
//   - 批里全是 parallelReadTools 才并行；混进任何别的工具（发消息、写记忆、生图…）
//     退回原来的逐个执行，严格按模型给的顺序，一次一个；
//   - agent_finalize 不和检索同步执行，留到看过结果的下一步；
//   - 结果按模型的调用顺序回填，不按完成先后；超出步数预算的调用明确标为未执行。
var parallelReadTools = map[string]bool{
	WebSearchToolName:     true,
	browserRenderToolName: true,
}

type parallelRead struct {
	call      llm.ToolCall
	action    llmAction
	tool      Tool
	record    Step
	rawOutput string
	output    string
	err       error
	metadata  map[string]any
}

// parallelReadBatch 判断这一批原生调用能否并行执行，能则返回要执行的调用下标（不含收尾）。
func (r *Runner) parallelReadBatch(calls []llm.ToolCall) ([]int, bool) {
	var indices []int
	for index, call := range calls {
		if call.Name == finalizeToolName {
			continue
		}
		if !parallelReadTools[call.Name] {
			return nil, false
		}
		tool, ok := r.registry.Get(call.Name)
		if !ok || r.registry.OperationDisabledError(call.Name, call.Arguments) != nil || explicitUserRequestKind(tool, call.Arguments) != "" {
			return nil, false
		}
		// 先检查已加载的契约；不能在这里自动加载后立即执行，模型尚未看到契约。
		// 不满足条件时退回普通路径，由 dispatch 返回加载提示或参数错误。
		if r.loader != nil && !r.loader.core[call.Name] {
			if _, err := r.loader.resolveLoaded(llmAction{Action: "tool", Tool: call.Name, Input: cloneToolInput(call.Arguments)}); err != nil {
				return nil, false
			}
		}
		indices = append(indices, index)
	}
	return indices, len(indices) >= 2
}

func (r *Runner) runParallelReads(ctx context.Context, observer RunObserver, traceID string, modelTurn, firstCall int, calls []llm.ToolCall) []parallelRead {
	reads := make([]parallelRead, len(calls))
	var wg sync.WaitGroup
	for index, call := range calls {
		tool, _ := r.registry.Get(call.Name)
		input := cloneToolInput(call.Arguments)
		if typed, ok := tool.(ToolInputSchema); ok {
			input = coerceToolInputArrays(typed.InputSchema(), input)
		}
		input = minimalToolInput(call.Name, input)
		reads[index] = parallelRead{call: call, tool: tool, action: llmAction{Action: "tool", Tool: call.Name, Input: input}}
		wg.Add(1)
		go func(read *parallelRead, ordinal int) {
			defer recoverGoroutinePanic("parallel_reads")
			defer wg.Done()
			r.runParallelRead(ctx, observer, traceID, modelTurn, ordinal, read)
		}(&reads[index], firstCall+index+1)
	}
	wg.Wait()
	return reads
}

func (r *Runner) runParallelRead(ctx context.Context, observer RunObserver, traceID string, modelTurn, ordinal int, read *parallelRead) {
	action := read.action
	inputKeys := sortedInputKeys(action.Input)
	read.metadata = webSearchRunMetadataFromInput(action.Tool, action.Input)
	emitRunEvent(ctx, observer, RunEvent{TraceID: traceID, Phase: RunPhaseToolStarted, ModelTurn: modelTurn, ToolCall: ordinal, MaxToolCalls: r.cfg.MaxSteps, Tool: action.Tool, InputKeys: inputKeys, ToolInput: cloneToolInput(action.Input), Metadata: read.metadata})
	outputLimit := r.toolOutputLimit(read.tool)
	toolCtx, cancel := contextWithToolBudget(WithToolOutputBudget(ctx, outputLimit), time.Duration(r.cfg.ToolTimeoutMS)*time.Millisecond, time.Duration(r.cfg.FinalizationReserveMS)*time.Millisecond)
	startedAt := time.Now()
	output, err := read.tool.Run(toolCtx, action.Input)
	cancel()
	read.record = Step{Tool: action.Tool, Input: action.Input, DurationMS: time.Since(startedAt).Milliseconds()}
	// 与逐个执行同一套遮罩和截断，见 Runner.Run。
	output = secretmask.Output(output)
	read.rawOutput = output
	read.err = err
	if err != nil {
		read.record.Error = secretmask.Text(normalizeToolError(err, toolCtx, ctx, r.cfg.ToolTimeoutMS))
		output = toolExecutionErrorForModel(action.Tool, read.record.Error)
	} else {
		read.record.Output = truncateToolOutput(output, outputLimit)
		output = read.record.Output
	}
	read.output = output
	read.metadata = mergeRunMetadata(read.metadata, researchRunMetadataFromOutput(action.Tool, read.rawOutput, err))
	emitRunEvent(ctx, observer, RunEvent{TraceID: traceID, Phase: RunPhaseToolCompleted, ModelTurn: modelTurn, ToolCall: ordinal, MaxToolCalls: r.cfg.MaxSteps, Tool: action.Tool, InputKeys: inputKeys, ToolInput: cloneToolInput(action.Input), ToolOutput: read.record.Output, Metadata: read.metadata, OutputChars: len([]rune(read.record.Output)), DurationMS: read.record.DurationMS, Error: read.record.Error})
}

// parallelReadGuidance 合并同一批里各检索结果的观察提示，相同的只留一份，进度放最后。
func parallelReadGuidance(reads []parallelRead, steps []Step, remaining int) string {
	var parts []string
	seen := map[string]bool{}
	for _, read := range reads {
		if read.err != nil {
			continue
		}
		guidance := researchObservationGuidance(read.action.Tool, read.rawOutput, remaining)
		if guidance == "" || seen[guidance] {
			continue
		}
		seen[guidance] = true
		parts = append(parts, guidance)
	}
	if len(parts) == 0 {
		return ""
	}
	text := strings.Join(parts, "\n")
	if note := collectResearchProgress(steps).note(); note != "" {
		text += "\n\n" + note
	}
	return text
}
