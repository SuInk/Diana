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

// 历史图片描述队列。
//
// 以前是「每张图起一个 goroutine，各自先起 90 秒倒计时，再去抢容量 1 的信号量」。
// 线上一条 B 站视频分享带 8～21 张图（封面 + 关键帧），它们同时入队、同时开始倒计时，
// 单并发一张 9 秒左右，90 秒只够前七八张，后面的在同一秒一起超时；10 分钟后重试
// 又整批挤一遍，同一条消息能超时 37 次。再加上后台描述只在整个 bot 完全空闲时才跑，
// 群里有人说话它就一直排着，倒计时照样在走。
//
// 现在改成一条有序队列加一个 worker：
//   - 超时只罩住真正的识图调用，排队不计时；队列长度由 historyImageDescriptionQueueLimit 兜底。
//   - 任务按入队顺序一张一张做，一条消息的所有图（包括全部视频关键帧）排在一起。
//   - 普通任务仍然让路给前台：有回复在处理时不开工，新消息进来会打断正在跑的普通任务，
//     被打断的任务放回队首重新开始，不算失败，也不沿用旧的计时。
//   - 前台真正依赖的图（用户在问的那张）走加急：插到队首、不等空闲、不会被打断，
//     前台用 awaitHistoryImageDescriptions 有限时地等它做完。不加急的话前台一等，
//     后台就因为「前台忙」永远不开工，两边互相等死。
//   - 失败 historyImageDescriptionMaxFailures 次后自动路径不再重试；模型或用户真的
//     读这张图时（explicit/加急）还会再试。

const (
	// historyImageDescriptionMaxFailures 之后自动路径放弃这张图。
	historyImageDescriptionMaxFailures = 3
	// historyImageDescriptionFailureLimit 限制失败记录的条数，放弃过的图不能无限攒。
	historyImageDescriptionFailureLimit = 4096
	// replyImageDescriptionWait 是前台为「用户正在问的图」等描述的上限。
	// 单张识图中位数 9 秒左右；等不完的继续在后台做，这一轮就按现有摘要回答。
	replyImageDescriptionWait = 45 * time.Second
	// replyImageDescriptionMaxImages 限制前台一次最多等几张，多的只入队不等。
	replyImageDescriptionMaxImages = 24
	// recentSenderImageWindow：同一个人先发图、隔一会儿再单独发文字来问，
	// 这段时间内他发的图算作这句话的依赖。
	recentSenderImageWindow = 3 * time.Minute
	// recentSenderImageMessages 最多回看这个人最近几条带图消息。
	recentSenderImageMessages = 2
)

type historyImageDescJob struct {
	hash       string
	source     string
	event      MessageEvent
	indexEvent MessageEvent
	explicit   bool
	urgent     bool
	done       chan struct{}
	cancel     context.CancelFunc
	preempted  bool
}

type historyImageDescFailure struct {
	count   int
	retryAt time.Time
}

// enqueueHistoryImageDescriptions fills the durable summary layer away from
// the visible reply path. Content hashes deduplicate identical images across
// messages and the bounded queue prevents image bursts from creating an
// unbounded background workload.
func (r *Runtime) enqueueHistoryImageDescriptions(event MessageEvent) {
	// 自动路径只补近期图片。重连回填会把很久以前的消息重放一遍，每条都排一次
	// 识图，等于拿单并发去补一整个库——按当前速度是几十小时起步，而这些老图
	// 绝大多数没人再提起。真被引用时会走 enqueueHistoryImageDescriptionsNow。
	if r == nil || !boolValue(r.effectiveConfigForEvent(event).AutoImageDescription, true) || !withinHistoryImageDescriptionWindow(event, time.Now()) {
		return
	}
	r.enqueueHistoryImageDescriptionsWithPolicy(event, false, false)
}

// enqueueHistoryImageDescriptionsNow 不看时间，用于用户/模型真的在读这张图的路径。
func (r *Runtime) enqueueHistoryImageDescriptionsNow(event MessageEvent) {
	r.enqueueHistoryImageDescriptionsWithPolicy(event, true, false)
}

