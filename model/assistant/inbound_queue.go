// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/SuInk/diana/model/applog"
)

const (
	inboundPollInterval     = 500 * time.Millisecond
	inboundLeaseDuration    = 10 * time.Minute
	inboundGroupConcurrency = 3
	historyInitialDelay     = time.Second
	historyRetryDelay       = 30 * time.Second
	historyBaselineOverlap  = 5 * time.Second
	inboundReplayPadding    = 5 * time.Minute
	inboundCheckpointPeriod = 30 * time.Second
	// NapCat history calls can stall when several large responses are requested
	// concurrently. Serialize the small session set to keep backfill complete.
	historyFetchWorkers = 1
	historyPageSize     = 100
	// InboundMediaMergeWindow gives adjacent media and an explicit textual
	// follow-up enough time to become one durable turn before either can reply.
	InboundMediaMergeWindow = 15 * time.Second
)

const (
	InboundPriorityNormal    = 0
	InboundPriorityResolver  = 60
	InboundPriorityReply     = 80
	InboundPriorityTriggered = 100
	InboundPriorityMediaTurn = 110
)

// InboundReplayWindow is the maximum recovery window. Each reconnect normally
// uses the observed offline duration plus inboundReplayPadding instead.
const InboundReplayWindow = 12 * time.Hour

// InboundQueueItem is a persisted QQ message waiting to be processed.
type InboundQueueItem struct {
	ID       string
	Session  string
	Event    MessageEvent
	Attempts int
	Priority int
}

// HistorySession identifies a conversation that can be backfilled from OneBot.
type HistorySession struct {
	Kind          EventKind
	ID            string
	LastEventTime int64
}

// InboundEventStore persists inbound messages before routing or reply generation.
type InboundEventStore interface {
	EnqueueInboundEvent(ctx context.Context, session string, event MessageEvent, priority ...int) (id string, inserted bool, err error)
	ClaimNextInboundEvent(ctx context.Context, leaseOwner string, leaseUntil time.Time, groupConcurrency ...int) (InboundQueueItem, bool, error)
	CompleteInboundEvent(ctx context.Context, id string, leaseOwner string, outcome string) error
	RetryInboundEvent(ctx context.Context, id string, leaseOwner string, availableAt time.Time, lastError string) error
	ReleaseInboundLeases(ctx context.Context, leaseOwner string) error
	PendingInboundCount(ctx context.Context) (int, error)
	GroupHistoryWatermark(ctx context.Context, groupID string) (int64, bool, error)
	ListHistorySessions(ctx context.Context) ([]HistorySession, error)
}

// InboundMediaTurnStore atomically assigns adjacent media to a textual turn
// and exposes the supersession marker to the final outbound send guard.
type InboundMediaTurnStore interface {
	ClaimInboundMediaForTurn(ctx context.Context, currentID, session string, event MessageEvent, window time.Duration) ([]MessageEvent, error)
	InboundEventSuperseded(ctx context.Context, event MessageEvent) (string, bool, error)
}

var errInboundTurnSuperseded = errors.New("qqbot: inbound turn superseded by correlated follow-up")

func (r *Runtime) inboundTurnSuperseded(ctx context.Context, event MessageEvent) (string, bool) {
	r.mu.RLock()
	store, _ := r.inboundStore.(InboundMediaTurnStore)
	r.mu.RUnlock()
	if store == nil {
		return "", false
	}
	checkCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	turnID, superseded, err := store.InboundEventSuperseded(checkCtx, event)
	if err != nil {
		log.Printf("qqbot inbound supersession check failed: %v", err)
		return "", false
	}
	return turnID, superseded
}

// InboundRecoveryCheckpointStore persists the latest instant at which the QQ
// channel was known to be online, allowing restart recovery to match downtime.
type InboundRecoveryCheckpointStore interface {
	LoadInboundRecoveryCheckpoint(ctx context.Context) (time.Time, bool, error)
	SaveInboundRecoveryCheckpoint(ctx context.Context, connectedAt time.Time) error
}

// InboundEventAuditStore lets the runtime persist the human-readable routing
// decision before the durable worker marks the queue item complete.
type InboundEventAuditStore interface {
	RecordInboundEventAudit(ctx context.Context, event EventRecord) error
}

type OutboundDeliveryStage string

const (
	OutboundDeliveryGenerated     OutboundDeliveryStage = "generated"
	OutboundDeliverySendAttempted OutboundDeliveryStage = "send_attempted"
	OutboundDeliveryAcknowledged  OutboundDeliveryStage = "acknowledged"
	OutboundDeliveryEchoPersisted OutboundDeliveryStage = "echo_persisted"
	OutboundDeliveryFailed        OutboundDeliveryStage = "failed"
)

// InboundEventDeliveryAuditStore records transport evidence independently of
// the model outcome so a generated reply is never confused with a delivered one.
type InboundEventDeliveryAuditStore interface {
	RecordInboundEventDelivery(ctx context.Context, event MessageEvent, stage OutboundDeliveryStage, outboundMessageID, detail string) error
	RecordInboundEventSelfEcho(ctx context.Context, outboundMessageID string, observedAt time.Time) error
}

type InboundEventDuplicateCleanupStore interface {
	MarkLegacyInboundNamespaceDuplicates(ctx context.Context) (int64, error)
}

