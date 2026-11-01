// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
	"fmt"
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
//   - send 和检索同一步时是中途发言，已先发出；收尾留到看过结果的下一步；
//   - 结果按模型的调用顺序回填，不按完成先后；超出步数预算的调用明确标为未执行。
var parallelReadTools = map[string]bool{
	WebSearchToolName:     true,
	browserRenderToolName: true,
}

type parallelRead struct {
	cachedOutput *string
	call         llm.ToolCall
	action       llmAction
	tool         Tool
	record       Step
	rawOutput    string
	output       string
	err          error
	metadata     map[string]any
}

// Resolve the execution target without replacing the provider's call envelope.
func (r *Runner) parallelReadAction(call llm.ToolCall) (llmAction, bool) {
	action := llmAction{Action: "tool", Tool: call.Name, Input: cloneToolInput(call.Arguments)}
	if call.Name == ToolsExecuteToolName {
		name, _ := call.Arguments["name"].(string)
		if !parallelReadTools[name] {
			return action, false
		}
		var err error
		action, err = r.loader.dispatch(action)
		if err != nil {
			return action, false
		}
	}
	return action, parallelReadTools[action.Tool]
}

// parallelReadBatch 判断这一批原生调用能否并行执行，能则返回要执行的调用下标（不含收尾）。
func (r *Runner) parallelReadBatch(calls []llm.ToolCall) ([]int, bool) {
	var indices []int
	for index, call := range calls {
		if call.Name == finalizeToolName {
			continue
		}
		action, allowed := r.parallelReadAction(call)
		if !allowed {
			return nil, false
		}
		tool, ok := r.registry.Get(action.Tool)
		if !ok || r.registry.OperationDisabledError(action.Tool, action.Input) != nil || explicitUserRequestKind(tool, action.Input) != "" {
			return nil, false
		}
		// 和串行路径使用相同契约校验；合法的首次调用可按需登记，非法输入退回修复。
		if r.loader != nil && !r.loader.core[action.Tool] {
			if _, err := r.loader.resolveLoaded(action); err != nil {
				return nil, false
			}
		}
		indices = append(indices, index)
	}
	return indices, len(indices) >= 2
}

func (r *Runner) runParallelReads(ctx context.Context, hooks *runHookState, observer RunObserver, traceID string, modelTurn, firstCall int, calls []llm.ToolCall, steps []Step) []parallelRead {
	reads := make([]parallelRead, len(calls))
	var wg sync.WaitGroup
	for index, call := range calls {
		action, _ := r.parallelReadAction(call)
		tool, _ := r.registry.Get(action.Tool)
		input := action.Input
		if typed, ok := tool.(ToolInputSchema); ok {
			input = coerceToolInputArrays(typed.InputSchema(), input)
		}
		input = resolveSearchRefs(action.Tool, minimalToolInput(action.Tool, input), steps)
		reads[index] = parallelRead{call: call, tool: tool, action: llmAction{Action: "tool", Tool: action.Tool, Input: input}}
		wg.Add(1)
		go func(read *parallelRead, ordinal int) {
			defer recoverGoroutinePanic("parallel_reads")
			defer wg.Done()
			r.runParallelRead(ctx, hooks, observer, traceID, modelTurn, ordinal, read)
		}(&reads[index], firstCall+index+1)
	}
	wg.Wait()
	return reads
}

func (r *Runner) runParallelRead(ctx context.Context, hooks *runHookState, observer RunObserver, traceID string, modelTurn, ordinal int, read *parallelRead) {
	action := read.action
	inputKeys := sortedInputKeys(action.Input)
	read.metadata = webSearchRunMetadataFromInput(action.Tool, action.Input)
	emitRunEvent(ctx, observer, RunEvent{TraceID: traceID, Phase: RunPhaseToolStarted, ModelTurn: modelTurn, ToolCall: ordinal, MaxToolCalls: r.cfg.MaxSteps, Tool: action.Tool, InputKeys: inputKeys, ToolInput: cloneToolInput(action.Input), Metadata: read.metadata})
	outputLimit := r.toolOutputLimit(read.tool)
	toolTimeout := time.Duration(r.cfg.ToolTimeoutMS) * time.Millisecond
	if bounded, ok := read.tool.(ToolTimeout); ok && bounded.Timeout() >= 0 {
		toolTimeout = bounded.Timeout()
	}
	toolCtx, cancel := contextWithToolBudget(WithToolOutputBudget(ctx, outputLimit), toolTimeout, time.Duration(r.cfg.FinalizationReserveMS)*time.Millisecond)
	startedAt := time.Now()
	err := r.registry.OperationDisabledError(action.Tool, action.Input)
	if err == nil {
		err = hooks.before(toolCtx, action.Tool, action.Input)
	}
	blocked := err != nil
	var output string
	if err == nil {
		if read.cachedOutput != nil {
			output = *read.cachedOutput
		} else {
			output, err = invokeParallelTool(toolCtx, read.tool, action.Input)
		}
	}
	cancel()
	read.record = Step{Skipped: blocked, Tool: action.Tool, Input: action.Input, DurationMS: time.Since(startedAt).Milliseconds()}
	read.record.CacheHit = err == nil && read.cachedOutput != nil
	// 与逐个执行同一套遮罩和截断，见 Runner.Run。
	output = secretmask.Output(output)
	read.rawOutput = output
	read.err = err
	if err != nil {
		read.record.Error = secretmask.Text(normalizeToolError(err, toolCtx, ctx, int(toolTimeout/time.Millisecond)))
		output = toolExecutionErrorForModel(action.Tool, read.record.Error)
	} else {
		read.record.Output = truncateToolOutput(output, outputLimit)
		if action.Tool == ToolsLoadToolName {
			read.record.Output = output
		}
		output = read.record.Output
	}
	hooks.after(ctx, read.record)
	read.output = output
	read.metadata = mergeRunMetadata(read.metadata, researchRunMetadataFromOutput(action.Tool, read.rawOutput, err))
	if read.record.CacheHit {
		if read.metadata == nil {
			read.metadata = map[string]any{}
		}
		read.metadata["cache_hit"] = true
	}
	emitRunEvent(ctx, observer, RunEvent{TraceID: traceID, Phase: RunPhaseToolCompleted, ModelTurn: modelTurn, ToolCall: ordinal, MaxToolCalls: r.cfg.MaxSteps, Tool: action.Tool, InputKeys: inputKeys, ToolInput: cloneToolInput(action.Input), ToolOutput: read.record.Output, Metadata: read.metadata, OutputChars: len([]rune(read.record.Output)), DurationMS: read.record.DurationMS, Error: read.record.Error})
}

// Tool panics must become audited failures, not escape a Code Mode RPC worker
// goroutine and terminate the process. Trusted host hooks keep their own contract.
func invokeParallelTool(ctx context.Context, tool Tool, input map[string]any) (output string, err error) {
	defer func() {
		if value := recover(); value != nil {
			output = ""
			err = fmt.Errorf("tool execution panicked: %s", secretmask.Text(fmt.Sprint(value)))
		}
	}()
	return tool.Run(ctx, input)
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
