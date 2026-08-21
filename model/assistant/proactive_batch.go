// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"errors"
	"strings"
	"time"
)

const (
	defaultProactiveReplyBatchWindow  = 5 * time.Second
	defaultProactiveReplyBatchMaxWait = 10 * time.Second
	proactiveReplyBatchMaxItems       = 20
	proactiveReplyDecisionMaxItems    = 3
	proactiveReplyDecisionWindow      = 15 * time.Second
	proactiveReplyMaxReroutes         = 1
)

var (
	errProactiveReplySuperseded = errors.New("chatbot: proactive reply superseded by newer candidates")
	errChatInReplyDeclined      = errors.New("chatbot: chat-in generation declined to produce a substantive reply")
)

type proactiveReplyCandidate struct {
	Event      MessageEvent
	Text       string
	QueuedAt   time.Time
	Generation uint64
}

type proactiveReplyBatch struct {
	items      []proactiveReplyCandidate
	startedAt  time.Time
	generation uint64
	timer      *time.Timer
	processing bool
}

type proactiveReplyRunContextKey struct{}

type proactiveReplyRunContext struct {
	key              string
	generation       uint64
	allowSuperseding bool
}

func proactiveReplyBatchKey(event MessageEvent) string {
	key := sessionKey(event)
	if event.Kind != EventKindGroup {
		return key
	}
	userID := strings.TrimSpace(event.UserID)
	if userID == "" {
		userID = "unknown"
	}
	return key + "|sender:" + userID
}

type proactiveReplyTurnContextKey struct{}

func withProactiveReplyRunContext(ctx context.Context, key string, generation uint64, allowSuperseding bool) context.Context {
	return context.WithValue(ctx, proactiveReplyRunContextKey{}, proactiveReplyRunContext{
		key:              key,
		generation:       generation,
		allowSuperseding: allowSuperseding,
	})
}

func proactiveReplyRunFromContext(ctx context.Context) (proactiveReplyRunContext, bool) {
	if ctx == nil {
		return proactiveReplyRunContext{}, false
	}
	run, ok := ctx.Value(proactiveReplyRunContextKey{}).(proactiveReplyRunContext)
	return run, ok
}

func withProactiveReplyTurnContext(ctx context.Context, candidates []proactiveReplyCandidate) context.Context {
	if len(candidates) == 0 {
		return ctx
	}
	return context.WithValue(ctx, proactiveReplyTurnContextKey{}, append([]proactiveReplyCandidate(nil), candidates...))
}

func proactiveReplyTurnFromContext(ctx context.Context) []proactiveReplyCandidate {
	if ctx == nil {
		return nil
	}
	candidates, _ := ctx.Value(proactiveReplyTurnContextKey{}).([]proactiveReplyCandidate)
	return candidates
}

// enqueueProactiveReply keeps proactive routing off the inbound workers. Messages
// have already been persisted and remembered when they enter this buffer.
func (r *Runtime) enqueueProactiveReply(event MessageEvent, text string) bool {
	r.mu.RLock()
	ctx := r.runCtx
	running := r.running
	r.mu.RUnlock()
	if !running || ctx == nil || event.Kind != EventKindGroup || strings.TrimSpace(event.GroupID) == "" {
		return false
	}

	key := proactiveReplyBatchKey(event)
	now := time.Now()
	r.proactiveBatchMu.Lock()
	batch := r.proactiveBatches[key]
	if batch == nil {
		batch = &proactiveReplyBatch{startedAt: now}
		r.proactiveBatches[key] = batch
	}
	batch.generation++
	batch.items = append(batch.items, proactiveReplyCandidate{
		Event:      event,
		Text:       text,
		QueuedAt:   now,
		Generation: batch.generation,
	})
	if len(batch.items) > proactiveReplyBatchMaxItems {
		batch.items = batch.items[len(batch.items)-proactiveReplyBatchMaxItems:]
	}
	if batch.processing {
		r.proactiveBatchMu.Unlock()
		return true
	}
	r.scheduleProactiveReplyBatchLocked(ctx, key, batch, now)
	r.proactiveBatchMu.Unlock()
	return true
}

