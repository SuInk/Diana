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

// errChannelNotConnected 标记「连接还没建立或刚断开」这一类发送失败。WebSocket 通道
// 没有活连接时直接返回它，不去碰网络。和对端回了错误的发送失败不同，这一类只要连接
// 回来就能发出去。
var errChannelNotConnected = errors.New("diana: channel is not connected")

type channelNotConnectedError struct {
	message string
}

// newChannelNotConnectedError 保留各通道原来的报错原文，只额外挂上 errChannelNotConnected，
// 让上层能用 errors.Is 认出来。
func newChannelNotConnectedError(message string) error {
	return &channelNotConnectedError{message: message}
}

func (e *channelNotConnectedError) Error() string { return e.message }

func (e *channelNotConnectedError) Is(target error) bool { return target == errChannelNotConnected }

const (
	// scheduledDeliveryReadyWait 是一次定时投递最多等连接就绪多久。等不到就把这次
	// 认领放掉，下一秒的调度会重新认领、接着等：连接迟迟不回来时，停用机器人、取消
	// 提醒这些改动仍然能在下一轮认领时生效，不会被一直占着的认领挡住。
	scheduledDeliveryReadyWait = 10 * time.Minute
	// scheduledDeliveryPollInterval 是等连接时查一次状态的间隔。状态在内存里，间隔短
	// 一点，接入端连上之后消息几乎是跟着就到。
	scheduledDeliveryPollInterval = time.Second
)

// scheduledDeliveryWaitTiming 是 Runtime 上等连接的节奏，零值走默认；只给测试调快。
type scheduledDeliveryWaitTiming struct {
	limit time.Duration
	poll  time.Duration
}

func (r *Runtime) scheduledDeliveryTiming() scheduledDeliveryWaitTiming {
	r.mu.RLock()
	timing := r.scheduledDeliveryWait
	r.mu.RUnlock()
	if timing.limit <= 0 {
		timing.limit = scheduledDeliveryReadyWait
	}
	if timing.poll <= 0 {
		timing.poll = scheduledDeliveryPollInterval
	}
	return timing
}

type scheduledDeliveryContextKey struct{}

// withScheduledDelivery 标记这次投递来自提醒和订阅调度：连接没就绪时，发送要等连接
// 回来再发，不能直接判失败。聊天回复不走这条路，它有入站队列的离线回补。
func withScheduledDelivery(ctx context.Context) context.Context {
	return context.WithValue(ctx, scheduledDeliveryContextKey{}, true)
}

func scheduledDelivery(ctx context.Context) bool {
	marked, _ := ctx.Value(scheduledDeliveryContextKey{}).(bool)
	return marked
}

// deliveryNotReady 判断这次失败是不是只因为连接还没就绪：WebSocket 没有活连接，或者
// 群发送闸门看到连接离线、账号异常直接拒发。扇出投递合并的多个错误要全部属于这一类
// 才算；只要有一个目标是真的发失败，就交给原来的失败重试和告警。
func deliveryNotReady(err error) bool {
	if err == nil {
		return false
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		children := joined.Unwrap()
		if len(children) == 0 {
			return false
		}
		for _, child := range children {
			if !deliveryNotReady(child) {
				return false
			}
		}
		return true
	}
	if err == errChannelNotConnected || err == errOutboundChannelOffline {
		return true
	}
	if matcher, ok := err.(interface{ Is(error) bool }); ok && (matcher.Is(errChannelNotConnected) || matcher.Is(errOutboundChannelOffline)) {
		return true
	}
	return deliveryNotReady(errors.Unwrap(err))
}

// deliverNotice 把一条通知分条发出去。定时投递碰上连接没就绪时，等目标机器人的连接
// 就绪再发，最多等 scheduledDeliveryReadyWait；等到头还没连上，就原样返回「未就绪」，
// 由调度那边放掉认领，下一轮再来。
//
// 只有「连接没就绪」这一种会等：连接就绪后发送失败的，照旧返回错误，走原来的持久化
// 重试和告警。「没就绪」是在碰网络之前被拦下的，启动期那一次整条都没发出去，等完
// 重发不会重复。一条长通知发到一半连接断了，重发会把前面几段再发一遍——原来的
// 失败重试也是整条重发，这点没有变。
func (r *Runtime) deliverNotice(ctx context.Context, event MessageEvent, text string) ([]string, error) {
	send := func() ([]string, error) {
		cfg := r.effectiveConfigForEvent(event)
		return r.deliverChunks(ctx, event, splitReply(text, notificationChunkSize), cfg, outboundDecoration{
			MentionUserID: strings.TrimSpace(event.UserID),
			MentionAlways: true,
		})
	}
	messageIDs, err := send()
	if err == nil || !scheduledDelivery(ctx) || !deliveryNotReady(err) {
		return messageIDs, err
	}
	timing := r.scheduledDeliveryTiming()
	deadline := time.Now().Add(timing.limit)
	log.Printf("diana scheduled delivery to profile %q waits for its connection: %v", strings.TrimSpace(event.ProfileID), err)
	for {
		ready, waitErr := r.waitScheduledDeliveryReady(ctx, event.ProfileID, deadline, timing.poll)
		if waitErr != nil {
			return nil, waitErr
		}
		if !ready {
			return nil, err
		}
		messageIDs, err = send()
		if err == nil || !deliveryNotReady(err) {
			return messageIDs, err
		}
	}
}

// waitScheduledDeliveryReady 等目标机器人的连接可以发消息：连接在、账号也正常。至少
// 先等一个轮询间隔，状态说已就绪、发送却仍报未连接时不会原地空转。ctx 结束或机器人
// 被停用返回错误；到了 deadline 仍未就绪返回 false。
func (r *Runtime) waitScheduledDeliveryReady(ctx context.Context, profileID string, deadline time.Time, poll time.Duration) (bool, error) {
	ticker := time.NewTicker(poll)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-ticker.C:
		}
		if r.profileDisabled(profileID) {
			return false, fmt.Errorf("%w: %s", ErrDeliveryTargetDisabled, strings.TrimSpace(profileID))
		}
		if status, known := r.deliveryConnectionStatus(profileID); !known || channelEffectivelyOnline(status) {
			return true, nil
		}
		if !time.Now().Before(deadline) {
			return false, nil
		}
	}
}

// deferNotReadyReminder 处理「等了一整段连接仍没就绪」的那次运行：不计连败、不发
// 失败告警、不推迟下次触发，原样留着让下一轮调度重新认领接着等。周期订阅要发的内容
// 已经存进 PendingDelivery，下一轮只补投这一份，不会重新抓取或重新生成。
func (r *Runtime) deferNotReadyReminder(item Reminder, err error) bool {
	if !deliveryNotReady(err) {
		return false
	}
	log.Printf("diana reminder %s delivery deferred until its connection is ready: %v", item.ID, err)
	return true
}