func (r *Runtime) runInboundCoordinator(ctx context.Context, leaseOwner string, workers int, releaseStaleLeases bool, done chan struct{}) {
	defer close(done)
	r.mu.RLock()
	store := r.inboundStore
	r.mu.RUnlock()
	if store == nil {
		return
	}
	if cleanupStore, ok := store.(InboundEventDuplicateCleanupStore); ok {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		duplicates, err := cleanupStore.MarkLegacyInboundNamespaceDuplicates(cleanupCtx)
		cancel()
		if err != nil {
			log.Printf("qqbot inbound duplicate cleanup failed: %v", err)
		} else if duplicates > 0 {
			log.Printf("qqbot inbound duplicate cleanup ignored %d namespace duplicates", duplicates)
		}
	}
	if releaseStaleLeases {
		callCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := store.ReleaseInboundLeases(callCtx, ""); err != nil {
			log.Printf("qqbot inbound stale lease recovery failed: %v", err)
		}
		cancel()
	}
	if workers <= 0 {
		workers = 1
	}
	baselineCapturedTime := time.Now()
	baselineCapturedAt := baselineCapturedTime.Unix()
	backfillBaseline, baselineErr := store.ListHistorySessions(ctx)
	backfillBaselineReady := baselineErr == nil
	if baselineErr != nil {
		log.Printf("qqbot inbound history baseline snapshot failed: %v", baselineErr)
	}
	disconnectedAt := inferredInboundDisconnectTime(backfillBaseline, baselineCapturedTime)
	recoveryStore, recoveryStoreReady := store.(InboundRecoveryCheckpointStore)
	if recoveryStoreReady {
		checkpointCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		checkpoint, ok, checkpointErr := recoveryStore.LoadInboundRecoveryCheckpoint(checkpointCtx)
		cancel()
		if checkpointErr != nil {
			log.Printf("qqbot inbound recovery checkpoint load failed: %v", checkpointErr)
		} else if ok && !checkpoint.IsZero() && !checkpoint.After(baselineCapturedTime) {
			disconnectedAt = checkpoint
		}
	}
	saveRecoveryCheckpoint := func(at time.Time) {
		if !recoveryStoreReady || at.IsZero() {
			return
		}
		checkpointCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := recoveryStore.SaveInboundRecoveryCheckpoint(checkpointCtx, at); err != nil {
			log.Printf("qqbot inbound recovery checkpoint save failed: %v", err)
		}
		cancel()
	}

	var workerWG sync.WaitGroup
	var backfillWG sync.WaitGroup
	type historyBackfillResult struct {
		err       error
		sessions  []HistorySession
		checkedAt int64
	}
	backfillResult := make(chan historyBackfillResult, 1)
	backfillRunning := false
	backfillRequested := false
	nextBackfillAt := time.Time{}
	var observedConnectionEpoch uint64
	var observedDuplicateConnections uint64
	launchBackfill := func() {
		if backfillRunning {
			backfillRequested = true
			return
		}
		backfillRunning = true
		r.recordOneBotConnectionLifecycle(ctx, r.channelStatus(), "backfill_started", "OneBot 断线消息回补已开始", nil)
		cutoff := r.inboundReplayCutoffAt(time.Now())
		baseline := historyBackfillBaselineWithPadding(backfillBaseline, cutoff)
		baselineReady := backfillBaselineReady
		fallbackWatermark := historyBackfillWatermarkWithPadding(baselineCapturedAt, cutoff)
		checkedAt := time.Now().Unix()
		backfillWG.Add(1)
		go func() {
			defer backfillWG.Done()
			var sessions []HistorySession
			var err error
			if baselineReady {
				sessions, err = r.backfillInboundHistoryFromSessions(ctx, store, baseline, fallbackWatermark)
			} else {
				err = r.backfillInboundHistory(ctx, store)
			}
			select {
			case backfillResult <- historyBackfillResult{err: err, sessions: sessions, checkedAt: checkedAt}:
			case <-ctx.Done():
			}
		}()
	}
	for i := 0; i < workers; i++ {
		workerWG.Add(1)
		go func() {
			defer workerWG.Done()
			r.runInboundWorker(ctx, leaseOwner, store)
		}()
	}

	ticker := time.NewTicker(inboundPollInterval)
	defer ticker.Stop()
	connected := false
	lastConnectedAt := time.Time{}
	nextCheckpointAt := time.Time{}
	for {
		select {
		case <-ctx.Done():
			r.setInboundReady(false)
			if connected {
				saveRecoveryCheckpoint(time.Now())
			}
			workerWG.Wait()
			backfillWG.Wait()
			releaseCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			if err := store.ReleaseInboundLeases(releaseCtx, leaseOwner); err != nil {
				log.Printf("qqbot inbound lease release failed: %v", err)
			}
			cancel()
			return
		case result := <-backfillResult:
			backfillRunning = false
			if result.err != nil && ctx.Err() == nil {
				log.Printf("qqbot inbound history backfill incomplete: %v", result.err)
				r.recordOneBotConnectionLifecycle(ctx, r.channelStatus(), "backfill_failed", "OneBot 断线消息回补失败", result.err)
				nextBackfillAt = time.Now().Add(historyRetryDelay)
			} else {
				r.recordOneBotConnectionLifecycle(ctx, r.channelStatus(), "backfill_completed", "OneBot 断线消息回补已完成", nil)
				nextBackfillAt = time.Time{}
			}
			if backfillRequested && ctx.Err() == nil && r.channelStatus().Connected {
				backfillRequested = false
				launchBackfill()
			} else if result.err == nil && len(result.sessions) > 0 {
				watermark := result.checkedAt - int64(historyBaselineOverlap/time.Second)
				backfillBaseline = advanceHistoryBackfillBaseline(result.sessions, watermark)
				backfillBaselineReady = true
				baselineCapturedAt = result.checkedAt
			}
		case <-ticker.C:
			status := r.channelStatus()
			now := time.Now()
			if status.DuplicateConnections > observedDuplicateConnections {
				r.recordOneBotConnectionLifecycle(ctx, status, "duplicate_client_conflict", "已拒绝重复 OneBot 客户端连接", nil)
				observedDuplicateConnections = status.DuplicateConnections
			}
			if !status.Connected {
				if connected {
					r.recordOneBotConnectionLifecycle(ctx, status, "disconnected", "OneBot 客户端已断开", nil)
					disconnectedAt = lastConnectedAt
					if disconnectedAt.IsZero() {
						disconnectedAt = now
					}
					saveRecoveryCheckpoint(disconnectedAt)
				}
				connected = false
				r.setInboundReady(false)
				continue
			}
			epochChanged := status.ConnectionEpoch != 0 && observedConnectionEpoch != 0 && status.ConnectionEpoch != observedConnectionEpoch
			if connected && !epochChanged {
				lastConnectedAt = now
				if nextCheckpointAt.IsZero() || !now.Before(nextCheckpointAt) {
					saveRecoveryCheckpoint(now)
					nextCheckpointAt = now.Add(inboundCheckpointPeriod)
				}
				if !nextBackfillAt.IsZero() && !now.Before(nextBackfillAt) {
					nextBackfillAt = time.Time{}
					launchBackfill()
				}
				continue
			}
			wasConnected := connected
			if epochChanged && connected {
				disconnectedAt = lastConnectedAt
				if disconnectedAt.IsZero() {
					disconnectedAt = now
				}
			}
			r.setInboundReplayCutoff(inboundReplayCutoff(disconnectedAt, now))
			connected = true
			lastConnectedAt = now
			disconnectedAt = now
			saveRecoveryCheckpoint(now)
			nextCheckpointAt = now.Add(inboundCheckpointPeriod)
			if status.ConnectionEpoch != 0 {
				observedConnectionEpoch = status.ConnectionEpoch
			}
			r.setInboundReady(true)
			r.wakeInboundWorkers()
			if wasConnected {
				r.recordOneBotConnectionLifecycle(ctx, status, "reconnected", "OneBot 连接 epoch 已变化，已安排消息回补", nil)
			} else if status.ConnectionEpoch > 1 {
				r.recordOneBotConnectionLifecycle(ctx, status, "reconnected", "OneBot 客户端已重新连接", nil)
			} else {
				r.recordOneBotConnectionLifecycle(ctx, status, "connection_opened", "OneBot 客户端已连接", nil)
			}
			if nextBackfillAt.IsZero() {
				nextBackfillAt = now.Add(historyInitialDelay)
			}
		}
	}
}

