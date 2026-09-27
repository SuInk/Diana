// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"
)

const (
	// codingReportPollInterval 是汇报协程查一次连接状态的间隔。状态在内存里，查一次
	// 只是读几个字段；间隔短一点，接入端连上之后汇报几乎是跟着就到。
	codingReportPollInterval = time.Second
	// codingReportRetryInitialDelay / codingReportRetryMaxDelay 是连接在、发送却失败
	// （被禁言、接口报错）时的退避。汇报攒的是一小时的活，不设上限次数，进程在就接着试。
	codingReportRetryInitialDelay = 5 * time.Second
	codingReportRetryMaxDelay     = 5 * time.Minute
	// codingSubmitHandOffWindow 是 submit 等任务结束的那几秒。认证失败、账号被封、
	// 命令不存在这类错几秒内就出结果，这时候汇报会比派活那一轮的回复先到，聊天里
	// 就成了「失败了」排在「已经在后台跑了」前面。窗口内结束的交给那一轮自己说。
	codingSubmitHandOffWindow = 8 * time.Second
	// codingReportInlineRunes 以内的结果和头部放一条发。
	codingReportInlineRunes = 1500
	// codingReportPreviewRunes 是长结果在头部里带的开头：编码 CLI 的报告通常先说结论，
	// 群友只看这一条也知道成没成。
	codingReportPreviewRunes = 300
)

type codingReportTiming struct {
	poll         time.Duration
	initialDelay time.Duration
	maxDelay     time.Duration
	handOff      time.Duration
}

func (t codingReportTiming) withDefaults() codingReportTiming {
	if t.poll <= 0 {
		t.poll = codingReportPollInterval
	}
	if t.initialDelay <= 0 {
		t.initialDelay = codingReportRetryInitialDelay
	}
	if t.maxDelay < t.initialDelay {
		t.maxDelay = max(codingReportRetryMaxDelay, t.initialDelay)
	}
	if t.handOff <= 0 {
		t.handOff = codingSubmitHandOffWindow
	}
	return t
}

// codingReportHold 是派活那一轮还在等的任务。等的期间结束了，结果交给那一轮的
// 工具返回值，不再单独推汇报。
type codingReportHold struct {
	done   chan struct{}
	handed bool
}

// holdReport 在守望协程起来之前登记，否则任务秒结束时汇报会抢在登记前发出去。
func (g *codingJobRegistry) holdReport(jobID string) <-chan struct{} {
	g.mu.Lock()
	defer g.mu.Unlock()
	hold := &codingReportHold{done: make(chan struct{})}
	g.reportHolds[jobID] = hold
	return hold.done
}

// handOffReport 由收尾调用：有人在等就把结果交给它，返回真表示这条汇报不用发了。
func (g *codingJobRegistry) handOffReport(jobID string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	hold, ok := g.reportHolds[jobID]
	if !ok || hold.handed {
		return ok
	}
	hold.handed = true
	close(hold.done)
	return true
}

// releaseReportHold 撤掉登记，返回等待期间结果是否已经交过来。撤掉之后再结束的
// 任务照常汇报。
func (g *codingJobRegistry) releaseReportHold(jobID string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	hold, ok := g.reportHolds[jobID]
	delete(g.reportHolds, jobID)
	return ok && hold.handed
}

// claimHandedOffReport 把交到派活那一轮手里的结果记成已汇报，返回落盘后的记录。
// 和 attemptCodingReport 共用 reportMu：同一个结果只能有一处说出去。
func (r *Runtime) claimHandedOffReport(jobID string) (CodingJob, bool) {
	registry := r.codingJobs()
	registry.reportMu.Lock()
	defer registry.reportMu.Unlock()
	job, err := loadCodingJob(jobID)
	if err != nil || job.Reported || !job.finished() {
		return job, false
	}
	job.Reported = true
	if err := saveCodingJob(job); err != nil {
		r.setError(err.Error())
		return job, false
	}
	return job, true
}

// codingReportRetry 是一个汇报协程的登记。记下它跟着哪一轮运行的 ctx：那一轮停了
// 协程就会退出，新一轮的接回不该因为「已经有人在等」而跳过。
type codingReportRetry struct {
	ctx context.Context
}

// beginReportRetry 登记一个汇报协程。同一轮运行里已经有一个在等就不再起第二个。
func (g *codingJobRegistry) beginReportRetry(ctx context.Context, jobID string) (*codingReportRetry, bool) {
	if ctx.Err() != nil {
		return nil, false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if existing, ok := g.reportRetries[jobID]; ok && existing.ctx.Err() == nil {
		return nil, false
	}
	retry := &codingReportRetry{ctx: ctx}
	g.reportRetries[jobID] = retry
	return retry, true
}

func (g *codingJobRegistry) endReportRetry(jobID string, retry *codingReportRetry) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.reportRetries[jobID] == retry {
		delete(g.reportRetries, jobID)
	}
}

