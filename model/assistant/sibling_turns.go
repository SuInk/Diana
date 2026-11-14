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
	"github.com/SuInk/diana/model/llm"
)

// 同一个群里同时可以跑几轮回复（InboundGroupConcurrency）。每轮的历史在开跑那一刻
// 就定了：另一轮在干什么、中途发了什么，这一轮都不知道。2026-11-11 两个人前后脚要
// 同一个 GitHub 头像，两轮各自查、各自发，4 秒内同一张图发了两遍；第二轮直到收尾
// 那一步，请求里都没有第一轮已经发出的那张图。
//
// 这里补两件事：开跑时告诉它同群还有哪几轮在跑、各自在回谁的哪句（siblingRepliesNote）；
// 跑的过程中，别的轮发到群里的东西在下一步规划前插进来（siblingSendInterjections）。

const (
	sessionSendKeep        = 10 * time.Minute
	sessionSendMaxKeep     = 40
	siblingDuplicateWindow = 90 * time.Second
	siblingDuplicateRunes  = 8
)

type sessionSendRecord struct {
	at       time.Time
	turnID   string
	text     string
	fullText string
	images   int
	videos   int
	audios   int
}

// sessionSendLog 记各会话最近发出去的消息，按机器人分开。自带锁。
type sessionSendLog struct {
	mu    sync.Mutex
	items map[string][]sessionSendRecord
}

func sessionSendLogKey(event MessageEvent) string {
	session := sessionKey(event)
	if session == "" {
		return ""
	}
	return strings.TrimSpace(event.ProfileID) + "|" + session
}

