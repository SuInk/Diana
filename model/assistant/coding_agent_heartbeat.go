// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"
)

// 编码任务以前只在开头受理、结尾汇报，中间几十分钟一声不吭，主人分不清它是在干活
// 还是已经挂了。跑久了就隔一阵报一句：跑了多久、调了多少次工具、最近在干什么；
// 日志长时间没动就直说可能卡住、怎么取消。次数有上限，不至于刷屏。
var (
	codingHeartbeatFirst = 5 * time.Minute
	codingHeartbeatEvery = 10 * time.Minute
	codingHeartbeatMax   = 6
)

// runCodingHeartbeat 在任务结束（ctx 取消）前按节奏发进度。send 返回错误只记日志，
// 不影响任务本身。
func runCodingHeartbeat(ctx context.Context, job CodingJob, send func(context.Context, string) error) {
	every, limit := codingHeartbeatEvery, codingHeartbeatMax
	timer := time.NewTimer(codingHeartbeatFirst)
	defer timer.Stop()
	lastAction := ""
	for sent := 0; sent < limit; sent++ {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		snapshot := parseCodingLog(job.LogPath)
		if snapshot.Done {
			return
		}
		text := renderCodingHeartbeat(job, snapshot, lastAction, every, time.Now())
		lastAction = snapshot.LastAction
		if err := send(ctx, text); err != nil {
			log.Printf("diana coding job %s heartbeat not sent: %v", job.ID, err)
		}
		timer.Reset(every)
	}
}

func renderCodingHeartbeat(job CodingJob, snapshot codingJobSnapshot, lastAction string, stallAfter time.Duration, now time.Time) string {
	var builder strings.Builder
	fmt.Fprintf(&builder, "⏳ 编码任务 %s 还在跑，已 %s", job.ID, formatCodingDuration(now.Sub(job.StartedAt)))
	if snapshot.Tools > 0 {
		fmt.Fprintf(&builder, "，调用了 %d 次工具", snapshot.Tools)
	}
	idle := time.Duration(0)
	if !snapshot.UpdatedAt.IsZero() {
		idle = now.Sub(snapshot.UpdatedAt)
	}
	switch {
	case idle >= stallAfter:
		fmt.Fprintf(&builder, "。\n最近 %s 没有新动作，可能卡住了；不想等可以让我取消任务 %s。", formatCodingDuration(idle), job.ID)
	case snapshot.LastAction != "" && snapshot.LastAction != lastAction:
		builder.WriteString("。\n最近在：" + truncateRunes(snapshot.LastAction, 80))
	default:
		builder.WriteString("。")
	}
	return builder.String()
}
