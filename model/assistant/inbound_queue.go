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
	// inboundPollInterval 是协调者那一圈的节拍：连接状态、回补调度都跟着它走，
	// 和 worker 领活没关系，保持原样。
	inboundPollInterval = 500 * time.Millisecond
	// inboundWorkerPollInterval 是 worker 兜底轮询的起始间隔，不是响应延迟：新事件
	// 进来会敲 inboundWake，worker 立刻醒。轮询只覆盖三种拿不到唤醒的情况——
	//   1. 唤醒丢了：inboundWake 是缓冲 1 的非阻塞发送，所有 worker 都在忙时后续
	//      唤醒会被丢弃。影响也就是慢一拍：worker 领到一条之后会在内层循环里一直
	//      领到队列空，不会漏；
	//   2. 租约过期要重投（inboundLeaseDuration 10 分钟，分钟级）；
	//   3. 重试到期（走 inboundRetryDelay 的退避表，秒级起；实际到期时
	//      ReplayInboundRetries 也会显式唤醒）。
	// 三种都不需要亚秒级粒度。
	//
	// worker 这一路原来直接复用协调者的 500 毫秒，是入站队列第一版随手写的，没有
	// 依据。对齐同样「有推送通知 + 轮询兜底」的成熟实现：River（Go + Postgres，
	// LISTEN/NOTIFY）默认 FetchPollInterval 1 秒，GoodJob（Rails + Postgres，同样有
	// NOTIFY）默认 10 秒；而没有推送、只能靠轮询发现的 Solid Queue，worker 默认
	// 0.1 秒。Diana 有唤醒通道，属于前一类，取 2 秒。
	inboundWorkerPollInterval = 2 * time.Second
	// inboundWorkerPollMax 是空闲时的上限，空手而归就翻倍一路退到这里。30 秒是按
	// 「三条唤醒路径全失灵时最久等多久」定的，不是按响应速度定的。
	inboundWorkerPollMax    = 30 * time.Second
	inboundLeaseDuration    = 10 * time.Minute
	historyInitialDelay     = time.Second
	historyRetryDelay       = 30 * time.Second
	historyBaselineOverlap  = 5 * time.Second
	inboundReplayPadding    = 30 * time.Minute
	inboundCheckpointPeriod = 30 * time.Second
	// OneBot history calls can stall when several large responses are requested
	// concurrently. Serialize the small session set to keep backfill complete.
	historyFetchWorkers = 1
	historyPageSize     = 100
	// historyBackfillScanLimit 是单个会话一次回补最多往回扫多少条。回补现在要一直翻到
	// 断线前的水位线把漏掉的消息都补进历史，热闹的群一天能攒几千条；这里兜住上限，
	// 更早的不再补。上下文窗口本来也用不了这么多，多出来的只会变成摘要任务。
	historyBackfillScanLimit = 200
)

const (
	InboundPriorityNormal    = 0
	InboundPriorityResolver  = 60
	InboundPriorityReply     = 80
	InboundPriorityTriggered = 100
)

const (
	// inboundMaxAttempts 是同一条入站事件默认的最大处理次数（可按机器人/分群配置，见
	// send_retry_policy.go）。超过后落终态，避免
	// 一条永远失败的消息按退避节奏无限重跑。
	inboundMaxAttempts = 5
	// inboundOutcomeRetriesExhausted 标记因重试次数用尽而停止的事件。
	inboundOutcomeRetriesExhausted = "dropped_retries_exhausted"
	// inboundOutcomeSendRejected 标记上游明确拒收、重试也不可能成功的事件。
	// 它和上面那条的区别是「已经知道没救了」：不必再跑满五次。
	inboundOutcomeSendRejected = "dropped_send_rejected"
	// inboundOutcomeDroppedOutboundUnconfirmed 标记回复已经写给接入端、却确认不了
	// 送没送到的事件：不重发、不重新生成，免得同一条回复刷两遍。
	inboundOutcomeDroppedOutboundUnconfirmed = "dropped_outbound_unconfirmed"
	// inboundOutcomeSupersededReplyTurn 标记已经并进另一轮回复（追发合并）、自己
	// 不再单独发送的消息。
	inboundOutcomeSupersededReplyTurn = "superseded_reply_turn"
	// inboundOutcomeLegacySupersededMediaTurn 是旧版相邻媒体合并留下的终态，只读不写。
	inboundOutcomeLegacySupersededMediaTurn = "superseded_media_turn"
)

// InboundReplayWindow is the maximum recovery window. Each reconnect normally
// uses the observed offline duration plus inboundReplayPadding instead.
const InboundReplayWindow = 24 * time.Hour

// InboundConcurrency 是同一会话允许同时处理的入站事件数，按会话类型分开。
// 以前这两个数是写死的（群 3，私聊在 SQL 里直接写成 1），现在由配置决定；
// 零值仍然退回原来的默认，所以没配过的部署行为不变。
type InboundConcurrency struct {
	Group   int
	Private int
}

// inboundConcurrencyForConfig 把配置翻译成队列认识的形状。
// inboundConcurrency 是共用入站队列的并发上限，取各台启用机器人里最大的设置。
func (r *Runtime) inboundConcurrency() InboundConcurrency {
	r.mu.RLock()
	enabled := r.enabledProfilesLocked()
	r.mu.RUnlock()
	limits := inboundConcurrencyForConfig(DefaultBotConfig())
	for i, profile := range enabled {
		next := inboundConcurrencyForConfig(profile)
		if i == 0 {
			limits = next
			continue
		}
		limits.Group = max(limits.Group, next.Group)
		limits.Private = max(limits.Private, next.Private)
	}
	return limits
}

// historyBackfillProfile 返回负责 OneBot 历史回填的那台机器人的配置。
func (r *Runtime) historyBackfillProfile() BotConfig {
	r.mu.RLock()
	channel := r.channel
	r.mu.RUnlock()
	if multi, ok := channel.(*MultiChannel); ok {
		if binding, found := multi.OneBotBinding(); found {
			return r.profileConfig(binding.ProfileID)
		}
	}
	if profile, err := r.soleOneBotProfile(); err == nil {
		return profile
	}
	return r.profileConfig("")
}

func inboundConcurrencyForConfig(cfg BotConfig) InboundConcurrency {
	limits := InboundConcurrency{Group: cfg.InboundGroupConcurrency, Private: cfg.InboundPrivateConcurrency}
	if limits.Group <= 0 {
		limits.Group = defaultInboundGroupConcurrency
	}
	if limits.Private <= 0 {
		limits.Private = defaultInboundPrivateConcurrency
	}
	return limits
}

