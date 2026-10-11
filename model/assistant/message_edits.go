// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"strings"
	"time"
)

const (
	messageEditRevisionRetention = time.Hour
	messageEditRevisionLimit     = 4096
)

type messageEditRevision struct {
	Event  MessageEvent
	SeenAt time.Time
}

func messageStoreSession(event MessageEvent) string {
	if session := strings.TrimSpace(event.durableSession); session != "" {
		return session
	}
	return sessionKey(event)
}

// messageEditRevisionKey deliberately omits ContextNamespace. A message first
// stored under a legacy unnamespaced session can later be edited after the bot
// profile namespace has been filled in; it is still the same transport message.
func messageEditRevisionKey(event MessageEvent) string {
	if strings.TrimSpace(event.MessageID) == "" {
		return ""
	}
	return strings.Join([]string{
		NormalizePlatformID(event.Platform),
		strings.TrimSpace(event.ProfileID),
		strings.TrimSpace(event.SelfID),
		string(event.Kind),
		strings.TrimSpace(event.GroupID),
		strings.TrimSpace(event.UserID),
		strings.TrimSpace(event.MessageID),
	}, "\x00")
}

func messageEventRevisionOlder(candidate, current MessageEvent) bool {
	if !current.Edited {
		return false
	}
	if !candidate.Edited {
		return true
	}
	if candidate.TelegramUpdateID > 0 && current.TelegramUpdateID > 0 && candidate.TelegramUpdateID == current.TelegramUpdateID {
		return false
	}
	if candidate.EditTime > 0 && current.EditTime > 0 && candidate.EditTime != current.EditTime {
		return candidate.EditTime < current.EditTime
	}
	if candidate.EditTime == 0 && current.EditTime > 0 {
		return true
	}
	if candidate.EditTime > 0 && current.EditTime == 0 {
		return false
	}
	if candidate.TelegramUpdateID > 0 && current.TelegramUpdateID > 0 {
		return candidate.TelegramUpdateID < current.TelegramUpdateID
	}
	if current.TelegramUpdateID > 0 {
		return true
	}
	return false
}

// currentMessageRevisionLocked returns the newest edit known to this runtime.
// r.mu must be held by the caller.
func (r *Runtime) currentMessageRevisionLocked(event MessageEvent, now time.Time) MessageEvent {
	key := messageEditRevisionKey(event)
	if key == "" {
		return event
	}
	if revision, ok := r.latestMessageEdits[key]; ok && messageEventRevisionOlder(event, revision.Event) {
		return revision.Event
	}
	if event.Edited {
		r.rememberMessageEditRevisionLocked(key, event, now)
	}
	return event
}

// rememberMessageEditRevisionLocked records a guard against an already claimed
// worker later remembering an older snapshot. r.mu must be held by the caller.
func (r *Runtime) rememberMessageEditRevisionLocked(key string, event MessageEvent, now time.Time) {
	if key == "" || !event.Edited {
		return
	}
	if r.latestMessageEdits == nil {
		r.latestMessageEdits = make(map[string]messageEditRevision)
	}
	if current, ok := r.latestMessageEdits[key]; ok && messageEventRevisionOlder(event, current.Event) {
		return
	}
	r.latestMessageEdits[key] = messageEditRevision{Event: withoutReplyRuntimeState(event), SeenAt: now}
	if len(r.latestMessageEdits) <= messageEditRevisionLimit {
		return
	}
	cutoff := now.Add(-messageEditRevisionRetention)
	for revisionKey, revision := range r.latestMessageEdits {
		if revision.SeenAt.Before(cutoff) {
			delete(r.latestMessageEdits, revisionKey)
		}
	}
	// Edits are rare, but keep the guard bounded even if a busy deployment sees
	// more than the retention window can remove.
	for len(r.latestMessageEdits) > messageEditRevisionLimit {
		var oldestKey string
		var oldest time.Time
		for revisionKey, revision := range r.latestMessageEdits {
			if oldestKey == "" || revision.SeenAt.Before(oldest) {
				oldestKey, oldest = revisionKey, revision.SeenAt
			}
		}
		delete(r.latestMessageEdits, oldestKey)
	}
}

// refreshRememberedMessageEdit updates only copies that are already present in
// memory. An edit of an evicted, completed message must not reappear at the end
// of a bounded history window. storedSession is included because legacy rows can
// live under an unnamespaced durable session even when the live event is scoped.
func (r *Runtime) refreshRememberedMessageEdit(event MessageEvent, storedSession string) MessageEvent {
	if !event.Edited || strings.TrimSpace(event.MessageID) == "" {
		return event
	}
	event = withoutReplyRuntimeState(event)
	now := time.Now()

	r.mu.Lock()
	key := messageEditRevisionKey(event)
	if current, ok := r.latestMessageEdits[key]; ok && messageEventRevisionOlder(event, current.Event) {
		event = current.Event
	} else {
		r.rememberMessageEditRevisionLocked(key, event, now)
	}
	sessions := []string{sessionKey(event)}
	if storedSession = strings.TrimSpace(storedSession); storedSession != "" && storedSession != sessions[0] {
		sessions = append(sessions, storedSession)
	}
	for _, session := range sessions {
		history := r.history[session]
		for index := range history {
			if strings.TrimSpace(history[index].MessageID) != strings.TrimSpace(event.MessageID) {
				continue
			}
			if messageEventRevisionOlder(event, history[index]) {
				break
			}
			history[index] = event
			r.history[session] = history
			break
		}
	}
	for promptKey, buffer := range r.groupPromptHistory {
		changed := false
		for index := range buffer.Events {
			if !sameEditedMessageIdentity(buffer.Events[index], event) || messageEventRevisionOlder(event, buffer.Events[index]) {
				continue
			}
			buffer.Events[index] = event
			changed = true
		}
		if changed {
			r.groupPromptHistory[promptKey] = buffer
		}
	}
	r.mu.Unlock()

	// A not-yet-claimed event has a routing placeholder with no durable inbound
	// ID. Refresh that placeholder so the worker sees the edited event. Once it
	// has been claimed, keep the turn's original processing snapshot intact.
	mergeKey := directReplyMergeKey(event)
	if mergeKey == "" {
		return event
	}
	r.replyInterruptMu.Lock()
	if turn := r.senderTurnLocked(mergeKey, strings.TrimSpace(event.MessageID)); turn != nil && turn.inboundID == "" && turn.stage == senderTurnRouting {
		if !messageEventRevisionOlder(event, turn.event) {
			turn.event = event
		}
	}
	r.replyInterruptMu.Unlock()
	return event
}

func sameEditedMessageIdentity(left, right MessageEvent) bool {
	if strings.TrimSpace(left.MessageID) == "" || strings.TrimSpace(left.MessageID) != strings.TrimSpace(right.MessageID) ||
		left.Kind != right.Kind || strings.TrimSpace(left.GroupID) != strings.TrimSpace(right.GroupID) ||
		strings.TrimSpace(left.UserID) != strings.TrimSpace(right.UserID) {
		return false
	}
	for _, pair := range [][2]string{
		{NormalizePlatformID(left.Platform), NormalizePlatformID(right.Platform)},
		{strings.TrimSpace(left.ProfileID), strings.TrimSpace(right.ProfileID)},
		{strings.TrimSpace(left.SelfID), strings.TrimSpace(right.SelfID)},
	} {
		if pair[0] != "" && pair[1] != "" && pair[0] != pair[1] {
			return false
		}
	}
	return true
}
