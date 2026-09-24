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

const (
	// codingReportPollInterval 是汇报协程查一次连接状态的间隔。状态在内存里，查一次
	// 只是读几个字段；间隔短一点，接入端连上之后汇报几乎是跟着就到。
	codingReportPollInterval = time.Second
	// codingReportRetryInitialDelay / codingReportRetryMaxDelay 是连接在、发送却失败
	// （被禁言、接口报错）时的退避。汇报攒的是一小时的活，不设上限次数，进程在就接着试。
	codingReportRetryInitialDelay = 5 * time.Second
	codingReportRetryMaxDelay     = 5 * time.Minute
)

type codingReportTiming struct {
	poll         time.Duration
	initialDelay time.Duration
	maxDelay     time.Duration
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
	return t
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
	if job.Reported || !job.finished() || !codingJobHasRecipient(job) {
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
	if err := r.sendSubscriberNotice(ctx, job.Target.event(), renderCodingJobReport(job)); err != nil {
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
