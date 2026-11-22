// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"strings"
	"sync"
	"time"
)

// 后台任务受理时，模型拿到的工具结果是「已排队，完成后自动发出」，这句话会一直留在
// 续接前缀里。任务后来失败了，失败提示又可能没发进聊天（错误提示关着、人设改写也
// 调用失败），模型就只知道「还在画」：11-22 生产里生图上游 503，群友连问两次「画好了
// 吗」「图呢」，回的都是「还在后台画，出来会自动发」。
//
// 所以失败要回到模型能看到的地方：本会话近期失败的后台任务作为尾部一段注入，直到
// 过期或同类任务之后又成功过。尾部段和上一版一样时续接不重发，见 append_context.go。

const (
	// failedSubagentTaskTTL 是失败记录留在尾部的时长。超过这么久还没人问起，
	// 这件事已经过去了。
	failedSubagentTaskTTL = time.Hour
	// failedSubagentTaskMax 是每个会话最多留几条。
	failedSubagentTaskMax = 5
	// failedSubagentReasonMaxRunes 限制单条失败原因的长度。
	failedSubagentReasonMaxRunes = 80
)

type failedSubagentTask struct {
	id     string
	kind   string
	name   string
	reason string
	at     time.Time
}

// failedSubagentTaskLog 按会话（机器人 + 会话）记近期失败的后台任务，只在内存里。
// 零值可用。
type failedSubagentTaskLog struct {
	mu      sync.Mutex
	entries map[string][]failedSubagentTask
}

func failedSubagentTaskKey(event MessageEvent) string {
	return strings.TrimSpace(event.ProfileID) + "\x00" + sessionKey(event)
}

func (l *failedSubagentTaskLog) record(event MessageEvent, task failedSubagentTask) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.entries == nil {
		l.entries = map[string][]failedSubagentTask{}
	}
	key := failedSubagentTaskKey(event)
	items := pruneFailedSubagentTasks(l.entries[key], task.at)
	items = append(items, task)
	if len(items) > failedSubagentTaskMax {
		items = items[len(items)-failedSubagentTaskMax:]
	}
	l.entries[key] = items
}

// clearKind 在同类任务成功之后清掉之前的失败：用户重试成功了，旧的失败不用再提。
func (l *failedSubagentTaskLog) clearKind(event MessageEvent, kind string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	key := failedSubagentTaskKey(event)
	items := l.entries[key]
	kept := items[:0]
	for _, item := range items {
		if item.kind != kind {
			kept = append(kept, item)
		}
	}
	if len(kept) == 0 {
		delete(l.entries, key)
		return
	}
	l.entries[key] = kept
}

func (l *failedSubagentTaskLog) recent(event MessageEvent, now time.Time) []failedSubagentTask {
	l.mu.Lock()
	defer l.mu.Unlock()
	key := failedSubagentTaskKey(event)
	items := pruneFailedSubagentTasks(l.entries[key], now)
	if len(items) == 0 {
		delete(l.entries, key)
		return nil
	}
	l.entries[key] = items
	return append([]failedSubagentTask(nil), items...)
}

func pruneFailedSubagentTasks(items []failedSubagentTask, now time.Time) []failedSubagentTask {
	kept := items[:0]
	for _, item := range items {
		if now.Sub(item.at) < failedSubagentTaskTTL {
			kept = append(kept, item)
		}
	}
	return kept
}

func (r *Runtime) recordFailedSubagentTask(item reservedSubagentTask, reason string) {
	reason = truncateRunes(strings.Join(strings.Fields(strings.TrimRight(reason, "。")), " "), failedSubagentReasonMaxRunes)
	r.failedTasks.record(item.event, failedSubagentTask{
		id:     item.id,
		kind:   item.task.Kind,
		name:   strings.TrimSpace(item.task.Name),
		reason: reason,
		at:     time.Now(),
	})
}

// failedSubagentTasksPrompt 是回复尾部那段「后台任务失败」，没有近期失败时返回空串。
func (r *Runtime) failedSubagentTasksPrompt(event MessageEvent, cfg BotConfig) string {
	items := r.failedTasks.recent(event, time.Now())
	if len(items) == 0 {
		return ""
	}
	lines := make([]string, 0, len(items))
	for _, item := range items {
		name := item.name
		if name == "" {
			name = "后台任务"
		}
		line := "- " + formatZonedTime(item.at.In(cfg.Location()), "15:04") + " " + name + "（task_id=" + item.id + "）"
		if item.reason != "" {
			line += "：" + item.reason
		}
		lines = append(lines, line)
	}
	return cfg.promptf(promptFailedSubagentTasksSpec, map[string]string{"tasks": strings.Join(lines, "\n")})
}

const promptFailedSubagentTasks = "【后台任务失败】下面这些后台任务已经失败，结果没有发出去，也不会再自动发。有人问起就照实说没做成，别再说还在处理或做完会自动发；要再试就重新调用工具：\n{tasks}"

var promptFailedSubagentTasksSpec = tailSpec("failed_subagent_tasks", "后台任务失败", "本会话一小时内有后台任务（生图、文档识别等）失败时注入，列出失败的任务和原因，免得模型照着受理时的「完成后自动发出」继续说还在处理。同类任务之后成功过就不再列。",
	promptFailedSubagentTasks,
	PromptVar{Name: "tasks", Description: "失败任务列表，每行是时间、任务名、task_id 和失败原因"})