func (r *Runtime) runInboundWorker(ctx context.Context, leaseOwner string, store InboundEventStore) {
	ticker := time.NewTicker(inboundPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-r.inboundWake:
		}
		if !r.inboundProcessingReady() {
			continue
		}
		for r.inboundProcessingReady() {
			claimCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			item, ok, err := store.ClaimNextInboundEvent(claimCtx, leaseOwner, time.Now().Add(inboundLeaseDuration), inboundGroupConcurrency)
			cancel()
			if err != nil {
				if ctx.Err() == nil {
					log.Printf("qqbot inbound claim failed: %v", err)
				}
				break
			}
			if !ok {
				break
			}
			outcome, processErr := r.processInboundQueueItem(ctx, item)
			commitCtx, commitCancel := context.WithTimeout(context.Background(), 5*time.Second)
			if processErr == nil {
				err = store.CompleteInboundEvent(commitCtx, item.ID, leaseOwner, outcome)
			} else {
				nextAttempt := time.Now()
				if ctx.Err() == nil {
					nextAttempt = nextAttempt.Add(inboundRetryDelay(item.Attempts))
				}
				err = store.RetryInboundEvent(commitCtx, item.ID, leaseOwner, nextAttempt, processErr.Error())
			}
			commitCancel()
			if err != nil {
				log.Printf("qqbot inbound state update failed: %v", err)
			}
			if ctx.Err() != nil {
				return
			}
		}
	}
}