func (l *sessionSendLog) note(event MessageEvent, turnID string, msg OutgoingMessage, at time.Time) {
	key := sessionSendLogKey(event)
	if key == "" {
		return
	}
	images, videos, audios := outgoingMediaCounts(msg)
	cleanText := normalizeSiblingCompareText(msg.Text)
	record := sessionSendRecord{
		at:       at,
		turnID:   turnID,
		text:     truncateReplyExcerpt(stripReplyMarkers(msg.Text), 80),
		fullText: cleanText,
		images:   images,
		videos:   videos,
		audios:   audios,
	}
	if record.text == "" && images+videos+audios == 0 {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.items == nil {
		l.items = make(map[string][]sessionSendRecord)
	}
	kept := l.items[key][:0]
	for _, item := range l.items[key] {
		if at.Sub(item.at) <= sessionSendKeep {
			kept = append(kept, item)
		}
	}
	kept = append(kept, record)
	if len(kept) > sessionSendMaxKeep {
		kept = kept[len(kept)-sessionSendMaxKeep:]
	}
	l.items[key] = kept
}

// after 返回 cursor 之后、不是 ownTurn 发的记录。ownTurn 为空时全算别人的。
func (l *sessionSendLog) after(event MessageEvent, cursor time.Time, ownTurn string) []sessionSendRecord {
	key := sessionSendLogKey(event)
	if key == "" {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []sessionSendRecord
	for _, item := range l.items[key] {
		if !item.at.After(cursor) || (ownTurn != "" && item.turnID == ownTurn) {
			continue
		}
		out = append(out, item)
	}
	return out
}

func normalizeSiblingCompareText(text string) string {
	return strings.TrimSpace(stripChatPeriods(stripReplyMarkers(text)))
}

// repeatsRecentSibling 判断候选回复是否把同会话并发另一轮（在本轮启动之后）刚发出的句子原样复读了一遍。
// 在本轮启动前已经结束的旧轮次不算并发兄弟轮次；短促应答（不足 8 字）也允许各自正常回应。
func (l *sessionSendLog) repeatsRecentSibling(ctx context.Context, event MessageEvent, candidate string, now time.Time) bool {
	ownTurn := strings.TrimSpace(outboundTurnID(ctx))
	if ownTurn == "" {
		return false
	}
	turnStart, ok := replyTurnStartFromContext(ctx)
	if !ok || turnStart.IsZero() {
		return false
	}
	key := sessionSendLogKey(event)
	if key == "" {
		return false
	}
	clean := normalizeSiblingCompareText(candidate)
	if len([]rune(clean)) < siblingDuplicateRunes {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, item := range l.items[key] {
		if item.turnID == "" || item.turnID == ownTurn {
			continue
		}
		if item.at.Before(turnStart) || now.Sub(item.at) > siblingDuplicateWindow {
			continue
		}
		if item.fullText != "" && item.fullText == clean {
			return true
		}
	}
	return false
}

func forwardNodesSessionSummary(nodes []map[string]any) string {
	var parts []string
	for _, node := range nodes {
		data, _ := node["data"].(map[string]any)
		if data == nil {
			continue
		}
		if text := strings.TrimSpace(forwardNodeContentText(data["content"])); text != "" {
			parts = append(parts, text)
		}
	}
	if len(parts) > 0 {
		return fmt.Sprintf("[合并转发×%d] %s", len(nodes), strings.Join(parts, " / "))
	}
	return fmt.Sprintf("[合并转发×%d]", len(nodes))
}

func (s sessionSendRecord) describe() string {
	parts := make([]string, 0, 4)
	if s.images > 0 {
		parts = append(parts, fmt.Sprintf("图片×%d", s.images))
	}
	if s.videos > 0 {
		parts = append(parts, fmt.Sprintf("视频×%d", s.videos))
	}
	if s.audios > 0 {
		parts = append(parts, fmt.Sprintf("语音×%d", s.audios))
	}
	if s.text != "" {
		parts = append(parts, "「"+s.text+"」")
	}
	return strings.Join(parts, " ")
}

// siblingSendInterjections 在每步规划前把别的轮刚发到这个会话的东西接进来。收尾那一步
// 回来时有新的，Runner 会丢掉这一步带着它重做，免得答案和已经发出去的撞车。
type siblingSendInterjections struct {
	r      *Runtime
	event  MessageEvent
	turnID string
	mu     sync.Mutex
	cursor time.Time
}

func (r *Runtime) startSiblingSendInterjections(ctx context.Context, event MessageEvent) agent.Interjections {
	turn := outboundTurnFromContext(ctx)
	if turn == nil || sessionSendLogKey(event) == "" {
		return nil
	}
	cursor := time.Now()
	if start, ok := replyTurnStartFromContext(ctx); ok && !start.IsZero() && start.Before(cursor) {
		// 从本轮入站起点算起，避免识图、跨群预取或路由耗时期间另一轮发出的消息漏进盲区。
		cursor = start
	}
	return &siblingSendInterjections{r: r, event: event, turnID: turn.id, cursor: cursor}
}

func (s *siblingSendInterjections) Pending() bool {
	s.mu.Lock()
	cursor := s.cursor
	s.mu.Unlock()
	return len(s.r.sessionSends.after(s.event, cursor, s.turnID)) > 0
}

func (s *siblingSendInterjections) Take() []llm.Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	records := s.r.sessionSends.after(s.event, s.cursor, s.turnID)
	if len(records) == 0 {
		return nil
	}
	s.cursor = records[len(records)-1].at
	return []llm.Message{{Role: llm.RoleUser, Content: siblingSendsPrompt(records, profileLocation(s.event.ProfileID))}}
}

func siblingSendsPrompt(records []sessionSendRecord, location *time.Location) string {
	var builder strings.Builder
	builder.WriteString("【你另一条回复刚发到了这里，由运行时告知】\n")
	images := false
	for _, record := range records {
		builder.WriteString("- ")
		builder.WriteString(record.at.In(location).Format("15:04:05"))
		builder.WriteString(" ")
		builder.WriteString(record.describe())
		builder.WriteString("\n")
		images = images || record.images > 0
	}
	builder.WriteString("上面引号里的句子是你刚真实发进会话里的消息，大家都已经看到了。严禁把上面发过的句子再调 send 重发一遍")
	if images {
		builder.WriteString("，同一张图也别再发")
	}
	builder.WriteString("；如果你这轮本来想说的已经被上面涵盖了，直接调 send(silent=true) 结束本轮，或只补充还没说到的新内容。")
	return builder.String()
}

// combinedInterjections 把几路中途补充合成一路，nil 的跳过。
type combinedInterjections []agent.Interjections

func combineInterjections(sources ...agent.Interjections) agent.Interjections {
	out := make(combinedInterjections, 0, len(sources))
	for _, source := range sources {
		if source != nil {
			out = append(out, source)
		}
	}
	switch len(out) {
	case 0:
		return nil
	case 1:
		return out[0]
	}
	return out
}

func (c combinedInterjections) Pending() bool {
	for _, source := range c {
		if source.Pending() {
			return true
		}
	}
	return false
}

func (c combinedInterjections) Take() []llm.Message {
	var out []llm.Message
	for _, source := range c {
		out = append(out, source.Take()...)
	}
	return out
}

// siblingRepliesNote 列出同一会话里别人触发、还在跑的回复。同一个人的上一轮由
// runningReplyNote 说，这里不重复。
func (r *Runtime) siblingRepliesNote(event MessageEvent) string {
	session := sessionSendLogKey(event)
	if session == "" {
		return ""
	}
	self := strings.TrimSpace(event.MessageID)
	sender := strings.TrimSpace(event.UserID)
	now := time.Now()
	r.replyProgressMu.Lock()
	running := make([]*replyProgress, 0, len(r.sessionReplyProgresses[session]))
	for progress := range r.sessionReplyProgresses[session] {
		if progress.messageID == self || (sender != "" && progress.senderID == sender) {
			continue
		}
		running = append(running, progress)
	}
	r.replyProgressMu.Unlock()
	if len(running) == 0 {
		return ""
	}
	sortReplyProgressByStart(running)
	var builder strings.Builder
	builder.WriteString("【这里你还有别的回复在跑】\n")
	for _, progress := range running {
		builder.WriteString("- 在回 ")
		builder.WriteString(progress.sender)
		if progress.text != "" {
			builder.WriteString(" 的「" + progress.text + "」")
		}
		builder.WriteString("，" + progress.describe(now) + "\n")
	}
	builder.WriteString("那几轮做完会自己把结果发出去，你这轮看不到它们发了什么，中途发出来的会再告诉你。" +
		"这条要的和那边是同一件事（同一张图、同一个查询）时，别各做一遍，交给那一轮，这条不回或者随口应一声就行；不是同一件事就照常回答。" +
		"「另一轮」「另一路」是你内部的事，别跟群友提。")
	return builder.String()
}
