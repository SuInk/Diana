// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
	"errors"
	"sync"

	"github.com/SuInk/diana/model/llm"
)

// RunHooks are trusted, per-run host callbacks, not model-visible tools. They
// complement registry permissions; a callback cannot grant a denied operation.
// Callbacks must honor cancellation and must not mutate their input snapshots.
type RunHooks struct {
	// PrepareRequest runs before every model request, including forced finalization.
	// Returned changes affect that request only, not the canonical conversation.
	PrepareRequest func(context.Context, llm.GenerateRequest) (llm.GenerateRequest, error)
	// BeforeToolCall runs on resolved tools and normalized arguments. An error
	// blocks execution and is returned to the model as a failed tool result.
	BeforeToolCall func(context.Context, ToolCallContext) error
	// AfterToolCall observes the sanitized result, including failure/block status.
	// It cannot turn a failed execution into success or rewrite execution evidence.
	AfterToolCall func(context.Context, Step)
	// BeforeFinish checks every final output path. Feedback rejects the draft and
	// requests another turn without reexecuting completed tools. Rejection during
	// forced finalization, or after two retries, fails closed with ErrFinishRejected.
	BeforeFinish func(context.Context, FinishCandidate) (FinishDecision, error)
}

type ToolCallContext struct {
	Tool  string
	Input map[string]any
}

type FinishCandidate struct {
	Text        string
	Reason      string
	Steps       []Step
	ModelTurns  int
	CanContinue bool
}

type FinishDecision struct {
	// Empty feedback accepts the candidate. This is a host decision, not a model
	// confidence score. A hook error also blocks the answer.
	Feedback string
}

var ErrFinishRejected = errors.New("agent: final answer rejected")

const maxFinishRetries = 2

// Parallel tools share callbacks. Serialize host callbacks without serializing
// the tools themselves, so a host can maintain a per-run ledger without races.
type runHookState struct {
	hooks RunHooks
	mu    sync.Mutex
}

func (h *runHookState) before(ctx context.Context, tool string, input map[string]any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if h.hooks.BeforeToolCall == nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.hooks.BeforeToolCall(ctx, ToolCallContext{Tool: tool, Input: cloneToolInput(input)})
}

func (h *runHookState) after(ctx context.Context, step Step) {
	if h.hooks.AfterToolCall == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	step.Input = cloneToolInput(step.Input)
	h.hooks.AfterToolCall(ctx, step)
}

func (h *runHookState) prepare(ctx context.Context, request llm.GenerateRequest) (llm.GenerateRequest, error) {
	if err := ctx.Err(); err != nil {
		return request, err
	}
	if h.hooks.PrepareRequest == nil {
		return request, nil
	}
	// Isolate append/replacement of messages from the run's persisted transcript.
	request.Messages = append([]llm.Message(nil), request.Messages...)
	request.Tools = append([]llm.ToolDefinition(nil), request.Tools...)
	return h.hooks.PrepareRequest(ctx, request)
}

// Execution counters exclude attempts rejected before invoking the tool.
func executedStepCount(steps []Step) int {
	count := 0
	for _, step := range steps {
		if !step.Skipped {
			count++
		}
	}
	return count
}