func (r *Runtime) processInboundQueueItem(ctx context.Context, item InboundQueueItem) (string, error) {
	if r.inboundEventIsStale(item.Event, time.Now()) {
		return "ignored_stale", nil
	}
	ctx = r.withDebugTraceContext(ctx, item.Event)
	event := item.Event
	r.mu.RLock()
	mediaTurnStore, _ := r.inboundStore.(InboundMediaTurnStore)
	r.mu.RUnlock()
	if mediaTurnStore != nil && !EventHasDirectMediaReference(event) {
		claimCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		sources, err := mediaTurnStore.ClaimInboundMediaForTurn(claimCtx, item.ID, item.Session, event, InboundMediaMergeWindow)
		cancel()
		if err != nil {
			return "", fmt.Errorf("claim inbound media turn: %w", err)
		}
		if len(sources) > 0 {
			event = attachInboundTurnMedia(event, sources)
			r.recordInboundMediaTurn(ctx, item.ID, event, sources)
		}
	}
	// Transcription happens in the durable worker, never on the OneBot ingest
	// goroutine. Only explicitly transient failures requeue this same event.
	event = r.prepareIncomingVoice(ctx, event)
	if event.voiceSTTTransient && event.voiceSTTErr != nil {
		return "", event.voiceSTTErr
	}
	if eventHasVoiceTranscript(event) {
		// Enqueue persists the transport event before STT. Upsert the enriched
		// event so later semantic references can reuse its transcript directly.
		r.persistMessageEvent(event)
	}
	event, text, handled, outcome := r.prepareMessageEvent(ctx, event)
	if !handled {
		return outcome, nil
	}
	r.mu.RLock()
	sem := r.sem
	r.mu.RUnlock()
	if sem != nil {
		select {
		case sem <- struct{}{}:
			r.incActive(1)
			defer func() {
				<-sem
				r.incActive(-1)
			}()
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	return r.replyAndRecord(ctx, event, text, outcome)
}

func eventHasVoiceTranscript(event MessageEvent) bool {
	for _, segment := range event.Segments {
		if segment.Type == "record" && strings.TrimSpace(segment.Data[voiceSTTTranscriptKey]) != "" {
			return true
		}
	}
	return event.Quoted != nil && hasVoiceTranscriptSegment(event.Quoted.Segments)
}

func hasVoiceTranscriptSegment(segments []MessageSegment) bool {
	for _, segment := range segments {
		if segment.Type == "record" && strings.TrimSpace(segment.Data[voiceSTTTranscriptKey]) != "" {
			return true
		}
	}
	return false
}

// EventHasDirectMediaReference covers media carried by this message or by an
// explicit quote. These events do not need the merge-window delay.
func EventHasDirectMediaReference(event MessageEvent) bool {
	return eventHasDirectReferenceContent(event) || quotedMessageHasReferenceContent(event.Quoted)
}

// EventIsMergeableMediaOnly reports media messages that have no independent
// textual intent and therefore benefit from the short turn-assembly hold.
func EventIsMergeableMediaOnly(event MessageEvent) bool {
	hasMedia := false
	for _, segment := range event.Segments {
		switch segment.Type {
		case "image", "video", "file", "record":
			hasMedia = true
		case "text":
			if strings.TrimSpace(segment.Data["text"]) != "" {
				return false
			}
		}
	}
	return hasMedia
}

func attachInboundTurnMedia(event MessageEvent, sources []MessageEvent) MessageEvent {
	sourceIDs := eventSemanticSourceMessageIDs(event)
	seen := make(map[string]bool)
	for _, segment := range event.Segments {
		seen[segmentMediaTurnKey(segment)] = true
	}
	for _, source := range sources {
		sourceID := strings.TrimSpace(source.MessageID)
		if sourceID != "" {
			sourceIDs = appendUniqueStrings(sourceIDs, sourceID)
		}
		for _, segment := range source.Segments {
			switch segment.Type {
			case "image", "video", "file", "record":
			default:
				continue
			}
			segment.Data = cloneSegmentData(segment.Data)
			if sourceID != "" {
				segment.Data["source_message_id"] = sourceID
			}
			key := segmentMediaTurnKey(segment)
			if seen[key] {
				continue
			}
			seen[key] = true
			event.Segments = append(event.Segments, segment)
		}
	}
	setEventSemanticSourceMessageIDs(&event, sourceIDs)
	return event
}

func segmentMediaTurnKey(segment MessageSegment) string {
	return segment.Type + "|" + firstNonEmpty(
		strings.TrimSpace(segment.Data["cached_file"]),
		strings.TrimSpace(segment.Data["url"]),
		strings.TrimSpace(segment.Data["file"]),
		strings.TrimSpace(segment.Data["path"]),
		strings.TrimSpace(segment.Data["source_message_id"]),
	)
}

func (r *Runtime) recordInboundMediaTurn(ctx context.Context, turnID string, event MessageEvent, sources []MessageEvent) {
	writer := r.appLogWriter()
	if writer == nil {
		return
	}
	mediaIDs := make([]string, 0, len(sources))
	for _, source := range sources {
		mediaIDs = appendUniqueStrings(mediaIDs, strings.TrimSpace(source.MessageID))
	}
	_ = writer.AppendLog(ctx, applog.Entry{
		Kind:    applog.KindOperation,
		Level:   applog.LevelInfo,
		Action:  "qqbot.inbound.media_turn_assembled",
		Message: "已合并相邻媒体与后续问题",
		Actor:   qqEventActor(event),
		Target:  strings.TrimSpace(event.MessageID),
		Metadata: map[string]any{
			"turn_id":              turnID,
			"trigger_message_id":   event.MessageID,
			"media_message_ids":    mediaIDs,
			"association_method":   "nearest_unconsumed_same_sender",
			"superseded_media_job": true,
		},
	})
}

func (r *Runtime) recordInboundMediaSupersededBeforeSend(ctx context.Context, event MessageEvent, turnID string) {
	writer := r.appLogWriter()
	if writer == nil {
		return
	}
	_ = writer.AppendLog(ctx, applog.Entry{
		Kind:    applog.KindOperation,
		Level:   applog.LevelInfo,
		Action:  "qqbot.inbound.media_turn_superseded",
		Message: "媒体任务已由关联问题接管，取消独立发送",
		Actor:   qqEventActor(event),
		Target:  strings.TrimSpace(event.MessageID),
		Metadata: map[string]any{
			"turn_id":               turnID,
			"media_message_id":      event.MessageID,
			"superseded_state":      "before_send",
			"outbound_acknowledged": false,
		},
	})
}

func (r *Runtime) inboundPriority(event MessageEvent) int {
	text := PlainText(event.Segments)
	if text == "" {
		text = event.RawMessage
	}
	if r.shouldHandleChat(event, text) {
		return InboundPriorityTriggered
	}
	if event.Quoted != nil {
		return InboundPriorityReply
	}
	for _, segment := range event.Segments {
		if segment.Type == "reply" {
			return InboundPriorityReply
		}
	}
	if r.shouldHandleResolver(event, text) {
		return InboundPriorityResolver
	}
	return InboundPriorityNormal
}

func (r *Runtime) inboundEventIsStale(event MessageEvent, now time.Time) bool {
	if event.Time <= 0 || now.IsZero() {
		return false
	}
	return time.Unix(event.Time, 0).Before(r.inboundReplayCutoffAt(now))
}

func inboundReplayCutoff(disconnectedAt, reconnectedAt time.Time) time.Time {
	if reconnectedAt.IsZero() {
		reconnectedAt = time.Now()
	}
	if disconnectedAt.IsZero() || disconnectedAt.After(reconnectedAt) {
		disconnectedAt = reconnectedAt
	}
	cutoff := disconnectedAt.Add(-inboundReplayPadding)
	earliest := reconnectedAt.Add(-InboundReplayWindow)
	if cutoff.Before(earliest) {
		return earliest
	}
	return cutoff
}

func inferredInboundDisconnectTime(sessions []HistorySession, now time.Time) time.Time {
	latest := time.Time{}
	for _, session := range sessions {
		if session.LastEventTime <= 0 {
			continue
		}
		candidate := time.Unix(session.LastEventTime, 0)
		if candidate.After(now) || !candidate.After(latest) {
			continue
		}
		latest = candidate
	}
	if latest.IsZero() {
		return now
	}
	return latest
}

func historyBackfillBaselineWithPadding(sessions []HistorySession, cutoff time.Time) []HistorySession {
	out := append([]HistorySession(nil), sessions...)
	for index := range out {
		out[index].LastEventTime = historyBackfillWatermarkWithPadding(out[index].LastEventTime, cutoff)
	}
	return out
}

func historyBackfillWatermarkWithPadding(watermark int64, cutoff time.Time) int64 {
	padded := watermark - int64(inboundReplayPadding/time.Second)
	if !cutoff.IsZero() && padded < cutoff.Unix() {
		return cutoff.Unix()
	}
	return padded
}

func inboundRetryDelay(attempts int) time.Duration {
	if attempts < 1 {
		attempts = 1
	}
	delay := 5 * time.Second
	for i := 1; i < attempts && delay < 5*time.Minute; i++ {
		delay *= 2
	}
	if delay > 5*time.Minute {
		return 5 * time.Minute
	}
	return delay
}

func (r *Runtime) channelStatus() ChannelStatus {
	r.mu.RLock()
	channel := r.channel
	currentProfileID := r.cfg.ID
	r.mu.RUnlock()
	if channel == nil {
		return ChannelStatus{}
	}
	if provider, ok := channel.(interface{ ChannelStatuses() []ChannelStatus }); ok {
		var fallback ChannelStatus
		for _, status := range provider.ChannelStatuses() {
			if !IsOneBotPlatform(status.Platform) {
				continue
			}
			if fallback.Platform == "" {
				fallback = status
			}
			if status.ProfileID == currentProfileID {
				return status
			}
		}
		if fallback.Platform != "" {
			return fallback
		}
	}
	return channel.Status()
}

func (r *Runtime) recordOneBotConnectionLifecycle(ctx context.Context, status ChannelStatus, event string, message string, eventErr error) {
	writer := r.appLogWriter()
	if writer == nil {
		return
	}
	kind := applog.KindOperation
	level := applog.LevelInfo
	detail := ""
	if eventErr != nil {
		kind = applog.KindError
		level = applog.LevelError
		detail = eventErr.Error()
	}
	if event == "duplicate_client_conflict" {
		kind = applog.KindError
		level = applog.LevelError
	}
	metadata := map[string]any{
		"connection_epoch":      status.ConnectionEpoch,
		"duplicate_connections": status.DuplicateConnections,
	}
	if status.ProfileID != "" {
		metadata["profile_id"] = status.ProfileID
	}
	if status.Platform != "" {
		metadata["platform"] = status.Platform
	}
	if status.ConnectionOwner != "" {
		metadata["connection_owner"] = status.ConnectionOwner
	}
	if status.LastRejectedClient != "" {
		metadata["rejected_client"] = status.LastRejectedClient
	}
	_ = writer.AppendLog(ctx, applog.Entry{
		Kind:      kind,
		Level:     level,
		Action:    "qqbot." + event,
		Message:   message,
		Detail:    detail,
		Target:    status.ProfileID,
		Metadata:  metadata,
		CreatedAt: time.Now(),
	})
}

func advanceHistoryBackfillBaseline(sessions []HistorySession, watermark int64) []HistorySession {
	out := append([]HistorySession(nil), sessions...)
	for index := range out {
		if out[index].LastEventTime < watermark {
			out[index].LastEventTime = watermark
		}
	}
	return out
}

func (r *Runtime) setInboundReady(ready bool) {
	r.inboundReadyMu.Lock()
	r.inboundReady = ready
	r.inboundReadyMu.Unlock()
}

func (r *Runtime) setInboundReplayCutoff(cutoff time.Time) {
	r.inboundReadyMu.Lock()
	r.inboundReplayCutoff = cutoff
	r.inboundReadyMu.Unlock()
}

func (r *Runtime) inboundReplayCutoffAt(now time.Time) time.Time {
	r.inboundReadyMu.RLock()
	cutoff := r.inboundReplayCutoff
	r.inboundReadyMu.RUnlock()
	if cutoff.IsZero() {
		return now.Add(-InboundReplayWindow)
	}
	return cutoff
}

func (r *Runtime) inboundProcessingReady() bool {
	r.inboundReadyMu.RLock()
	ready := r.inboundReady
	r.inboundReadyMu.RUnlock()
	return ready && r.channelStatus().Connected
}

func (r *Runtime) wakeInboundWorkers() {
	select {
	case r.inboundWake <- struct{}{}:
	default:
	}
}

func (r *Runtime) pendingInboundCount() int {
	r.mu.RLock()
	store := r.inboundStore
	r.mu.RUnlock()
	if store == nil {
		return 0
	}
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	count, err := store.PendingInboundCount(ctx)
	if err != nil {
		return 0
	}
	return count
}

func (r *Runtime) backfillInboundHistory(ctx context.Context, store InboundEventStore) error {
	sessions, err := store.ListHistorySessions(ctx)
	if err != nil {
		return fmt.Errorf("list history sessions: %w", err)
	}
	_, err = r.backfillInboundHistoryFromSessions(ctx, store, sessions, time.Now().Unix())
	return err
}

func (r *Runtime) backfillInboundHistoryFromSessions(ctx context.Context, store InboundEventStore, sessions []HistorySession, fallbackWatermark int64) ([]HistorySession, error) {
	byKey := make(map[string]HistorySession, len(sessions))
	globalWatermark := int64(0)
	for _, session := range sessions {
		if session.ID == "" {
			continue
		}
		byKey[historySessionKey(session.Kind, session.ID)] = session
		if session.LastEventTime > globalWatermark {
			globalWatermark = session.LastEventTime
		}
	}
	if globalWatermark <= 0 {
		globalWatermark = fallbackWatermark
		if globalWatermark <= 0 {
			globalWatermark = time.Now().Unix()
		}
	}

	var backfillErrors []error
	if data, callErr := r.callBackfillAPI(ctx, "get_group_list", map[string]any{}); callErr != nil {
		backfillErrors = append(backfillErrors, fmt.Errorf("get group list: %w", callErr))
	} else {
		for _, raw := range oneBotListItems(data) {
			item, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			id := stringFromAny(item["group_id"])
			addHistorySession(byKey, EventKindGroup, id, globalWatermark)
		}
	}
	if data, callErr := r.callBackfillAPI(ctx, "get_recent_contact", map[string]any{"count": 1000}); callErr != nil {
		backfillErrors = append(backfillErrors, fmt.Errorf("get recent contacts: %w", callErr))
	} else {
		for _, raw := range oneBotListItems(data) {
			item, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			id := firstNonEmpty(stringFromAny(item["peerUin"]), stringFromAny(item["peer_uin"]))
			switch intFromAny(item["chatType"]) {
			case 2:
				addHistorySession(byKey, EventKindGroup, id, globalWatermark)
			case 1, 99, 100:
				addHistorySession(byKey, EventKindPrivate, id, globalWatermark)
			}
		}
	}

	ordered := make([]HistorySession, 0, len(byKey))
	for _, session := range byKey {
		ordered = append(ordered, session)
	}
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].Kind == ordered[j].Kind {
			return ordered[i].ID < ordered[j].ID
		}
		return ordered[i].Kind < ordered[j].Kind
	})
	type historyFetchResult struct {
		session HistorySession
		events  []MessageEvent
		err     error
	}
	jobs := make(chan HistorySession, len(ordered))
	results := make(chan historyFetchResult, len(ordered))
	botQQ := strings.TrimSpace(r.Config().BotQQ)
	for _, session := range ordered {
		if session.Kind != EventKindPrivate || session.ID != botQQ {
			jobs <- session
		}
	}
	close(jobs)
	workerCount := historyFetchWorkers
	if workerCount > len(jobs) {
		workerCount = len(jobs)
	}
	var fetchWG sync.WaitGroup
	for i := 0; i < workerCount; i++ {
		fetchWG.Add(1)
		go func() {
			defer fetchWG.Done()
			for session := range jobs {
				events, fetchErr := r.fetchHistorySince(ctx, session)
				results <- historyFetchResult{session: session, events: events, err: fetchErr}
			}
		}()
	}
	fetchWG.Wait()
	close(results)
	for result := range results {
		if result.err != nil {
			backfillErrors = append(backfillErrors, fmt.Errorf("%s %s: %w", result.session.Kind, result.session.ID, result.err))
			continue
		}
		for _, event := range result.events {
			if event.historyRecallCandidate {
				recovered, recoverErr := r.recoverGroupRecallFromHistory(ctx, event)
				if recoverErr != nil {
					backfillErrors = append(backfillErrors, fmt.Errorf("recover backfilled recall %s: %w", event.MessageID, recoverErr))
					continue
				}
				if recovered {
					continue
				}
			}
			if r.isSelfMessage(event) {
				continue
			}
			persistCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_, inserted, persistErr := store.EnqueueInboundEvent(persistCtx, sessionKey(event), event, r.inboundPriority(event))
			cancel()
			if persistErr != nil {
				backfillErrors = append(backfillErrors, fmt.Errorf("enqueue backfilled message %s: %w", event.MessageID, persistErr))
				continue
			}
			if inserted {
				r.wakeInboundWorkers()
			}
		}
	}
	return ordered, errors.Join(backfillErrors...)
}