func (g *codingJobRegistry) timing() codingReportTiming {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.reportTiming.withDefaults()
}

// reportCodingJob 把结果发回派活的那个会话，恰好一次：Reported 只在发送成功后落盘。
//
// 启动接回跑在聊天客户端连上来之前。反向 WebSocket 下这时一发就是「未连接」，以前
// 这条汇报就搁到下一次重启。现在目标机器人的连接没就绪就不去撞，交给汇报协程等连上
// 再发；连着却发失败的按退避重试，直到发出去或者这一轮运行结束。
func (r *Runtime) reportCodingJob(ctx context.Context, job CodingJob) {
	if job.Reported || !job.finished() {
		return
	}
	if r.codingJobs().handOffReport(job.ID) || !codingJobHasRecipient(job) {
		return
	}
	if r.codingReportConnectionReady(job) && !r.attemptCodingReport(ctx, job) {
		return
	}
	r.retryCodingReport(ctx, job)
}

func codingJobHasRecipient(job CodingJob) bool {
	target := job.Target.event()
	return strings.TrimSpace(target.UserID) != "" || strings.TrimSpace(target.GroupID) != ""
}

// codingReportProfile 是汇报要走的那台机器人：没写档案 ID 的旧记录归到唯一那台。
func (r *Runtime) codingReportProfile(job CodingJob) string {
	if owner, ok := r.codingJobOwner(job.Target.ProfileID); ok {
		return owner
	}
	return strings.TrimSpace(job.Target.ProfileID)
}

func (r *Runtime) codingReportConnectionReady(job CodingJob) bool {
	status, known := r.deliveryConnectionStatus(r.codingReportProfile(job))
	return !known || status.Connected
}

// attemptCodingReport 发一次汇报，返回还要不要再试。
//
// 发送前从磁盘重读一次，整段放在 reportMu 里：重试协程、任务收尾、再一轮的接回可能
// 同时想汇报同一个任务，后到的读盘就看见 Reported，不会把同一个结果发两遍。
func (r *Runtime) attemptCodingReport(ctx context.Context, job CodingJob) bool {
	registry := r.codingJobs()
	registry.reportMu.Lock()
	defer registry.reportMu.Unlock()
	if latest, err := loadCodingJob(job.ID); err == nil {
		job = latest
	}
	if job.Reported || !job.finished() {
		return false
	}
	// 等的这段时间里机器人可能被删掉了：不是本机器人的任务一律不碰（#757）。
	owner, ok := r.codingJobOwner(job.Target.ProfileID)
	if !ok {
		return false
	}
	// 盘上的记录可能还记着旧号（#760）：按旧号发，MultiChannel 找不到连接。和接回
	// 一样改成现在的 ID 再发，汇报成功后连同 Reported 一起写回。
	if strings.TrimSpace(job.Target.ProfileID) != "" {
		job.Target.ProfileID = owner
	}
	if err := r.deliverCodingJobReport(ctx, job.Target.event(), job); err != nil {
		if ctx.Err() != nil {
			return false
		}
		r.setError(err.Error())
		// 机器人停用了：重新启用时这一轮运行会重启，接回会再补这条汇报。
		return !errors.Is(err, ErrDeliveryTargetDisabled)
	}
	job.Reported = true
	if err := saveCodingJob(job); err != nil {
		r.setError(err.Error())
	}
	return false
}

// deliverCodingJobReport 把汇报发出去。结果不长就一条；长的先发一条带 @ 的头部和
// 开头摘要，全文走合并转发——几千字的报告拆成十几条会刷屏，群友往上翻都找不到头。
// 平台不支持合并转发、或者机器人关了合并转发，就在头部后面分条发全文。
//
// 头部发出去之后全文没发成会返回错误，汇报协程重试时头部会再发一遍：只有头部没有
// 全文等于没汇报，宁可多一条头部。合并转发自带出站去重，已经送达的那张不会重发。
func (r *Runtime) deliverCodingJobReport(ctx context.Context, event MessageEvent, job CodingJob) error {
	body := codingJobReportBody(job)
	runes := len([]rune(body))
	if runes <= codingReportInlineRunes {
		return r.sendSubscriberNotice(ctx, event, renderCodingJobReport(job))
	}
	head := renderCodingJobReportHead(job) +
		fmt.Sprintf("\n结果共 %d 字，全文见下一条。开头：\n%s", runes, truncateRunes(body, codingReportPreviewRunes))
	if err := r.sendSubscriberNotice(ctx, event, head); err != nil {
		return err
	}
	return r.deliverCodingReportBody(ctx, event, body)
}