// InboundQueueItem is a persisted inbound message waiting to be processed.
type InboundQueueItem struct {
	ID       string
	Session  string
	Event    MessageEvent
	Attempts int
	Priority int
	// EnqueuedAt 是这条消息进队列的时刻。积压判断看的是它排了多久，而不是消息本身发出多久：
	// 断线回补的消息发出时间天然很早，但它们是刚进队的，不能被当成积压扔掉。
	EnqueuedAt time.Time
}

// HistorySession identifies a conversation that can be backfilled from OneBot.
type HistorySession struct {
	Kind          EventKind
	ID            string
	Platform      string
	ProfileID     string
	LastEventTime int64
}

// InboundLeaseExtender 是可选能力：处理中途确定还要等一阵（比如发送结果不明、
// 在等回推确认）时把租约往后推，免得租约到期被另一个 worker 领走重新生成一遍。
type InboundLeaseExtender interface {
	ExtendInboundLease(ctx context.Context, id string, leaseOwner string, leaseUntil time.Time) error
}

type inboundLeaseExtensionContextKey struct{}

// withInboundLeaseExtension 让这条入站事件的处理链路能延长自己的租约。
// leaseUntil 是领取时的租约到期时间；够用的时候不写库，只有真要往后推才写。
func withInboundLeaseExtension(ctx context.Context, store InboundEventStore, id, leaseOwner string, leaseUntil time.Time) context.Context {
	extender, ok := store.(InboundLeaseExtender)
	if !ok || strings.TrimSpace(id) == "" {
		return ctx
	}
	var mu sync.Mutex
	held := leaseUntil
	extend := func(until time.Time) error {
		mu.Lock()
		defer mu.Unlock()
		if !until.After(held) {
			return nil
		}
		// 多推一分钟，免得每条分片都写一次库。
		until = until.Add(time.Minute)
		extendCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := extender.ExtendInboundLease(extendCtx, id, leaseOwner, until); err != nil {
			return err
		}
		held = until
		return nil
	}
	return context.WithValue(ctx, inboundLeaseExtensionContextKey{}, extend)
}

// extendInboundLease 把当前入站事件的租约延到至少 now+d；不在入站处理链路里时什么也不做。
func extendInboundLease(ctx context.Context, d time.Duration) {
	extend, ok := ctx.Value(inboundLeaseExtensionContextKey{}).(func(time.Time) error)
	if !ok {
		return
	}
	if err := extend(time.Now().Add(d)); err != nil {
		log.Printf("diana inbound lease extension failed: %v", err)
	}
}

// InboundEventStore persists inbound messages before routing or reply generation.
type InboundEventStore interface {
	EnqueueInboundEvent(ctx context.Context, session string, event MessageEvent, priority ...int) (id string, inserted bool, err error)
	ClaimNextInboundEvent(ctx context.Context, leaseOwner string, leaseUntil time.Time, limits ...InboundConcurrency) (InboundQueueItem, bool, error)
	CompleteInboundEvent(ctx context.Context, id string, leaseOwner string, outcome string) error
	RetryInboundEvent(ctx context.Context, id string, leaseOwner string, availableAt time.Time, lastError string) error
	ReleaseInboundLeases(ctx context.Context, leaseOwner string) error
	PendingInboundCount(ctx context.Context) (int, error)
	GroupHistoryWatermark(ctx context.Context, groupID string) (int64, bool, error)
	ListHistorySessions(ctx context.Context) ([]HistorySession, error)
}

// InboundSupersessionStore 把「这条消息已经并进别的回复轮」的标记交给最后那道
// 发送闸门。标记由追发合并写入（RecordInboundEventReplyMerge）：被并进去的那条
// 自己的任务如果还是走到了发送，就在这里拦下，别把同一个问题答两遍。
type InboundSupersessionStore interface {
	InboundEventSuperseded(ctx context.Context, event MessageEvent) (string, bool, error)
}

var errInboundTurnSuperseded = errors.New("diana: inbound turn superseded by correlated follow-up")

func (r *Runtime) inboundTurnSuperseded(ctx context.Context, event MessageEvent) (string, bool) {
	r.mu.RLock()
	store, _ := r.inboundStore.(InboundSupersessionStore)
	r.mu.RUnlock()
	if store == nil {
		return "", false
	}
	checkCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	turnID, superseded, err := store.InboundEventSuperseded(checkCtx, event)
	if err != nil {
		log.Printf("diana inbound supersession check failed: %v", err)
		return "", false
	}
	return turnID, superseded
}

// InboundRecoveryCheckpointStore persists the latest instant at which the bot
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

// InboundEventDeliveryByIDStore 是可选能力：知道入站事件 id 时按主键推进投递审计。
type InboundEventDeliveryByIDStore interface {
	RecordInboundEventDeliveryByID(ctx context.Context, inboundEventID string, event MessageEvent, stage OutboundDeliveryStage, outboundMessageID, detail string) error
}

// InboundEventDeliveryAuditStore records transport evidence independently of
// the model outcome so a generated reply is never confused with a delivered one.
type InboundEventDeliveryAuditStore interface {
	RecordInboundEventDelivery(ctx context.Context, event MessageEvent, stage OutboundDeliveryStage, outboundMessageID, detail string) error
	// RecordInboundEventSelfEcho 按回推的账号、会话和 message_id 精确关联回入站事件。
	RecordInboundEventSelfEcho(ctx context.Context, echo MessageEvent, observedAt time.Time) error
}