func (r *Runtime) callBackfillAPI(ctx context.Context, action string, params map[string]any) (map[string]any, error) {
	callCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return r.CallOneBotAPI(callCtx, action, params)
}

func addHistorySession(sessions map[string]HistorySession, kind EventKind, id string, fallbackWatermark int64) {
	id = strings.TrimSpace(id)
	if id == "" {
		return
	}
	key := historySessionKey(kind, id)
	if _, ok := sessions[key]; ok {
		return
	}
	sessions[key] = HistorySession{Kind: kind, ID: id, LastEventTime: fallbackWatermark}
}

func historySessionKey(kind EventKind, id string) string {
	return string(kind) + ":" + strings.TrimSpace(id)
}

func (r *Runtime) fetchHistorySince(ctx context.Context, session HistorySession) ([]MessageEvent, error) {
	action := "get_group_msg_history"
	idParam := "group_id"
	if session.Kind == EventKindPrivate {
		action = "get_friend_msg_history"
		idParam = "user_id"
	}
	if session.Kind != EventKindGroup && session.Kind != EventKindPrivate {
		return nil, nil
	}

	eventsByID := map[string]MessageEvent{}
	cursor := ""
	seenCursors := map[string]struct{}{}
	for {
		params := map[string]any{
			idParam:           oneBotIDParam(session.ID),
			"count":           historyPageSize,
			"reverse_order":   cursor != "",
			"disable_get_url": true,
		}
		if cursor != "" {
			params["message_seq"] = cursor
		}
		callCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		data, err := r.CallOneBotAPI(callCtx, action, params)
		cancel()
		if err != nil {
			if strings.Contains(err.Error(), "不存在") {
				break
			}
			if len(eventsByID) > 0 {
				break
			}
			return nil, err
		}
		items := oneBotHistoryItems(data)
		if len(items) == 0 {
			break
		}

		page := make([]MessageEvent, 0, len(items))
		for _, item := range items {
			event, ok := r.historyEventFromData(session, item)
			if ok {
				page = append(page, event)
			}
		}
		if len(page) == 0 {
			break
		}
		sort.Slice(page, func(i, j int) bool {
			if page[i].Time == page[j].Time {
				return page[i].MessageID < page[j].MessageID
			}
			return page[i].Time < page[j].Time
		})
		reachedWatermark := false
		for _, event := range page {
			if event.Time > 0 && event.Time < session.LastEventTime {
				reachedWatermark = true
				continue
			}
			key := firstNonEmpty(event.MessageID, event.MessageSeq)
			if key == "" {
				encoded, _ := json.Marshal(event)
				key = string(encoded)
			}
			eventsByID[key] = event
		}
		if reachedWatermark || len(items) < historyPageSize {
			break
		}
		oldest := page[0]
		nextCursor := firstNonEmpty(oldest.MessageSeq, oldest.MessageID)
		if nextCursor == "" || nextCursor == cursor {
			break
		}
		if _, exists := seenCursors[nextCursor]; exists {
			break
		}
		seenCursors[nextCursor] = struct{}{}
		cursor = nextCursor
	}

	events := make([]MessageEvent, 0, len(eventsByID))
	for _, event := range eventsByID {
		events = append(events, event)
	}
	sort.Slice(events, func(i, j int) bool {
		if events[i].Time == events[j].Time {
			return events[i].MessageID < events[j].MessageID
		}
		return events[i].Time < events[j].Time
	})
	return events, nil
}