// deliverCodingReportBody 发长结果的全文，不带 @：点名在头部那条已经点过了。
func (r *Runtime) deliverCodingReportBody(ctx context.Context, event MessageEvent, body string) error {
	platform, err := r.outboundPlatformForEvent(event)
	if err != nil {
		return err
	}
	event.Platform = platform
	cfg := r.effectiveConfigForEvent(event)
	if IsOneBotPlatform(platform) && !chatSplitLimitsForEvent(cfg, event).SingleMessage && boolValue(cfg.ForwardReplyEnabled, true) {
		_, err := r.sendForwardReplyWithResult(ctx, event, body, cfg)
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// 卡片被账号安全审核拦下时不能退回逐条发：逐条发的是同一段文字，那条路不再
		// 审核。也不能返回错误——内容不变，重试多少次都是拦下，汇报协程会一直转。
		var safetyErr *replyAccountSafetyRejectedError
		if errors.As(err, &safetyErr) {
			return r.sendSubscriberNotice(ctx, event, "结果全文没通过发送前的安全审核，没有贴出来；全文还留在任务记录里。")
		}
		// 有的 OneBot 实现不支持合并转发，退回分条发，全文照样送到。
		log.Printf("diana coding report forward failed, falling back to chunks: %v", err)
	}
	_, err = r.deliverChunks(ctx, event, splitReply(body, notificationChunkSize), cfg, outboundDecoration{})
	return err
}

// retryCodingReport 起一个协程等时机重发。ctx 是这一轮运行的，Stop 时协程跟着退出，
// 没汇报的任务留给下一次启动接回。
func (r *Runtime) retryCodingReport(ctx context.Context, job CodingJob) {
	registry := r.codingJobs()
	retry, ok := registry.beginReportRetry(ctx, job.ID)
	if !ok {
		return
	}
	timing := registry.timing()
	profileID := r.codingReportProfile(job)
	log.Printf("diana coding job %s report deferred until profile %q can deliver", job.ID, profileID)
	go func() {
		defer recoverGoroutinePanic("coding.reportRetry")
		defer registry.endReportRetry(job.ID, retry)
		delay := timing.initialDelay
		for {
			if !r.waitCodingReportTurn(ctx, profileID, delay, timing.poll) {
				return
			}
			if !r.attemptCodingReport(ctx, job) {
				return
			}
			delay = min(delay*2, timing.maxDelay)
		}
	}()
}

// waitCodingReportTurn 等到该再试一次的时候：连接从断开变成连上、换了一条新连接，
// 或者连着的情况下退避到点。断开期间一直等，不去撞「未连接」。ctx 结束返回 false。
func (r *Runtime) waitCodingReportTurn(ctx context.Context, profileID string, delay, poll time.Duration) bool {
	start, _ := r.deliveryConnectionStatus(profileID)
	timer := time.NewTimer(delay)
	defer timer.Stop()
	ticker := time.NewTicker(poll)
	defer ticker.Stop()
	elapsed := false
	for {
		status, known := r.deliveryConnectionStatus(profileID)
		switch {
		case !known:
			// 认不出走哪条连接就只按退避来，发送自己会报清楚错在哪。
			if elapsed {
				return true
			}
		case status.Connected:
			if elapsed || !start.Connected || status.ConnectionEpoch != start.ConnectionEpoch {
				return true
			}
		}
		select {
		case <-ctx.Done():
			return false
		case <-timer.C:
			elapsed = true
		case <-ticker.C:
		}
	}
}

// waitDeliveryConnection 等某台机器人的连接连上，最多等 limit。连上了或者认不出走
// 哪条连接都返回 true；等到头仍没连上、或者 ctx 结束返回 false。
func (r *Runtime) waitDeliveryConnection(ctx context.Context, profileID string, limit, poll time.Duration) bool {
	deadline := time.NewTimer(limit)
	defer deadline.Stop()
	ticker := time.NewTicker(poll)
	defer ticker.Stop()
	for {
		if status, known := r.deliveryConnectionStatus(profileID); !known || status.Connected {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-deadline.C:
			return false
		case <-ticker.C:
		}
	}
}

// deliveryConnectionStatus 返回发给某台机器人时要走的那条连接的状态。选法和
// MultiChannel.bindingFor 一致：先按档案 ID，只有一台时兜底。不能看合并后的
// Status()：几台里只要有一台连着它就报已连接，等的那一台其实还没连上。
func (r *Runtime) deliveryConnectionStatus(profileID string) (ChannelStatus, bool) {
	r.mu.RLock()
	channel := r.channel
	r.mu.RUnlock()
	if channel == nil {
		return ChannelStatus{}, false
	}
	provider, ok := channel.(interface{ ChannelStatuses() []ChannelStatus })
	if !ok {
		return channel.Status(), true
	}
	statuses := provider.ChannelStatuses()
	profileID = strings.TrimSpace(profileID)
	if profileID != "" {
		for _, status := range statuses {
			if status.ProfileID == profileID {
				return status, true
			}
		}
	}
	if len(statuses) == 1 {
		return statuses[0], true
	}
	return ChannelStatus{}, false
}
