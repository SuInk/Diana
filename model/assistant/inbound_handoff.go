// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"log"
	"strings"
	"time"
)

// 连发交接的持久化。同一个人连发时，更早那条交给后到那条一并回答（见 sender_burst.go）：
// 早的那一轮立刻以 handed_off_pending 收尾、让出所有名额和锁，不等；交接状态写进
// inbound_events，后到那条回出去才落定，没回出去就把早的那条重新排进队列。
//
// 进程重启后内存里的交接关系全没了，没有哪一轮还能替它落定，所以协调者启动时和
// 之后每隔一段时间扫一遍：凡是「不属于本进程里一个还在跑的接手轮次」的待定交接，
// 一律放回去，让那条消息自己回答。
const (
	// inboundOutcomeHandedOffPending 是交给后一条、还没落定的那一轮的结果。
	inboundOutcomeHandedOffPending = "handed_off_pending"
	// inboundHandoffSweepPeriod 是巡检待定交接的间隔。
	inboundHandoffSweepPeriod = 30 * time.Second
	inboundHandoffSweepLimit  = 100
)

// InboundHandoff 是一条待定的交接：Event 交给了 AbsorberID 那一轮。
type InboundHandoff struct {
	Event      MessageEvent
	AbsorberID string
	MarkedAt   time.Time
}

// InboundHandoffStore 持久化连发交接。事件按会话、发送者和消息 ID 定位（取最新那行），
// AbsorberID 是接手那一轮的入站事件 ID。
type InboundHandoffStore interface {
	// MarkInboundHandoff 把事件标成交给 absorberID 那一轮、待定。已经被标过的不改。
	MarkInboundHandoff(ctx context.Context, event MessageEvent, absorberID string) error
	// FinalizeInboundHandoff 在接手那一轮真的回出去之后落定交接。
	FinalizeInboundHandoff(ctx context.Context, event MessageEvent, absorberID string) error
	// ReleaseInboundHandoff 撤销交接；那一轮已经以 handed_off_pending 收尾的，
	// 重新排进队列（提高优先级）。返回是否重新排队了。
	ReleaseInboundHandoff(ctx context.Context, event MessageEvent, absorberID string) (bool, error)
	// CompleteInboundHandoff 是交出去那一轮的收尾：落终态，并写上交接状态（absorberID、
	// 待定或已落定）。撤销若赶在它之前、又没重排成功，由巡检兜底放回。
	CompleteInboundHandoff(ctx context.Context, id string, leaseOwner string, absorberID string, final bool) error
	// ListPendingInboundHandoffs 列出待定的交接。
	ListPendingInboundHandoffs(ctx context.Context, limit int) ([]InboundHandoff, error)
}

func (r *Runtime) inboundHandoffStore() InboundHandoffStore {
	r.mu.RLock()
	defer r.mu.RUnlock()
	store, _ := r.inboundStore.(InboundHandoffStore)
	return store
}

// sweepInboundHandoffs 放回不属于本进程里任何在跑的接手轮次的待定交接。
func (r *Runtime) sweepInboundHandoffs(ctx context.Context) {
	store := r.inboundHandoffStore()
	if store == nil {
		return
	}
	listCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	pending, err := store.ListPendingInboundHandoffs(listCtx, inboundHandoffSweepLimit)
	cancel()
	if err != nil {
		log.Printf("diana inbound handoff sweep failed: %v", err)
		return
	}
	requeued := false
	for _, handoff := range pending {
		if r.absorberLive(handoff.AbsorberID) {
			continue
		}
		releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		again, err := store.ReleaseInboundHandoff(releaseCtx, handoff.Event, handoff.AbsorberID)
		cancel()
		if err != nil {
			log.Printf("diana inbound handoff release failed: %v", err)
			continue
		}
		log.Printf("diana inbound handoff: message %s released from absorber %s (absorber no longer running)", strings.TrimSpace(handoff.Event.MessageID), handoff.AbsorberID)
		requeued = requeued || again
	}
	if requeued {
		r.wakeInboundWorkers()
	}
}

// absorberLive 报告这个接手轮次是不是还在本进程里跑。
func (r *Runtime) absorberLive(absorberID string) bool {
	absorberID = strings.TrimSpace(absorberID)
	if absorberID == "" {
		return false
	}
	r.replyInterruptMu.Lock()
	defer r.replyInterruptMu.Unlock()
	return r.liveAbsorbers[absorberID] > 0
}

// completeHandedOffInbound 是交出去那一轮在入站队列里的收尾：交接还在就落终态
// （并把交接状态写上，免得接手那边的落库慢一步），交接已经撤销就直接重新排队。
func (r *Runtime) completeHandedOffInbound(ctx context.Context, store InboundEventStore, item InboundQueueItem, leaseOwner string) error {
	absorberID, final, still := r.handedOffState(item.Event)
	handoffStore, durable := store.(InboundHandoffStore)
	if !still {
		r.clearOutboundSteps(item.ID)
		return store.RetryInboundEvent(ctx, item.ID, leaseOwner, time.Now(), "连发交接已撤销，这条重新回答")
	}
	if !durable {
		r.clearOutboundSteps(item.ID)
		return store.CompleteInboundEvent(ctx, item.ID, leaseOwner, inboundOutcomeHandedOffPending)
	}
	r.clearOutboundSteps(item.ID)
	return handoffStore.CompleteInboundHandoff(ctx, item.ID, leaseOwner, absorberID, final)
}