// awaitHistoryImageDescriptions 把前台依赖的图加急入队，并在 ctx 内等它们描述完。
// 返回时没做完的图继续留在队列里，之后的轮次能直接读到缓存。
func (r *Runtime) awaitHistoryImageDescriptions(ctx context.Context, events ...MessageEvent) {
	if r == nil || len(events) == 0 {
		return
	}
	var pending []chan struct{}
	for _, event := range events {
		pending = append(pending, r.enqueueHistoryImageDescriptionsWithPolicy(event, true, true)...)
		if len(pending) >= replyImageDescriptionMaxImages {
			pending = pending[:replyImageDescriptionMaxImages]
			break
		}
	}
	for _, done := range pending {
		select {
		case <-done:
		case <-ctx.Done():
			return
		}
	}
}

// enqueueHistoryImageDescriptionsWithPolicy 返回本次入队（或已在队中）的任务的完成信号。
func (r *Runtime) enqueueHistoryImageDescriptionsWithPolicy(event MessageEvent, explicit, urgent bool) []chan struct{} {
	if r == nil || r.recallImageDescriptionStore() == nil {
		return nil
	}
	var pending []chan struct{}
	for _, sourceEvent := range historyImageDescriptionEvents(event) {
		for _, segment := range sourceEvent.Segments {
			if !historyDescribableImageSegment(segment) || strings.EqualFold(strings.TrimSpace(segment.Data[imageUnavailableKey]), "true") {
				continue
			}
			if strings.TrimSpace(segment.Data[recallImageDescriptionKey]) != "" {
				continue
			}
			hash, ok := imageSegmentContentSHA256(segment)
			if !ok {
				continue
			}
			// 排队的任务不能攥着图片本体：削成「哈希 + 本地路径」再入队（见 history_image_queue.go）。
			source, retained := queuedImageSourceRetained(segment)
			if !retained {
				continue
			}
			jobEvent := historyImageDescriptionQueueEvent(sourceEvent)
			jobEvent.Segments = []MessageSegment{stripImageSegmentForQueue(segment)}
			job := &historyImageDescJob{
				hash:       hash,
				source:     source,
				event:      jobEvent,
				indexEvent: historyImageDescriptionQueueEvent(sourceEvent),
				explicit:   explicit,
				urgent:     urgent,
				done:       make(chan struct{}),
			}
			if done := r.pushHistoryImageDescriptionJob(job); done != nil {
				pending = append(pending, done)
			}
		}
	}
	return pending
}

// pushHistoryImageDescriptionJob 把任务放进队列；同一张图已在队中时合并，
// 需要加急就把已有任务提到前面。返回 nil 表示这张图不需要再做。
func (r *Runtime) pushHistoryImageDescriptionJob(job *historyImageDescJob) chan struct{} {
	now := time.Now()
	r.historyImageDescMu.Lock()
	if r.historyImageDescJobs == nil {
		r.historyImageDescJobs = map[string]*historyImageDescJob{}
	}
	if r.historyImageDescReady == nil {
		r.historyImageDescReady = map[string]struct{}{}
	}
	if r.historyImageDescFailed == nil {
		r.historyImageDescFailed = map[string]historyImageDescFailure{}
	}
	if _, ready := r.historyImageDescReady[job.hash]; ready {
		r.historyImageDescMu.Unlock()
		return nil
	}
	if existing := r.historyImageDescJobs[job.hash]; existing != nil {
		var preempt context.CancelFunc
		if job.urgent && !existing.urgent {
			existing.urgent = true
			existing.explicit = true
			if existing != r.historyImageDescRunning {
				r.removeQueuedHistoryImageDescriptionJobLocked(existing)
				r.insertHistoryImageDescriptionJobLocked(existing)
			}
		} else if job.explicit {
			existing.explicit = true
		}
		if job.urgent {
			preempt = r.preemptRunningHistoryImageDescriptionLocked()
		}
		done := existing.done
		r.historyImageDescMu.Unlock()
		if preempt != nil {
			preempt()
		}
		r.wakeHistoryImageDescriptionWorker()
		return done
	}
	if failure, failed := r.historyImageDescFailed[job.hash]; failed && !job.urgent {
		if failure.retryAt.After(now) || (failure.count >= historyImageDescriptionMaxFailures && !job.explicit) {
			r.historyImageDescMu.Unlock()
			return nil
		}
	}
	if !job.urgent && len(r.historyImageDescJobs) >= historyImageDescriptionQueueLimit {
		r.historyImageDescMu.Unlock()
		return nil
	}
	r.historyImageDescJobs[job.hash] = job
	r.insertHistoryImageDescriptionJobLocked(job)
	var preempt context.CancelFunc
	if job.urgent {
		preempt = r.preemptRunningHistoryImageDescriptionLocked()
	}
	start := !r.historyImageDescWorker
	r.historyImageDescWorker = true
	r.historyImageDescMu.Unlock()
	if preempt != nil {
		preempt()
	}
	if start {
		go func() {
			defer recoverGoroutinePanic("recallImageContext.historyImageDescriptionWorker")
			r.historyImageDescriptionWorker()
		}()
	} else {
		r.wakeHistoryImageDescriptionWorker()
	}
	return job.done
}

