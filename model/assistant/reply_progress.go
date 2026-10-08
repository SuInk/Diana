// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/SuInk/diana/model/agent"
)

// 一轮回复连着搜索、读网页、生图时可能跑一两分钟，期间群里只看得到「正在输入」，
// 甚至什么都看不到，分不清机器人是在干活还是挂了。跑过一阵还没收尾，就报一句
// 做到哪了；次数有上限，回复一出来就停。
var (
	replyProgressFirst = 40 * time.Second
	replyProgressEvery = 90 * time.Second
	replyProgressMax   = 3
)

type replyProgress struct {
	mu      sync.Mutex
	started time.Time
	counts  map[string]int
	pages   int
	running string
	done    chan struct{}
	once    sync.Once
}

// startReplyProgress 返回要挂到 Runner 上的观察者（包着原观察者）和停止函数。
func (r *Runtime) startReplyProgress(ctx context.Context, event MessageEvent, inner agent.RunObserver) (agent.RunObserver, func()) {
	// 只报实时回复：定时查询、订阅、后台任务也走 generateReply，它们没人在等这一句，
	// 中途插一条「还在弄」只会打扰订阅的会话。
	if outboundTurnFromContext(ctx) == nil {
		return inner, func() {}
	}
	progress := &replyProgress{started: time.Now(), counts: map[string]int{}, done: make(chan struct{})}
	send := func(text string) {
		if err := r.sendOutgoing(ctx, event, routeOutgoingToEvent(event, OutgoingMessage{Text: text})); err != nil {
			log.Printf("diana reply progress not sent: %v", err)
			return
		}
		// 话已经出去了，这一轮不能再被合并重来，否则进度会重复发。
		r.sealDirectReply(ctx)
		if typing := typingIndicatorFromContext(ctx); typing != nil {
			typing.resume()
		}
	}
	first, every, limit := replyProgressFirst, replyProgressEvery, replyProgressMax
	go func() {
		defer recoverGoroutinePanic("reply.progress")
		progress.loop(ctx, send, first, every, limit)
	}()
	observer := func(ctx context.Context, runEvent agent.RunEvent) {
		progress.observe(runEvent)
		if inner != nil {
			inner(ctx, runEvent)
		}
	}
	return observer, progress.stop
}

func (p *replyProgress) stop() { p.once.Do(func() { close(p.done) }) }

func (p *replyProgress) observe(event agent.RunEvent) {
	p.mu.Lock()
	defer p.mu.Unlock()
	switch event.Phase {
	case agent.RunPhaseToolStarted:
		p.running = event.Tool
	case agent.RunPhaseToolCompleted:
		p.running = ""
		if event.Error != "" {
			return
		}
		p.counts[event.Tool]++
		if event.Tool == "browser_render" {
			p.pages += max(1, len(stringsFromAny(event.ToolInput["urls"])))
		}
	case agent.RunPhaseCompleted, agent.RunPhaseFailed:
		p.once.Do(func() { close(p.done) })
	}
}

func (p *replyProgress) loop(ctx context.Context, send func(string), first, every time.Duration, limit int) {
	timer := time.NewTimer(first)
	defer timer.Stop()
	for sent := 0; sent < limit; {
		select {
		case <-ctx.Done():
			return
		case <-p.done:
			return
		case <-timer.C:
		}
		timer.Reset(every)
		text, ok := p.render(time.Now())
		if !ok {
			continue
		}
		// 计时器和收尾可能同时到：收尾了就别再报「还在弄」。
		select {
		case <-p.done:
			return
		default:
		}
		send(text)
		sent++
	}
}

// render 只在真的调过或正在调工具时出声：光是模型想得慢，报了也没有内容。
func (p *replyProgress) render(now time.Time) (string, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	parts := make([]string, 0, 4)
	if n := p.counts["web_search"]; n > 0 {
		parts = append(parts, fmt.Sprintf("搜了 %d 次", n))
	}
	if p.pages > 0 {
		parts = append(parts, fmt.Sprintf("读了 %d 个网页", p.pages))
	}
	other := 0
	for tool, n := range p.counts {
		if tool != "web_search" && tool != "browser_render" {
			other += n
		}
	}
	if other > 0 {
		parts = append(parts, fmt.Sprintf("用了 %d 次其他工具", other))
	}
	if len(parts) == 0 && p.running == "" {
		return "", false
	}
	text := fmt.Sprintf("⏳ 还在弄，已经 %d 秒", int(now.Sub(p.started).Seconds()))
	if len(parts) > 0 {
		text += "，" + strings.Join(parts, "、")
	}
	if doing := replyProgressDoing(p.running); doing != "" {
		text += "，现在在" + doing
	}
	return text + "，弄好马上回。", true
}

func replyProgressDoing(tool string) string {
	switch tool {
	case "":
		return ""
	case "web_search":
		return "搜索"
	case "browser_render":
		return "读网页"
	case dianaCodingToolName:
		return "派编码任务"
	case "run_command":
		return "跑命令"
	default:
		if strings.Contains(tool, "image") {
			return "处理图片"
		}
		if strings.Contains(tool, "video") {
			return "处理视频"
		}
		return "调用工具"
	}
}

func stringsFromAny(value any) []string {
	switch typed := value.(type) {
	case []string:
		return typed
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			if text, ok := item.(string); ok && strings.TrimSpace(text) != "" {
				out = append(out, text)
			}
		}
		return out
	}
	return nil
}