func (r *Runtime) scheduleProactiveReplyBatchLocked(ctx context.Context, key string, batch *proactiveReplyBatch, now time.Time) {
	if batch.timer != nil {
		batch.timer.Stop()
	}
	generation := batch.generation
	wait := r.proactiveBatchWindow
	remaining := r.proactiveBatchMaxWait - now.Sub(batch.startedAt)
	if remaining < wait {
		wait = remaining
	}
	if wait < 0 {
		wait = 0
	}
	batch.timer = time.AfterFunc(wait, func() {
		r.flushProactiveReplyBatch(ctx, key, generation)
	})
}

func (r *Runtime) flushProactiveReplyBatch(ctx context.Context, key string, generation uint64) {
	r.proactiveBatchMu.Lock()
	batch := r.proactiveBatches[key]
	if batch == nil || batch.generation != generation || batch.processing {
		r.proactiveBatchMu.Unlock()
		return
	}
	batch.processing = true
	if batch.timer != nil {
		batch.timer.Stop()
		batch.timer = nil
	}
	r.proactiveBatchMu.Unlock()

	reroutes := 0
	for {
		items, currentGeneration, ok := r.proactiveReplyBatchSnapshot(key)
		if !ok || len(items) == 0 || ctx.Err() != nil {
			return
		}
		eligible := items[:0]
		for _, candidate := range items {
			if restriction, blocked := r.activeReplySuppression(candidate.Event, time.Now()); blocked {
				r.recordReplySuppressionBlocked(candidate.Event, restriction)
				continue
			}
			eligible = append(eligible, candidate)
		}
		items = eligible
		if len(items) == 0 {
			r.finishProactiveReplyBatch(ctx, key, currentGeneration)
			return
		}

		event, text, turn, allowed := r.routeProactiveReplyBatch(ctx, items)
		changed, newer := r.proactiveReplyBatchChanged(key, currentGeneration)
		if changed {
			if newer != nil && reroutes < proactiveReplyMaxReroutes {
				r.recordProactiveReplySuperseded(ctx, event, newer.Event, "after_route")
				reroutes++
				continue
			}
			if newer == nil {
				return
			}
		}
		if !allowed || ctx.Err() != nil {
			r.finishProactiveReplyBatch(ctx, key, currentGeneration)
			return
		}
		if restriction, blocked := r.activeReplySuppression(event, time.Now()); blocked {
			r.recordReplySuppressionBlocked(event, restriction)
			r.finishProactiveReplyBatch(ctx, key, currentGeneration)
			return
		}

		replyCtx := withProactiveReplyRunContext(ctx, key, currentGeneration, reroutes < proactiveReplyMaxReroutes)
		replyCtx = withProactiveReplyTurnContext(replyCtx, turn)
		outcome, err := func() (string, error) {
			select {
			case r.sem <- struct{}{}:
				r.incActive(1)
				defer func() {
					<-r.sem
					r.incActive(-1)
				}()
			case <-replyCtx.Done():
				return "", replyCtx.Err()
			}
			return r.replyAndRecord(replyCtx, event, text, "replied_proactive_batch")
		}()
		if errors.Is(err, errProactiveReplySuperseded) && reroutes < proactiveReplyMaxReroutes {
			reroutes++
			continue
		}
		r.finishProactiveReplyBatch(ctx, key, currentGeneration)
		if err != nil || outcome != "replied_proactive_batch" || ctx.Err() != nil {
			return
		}
		return
	}
}

func (r *Runtime) proactiveReplyBatchSnapshot(key string) ([]proactiveReplyCandidate, uint64, bool) {
	r.proactiveBatchMu.Lock()
	defer r.proactiveBatchMu.Unlock()
	batch := r.proactiveBatches[key]
	if batch == nil || !batch.processing {
		return nil, 0, false
	}
	items := append([]proactiveReplyCandidate(nil), batch.items...)
	return proactiveReplyDecisionCandidates(items), batch.generation, true
}