// insertHistoryImageDescriptionJobLocked 加急任务排在已有加急任务之后、普通任务之前，
// 普通任务排在末尾。
func (r *Runtime) insertHistoryImageDescriptionJobLocked(job *historyImageDescJob) {
	if !job.urgent {
		r.historyImageDescQueue = append(r.historyImageDescQueue, job)
		return
	}
	position := 0
	for position < len(r.historyImageDescQueue) && r.historyImageDescQueue[position].urgent {
		position++
	}
	r.historyImageDescQueue = append(r.historyImageDescQueue, nil)
	copy(r.historyImageDescQueue[position+1:], r.historyImageDescQueue[position:])
	r.historyImageDescQueue[position] = job
}

func (r *Runtime) removeQueuedHistoryImageDescriptionJobLocked(job *historyImageDescJob) {
	for index, queued := range r.historyImageDescQueue {
		if queued == job {
			r.historyImageDescQueue = append(r.historyImageDescQueue[:index], r.historyImageDescQueue[index+1:]...)
			return
		}
	}
}

// preemptRunningHistoryImageDescriptionLocked 标记正在跑的普通任务为被打断，
// 返回它的取消函数，由调用方在锁外调用。
func (r *Runtime) preemptRunningHistoryImageDescriptionLocked() context.CancelFunc {
	running := r.historyImageDescRunning
	if running == nil || running.urgent {
		return nil
	}
	// 刚取出、还没拿到取消函数的任务也要标记，runHistoryImageDescriptionJob 开工前会看这个标记。
	running.preempted = true
	if running.cancel == nil {
		return nil
	}
	cancel := running.cancel
	running.cancel = nil
	return cancel
}

func (r *Runtime) wakeHistoryImageDescriptionWorker() {
	r.historyImageDescMu.Lock()
	wake := r.historyImageDescWake
	if wake == nil {
		wake = make(chan struct{}, 1)
		r.historyImageDescWake = wake
	}
	r.historyImageDescMu.Unlock()
	select {
	case wake <- struct{}{}:
	default:
	}
}

func (r *Runtime) historyImageDescriptionBaseContext() context.Context {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.runCtx != nil {
		return r.runCtx
	}
	return context.Background()
}

func (r *Runtime) historyImageDescriptionTimeoutValue() time.Duration {
	if r.historyImageDescTimeout > 0 {
		return r.historyImageDescTimeout
	}
	return historyImageDescriptionTimeout
}

