// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"errors"
	"log"
	"strings"
	"time"
)

const directReplyMergeRetention = 2 * time.Minute

var errDirectReplySupplemented = errors.New("chatbot: direct reply received a same-turn supplement")

type activeDirectReply struct {
	token       uint64
	turnID      string
	root        MessageEvent
	startedAt   time.Time
	generation  uint64
	accepting   bool
	supplements []proactiveReplyCandidate
}

type directReplyRunContextKey struct{}

type directReplyRunContext struct {
	key        string
	token      uint64
	generation uint64
}

type InboundReplyMergeStore interface {
	RecordInboundEventReplyMerge(ctx context.Context, event MessageEvent, rootTurnID string) error
}

func directReplyMergeKey(event MessageEvent) string {
	if event.Kind != EventKindGroup || strings.TrimSpace(event.UserID) == "" {
		return ""
	}
	return sessionKey(event) + "|sender:" + strings.TrimSpace(event.UserID)
}

func (r *Runtime) beginDirectReply(ctx context.Context, event MessageEvent) (context.Context, func()) {
	key := directReplyMergeKey(event)
	if key == "" {
		return ctx, func() {}
	}
	turnID := strings.TrimSpace(event.MessageID)
	if turn := outboundTurnFromContext(ctx); turn != nil && strings.TrimSpace(turn.id) != "" {
		turnID = strings.TrimSpace(turn.id)
	}
	r.replyInterruptMu.Lock()
	if r.activeDirectReplies == nil {
		r.activeDirectReplies = map[string]*activeDirectReply{}
	}
	r.directReplySeq++
	token := r.directReplySeq
	r.activeDirectReplies[key] = &activeDirectReply{token: token, turnID: turnID, root: event, startedAt: time.Now(), accepting: true}
	r.replyInterruptMu.Unlock()
	ctx = context.WithValue(ctx, directReplyRunContextKey{}, directReplyRunContext{key: key, token: token})
	return ctx, func() {
		r.replyInterruptMu.Lock()
		if active := r.activeDirectReplies[key]; active != nil && active.token == token {
			delete(r.activeDirectReplies, key)
		}
		r.replyInterruptMu.Unlock()
	}
}

func (r *Runtime) directReplyAttemptContext(ctx context.Context) context.Context {
	run, ok := ctx.Value(directReplyRunContextKey{}).(directReplyRunContext)
	if !ok {
		return ctx
	}
	r.replyInterruptMu.Lock()
	if active := r.activeDirectReplies[run.key]; active != nil && active.token == run.token {
		run.generation = active.generation
	}
	r.replyInterruptMu.Unlock()
	return context.WithValue(ctx, directReplyRunContextKey{}, run)
}

func (r *Runtime) directReplySupplements(ctx context.Context) []proactiveReplyCandidate {
	run, ok := ctx.Value(directReplyRunContextKey{}).(directReplyRunContext)
	if !ok {
		return nil
	}
	r.replyInterruptMu.Lock()
	defer r.replyInterruptMu.Unlock()
	active := r.activeDirectReplies[run.key]
	if active == nil || active.token != run.token {
		return nil
	}
	return append([]proactiveReplyCandidate(nil), active.supplements...)
}

func (r *Runtime) directReplyHasNewSupplements(ctx context.Context) bool {
	run, ok := ctx.Value(directReplyRunContextKey{}).(directReplyRunContext)
	if !ok {
		return false
	}
	r.replyInterruptMu.Lock()
	defer r.replyInterruptMu.Unlock()
	active := r.activeDirectReplies[run.key]
	return active != nil && active.token == run.token && active.generation > run.generation
}

func (r *Runtime) sealDirectReply(ctx context.Context) {
	run, ok := ctx.Value(directReplyRunContextKey{}).(directReplyRunContext)
	if !ok {
		return
	}
	r.replyInterruptMu.Lock()
	if active := r.activeDirectReplies[run.key]; active != nil && active.token == run.token {
		active.accepting = false
	}
	r.replyInterruptMu.Unlock()
}

func (r *Runtime) mergeIntoActiveDirectReply(ctx context.Context, event MessageEvent, text string) (string, bool) {
	key := directReplyMergeKey(event)
	if key == "" || strings.TrimSpace(event.MessageID) == "" {
		return "", false
	}
	r.replyInterruptMu.Lock()
	active := r.activeDirectReplies[key]
	if active == nil || !active.accepting || active.root.MessageID == event.MessageID || time.Since(active.startedAt) > directReplyMergeRetention {
		r.replyInterruptMu.Unlock()
		return "", false
	}
	active.generation++
	active.supplements = append(active.supplements, proactiveReplyCandidate{Event: event, Text: text, QueuedAt: time.Now(), Generation: active.generation})
	rootTurnID, rootMessageID := active.turnID, active.root.MessageID
	r.replyInterruptMu.Unlock()

	r.mu.RLock()
	store, _ := r.inboundStore.(InboundReplyMergeStore)
	r.mu.RUnlock()
	if store != nil {
		mergeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		if err := store.RecordInboundEventReplyMerge(mergeCtx, event, rootTurnID); err != nil {
			log.Printf("chatbot record inbound reply merge failed: %v", err)
		}
		cancel()
	}
	return strings.TrimSpace(rootMessageID), true
}