func oneBotHistoryItems(data map[string]any) []map[string]any {
	if nested, ok := data["data"].(map[string]any); ok {
		data = nested
	}
	for _, key := range []string{"messages", "message", "items", "list"} {
		switch value := data[key].(type) {
		case []any:
			out := make([]map[string]any, 0, len(value))
			for _, raw := range value {
				if item, ok := raw.(map[string]any); ok {
					out = append(out, item)
				}
			}
			return out
		case []map[string]any:
			return value
		case map[string]any:
			return []map[string]any{value}
		}
	}
	return nil
}

func (r *Runtime) historyEventFromData(session HistorySession, data map[string]any) (MessageEvent, bool) {
	normalized := make(map[string]any, len(data)+5)
	for key, value := range data {
		normalized[key] = value
	}
	normalized["post_type"] = "message"
	if strings.TrimSpace(stringFromAny(normalized["message_type"])) == "" {
		normalized["message_type"] = string(session.Kind)
	}
	if session.Kind == EventKindGroup && strings.TrimSpace(stringFromAny(normalized["group_id"])) == "" {
		normalized["group_id"] = session.ID
	}
	if strings.TrimSpace(stringFromAny(normalized["self_id"])) == "" {
		normalized["self_id"] = r.Config().BotQQ
	}
	payload, err := json.Marshal(normalized)
	if err != nil {
		return MessageEvent{}, false
	}
	var envelope oneBotEnvelope
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return MessageEvent{}, false
	}
	event := messageEventFromEnvelope(envelope)
	if event.Kind == "" {
		return MessageEvent{}, false
	}
	if event.MessageSeq == "" {
		event.MessageSeq = firstNonEmpty(stringFromAny(data["message_seq"]), stringFromAny(data["real_id"]))
	}
	if event.MessageID == "" {
		event.MessageID = event.MessageSeq
	}
	if event.Kind == EventKindPrivate && event.UserID == "" {
		event.UserID = session.ID
	}
	event.historyRecallCandidate = session.Kind == EventKindGroup && historyMessageIsEmpty(data, event)
	return event, true
}