// historyImageDescriptionBackoffValue 为负时表示不退避（测试用）。
func (r *Runtime) historyImageDescriptionBackoffValue() time.Duration {
	switch {
	case r.historyImageDescBackoff > 0:
		return r.historyImageDescBackoff
	case r.historyImageDescBackoff < 0:
		return 0
	}
	return historyImageDescriptionRetryBackoff
}

func (r *Runtime) historyImageDescriptionWorker() {
	for {
		job := r.nextHistoryImageDescriptionJob()
		if job == nil {
			return
		}
		r.runHistoryImageDescriptionJob(job)
	}
}

// nextHistoryImageDescriptionJob 取下一个可以开工的任务。加急任务随时开工；
// 普通任务要等前台和其他 worker 都空下来。队列空了 worker 退出，下次入队再起。
func (r *Runtime) nextHistoryImageDescriptionJob() *historyImageDescJob {
	base := r.historyImageDescriptionBaseContext()
	ticker := time.NewTicker(historyImageDescriptionIdlePoll)
	defer ticker.Stop()
	for {
		r.historyImageDescMu.Lock()
		if r.historyImageDescWake == nil {
			r.historyImageDescWake = make(chan struct{}, 1)
		}
		wake := r.historyImageDescWake
		if len(r.historyImageDescQueue) == 0 || base.Err() != nil {
			r.historyImageDescWorker = false
			r.historyImageDescMu.Unlock()
			return nil
		}
		head := r.historyImageDescQueue[0]
		if head.urgent || (r.historyImageDescFront == 0 && r.activeCount() == 0) {
			r.historyImageDescQueue = r.historyImageDescQueue[1:]
			r.historyImageDescRunning = head
			r.historyImageDescMu.Unlock()
			return head
		}
		r.historyImageDescMu.Unlock()
		select {
		case <-base.Done():
		case <-wake:
		case <-ticker.C:
		}
	}
}

func (r *Runtime) runHistoryImageDescriptionJob(job *historyImageDescJob) {
	base := r.historyImageDescriptionBaseContext()
	ctx, cancel := context.WithTimeout(base, r.historyImageDescriptionTimeoutValue())
	r.historyImageDescMu.Lock()
	job.cancel = cancel
	preempted := job.preempted
	r.historyImageDescMu.Unlock()
	var err error
	if preempted {
		err = context.Canceled
	} else {
		err = r.describeHistoryImageJob(ctx, job)
	}
	cancel()

	r.historyImageDescMu.Lock()
	r.historyImageDescRunning = nil
	job.cancel = nil
	if job.preempted && base.Err() == nil {
		// 被新消息打断：放回同类任务的最前面，下次从头开始，不算失败。
		job.preempted = false
		if job.urgent {
			r.insertHistoryImageDescriptionJobLocked(job)
		} else {
			position := 0
			for position < len(r.historyImageDescQueue) && r.historyImageDescQueue[position].urgent {
				position++
			}
			r.historyImageDescQueue = append(r.historyImageDescQueue, nil)
			copy(r.historyImageDescQueue[position+1:], r.historyImageDescQueue[position:])
			r.historyImageDescQueue[position] = job
		}
		r.historyImageDescMu.Unlock()
		return
	}
	delete(r.historyImageDescJobs, job.hash)
	failures := 0
	if err != nil && base.Err() == nil {
		failure := r.historyImageDescFailed[job.hash]
		failure.count++
		failure.retryAt = time.Now().Add(r.historyImageDescriptionBackoffValue())
		if len(r.historyImageDescFailed) >= historyImageDescriptionFailureLimit {
			for existing := range r.historyImageDescFailed {
				delete(r.historyImageDescFailed, existing)
				break
			}
		}
		r.historyImageDescFailed[job.hash] = failure
		failures = failure.count
	} else if err == nil {
		delete(r.historyImageDescFailed, job.hash)
	}
	close(job.done)
	r.historyImageDescMu.Unlock()

	if failures > 0 {
		if failures >= historyImageDescriptionMaxFailures {
			log.Printf("diana history image description failed %d times, giving up until explicitly read: message_id=%s err=%v", failures, job.event.MessageID, err)
		} else {
			log.Printf("diana history image description failed: message_id=%s attempt=%d err=%v", job.event.MessageID, failures, err)
		}
	}
}

