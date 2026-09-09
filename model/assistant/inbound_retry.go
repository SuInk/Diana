package assistant

import (
	"context"
	"fmt"
	"log"
	"time"
)

// Retry persistence is independent of SQLite and survives process restarts.
type InboundRetryStore interface {
	SaveInboundRetry(context.Context, string, MessageEvent, int) error
	ReplayInboundRetries(context.Context, int) (int, error)
}

func (r *Runtime) retainFailedInbound(event MessageEvent, cause error) error {
	r.noteFailedInbound(event)
	r.mu.RLock()
	journal, ok := r.inboundStore.(InboundRetryStore)
	r.mu.RUnlock()
	if !ok {
		return fmt.Errorf("persist inbound event: %w", cause)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := journal.SaveInboundRetry(ctx, sessionKey(event), event, r.inboundPriority(event)); err != nil {
		log.Printf("diana inbound retry journal failed: profile=%s message=%s error=%v", event.ProfileID, event.MessageID, err)
		return fmt.Errorf("persist inbound event: %w; retry journal: %v", cause, err)
	}
	log.Printf("diana inbound retained for retry: profile=%s message=%s cause=%v", event.ProfileID, event.MessageID, cause)
	return nil
}

func (r *Runtime) noteFailedInbound(event MessageEvent) {
	if platform := NormalizePlatformID(event.Platform); platform != "" && platform != PlatformOneBotV11 {
		return
	}
	at := time.Unix(event.Time, 0)
	if event.Time <= 0 || at.After(time.Now()) {
		at = time.Now()
	}
	r.mu.Lock()
	if r.inboundFailedAt.IsZero() || at.Before(r.inboundFailedAt) {
		r.inboundFailedAt = at
	}
	r.mu.Unlock()
}

func (r *Runtime) takeFailedInbound() time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	at := r.inboundFailedAt
	r.inboundFailedAt = time.Time{}
	return at
}

func (r *Runtime) hasFailedInbound() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return !r.inboundFailedAt.IsZero()
}

func (r *Runtime) runInboundRetries(ctx context.Context, journal InboundRetryStore) {
	delay := time.Duration(0)
	for {
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		callCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		count, err := journal.ReplayInboundRetries(callCtx, 32)
		cancel()
		if count > 0 {
			r.wakeInboundWorkers()
			log.Printf("diana inbound retry restored: count=%d", count)
		}
		if err != nil && ctx.Err() == nil {
			log.Printf("diana inbound retry pending: error=%v", err)
			if delay < 2*time.Second {
				delay = 2 * time.Second
			} else {
				delay *= 2
			}
			if delay > 30*time.Second {
				delay = 30 * time.Second
			}
		} else {
			delay = 2 * time.Second
		}
	}
}
