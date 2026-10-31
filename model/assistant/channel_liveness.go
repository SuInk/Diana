// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"fmt"
	"log"
	"sync/atomic"
	"time"
)

// receiveLiveness 记录收消息循环最近一次有进展的时间。
//
// 各平台只在连上或出错时改 Connected；收消息循环如果自己卡住（死锁、阻塞在
// 某次处理里），既不会出错也不会返回，状态就一直停在「已连接」，平台那边其实
// 早已判我们离线。所以对外报告状态时再看一眼进展：超过时限没动静就按离线报，
// 并记一条日志，免得只有登录 WebUI 才看得到。
type receiveLiveness struct {
	last   atomic.Int64 // UnixNano；0 表示还没开始收
	limit  atomic.Int64 // 时限（纳秒）；0 表示不判
	logged atomic.Bool
}

// mark 记一次进展：读到一帧网关报文，或完成一次轮询。
func (l *receiveLiveness) mark(now time.Time) {
	l.last.Store(now.UnixNano())
	l.logged.Store(false)
}

// setLimit 设定多久没进展算停滞，一般取两到三个心跳或轮询周期。
func (l *receiveLiveness) setLimit(limit time.Duration) {
	l.limit.Store(int64(limit))
}

// reset 在连接结束时清掉进展，下一轮连上再重新计。
func (l *receiveLiveness) reset() {
	l.last.Store(0)
	l.logged.Store(false)
}

// apply 把停滞折算进状态：已连接但收消息循环超时没动静时，改报离线。
func (l *receiveLiveness) apply(platform string, status ChannelStatus, now time.Time) ChannelStatus {
	last, limit := l.last.Load(), time.Duration(l.limit.Load())
	if !status.Connected || last == 0 || limit <= 0 {
		return status
	}
	idle := now.Sub(time.Unix(0, last))
	if idle <= limit {
		return status
	}
	status.Connected = false
	status.LastError = fmt.Sprintf("收消息已停滞 %s（超过 %s 没有任何进展），平台侧多半已判离线", idle.Round(time.Second), limit)
	if l.logged.CompareAndSwap(false, true) {
		log.Printf("%s: %s", platform, status.LastError)
	}
	return status
}