func (r *Runtime) describeHistoryImageJob(ctx context.Context, job *historyImageDescJob) error {
	if !job.explicit && !boolValue(r.effectiveConfigForEvent(job.event).AutoImageDescription, true) {
		return nil
	}
	store := r.recallImageDescriptionStore()
	if store == nil {
		return fmt.Errorf("image description store is not configured")
	}
	record, found, err := store.GetImageDescription(ctx, job.hash)
	if err != nil {
		return err
	}
	if found && strings.TrimSpace(record.Description) != "" {
		r.markHistoryImageDescriptionReady(job.hash)
		// 描述早就生成过（同一张表情包、或升级前留下的记录），但这条消息的
		// 检索文本可能还是空的：顺手补上，老历史才搜得到。
		r.refreshMessageImageSearchText(ctx, job.indexEvent)
		return nil
	}
	description, err := r.describeRecallImage(ctx, job.event, job.source)
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return fmt.Errorf("vision call exceeded %s: %w", r.historyImageDescriptionTimeoutValue(), err)
		}
		return err
	}
	if err := store.SaveImageDescription(ctx, ImageDescriptionRecord{
		ContentSHA256:   job.hash,
		Description:     compactRecallImageDescription(description),
		SourceSession:   sessionKey(job.event),
		SourceMessageID: job.event.MessageID,
		Source:          "vision",
		Version:         recallImageDescriptionVersion,
	}); err != nil {
		return err
	}
	r.markHistoryImageDescriptionReady(job.hash)
	r.refreshMessageImageSearchText(ctx, job.indexEvent)
	return nil
}

// beginHistoryImageDescriptionForeground 让普通描述任务让路给可见回复；加急任务
// 正是这次回复在等的，不打断。
func (r *Runtime) beginHistoryImageDescriptionForeground() {
	if r == nil {
		return
	}
	r.historyImageDescMu.Lock()
	r.historyImageDescFront++
	preempt := r.preemptRunningHistoryImageDescriptionLocked()
	r.historyImageDescMu.Unlock()
	if preempt != nil {
		preempt()
	}
}

func (r *Runtime) endHistoryImageDescriptionForeground() {
	if r == nil {
		return
	}
	r.historyImageDescMu.Lock()
	if r.historyImageDescFront > 0 {
		r.historyImageDescFront--
	}
	r.historyImageDescMu.Unlock()
	r.wakeHistoryImageDescriptionWorker()
}

// recentSenderImageEvents 找出当前发言者在不久前单独发的带图消息：先发图、
// 隔一会儿再发文字问「这是啥」时，这句话的意思取决于那张图的描述。
// 同一轮合并进来的消息原图会直接附上，不在这里等。
func recentSenderImageEvents(history []MessageEvent, event MessageEvent, skip map[string]bool) []MessageEvent {
	userID := strings.TrimSpace(event.UserID)
	if userID == "" {
		return nil
	}
	var out []MessageEvent
	for index := len(history) - 1; index >= 0 && len(out) < recentSenderImageMessages; index-- {
		item := history[index]
		messageID := strings.TrimSpace(item.MessageID)
		if messageID == "" || messageID == event.MessageID || skip[messageID] {
			continue
		}
		if strings.TrimSpace(item.botReply) != "" || strings.TrimSpace(item.UserID) != userID {
			continue
		}
		if event.Time > 0 && item.Time > 0 && time.Duration(event.Time-item.Time)*time.Second > recentSenderImageWindow {
			break
		}
		if historicalMediaCount(item) > 0 && hasImageSegment(append(append([]MessageSegment(nil), item.Segments...), quotedSegments(item)...)) {
			out = append(out, item)
		}
	}
	return out
}

func quotedSegments(event MessageEvent) []MessageSegment {
	if event.Quoted == nil {
		return nil
	}
	return event.Quoted.Segments
}