func historyMessageIsEmpty(data map[string]any, event MessageEvent) bool {
	if strings.TrimSpace(event.MessageID) == "" || strings.TrimSpace(event.RawMessage) != "" || len(event.Segments) != 0 {
		return false
	}
	value, exists := data["message"]
	if !exists {
		return false
	}
	switch typed := value.(type) {
	case nil:
		return true
	case string:
		return strings.TrimSpace(typed) == ""
	case []any:
		return len(typed) == 0
	case []map[string]any:
		return len(typed) == 0
	default:
		return false
	}
}

const historyBackfillOperatorRole = "history_backfill"

func (r *Runtime) recoverGroupRecallFromHistory(ctx context.Context, candidate MessageEvent) (bool, error) {
	if candidate.Kind != EventKindGroup || strings.TrimSpace(candidate.GroupID) == "" || strings.TrimSpace(candidate.MessageID) == "" {
		return false, nil
	}
	r.mu.RLock()
	store := r.messageStore
	r.mu.RUnlock()
	lookup, ok := store.(MessageEventLookupStore)
	if !ok {
		return false, nil
	}

	loadCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	original, found, err := lookup.FindMessageEvent(loadCtx, sessionKey(candidate), candidate.MessageID)
	cancel()
	if err != nil {
		return false, err
	}
	if !found || original.Kind != EventKindGroup || !recallEventHasContent(original) {
		return false, nil
	}
	if candidate.UserID != "" && original.UserID != "" && candidate.UserID != original.UserID {
		return false, nil
	}
	if recallStore, ok := store.(GroupRecallHistoryStore); ok {
		listCtx, listCancel := context.WithTimeout(ctx, 2*time.Second)
		recalls, listErr := recallStore.ListGroupRecallEvents(listCtx, candidate.GroupID)
		listCancel()
		if listErr != nil {
			return false, listErr
		}
		for _, recall := range recalls {
			if recall.MessageID == candidate.MessageID {
				return true, nil
			}
		}
	}

	recall := MessageEvent{
		Platform:         firstNonEmpty(candidate.Platform, original.Platform),
		ProfileID:        firstNonEmpty(candidate.ProfileID, original.ProfileID),
		ContextNamespace: firstNonEmpty(candidate.ContextNamespace, original.ContextNamespace),
		Kind:             EventKindNotice,
		SubType:          "group_recall",
		Time:             time.Now().Unix(),
		OriginalTime:     original.Time,
		SelfID:           firstNonEmpty(candidate.SelfID, original.SelfID),
		UserID:           firstNonEmpty(original.UserID, candidate.UserID),
		OperatorRole:     historyBackfillOperatorRole,
		GroupID:          original.GroupID,
		MessageID:        original.MessageID,
		MessageSeq:       firstNonEmpty(original.MessageSeq, candidate.MessageSeq),
		MessageType:      original.MessageType,
		RawMessage:       original.RawMessage,
		Segments:         append([]MessageSegment(nil), original.Segments...),
		SenderName:       original.SenderName,
		SenderRole:       original.SenderRole,
		SenderLevel:      original.SenderLevel,
		SenderLevelLabel: original.SenderLevelLabel,
		SenderTitle:      original.SenderTitle,
		Quoted:           original.Quoted,
	}
	if err := r.HandleEvent(ctx, recall); err != nil {
		return false, err
	}
	return true, nil
}