func (r *Runtime) runInboundCoordinator(ctx context.Context, leaseOwner string, workers int, releaseStaleLeases bool, done chan struct{}) {
	defer close(done)
	r.mu.RLock()
	store := r.inboundStore
	r.mu.RUnlock()
	if store == nil {
		return
	}
	if releaseStaleLeases {
		callCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := store.ReleaseInboundLeases(callCtx, ""); err != nil {
			log.Printf("diana inbound stale lease recovery failed: %v", err)
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
		log.Printf("diana inbound history baseline snapshot failed: %v", baselineErr)
	}
	disconnectedAt := inferredInboundDisconnectTime(backfillBaseline, baselineCapturedTime)
	recoveryStore, recoveryStoreReady := store.(InboundRecoveryCheckpointStore)
	if recoveryStoreReady {
		checkpointCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		checkpoint, ok, checkpointErr := recoveryStore.LoadInboundRecoveryCheckpoint(checkpointCtx)
		cancel()
		if checkpointErr != nil {
			log.Printf("diana inbound recovery checkpoint load failed: %v", checkpointErr)
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
			log.Printf("diana inbound recovery checkpoint save failed: %v", err)
		}
		cancel()
	}

	var workerWG sync.WaitGroup
	var backfillWG sync.WaitGroup
	type historyBackfillResult struct {
		err       error
		sessions  []HistorySession
		stats     historyBackfillStats
		checkedAt int64
	}
	backfillResult := make(chan historyBackfillResult, 1)
	backfillRunning := false
	backfillRequested := false
	// pendingManualFloor keeps a manual rewind alive when it arrives while a
	// backfill is already running: the completion handler re-applies it after
	// advancing the baseline, so the queued rerun still covers the window.
	pendingManualFloor := int64(0)
	nextBackfillAt := time.Time{}
	// followUps 是重连后还要补跑的整体回补时间点，followUpFloor 是补跑时水位退回到的位置。
	var followUps []time.Time
	followUpFloor := int64(0)
	nextIngestRecoveryAt := time.Time{}
	var observedConnectionEpoch uint64
	var observedDuplicateConnections uint64
	launchBackfill := func() {
		if backfillRunning {
			backfillRequested = true
			return
		}
		backfillRunning = true
		r.historyBackfillBusy.Store(true)
		r.recordOneBotConnectionLifecycle(ctx, r.channelStatus(), "backfill_started", "OneBot 断线消息回补已开始", nil)
		cutoff := r.inboundReplayCutoffAt(time.Now())
		baseline := historyBackfillBaselineWithPadding(backfillBaseline, cutoff)
		baselineReady := backfillBaselineReady
		fallbackWatermark := historyBackfillWatermarkWithPadding(baselineCapturedAt, cutoff)
		checkedAt := time.Now().Unix()
		backfillWG.Add(1)
		go func() {
			defer recoverGoroutinePanic("inbound_queue.go:223")
			defer backfillWG.Done()
			var sessions []HistorySession
			var stats historyBackfillStats
			var err error
			if baselineReady {
				sessions, stats, err = r.backfillInboundHistorySessions(ctx, store, baseline, fallbackWatermark)
			} else {
				stats, err = r.backfillInboundHistoryWithStats(ctx, store)
			}
			select {
			case backfillResult <- historyBackfillResult{err: err, sessions: sessions, stats: stats, checkedAt: checkedAt}:
			case <-ctx.Done():
			}
		}()
	}
	if journal, ok := store.(InboundRetryStore); ok {
		workerWG.Add(1)
		go func() {
			defer workerWG.Done()
			defer recoverGoroutinePanic("inbound.retry")
			r.runInboundRetries(ctx, journal)
		}()
	}
	for i := 0; i < workers; i++ {
		workerWG.Add(1)
		go func() {
			defer recoverGoroutinePanic("inbound_queue.go:240")
			defer workerWG.Done()
			r.runInboundWorker(ctx, leaseOwner, store)
		}()
	}

	// 上一个进程留下的待定连发交接没有哪一轮还能替它落定，先放回去（见 inbound_handoff.go）。
	r.sweepInboundHandoffs(ctx)
	nextHandoffSweepAt := time.Now().Add(inboundHandoffSweepPeriod)
	ticker := time.NewTicker(inboundPollInterval)
	defer ticker.Stop()
	connected := false
	offlineWasAccountOnly := false
	lastConnectedAt := time.Time{}
	nextCheckpointAt := time.Time{}
	for {
		select {
		case <-ctx.Done():
			r.setInboundReady(false)
			r.historyBackfillBusy.Store(false)
			// A pending or running backfill means the missed window has not been
			// persisted yet; advancing the checkpoint now would erase it on restart.
			if connected && !backfillRunning && !backfillRequested && nextBackfillAt.IsZero() && len(followUps) == 0 && r.seqGapActive.Load() == 0 && !r.hasFailedInbound() {
				saveRecoveryCheckpoint(time.Now())
			}
			workerWG.Wait()
			backfillWG.Wait()
			releaseCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			if err := store.ReleaseInboundLeases(releaseCtx, leaseOwner); err != nil {
				log.Printf("diana inbound lease release failed: %v", err)
			}
			cancel()
			return
		case window := <-r.inboundManualBackfill:
			status := r.channelStatus()
			if !channelEffectivelyOnline(status) {
				r.recordOneBotConnectionLifecycle(ctx, status, "backfill_manual_rejected", "手动回补已跳过：OneBot 连接或账号当前不在线", nil)
				continue
			}
			if window <= 0 || window > InboundReplayWindow {
				window = InboundReplayWindow
			}
			now := time.Now()
			manualCutoff := now.Add(-window)
			// Only ever lower the replay cutoff: raising it would mark messages a
			// pending reconnect backfill still owes as stale.
			if manualCutoff.Before(r.inboundReplayCutoffAt(now)) {
				r.setInboundReplayCutoff(manualCutoff)
			}
			backfillBaseline = rewindHistoryBackfillBaseline(backfillBaseline, manualCutoff.Unix())
			if backfillRunning && (pendingManualFloor == 0 || manualCutoff.Unix() < pendingManualFloor) {
				pendingManualFloor = manualCutoff.Unix()
			}
			r.recordOneBotConnectionLifecycle(ctx, status, "backfill_manual_requested", fmt.Sprintf("手动回补已触发，覆盖最近 %s 的消息", window), nil)
			launchBackfill()
		case probe := <-r.inboundSeqProbe:
			r.startGroupSeqGapCheck(ctx, store, probe, &backfillWG)
		case result := <-backfillResult:
			backfillRunning = false
			r.historyBackfillBusy.Store(false)
			if result.err != nil && ctx.Err() == nil {
				log.Printf("diana inbound history backfill incomplete: %v", result.err)
				r.recordOneBotConnectionLifecycleWithMetadata(ctx, r.channelStatus(), "backfill_failed", "OneBot 断线消息回补失败", result.err, result.stats.metadata())
				nextBackfillAt = time.Now().Add(historyRetryDelay)
			} else {
				r.recordOneBotConnectionLifecycleWithMetadata(ctx, r.channelStatus(), "backfill_completed",
					fmt.Sprintf("OneBot 断线消息回补已完成：拉取 %d 条，新入库 %d 条", result.stats.Fetched, result.stats.Inserted), nil, result.stats.metadata())
				nextBackfillAt = time.Time{}
			}
			if result.err == nil && len(result.sessions) > 0 {
				watermark := result.checkedAt - int64(historyBaselineOverlap/time.Second)
				backfillBaseline = advanceHistoryBackfillBaseline(result.sessions, watermark)
				backfillBaselineReady = true
				baselineCapturedAt = result.checkedAt
			}
			if pendingManualFloor > 0 {
				backfillBaseline = rewindHistoryBackfillBaseline(backfillBaseline, pendingManualFloor)
				pendingManualFloor = 0
			}
			if backfillRequested && ctx.Err() == nil && channelEffectivelyOnline(r.channelStatus()) {
				backfillRequested = false
				launchBackfill()
			}
		case <-ticker.C:
			status := r.channelStatus()
			now := time.Now()
			if !now.Before(nextHandoffSweepAt) {
				nextHandoffSweepAt = now.Add(inboundHandoffSweepPeriod)
				r.sweepInboundHandoffs(ctx)
			}
			if status.DuplicateConnections > observedDuplicateConnections {
				r.recordOneBotConnectionLifecycle(ctx, status, "duplicate_client_conflict", "已拒绝重复 OneBot 客户端连接", nil)
				observedDuplicateConnections = status.DuplicateConnections
			}
			// A banned or logged-out bot account misses messages exactly like a
			// dropped WebSocket, so heartbeat-reported account state shares the
			// disconnect/reconnect path instead of only reaching the status page.
			if !channelEffectivelyOnline(status) {
				if connected {
					event, message := "disconnected", "OneBot 客户端已断开"
					if status.Connected {
						event, message = "account_offline", "账号已离线或状态异常（连接仍在），恢复后将回补此期间消息"
					}
					r.recordOneBotConnectionLifecycle(ctx, status, event, message, nil)
					disconnectedAt = lastConnectedAt
					if disconnectedAt.IsZero() {
						disconnectedAt = now
					}
					saveRecoveryCheckpoint(disconnectedAt)
				}
				offlineWasAccountOnly = status.Connected
				connected = false
				r.setInboundReady(false)
				continue
			}
			if connected && !now.Before(nextIngestRecoveryAt) {
				if failedAt := r.takeFailedInbound(); !failedAt.IsZero() {
					nextIngestRecoveryAt = now.Add(30 * time.Second)
					cutoff := inboundReplayCutoff(failedAt, now)
					if cutoff.Before(r.inboundReplayCutoffAt(now)) {
						r.setInboundReplayCutoff(cutoff)
					}
					backfillBaseline = rewindHistoryBackfillBaseline(backfillBaseline, cutoff.Unix())
					if backfillRunning && (pendingManualFloor == 0 || cutoff.Unix() < pendingManualFloor) {
						pendingManualFloor = cutoff.Unix()
					}
					r.recordOneBotConnectionLifecycle(ctx, status, "backfill_ingest_recovery", "入队失败已自动安排消息回补", nil)
					launchBackfill()
				}
			}
			epochChanged := status.ConnectionEpoch != 0 && observedConnectionEpoch != 0 && status.ConnectionEpoch != observedConnectionEpoch
			if connected && !epochChanged {
				lastConnectedAt = now
				recoveryDebt := backfillRunning || backfillRequested || !nextBackfillAt.IsZero() || len(followUps) > 0 || r.seqGapActive.Load() > 0 || r.hasFailedInbound()
				if !recoveryDebt && (nextCheckpointAt.IsZero() || !now.Before(nextCheckpointAt)) {
					saveRecoveryCheckpoint(now)
					nextCheckpointAt = now.Add(inboundCheckpointPeriod)
				}
				if !nextBackfillAt.IsZero() && !now.Before(nextBackfillAt) {
					nextBackfillAt = time.Time{}
					launchBackfill()
				}
				// QQ 刚登录时离线消息可能还没同步到本地，第一次回补会「成功」地什么也拿不到。
				// 按断线窗口再补跑几轮，已入库的消息由入站去重挡住。
				if len(followUps) > 0 && !now.Before(followUps[0]) && !backfillRunning && !backfillRequested && nextBackfillAt.IsZero() {
					followUps = followUps[1:]
					backfillBaseline = rewindHistoryBackfillBaseline(backfillBaseline, followUpFloor)
					r.recordOneBotConnectionLifecycle(ctx, status, "backfill_follow_up", "重连后补跑消息回补，接住登录后才同步到的离线消息", nil)
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
			reconnectCutoff := inboundReplayCutoff(disconnectedAt, now)
			r.setInboundReplayCutoff(reconnectCutoff)
			followUpFloor = reconnectCutoff.Unix()
			followUps = followUps[:0]
			for _, delay := range historyFollowUpDelays {
				followUps = append(followUps, now.Add(delay))
			}
			r.armGroupSeqProbes()
			connected = true
			lastConnectedAt = now
			disconnectedAt = now
			previousEpoch := observedConnectionEpoch
			if status.ConnectionEpoch != 0 {
				observedConnectionEpoch = status.ConnectionEpoch
			}
			r.setInboundReady(true)
			r.wakeInboundWorkers()
			if wasConnected {
				r.recordOneBotConnectionLifecycle(ctx, status, "reconnected", "OneBot 连接 epoch 已变化，已安排消息回补", nil)
			} else if offlineWasAccountOnly && status.ConnectionEpoch == previousEpoch {
				r.recordOneBotConnectionLifecycle(ctx, status, "account_recovered", "账号已恢复在线，已安排消息回补", nil)
			} else if status.ConnectionEpoch > 1 {
				r.recordOneBotConnectionLifecycle(ctx, status, "reconnected", "OneBot 客户端已重新连接", nil)
			} else {
				r.recordOneBotConnectionLifecycle(ctx, status, "connection_opened", "OneBot 客户端已连接", nil)
			}
			offlineWasAccountOnly = false
			if nextBackfillAt.IsZero() {
				nextBackfillAt = now.Add(historyInitialDelay)
			}
			// 回补马上要跑，缺口复查先等它结束，免得刚连上就误报缺口。
			r.historyBackfillBusy.Store(true)
		}
	}
}

// runInboundWorker 领一条事件、处理、再领下一条。空闲时按退避拉长轮询间隔：新事件
// 进来会敲 inboundWake，轮询只是兜底（漏掉唤醒、租约过期重投），不该按「最坏情况」
// 的频率一直空敲。
//
// 线上（miku）实测过代价：N 个 worker 各自 500 毫秒一跳，队列空着也照敲，全部挤在唯一
// 那条写连接上；一次回合结束大家又被一起唤醒，日志里看到 7 个 claim 在同一秒完成、
// 等待时间从 1.4 秒线性累加到 11 秒，而 elapsed_ms 和 pool_wait_ms_delta 几乎相等——
// 时间全在排队，不是 SQLite 慢。
func (r *Runtime) runInboundWorker(ctx context.Context, leaseOwner string, store InboundEventStore) {
	delay := inboundWorkerPollInterval
	timer := time.NewTimer(delay)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		case <-r.inboundWake:
			// 有人明确说有活了，退避立刻清零。
			delay = inboundWorkerPollInterval
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
		}
		claimed := false
		if !r.inboundProcessingReady() {
			delay = nextInboundPollDelay(delay)
			timer.Reset(delay)
			continue
		}
		for r.inboundProcessingReady() {
			claimCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			// 每轮重读配置：改并发不该要重启。
			leaseUntil := time.Now().Add(inboundLeaseDuration)
			item, ok, err := store.ClaimNextInboundEvent(claimCtx, leaseOwner, leaseUntil, r.inboundConcurrency())
			cancel()
			if err != nil {
				if ctx.Err() == nil {
					log.Printf("diana inbound claim failed: %v", err)
				}
				break
			}
			if !ok {
				break
			}
			claimed = true
			// 自己领到了活，说明队列里可能还有：叫醒一个同伴一起干。没有这一下，
			// 退避期间的突发消息会被一个 worker 串行地慢慢消化。
			r.wakeInboundWorkers()
			outcome, processErr := r.processInboundQueueItem(withInboundLeaseExtension(ctx, store, item.ID, leaseOwner, leaseUntil), item)
			commitCtx, commitCancel := context.WithTimeout(context.Background(), 5*time.Second)
			switch {
			case processErr == nil && outcome == inboundOutcomeHandedOffPending:
				err = r.completeHandedOffInbound(commitCtx, store, item, leaseOwner)
			case processErr == nil:
				err = store.CompleteInboundEvent(commitCtx, item.ID, leaseOwner, outcome)
				r.clearOutboundSteps(item.ID)
			case errors.Is(processErr, errOutboundOutcomeUnconfirmed):
				// 发出去了但确认不了：兜底也不许重新生成再发一遍，直接落终态。
				log.Printf("diana inbound event %s finished without resend, outbound outcome unconfirmed: %v", item.ID, processErr)
				err = store.CompleteInboundEvent(commitCtx, item.ID, leaseOwner, inboundOutcomeDroppedOutboundUnconfirmed)
				r.clearOutboundSteps(item.ID)
			case ctx.Err() == nil && isPermanentSendRejection(processErr):
				// 上游已经说清楚这条永远发不出去（对方把机器人删了好友之类）。
				// 再退避重试只会把同一条消息重新生成一遍回复、再被拒一遍：实测
				// 一条消息因此烧掉五轮生成。直接落终态，原始错误留在
				// processing_error 里等人看。
				log.Printf("diana inbound event %s dropped on permanent send rejection: %v", item.ID, processErr)
				r.recordInboundSendRejected(item, processErr)
				err = store.CompleteInboundEvent(commitCtx, item.ID, leaseOwner, inboundOutcomeSendRejected)
				r.clearOutboundSteps(item.ID)
			case ctx.Err() == nil && inboundRetriesExhausted(item.Attempts, r.inboundRetryMaxAttemptsForEvent(item.Event)):
				// 无限重试只会让同一条消息反复重发。到达上限后落终态，并把最后
				// 一次失败原因写进事件明细，等人处理而不是继续骚扰群里。
				log.Printf("diana inbound event %s dropped after %d attempts: %v", item.ID, item.Attempts, processErr)
				r.recordInboundDeliveryExhausted(item, processErr)
				err = store.CompleteInboundEvent(commitCtx, item.ID, leaseOwner, inboundOutcomeRetriesExhausted)
				r.clearOutboundSteps(item.ID)
			default:
				nextAttempt := time.Now()
				if ctx.Err() == nil {
					nextAttempt = nextAttempt.Add(inboundRetryDelay(item.Attempts))
				}
				err = store.RetryInboundEvent(commitCtx, item.ID, leaseOwner, nextAttempt, processErr.Error())
			}
			commitCancel()
			if err != nil {
				log.Printf("diana inbound state update failed: %v", err)
			}
			if ctx.Err() != nil {
				return
			}
		}
		// 领到过就回到最短间隔：刚忙完的时候后面往往还有；一直空手才慢慢拉长。
		if claimed {
			delay = inboundWorkerPollInterval
		} else {
			delay = nextInboundPollDelay(delay)
		}
		timer.Reset(delay)
	}
}

// nextInboundPollDelay 空手而归时把间隔翻倍，封顶 inboundIdlePollMax。翻倍而不是直接
// 跳到上限：刚空下来的那几秒最可能又来消息，这时候还该反应快。
func nextInboundPollDelay(current time.Duration) time.Duration {
	next := current * 2
	if next > inboundWorkerPollMax {
		next = inboundWorkerPollMax
	}
	return next
}

func (r *Runtime) processInboundQueueItem(ctx context.Context, item InboundQueueItem) (string, error) {
	// 断线回补里没排上回复名额的消息在这里就收住，只补进上下文历史。它们不能走下面的
	// 语音转写、图片处理、插件观察、消息中继和回复：回补设条数上限防的就是
	// 一批积压消息同时开出一堆媒体任务，这些消息要是也走完整流程，上限等于没设。
	// 放在过期检查之前：过期管的是「别回复太旧的消息」，不是「别记住它」。
	if item.Event.BackfillHistoryOnly {
		event := item.Event
		r.remember(event)
		record := r.decisionEventRecord(event, inboundEventPlainText(event), "backfill_history_only")
		record.Reason = "断线回补：不在回复名额内，已补入上下文历史"
		r.record(record)
		return "backfill_history_only", nil
	}
	if r.inboundEventIsStale(item.Event, time.Now()) {
		return "ignored_stale", nil
	}
	// 断线回补、重启重放的消息没经过入站登记，这里补上；这一轮收尾时注销。
	r.noteSenderTurnArrival(item.Event)
	r.noteSenderTurnInbound(item.Event, item.ID)
	defer r.finishSenderTurn(item.Event)
	// 积压的消息不在这里直接收掉：插件观察、消息互通、历史和记忆这些不花回复 token 的环节
	// 还得走。交接判断放到 prepareMessageEvent 里登记积压包之前那一刻，这里只把队列信息带过去。
	probe := item
	item.Event.backlogProbe = &probe
	ctx = withLLMUsageContext(ctx, item.Event)
	ctx = r.withDebugTraceContext(ctx, item.Event)
	ctx = withContextBudgetCap(ctx, r.effectiveConfigForEvent(item.Event).MaxContextTokens)
	// 出站幂等账本按入站事件 ID 记账：失败重跑时已经送达的分片和媒体会被跳过。
	ctx = withOutboundTurn(ctx, item.ID)
	// 同一个人先发图、隔几秒再发字，两条各走各的：以前这里会把图并进那句话（先问
	// 一次模型「这句话指的是不是那张图」，再把图那条的任务注销），判错了收不回来，
	// 而且图被当成这句话自带的，改图时连模型点名的头像都盖过去了。现在图那条是一条
	// 普通消息；回复时这个人刚发过的图以「候选依赖图」单独附上，见 sender_dependency_images.go。
	event := item.Event
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
	return r.replyAndRecord(withInboundReplyTurnContext(ctx, event), event, text, outcome)
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

// inboundEventPlainText 取这条事件的纯文本。
func inboundEventPlainText(event MessageEvent) string {
	var builder strings.Builder
	for _, segment := range event.Segments {
		if segment.Type != "text" {
			continue
		}
		builder.WriteString(segment.Data["text"])
	}
	if text := strings.TrimSpace(builder.String()); text != "" {
		return text
	}
	return strings.TrimSpace(event.RawMessage)
}

// inboundTurnMediaKey 标记从并进这一轮的其他消息（追发合并、积压合并）借过来的
// 媒体段。借只为这一轮：回复时图和问题要一起看。进历史前要还回去（见
// withoutInboundTurnMedia），否则同一张图、
// 同一个文件在历史里出现两次，查历史找文件时排在前面的是这条文字消息，模型就
// 会引用它——它在平台上只是一句话，拿它的 ID 引用或取文件都对不上原来那条。
const inboundTurnMediaKey = "inbound_turn_media"

// withoutInboundTurnMedia 去掉借来的媒体段。这条消息问的是哪条媒体消息，
// SemanticSourceMessageIDs 里还留着。
func withoutInboundTurnMedia(event MessageEvent) MessageEvent {
	var segments []MessageSegment
	for index, segment := range event.Segments {
		if segment.Data[inboundTurnMediaKey] != "true" {
			if segments != nil {
				segments = append(segments, segment)
			}
			continue
		}
		if segments == nil {
			// 第一次碰到才复制：绝大多数消息没有借来的段，不该为它们分配。
			segments = append(make([]MessageSegment, 0, len(event.Segments)), event.Segments[:index]...)
		}
	}
	if segments != nil {
		event.Segments = segments
	}
	return event
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
			segment.Data[inboundTurnMediaKey] = "true"
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

func (r *Runtime) recordInboundTurnSupersededBeforeSend(ctx context.Context, event MessageEvent, turnID string) {
	writer := r.appLogWriter()
	if writer == nil {
		return
	}
	_ = writer.AppendLog(ctx, applog.Entry{
		Kind:    applog.KindOperation,
		Level:   applog.LevelInfo,
		Action:  "inbound_turn_superseded",
		Message: "这条消息已并入另一轮回复，取消独立发送",
		Actor:   oneBotEventActor(event),
		Target:  strings.TrimSpace(event.MessageID),
		Metadata: map[string]any{
			"turn_id":               turnID,
			"message_id":            event.MessageID,
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
	if event.Time <= 0 || now.IsZero() || event.ManualRetry {
		return false
	}
	if event.RetryRecovered {
		return time.Unix(event.Time, 0).Before(now.Add(-InboundReplayWindow))
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

// inboundRetriesExhausted 判断这条事件是否已经用尽重试次数。
func inboundRetriesExhausted(attempts, maxAttempts int) bool {
	if maxAttempts <= 0 {
		maxAttempts = inboundMaxAttempts
	}
	return attempts >= maxAttempts
}

// recordInboundDeliveryExhausted 把「重试次数用尽」写进这条事件的投递审计，
// WebUI 的事件明细据此显示为终态失败而不是仍在排队。
func (r *Runtime) recordInboundDeliveryExhausted(item InboundQueueItem, processErr error) {
	detail := fmt.Sprintf("连续 %d 次处理失败，已停止重试", item.Attempts)
	if processErr != nil {
		detail += "：" + processErr.Error()
	}
	r.recordInboundDelivery(item.ID, item.Event, OutboundDeliveryFailed, "", detail)
}

// recordInboundSendRejected 把「上游明确拒收」写进这条事件的投递审计。
// 原始错误本身已经由回复流程写进 processing_error（record.Error），这里只补上
// 「所以我们不再重试了」这句话，免得事件页看起来像还在排队。
func (r *Runtime) recordInboundSendRejected(item InboundQueueItem, processErr error) {
	detail := "上游明确拒收这条消息，重试不可能成功，已停止重试"
	if processErr != nil {
		detail += "：" + processErr.Error()
	}
	r.recordInboundDelivery(item.ID, item.Event, OutboundDeliveryFailed, "", detail)
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
		}
		if fallback.Platform != "" {
			return fallback
		}
	}
	return channel.Status()
}

// RequestHistoryBackfill schedules a manual history backfill covering the given
// window, capped at InboundReplayWindow. It returns once the request is queued;
// progress and outcome surface as diana.backfill_* application log entries.
func (r *Runtime) RequestHistoryBackfill(window time.Duration) error {
	r.mu.RLock()
	store := r.inboundStore
	running := r.running
	r.mu.RUnlock()
	if store == nil {
		return errors.New("diana: durable inbound store is not configured")
	}
	if !running {
		return errors.New("diana: runtime is not running")
	}
	if !channelEffectivelyOnline(r.channelStatus()) {
		return errors.New("diana: onebot connection or bot account is offline")
	}
	if window <= 0 || window > InboundReplayWindow {
		window = InboundReplayWindow
	}
	select {
	case r.inboundManualBackfill <- window:
		return nil
	default:
		return errors.New("diana: a manual backfill request is already pending")
	}
}

// FailedInboundRequeuer 是可选能力：把处理失败的入站事件放回队列重跑。
type FailedInboundRequeuer interface {
	RequeueFailedInboundEvent(ctx context.Context, id string) error
	RequeueFailedInboundEvents(ctx context.Context, since time.Time, profileID string, perSession int) (int, error)
}

func (r *Runtime) failedInboundRequeuer() (FailedInboundRequeuer, error) {
	r.mu.RLock()
	store := r.inboundStore
	running := r.running
	r.mu.RUnlock()
	requeuer, ok := store.(FailedInboundRequeuer)
	if store == nil || !ok {
		return nil, errors.New("diana: durable inbound store is not configured")
	}
	if !running {
		return nil, errors.New("diana: runtime is not running")
	}
	return requeuer, nil
}

// RetryFailedEvent 重跑一条处理失败的入站消息。出站账本按事件 ID 记账，
// 上次已经送达的分片不会再发一遍。
func (r *Runtime) RetryFailedEvent(ctx context.Context, id string) error {
	requeuer, err := r.failedInboundRequeuer()
	if err != nil {
		return err
	}
	if err := requeuer.RequeueFailedInboundEvent(ctx, id); err != nil {
		return err
	}
	r.wakeInboundWorkers()
	return nil
}

// RetryFailedEvents 重跑最近一个回放窗口内处理失败的入站消息，返回放回队列的条数。
// 每个会话的条数上限跟断线回补共用 HistoryBackfillMessageLimit：两者防的是同一件事，
// 一批积压消息同时开出一堆回复。
func (r *Runtime) RetryFailedEvents(ctx context.Context, profileID string) (int, error) {
	requeuer, err := r.failedInboundRequeuer()
	if err != nil {
		return 0, err
	}
	count, err := requeuer.RequeueFailedInboundEvents(ctx, time.Now().Add(-InboundReplayWindow), profileID, r.profileConfig(profileID).HistoryBackfillMessageLimit)
	if err != nil {
		return 0, err
	}
	if count > 0 {
		r.wakeInboundWorkers()
	}
	return count, nil
}

// channelAccountDown reports a heartbeat-confirmed unhealthy bot account: the
// transport may be fine while the OneBot client cannot receive messages for the account.
func channelAccountDown(status ChannelStatus) bool {
	return status.AccountStatusKnown && (!status.AccountOnline || !status.AccountGood)
}

// channelEffectivelyOnline requires both a live transport and a healthy account
// before inbound processing or history backfill may run.
func channelEffectivelyOnline(status ChannelStatus) bool {
	return status.Connected && !channelAccountDown(status)
}

func (r *Runtime) recordOneBotConnectionLifecycle(ctx context.Context, status ChannelStatus, event string, message string, eventErr error) {
	r.recordOneBotConnectionLifecycleWithMetadata(ctx, status, event, message, eventErr, nil)
}

func (r *Runtime) recordOneBotConnectionLifecycleWithMetadata(ctx context.Context, status ChannelStatus, event string, message string, eventErr error, extra map[string]any) {
	// Recovery must not wait indefinitely for the database it is recovering.
	ctx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
	defer cancel()
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
	for key, value := range extra {
		metadata[key] = value
	}
	_ = writer.AppendLog(ctx, applog.Entry{
		Kind:      kind,
		Level:     level,
		Action:    event,
		Message:   message,
		Detail:    detail,
		Target:    status.ProfileID,
		Metadata:  metadata,
		CreatedAt: time.Now(),
	})
}

// rewindHistoryBackfillBaseline 把每个会话的水位下调到 floor，让下一次回补重新
// 覆盖该时间段；已入库的消息由入站去重挡住，不会重复处理。
func rewindHistoryBackfillBaseline(sessions []HistorySession, floor int64) []HistorySession {
	out := append([]HistorySession(nil), sessions...)
	for index := range out {
		if out[index].LastEventTime > floor {
			out[index].LastEventTime = floor
		}
	}
	return out
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
	return ready && channelEffectivelyOnline(r.channelStatus())
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
	_, err := r.backfillInboundHistoryWithStats(ctx, store)
	return err
}

func (r *Runtime) backfillInboundHistoryWithStats(ctx context.Context, store InboundEventStore) (historyBackfillStats, error) {
	sessions, err := store.ListHistorySessions(ctx)
	if err != nil {
		return historyBackfillStats{}, fmt.Errorf("list history sessions: %w", err)
	}
	_, stats, err := r.backfillInboundHistorySessions(ctx, store, sessions, time.Now().Unix())
	return stats, err
}

func (r *Runtime) backfillInboundHistoryFromSessions(ctx context.Context, store InboundEventStore, sessions []HistorySession, fallbackWatermark int64) ([]HistorySession, error) {
	ordered, _, err := r.backfillInboundHistorySessions(ctx, store, sessions, fallbackWatermark)
	return ordered, err
}

func (r *Runtime) backfillInboundHistorySessions(ctx context.Context, store InboundEventStore, sessions []HistorySession, fallbackWatermark int64) ([]HistorySession, historyBackfillStats, error) {
	// This backfill protocol is made of OneBot APIs. Persisted sessions
	// from Telegram and other transports must keep their own signed/string IDs
	// and must never be replayed through OneBot's positive numeric group rules.
	oneBotSessions := sessions[:0]
	for _, session := range sessions {
		platform := NormalizePlatformID(session.Platform)
		if platform == "" || platform == PlatformOneBotV11 {
			oneBotSessions = append(oneBotSessions, session)
		}
	}
	sessions = oneBotSessions
	known := make(map[string]HistorySession, len(sessions))
	byKey := make(map[string]HistorySession, len(sessions))
	globalWatermark := int64(0)
	for _, session := range sessions {
		if session.ID == "" {
			continue
		}
		key := historySessionKey(session.Kind, session.ID)
		known[key] = session
		// Preserve the established group-history behavior. Private contacts are
		// different because OneBot may permanently lose their UIN-to-UID mapping;
		// those are admitted later only when current recent contacts still list them.
		if session.Kind == EventKindGroup {
			byKey[key] = session
		}
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
		addKnownHistorySessions(byKey, known, EventKindGroup)
	} else {
		for _, raw := range oneBotListItems(data) {
			item, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			id := stringFromAny(item["group_id"])
			addDiscoveredHistorySession(byKey, known, EventKindGroup, id, globalWatermark)
		}
	}
	if data, callErr := r.callBackfillAPI(ctx, "get_recent_contact", map[string]any{"count": 1000}); callErr != nil {
		backfillErrors = append(backfillErrors, fmt.Errorf("get recent contacts: %w", callErr))
		addKnownHistorySessions(byKey, known, EventKindPrivate)
	} else {
		for _, raw := range oneBotListItems(data) {
			item, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			id := firstNonEmpty(stringFromAny(item["peerUin"]), stringFromAny(item["peer_uin"]))
			switch intFromAny(item["chatType"]) {
			case 2:
				addDiscoveredHistorySession(byKey, known, EventKindGroup, id, globalWatermark)
			case 1, 99, 100:
				addDiscoveredHistorySession(byKey, known, EventKindPrivate, id, globalWatermark)
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
	botAccount := r.oneBotBotAccount()
	for _, session := range ordered {
		if session.Kind != EventKindPrivate || session.ID != botAccount {
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
			defer recoverGoroutinePanic("inbound_queue.go:1062")
			defer fetchWG.Done()
			for session := range jobs {
				events, fetchErr := r.fetchHistorySerialized(ctx, session)
				results <- historyFetchResult{session: session, events: events, err: fetchErr}
			}
		}()
	}
	fetchWG.Wait()
	close(results)
	stats := historyBackfillStats{Sessions: len(ordered)}
	for result := range results {
		if result.err != nil {
			if permanentPrivateHistoryBackfillError(result.session, result.err) {
				log.Printf("diana inbound history backfill skipped stale private %s: %v", result.session.ID, result.err)
				continue
			}
			backfillErrors = append(backfillErrors, fmt.Errorf("%s %s: %w", result.session.Kind, result.session.ID, result.err))
			continue
		}
		stats.Fetched += len(result.events)
		inserted, enqueueErrs := r.enqueueBackfilledEvents(ctx, store, result.events)
		stats.Inserted += inserted
		backfillErrors = append(backfillErrors, enqueueErrs...)
	}
	return ordered, stats, errors.Join(backfillErrors...)
}

// enqueueBackfilledEvents 把历史接口拉回来的消息写进持久化入站队列，返回新入库的条数。
func (r *Runtime) enqueueBackfilledEvents(ctx context.Context, store InboundEventStore, events []MessageEvent) (int, []error) {
	var errs []error
	insertedCount := 0
	for _, event := range events {
		if event.historyRecallCandidate {
			recovered, recoverErr := r.recoverGroupRecallFromHistory(ctx, event)
			if recoverErr != nil {
				errs = append(errs, fmt.Errorf("recover backfilled recall %s: %w", event.MessageID, recoverErr))
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
			errs = append(errs, fmt.Errorf("enqueue backfilled message %s: %w", event.MessageID, persistErr))
			continue
		}
		if inserted {
			insertedCount++
			r.wakeInboundWorkers()
		}
	}
	return insertedCount, errs
}

// fetchHistorySerialized 让整体回补和各群的缺口复查排队拉历史：接入端同时处理几个
// 大的历史请求时会卡住（见 historyFetchWorkers）。
func (r *Runtime) fetchHistorySerialized(ctx context.Context, session HistorySession) ([]MessageEvent, error) {
	r.historyFetchMu.Lock()
	defer r.historyFetchMu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return r.fetchHistorySince(ctx, session)
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

func addDiscoveredHistorySession(target, known map[string]HistorySession, kind EventKind, id string, fallbackWatermark int64) {
	id = strings.TrimSpace(id)
	if id == "" {
		return
	}
	key := historySessionKey(kind, id)
	if session, ok := known[key]; ok {
		target[key] = session
		return
	}
	addHistorySession(target, kind, id, fallbackWatermark)
}

func addKnownHistorySessions(target, known map[string]HistorySession, kind EventKind) {
	for key, session := range known {
		if session.Kind == kind {
			target[key] = session
		}
	}
}

// permanentPrivateHistoryBackfillError identifies contacts that the OneBot
// bridge can no longer map to a current private-chat identity. Retrying these
// stale sessions cannot recover messages and must not keep the whole reconnect
// checkpoint in debt forever. Other transport and server failures still retry.
func permanentPrivateHistoryBackfillError(session HistorySession, err error) bool {
	if session.Kind != EventKindPrivate || err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	for _, marker := range []string{
		"failed to resolve uid for uin",
		"friend not found",
		"not a friend",
		"user not found",
		"好友不存在",
		"非好友",
	} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
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
	messageLimit := r.historyBackfillProfile().HistoryBackfillMessageLimit
	cursor := ""
	seenCursors := map[string]struct{}{}
	for {
		// 名额只管进回复流程的条数，不管往回翻多远：按回复名额取页，名额是 3 就一次
		// 只拿 3 条，永远翻不到第二页。
		pageSize := historyPageSize
		params := map[string]any{
			idParam:           oneBotIDParam(session.ID),
			"count":           pageSize,
			"reverse_order":   cursor != "",
			"disable_get_url": true,
		}
		if cursor != "" {
			params["message_seq"] = cursor
		}
		callCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		event := MessageEvent{Kind: session.Kind, ProfileID: session.ProfileID, Platform: session.Platform}
		if session.Kind == EventKindGroup {
			event.GroupID = session.ID
		} else {
			event.UserID = session.ID
		}
		data, err := r.callOneBotAPIForEvent(callCtx, event, action, params)
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
		if reachedWatermark || len(eventsByID) >= historyBackfillScanLimit || len(items) < pageSize {
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
	markBackfillReplyEligible(events, messageLimit, func(event MessageEvent) bool {
		return !r.isSelfMessage(event) && r.shouldHandleChat(event, inboundEventPlainText(event))
	})
	return events, nil
}

// markBackfillReplyEligible 从最新往回挑出最多 limit 条会触发回复的消息留在回复流程
// 里，其余一律标成只进上下文历史。events 必须已按时间升序排好。
//
// 以前这里直接截最新 limit 条，不看会不会触发：断线期间群里十条闲聊加一条 @ 机器人，
// 只要那条 @ 不在最新三条里，它就既没人回、也进不了历史。名额现在只留给真会触发的
// 消息（和正常入站同一个确定性判定：私聊、@ 本机、引用本机、称呼命中，不调模型）；
// 没排上的照样补进上下文，机器人重连后仍然知道断线这段时间聊过什么。
func markBackfillReplyEligible(events []MessageEvent, limit int, triggers func(MessageEvent) bool) {
	remaining := limit
	for index := len(events) - 1; index >= 0; index-- {
		if remaining > 0 && triggers(events[index]) {
			remaining--
			continue
		}
		events[index].BackfillHistoryOnly = true
	}
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
		// 历史回填拉的是 OneBot 的消息，self_id 要用 OneBot 那台的账号；
		// 用了 Telegram 那台的账号，这批 QQ 消息就全认错了主人。
		normalized["self_id"] = r.oneBotBotAccount()
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
	// 这批消息是从 OneBot 的历史接口拉回来的，身份必须绑到 OneBot 那台机器人。
	event = r.bindInboundEventIdentityForPlatform(event, PlatformOneBotV11)
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
