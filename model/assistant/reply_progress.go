// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/SuInk/diana/model/agent"
)

// 记录正在执行的回复，供独立追问读取上下文；不主动发送聊天进度。
type replyProgress struct {
	mu      sync.Mutex
	started time.Time
	counts  map[string]int
	pages   int
	running string
	// messageID 是这一轮在回答的消息；同一个人后面的消息靠它区分「前一轮」和自己。
	messageID string
}

// startReplyProgress 返回要挂到 Runner 上的观察者（包着原观察者）和停止函数。
func (r *Runtime) startReplyProgress(ctx context.Context, event MessageEvent, inner agent.RunObserver) (agent.RunObserver, func()) {
	// 只跟踪实时回复，后台任务不参与同一发送者的追问上下文。
	if outboundTurnFromContext(ctx) == nil {
		return inner, func() {}
	}
	progress := &replyProgress{started: time.Now(), counts: map[string]int{}, messageID: strings.TrimSpace(event.MessageID)}
	key := consecutiveReplyKey(event)
	r.registerReplyProgress(key, progress)
	observer := func(ctx context.Context, runEvent agent.RunEvent) {
		progress.observe(runEvent)
		if inner != nil {
			inner(ctx, runEvent)
		}
	}
	return observer, func() {
		r.unregisterReplyProgress(key, progress)
	}
}

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
	}
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