func proactiveReplyDecisionCandidates(items []proactiveReplyCandidate) []proactiveReplyCandidate {
	if len(items) > proactiveReplyDecisionMaxItems {
		items = items[len(items)-proactiveReplyDecisionMaxItems:]
	}
	if len(items) < 2 {
		return append([]proactiveReplyCandidate(nil), items...)
	}
	latestAt := proactiveReplyCandidateTime(items[len(items)-1])
	if latestAt.IsZero() {
		return append([]proactiveReplyCandidate(nil), items...)
	}
	first := 0
	for first < len(items)-1 {
		candidateAt := proactiveReplyCandidateTime(items[first])
		if candidateAt.IsZero() || latestAt.Sub(candidateAt) <= proactiveReplyDecisionWindow {
			break
		}
		first++
	}
	return append([]proactiveReplyCandidate(nil), items[first:]...)
}

func proactiveReplyCandidateTime(candidate proactiveReplyCandidate) time.Time {
	if !candidate.QueuedAt.IsZero() {
		return candidate.QueuedAt
	}
	if candidate.Event.Time > 0 {
		return time.Unix(candidate.Event.Time, 0)
	}
	return time.Time{}
}

func (r *Runtime) proactiveReplyBatchChanged(key string, generation uint64) (bool, *proactiveReplyCandidate) {
	r.proactiveBatchMu.Lock()
	defer r.proactiveBatchMu.Unlock()
	batch := r.proactiveBatches[key]
	if batch == nil || !batch.processing {
		return true, nil
	}
	if batch.generation <= generation {
		return false, nil
	}
	for i := len(batch.items) - 1; i >= 0; i-- {
		if batch.items[i].Generation > generation {
			newer := batch.items[i]
			return true, &newer
		}
	}
	return true, nil
}

func (r *Runtime) finishProactiveReplyBatch(ctx context.Context, key string, throughGeneration uint64) {
	now := time.Now()
	r.proactiveBatchMu.Lock()
	defer r.proactiveBatchMu.Unlock()
	batch := r.proactiveBatches[key]
	if batch == nil {
		return
	}
	pending := make([]proactiveReplyCandidate, 0, len(batch.items))
	for _, item := range batch.items {
		if item.Generation > throughGeneration {
			pending = append(pending, item)
		}
	}
	if len(pending) == 0 {
		delete(r.proactiveBatches, key)
		return
	}
	batch.items = pending
	batch.processing = false
	batch.startedAt = proactiveReplyCandidateTime(pending[0])
	if batch.startedAt.IsZero() {
		batch.startedAt = now
	}
	r.scheduleProactiveReplyBatchLocked(ctx, key, batch, now)
}

func (r *Runtime) cancelProactiveReplyBatch(event MessageEvent) {
	if event.Kind != EventKindGroup || strings.TrimSpace(event.GroupID) == "" {
		return
	}
	r.proactiveBatchMu.Lock()
	key := proactiveReplyBatchKey(event)
	if batch := r.proactiveBatches[key]; batch != nil {
		if batch.timer != nil {
			batch.timer.Stop()
		}
		delete(r.proactiveBatches, key)
	}
	r.proactiveBatchMu.Unlock()
}

func (r *Runtime) cancelProactiveReplyBatchesForGroup(groupID string) {
	groupID = strings.TrimSpace(groupID)
	if groupID == "" {
		return
	}
	r.proactiveBatchMu.Lock()
	for key, batch := range r.proactiveBatches {
		if len(batch.items) == 0 || strings.TrimSpace(batch.items[0].Event.GroupID) != groupID {
			continue
		}
		if batch.timer != nil {
			batch.timer.Stop()
		}
		delete(r.proactiveBatches, key)
	}
	r.proactiveBatchMu.Unlock()
}

func (r *Runtime) clearProactiveReplyBatches() {
	r.proactiveBatchMu.Lock()
	for key, batch := range r.proactiveBatches {
		if batch.timer != nil {
			batch.timer.Stop()
		}
		delete(r.proactiveBatches, key)
	}
	r.proactiveBatchMu.Unlock()
}
