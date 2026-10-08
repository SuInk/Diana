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
	// replyProgressNudgeAfter：回复开跑没多久就又被 @，多半只是补个 @，不用急着报。
	replyProgressNudgeAfter = 10 * time.Second
)

type replyProgress struct {
	mu      sync.Mutex
	started time.Time
	counts  map[string]int
	pages   int
	running string
	// messageID 是这一轮在回答的消息；同一个人后面的消息靠它区分「前一轮」和自己。
	messageID string
	nudge     chan struct{}
	done      chan struct{}
	once      sync.Once
}

// startReplyProgress 返回要挂到 Runner 上的观察者（包着原观察者）和停止函数。
func (r *Runtime) startReplyProgress(ctx context.Context, event MessageEvent, inner agent.RunObserver) (agent.RunObserver, func()) {
	// 只报实时回复：定时查询、订阅、后台任务也走 generateReply，它们没人在等这一句，
	// 中途插一条「还在弄」只会打扰订阅的会话。
	if outboundTurnFromContext(ctx) == nil {
		return inner, func() {}
	}
	progress := &replyProgress{started: time.Now(), counts: map[string]int{}, messageID: strings.TrimSpace(event.MessageID), nudge: make(chan struct{}, 1), done: make(chan struct{})}
	key := consecutiveReplyKey(event)
	r.registerReplyProgress(key, progress)
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
	return observer, func() {
		progress.stop()
		r.unregisterReplyProgress(key, progress)
	}
}

func (p *replyProgress) stop() { p.once.Do(func() { close(p.done) }) }

// 同一个人在长回复跑着的时候又来 @，多半是以为机器人没理他。新消息并进这一轮时
// 当场报一句进度；单独成轮时把进度交给那一轮的提示词，让它说「还在弄」而不是
// 把问题自己重答一遍，见 runningReplyContext。
func (r *Runtime) registerReplyProgress(key string, progress *replyProgress) {
	if key == "" {
		return
	}
	r.replyProgressMu.Lock()
	defer r.replyProgressMu.Unlock()
	if r.replyProgresses == nil {
		r.replyProgresses = map[string]map[*replyProgress]struct{}{}
	}
	if r.replyProgresses[key] == nil {
		r.replyProgresses[key] = map[*replyProgress]struct{}{}
	}
	r.replyProgresses[key][progress] = struct{}{}
}

func (r *Runtime) unregisterReplyProgress(key string, progress *replyProgress) {
	r.replyProgressMu.Lock()
	defer r.replyProgressMu.Unlock()
	delete(r.replyProgresses[key], progress)
	if len(r.replyProgresses[key]) == 0 {
		delete(r.replyProgresses, key)
	}
}

// earlierReplyProgress 找同一个人还没回完的另一轮（不是 event 自己那一轮），取最早开始的。
func (r *Runtime) earlierReplyProgress(event MessageEvent) *replyProgress {
	key := consecutiveReplyKey(event)
	if key == "" {
		return nil
	}
	self := strings.TrimSpace(event.MessageID)
	r.replyProgressMu.Lock()
	defer r.replyProgressMu.Unlock()
	var found *replyProgress
	for progress := range r.replyProgresses[key] {
		if progress.messageID == self {
			continue
		}
		if found == nil || progress.started.Before(found.started) {
			found = progress
		}
	}
	return found
}

// nudgeReplyProgress 在新消息并进还没回完的那一轮时，让那一轮马上报一次进度。
func (r *Runtime) nudgeReplyProgress(event MessageEvent) {
	progress := r.earlierReplyProgress(event)
	if progress == nil || time.Since(progress.started) < replyProgressNudgeAfter {
		return
	}
	select {
	case progress.nudge <- struct{}{}:
	default:
	}
}

// runningReplyNote 描述同一个人前一轮做到哪了；没有在跑的就返回空。
func (r *Runtime) runningReplyNote(event MessageEvent) string {
	progress := r.earlierReplyProgress(event)
	if progress == nil {
		return ""
	}
	return progress.describe(time.Now())
}

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
	force := false
	for sent := 0; sent < limit; {
		select {
		case <-ctx.Done():
			return
		case <-p.done:
			return
		case <-p.nudge:
			force = true
		case <-timer.C:
			timer.Reset(every)
		}
		text, ok := p.render(time.Now(), force)
		force = false
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

// render 到点时只在真的调过或正在调工具时出声：光是模型想得慢，报了也没有内容。
// 有人催（force）时没内容也报，至少让人知道还在跑。
func (p *replyProgress) render(now time.Time, force bool) (string, bool) {
	detail := p.detail()
	if detail == "" && !force {
		return "", false
	}
	return fmt.Sprintf("⏳ 还在弄，已经 %d 秒", int(now.Sub(p.started).Seconds())) + detail + "，弄好马上回。", true
}

// describe 给提示词用：同样的内容，不带客套。
func (p *replyProgress) describe(now time.Time) string {
	return fmt.Sprintf("已经跑了 %d 秒", int(now.Sub(p.started).Seconds())) + p.detail()
}

func (p *replyProgress) detail() string {
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
	text := ""
	if len(parts) > 0 {
		text += "，" + strings.Join(parts, "、")
	}
	if doing := replyProgressDoing(p.running); doing != "" {
		text += "，现在在" + doing
	}
	return text
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
