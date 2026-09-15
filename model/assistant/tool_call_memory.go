// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"fmt"
	"strings"
	"time"

	"github.com/SuInk/diana/model/agent"
)

// 下一轮的上下文里只有机器人上一条回复的正文，看不到它当时调用过哪些工具。线上 09-15：
// 群友问 iOS 更新的 iCloud 协议，机器人 20:06 确实调了 web_search，回复开头还写着「这次
// 我老老实实联网搜过了」；20:21 被追问「搜索了吗」，它顺着回答「没搜，凭记忆答的」。
// 这里把每轮实际调用过的工具和关键参数记下来，下一轮作为运行时事实注入。
const (
	recentToolCallLimit = 8
	recentToolCallTTL   = 30 * time.Minute
	toolCallContextHint = "【你前几轮实际调用过的工具，运行时记录，按时间倒序】\n" +
		"有人问「搜了吗」「查过没」「用工具了吗」时照这份记录如实回答：记录里有就说查过、查的是什么；" +
		"记录里没有才说没查。不要因为对方质疑就改口认错，也不要声称调用过记录里没有的工具。没人问时不要主动复述这份记录。\n"
)

type toolCallRecord struct {
	Tool    string
	Summary string
	Failed  bool
	At      time.Time
}

// toolCallMemoryIgnored 是不值得记的内部工具：它们不是「去查了什么」。
var toolCallMemoryIgnored = map[string]bool{
	"agent.finalize": true,
}

// rememberToolCalls 记下本轮实际执行过的工具调用，跳过的调用不算。
func (r *Runtime) rememberToolCalls(event MessageEvent, steps []agent.Step) {
	if r == nil || len(steps) == 0 {
		return
	}
	now := r.clock()
	fresh := make([]toolCallRecord, 0, len(steps))
	for index := len(steps) - 1; index >= 0; index-- {
		step := steps[index]
		tool := strings.TrimSpace(step.Tool)
		if tool == "" || step.Skipped || toolCallMemoryIgnored[tool] {
			continue
		}
		fresh = append(fresh, toolCallRecord{
			Tool:    tool,
			Summary: toolCallInputSummary(step.Input),
			Failed:  strings.TrimSpace(step.Error) != "",
			At:      now,
		})
	}
	if len(fresh) == 0 {
		return
	}
	session := sessionKey(event)
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.recentToolCalls == nil {
		r.recentToolCalls = map[string][]toolCallRecord{}
	}
	r.recentToolCalls[session] = pruneToolCallRecords(append(fresh, r.recentToolCalls[session]...), now)
}

// toolCallContext 生成注入下一轮的工具调用记录；没有记录时返回空串。
func (r *Runtime) toolCallContext(event MessageEvent) string {
	if r == nil {
		return ""
	}
	session := sessionKey(event)
	now := r.clock()
	r.mu.Lock()
	records := pruneToolCallRecords(r.recentToolCalls[session], now)
	if len(records) == 0 {
		delete(r.recentToolCalls, session)
	} else if r.recentToolCalls != nil {
		r.recentToolCalls[session] = records
	}
	r.mu.Unlock()
	if len(records) == 0 {
		return ""
	}
	var builder strings.Builder
	builder.WriteString(toolCallContextHint)
	for _, record := range records {
		builder.WriteString("- ")
		builder.WriteString(record.At.Format("15:04"))
		builder.WriteString(" ")
		builder.WriteString(record.Tool)
		if record.Summary != "" {
			builder.WriteString("「")
			builder.WriteString(record.Summary)
			builder.WriteString("」")
		}
		if record.Failed {
			builder.WriteString("（调用失败）")
		}
		builder.WriteString("\n")
	}
	return strings.TrimRight(builder.String(), "\n")
}

func pruneToolCallRecords(records []toolCallRecord, now time.Time) []toolCallRecord {
	out := make([]toolCallRecord, 0, min(len(records), recentToolCallLimit))
	for _, record := range records {
		if !record.At.IsZero() && now.Sub(record.At) > recentToolCallTTL {
			continue
		}
		out = append(out, record)
		if len(out) >= recentToolCallLimit {
			break
		}
	}
	return out
}

// toolCallInputSummary 只取能说明「查了什么」的那几个参数，不把整份入参塞进上下文。
func toolCallInputSummary(input map[string]any) string {
	parts := make([]string, 0, 3)
	for _, key := range []string{"query", "url", "action", "operation", "message_ids", "keyword", "user_id"} {
		value, ok := input[key]
		if !ok {
			continue
		}
		text := strings.TrimSpace(toolCallInputValue(value))
		if text == "" {
			continue
		}
		parts = append(parts, text)
		if len(parts) == 3 {
			break
		}
	}
	return truncateRunes(strings.Join(parts, " · "), 80)
}

func toolCallInputValue(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case []any:
		items := make([]string, 0, len(typed))
		for _, item := range typed {
			items = append(items, fmt.Sprint(item))
		}
		return strings.Join(items, ",")
	case nil:
		return ""
	default:
		return fmt.Sprint(typed)
	}
}
