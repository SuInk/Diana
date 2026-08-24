// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"strings"
	"time"

	"github.com/SuInk/diana/model/applog"
)

const resolverDeliveryDedupeTTL = 10 * time.Minute

type resolverDeliveryReservation struct {
	token     uint64
	expiresAt time.Time
}

type resolverDeliveryHandle struct {
	token uint64
	keys  []string
}

func (r *Runtime) reserveResolverDelivery(event MessageEvent, resourceKeys []string) (resolverDeliveryHandle, bool) {
	keys := normalizeResolverResourceKeys(resourceKeys)
	if len(keys) == 0 {
		return resolverDeliveryHandle{}, false
	}
	now := r.clock()
	scope := sessionKey(event)
	r.resolverDeliveryMu.Lock()
	defer r.resolverDeliveryMu.Unlock()
	for key, reservation := range r.resolverDeliveries {
		if !reservation.expiresAt.After(now) {
			delete(r.resolverDeliveries, key)
		}
	}
	missing := make([]string, 0, len(keys))
	for _, key := range keys {
		scoped := scope + "\x00" + key
		if _, found := r.resolverDeliveries[scoped]; !found {
			missing = append(missing, scoped)
		}
	}
	if len(missing) == 0 {
		return resolverDeliveryHandle{}, true
	}
	r.resolverDeliverySeq++
	handle := resolverDeliveryHandle{token: r.resolverDeliverySeq, keys: missing}
	for _, key := range missing {
		r.resolverDeliveries[key] = resolverDeliveryReservation{token: handle.token, expiresAt: now.Add(resolverDeliveryDedupeTTL)}
	}
	return handle, false
}

func (r *Runtime) finishResolverDelivery(handle resolverDeliveryHandle, delivered bool) {
	if handle.token == 0 || len(handle.keys) == 0 {
		return
	}
	expiresAt := r.clock().Add(resolverDeliveryDedupeTTL)
	r.resolverDeliveryMu.Lock()
	defer r.resolverDeliveryMu.Unlock()
	for _, key := range handle.keys {
		reservation, ok := r.resolverDeliveries[key]
		if !ok || reservation.token != handle.token {
			continue
		}
		if delivered {
			reservation.expiresAt = expiresAt
			r.resolverDeliveries[key] = reservation
		} else {
			delete(r.resolverDeliveries, key)
		}
	}
}

func normalizeResolverResourceKeys(values []string) []string {
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func (r *Runtime) recordResolverDuplicateSuppressed(ctx context.Context, event MessageEvent, resourceKeys []string) {
	writer := r.appLogWriter()
	if writer == nil {
		return
	}
	logCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	_ = writer.AppendLog(logCtx, applog.Entry{
		Kind:    applog.KindOperation,
		Level:   applog.LevelInfo,
		Action:  "assistant.resolver_duplicate_suppressed",
		Message: "重复链接解析结果已抑制",
		Actor:   oneBotEventActor(event),
		Target:  event.MessageID,
		Metadata: map[string]any{
			"platform":      event.Platform,
			"profile_id":    event.ProfileID,
			"group_id":      event.GroupID,
			"user_id":       event.UserID,
			"message_id":    event.MessageID,
			"resource_keys": normalizeResolverResourceKeys(resourceKeys),
			"ttl_seconds":   int(resolverDeliveryDedupeTTL / time.Second),
		},
	})
}
