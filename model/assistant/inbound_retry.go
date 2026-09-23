package assistant

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/SuInk/diana/model/applog"
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
		err := fmt.Errorf("persist inbound event: %w", cause)
		r.recordInboundIngestLog(event, "inbound_event_lost", "消息入队失败且没有重试暂存，这条消息丢了", err)
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := journal.SaveInboundRetry(ctx, sessionKey(event), event, r.inboundPriority(event)); err != nil {
		log.Printf("diana inbound retry journal failed: profile=%s message=%s error=%v", event.ProfileID, event.MessageID, err)
		lost := fmt.Errorf("persist inbound event: %w; retry journal: %v", cause, err)
		r.recordInboundIngestLog(event, "inbound_event_lost", "消息入队失败，重试暂存也没写进去，这条消息丢了", lost)
		return lost
	}
	log.Printf("diana inbound retained for retry: profile=%s message=%s cause=%v", event.ProfileID, event.MessageID, cause)
	r.recordInboundIngestLog(event, "inbound_event_retained", "消息入队失败，已暂存等待重试入队", cause)
	return nil
}

// recordInboundIngestLog 记下入队失败的消息。事件明细只列进了队列的消息，没进
// 队列的在那里一条都查不到；丢了的必须在运行日志里留下是哪一条、为什么。
func (r *Runtime) recordInboundIngestLog(event MessageEvent, action, message string, cause error) {
	writer := r.appLogWriter()
	if writer == nil {
		return
	}
	kind, level := applog.KindOperation, applog.LevelInfo
	if action == "inbound_event_lost" {
		kind, level = applog.KindError, applog.LevelError
	}
	detail := ""
	if cause != nil {
		detail = cause.Error()
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = writer.AppendLog(ctx, applog.Entry{
		Kind:    kind,
		Level:   level,
		Action:  action,
		Message: message,
		Detail:  detail,
		Actor:   oneBotEventActor(event),
		Target:  event.MessageID,
		Metadata: map[string]any{
			"profile_id": event.ProfileID,
			"platform":   event.Platform,
			"group_id":   event.GroupID,
			"user_id":    event.UserID,
			"message_id": event.MessageID,
		},
		CreatedAt: time.Now(),
	})
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
	// 暂存区回放失败会每隔几秒重试一次，运行日志只记「开始失败」和「又恢复了」这两个
	// 转折点，不然一次数据库故障就能刷上千条。
	failing := false
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
			r.recordInboundRetryLog(ctx, applog.KindOperation, applog.LevelInfo, "inbound_retry_restored",
				fmt.Sprintf("暂存的 %d 条消息已重新入队", count), nil, count)
		}
		if err != nil && ctx.Err() == nil {
			log.Printf("diana inbound retry pending: error=%v", err)
			if !failing {
				failing = true
				r.recordInboundRetryLog(ctx, applog.KindError, applog.LevelError, "inbound_retry_pending",
					"暂存的消息重新入队失败，会继续自动重试", err, 0)
			}
			if delay < 2*time.Second {
				delay = 2 * time.Second
			} else {
				delay *= 2
			}
			if delay > 30*time.Second {
				delay = 30 * time.Second
			}
		} else {
			failing = false
			delay = 2 * time.Second
		}
	}
}

func (r *Runtime) recordInboundRetryLog(ctx context.Context, kind applog.Kind, level applog.Level, action, message string, cause error, count int) {
	writer := r.appLogWriter()
	if writer == nil {
		return
	}
	detail := ""
	if cause != nil {
		detail = cause.Error()
	}
	logCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
	defer cancel()
	_ = writer.AppendLog(logCtx, applog.Entry{
		Kind:      kind,
		Level:     level,
		Action:    action,
		Message:   message,
		Detail:    detail,
		Metadata:  map[string]any{"count": count},
		CreatedAt: time.Now(),
	})
}
