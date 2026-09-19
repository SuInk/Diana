package assistant

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// ContextHistoryStore separates automatic prompt history from the searchable
// archive. ResetContextHistory must commit the reset before reporting success.
type ContextHistoryStore interface {
	ResetContextHistory(ctx context.Context, session string, at time.Time) error
	ListContextMessageEvents(ctx context.Context, session string, limit int) ([]MessageEvent, error)
}

func listContextMessageEvents(ctx context.Context, store MessageHistoryStore, session string, limit int) ([]MessageEvent, error) {
	if scoped, ok := store.(ContextHistoryStore); ok {
		return scoped.ListContextMessageEvents(ctx, session, limit)
	}
	return store.ListRecentMessageEvents(ctx, session, limit)
}

func (r *Runtime) isOwnerContextResetCommand(event MessageEvent, text string) bool {
	if !r.effectiveConfigForEvent(event).IsOwnerEvent(event) {
		return false
	}
	command := strings.TrimSpace(r.cleanInput(event, text))
	return command == "清空上下文" || command == "清除上下文"
}

func (r *Runtime) clearSessionHistory(event MessageEvent) error {
	session := sessionKey(event)
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.messageStore != nil {
		store, ok := r.messageStore.(ContextHistoryStore)
		if !ok {
			return fmt.Errorf("message history store does not support context reset")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := store.ResetContextHistory(ctx, session, time.Now()); err != nil {
			return err
		}
	}
	delete(r.history, session)
	delete(r.contextSummaries, session)
	delete(r.contextSummaryMarks, session)
	delete(r.historyWindowAnchors, session)
	for key, buffer := range r.groupPromptHistory {
		if buffer.Session == session {
			delete(r.groupPromptHistory, key)
		}
	}
	for key, state := range r.groupPromptSessions {
		if state.session == session {
			delete(r.historyWindowAnchors, key)
			state.mu.Lock()
			state.invalidated = true
			state.mu.Unlock()
			delete(r.groupPromptSessions, key)
		}
	}
	delete(r.recentClaimSources, session)
	delete(r.recentToolCalls, session)
	for key := range r.agentCarryovers {
		if strings.HasPrefix(key, session+"\x00") {
			delete(r.agentCarryovers, key)
		}
	}
	r.replyTurnMu.Lock()
	defer r.replyTurnMu.Unlock()
	for key := range r.replyTurns {
		if strings.HasPrefix(key, session+"\x00") {
			delete(r.replyTurns, key)
		}
	}
	return nil
}
