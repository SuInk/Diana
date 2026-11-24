// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/SuInk/diana/model/agent"
	"github.com/SuInk/diana/model/llm"
	"github.com/google/uuid"
)

// 子 agent（subtask 的 mode=agent），照 Codex CLI 的 spawn_agent 做：子 agent 和主回复
// 用同一套工具（按发起人的权限）、共用工作目录，自己循环到做完，结果交回后再跑一步
// 收尾。怎么拆、拆几份由主回复自己定，这里不认识任何具体的活。
//
// 11-23 群友让翻译一份 151 页的 PDF：主回复一轮只读得了几页，analyze Worker 只有一次
// 模型调用、材料上限 6000 字，search Worker 只能搜索和读网页，都读不了聊天里的文件，
// 所以这活派不出去；后台长任务又只挂给主人。结果一天只出了 21 页，被问进度时嘴上说
// 「还在啃」，两轮之间其实什么都没在跑。
const (
	subtaskModeAgent = "agent"
	subtaskAgentKind = "subtask_agent"
	// 和 Codex 一样只有一层：子 agent 里不能再派子 agent。
	subtaskAgentWorkerLimit  = 40 * time.Minute
	subtaskAgentBatchTimeout = 3 * time.Hour
	subtaskAgentPerSession   = 2
	subtaskAgentThenRunes    = 1200
)

// subtaskAgentDefaultThen 是主回复没写收尾要求时用的那句。
const subtaskAgentDefaultThen = "把各子 agent 的结果按顺序交付给对方：产出是长文档就合并成一个文件发出，否则汇总成一条回复。"

const subtaskAgentWorkerPrompt = `【子 agent 模式】你是主回复派出的子 agent，在后台独立完成下面这一项任务。没有人在等你即时回话，你也看不到聊天历史，任务里写的就是全部背景。
- 用工具把任务从头做到完再结束，不要做一部分就停下来问要不要继续；一次读不完的材料按工具给的续读方式读完。
- 最后一步的 send 就是你交回的结果：任务要的产出（译文、整理好的内容、结论）完整写在里面，运行时会把它原样存成工作目录里的文件。它是文档正文，不是聊天消息：用正常的换行和空行排版，不用 <diana-br/>、<diana-message/> 这类聊天分条标记，也不用人设口吻。产出太长、一次写不下时，边做边用文件工具分段写进工作目录，send 里写清各段的路径和顺序。
- 不要给会话发消息或文件，交付由收尾那一步统一做；任务明确要你发的除外。
- 没做成的部分照实写出来和原因，不要说成做完了。`

const subtaskAgentFinishPrompt = `【子 agent 收尾】你之前把一件大活拆给了后台的子 agent，它们现在都结束了，结果在下面。按「收尾要求」把事情做完：要合并、发文件就调用工具去做（几个文本文件按顺序接成一个发出，用 send_file 的 paths），然后用 send 写一条给对方的回复，说清做成了什么。子 agent 没做成的部分照实转告，不要说成做完了。`

type subtaskAgentRunKey struct{}

func isSubtaskAgentRun(ctx context.Context) bool {
	running, _ := ctx.Value(subtaskAgentRunKey{}).(bool)
	return running
}

// finish 为 false 是子 agent 本身，交回的是文档正文；为 true 是全部结束后的收尾，
// 写的是发回会话的回复。
type subtaskAgentRunner func(ctx context.Context, event MessageEvent, finish bool, request string) (string, error)

type subtaskAgentRawResultKey struct{}

// subtaskAgentRawResult 说明这次 agent 运行交回的是文档正文：不走聊天回复的整理
// （换行并成分条标记、空行收掉、改写口吻），原样返回。
func subtaskAgentRawResult(ctx context.Context) bool {
	raw, _ := ctx.Value(subtaskAgentRawResultKey{}).(bool)
	return raw
}

type subtaskAgentBatch struct {
	event       MessageEvent
	tasks       []subtaskRequest
	then        string
	concurrency int
	run         subtaskAgentRunner
	// save 把一个子 agent 的完整结果存进工作目录，返回路径；存不了返回空。
	save func(name string, data []byte) string
}

type subtaskAgentOutcome struct {
	output string
	err    error
	path   string
}

// runSubtaskAgent 是默认的 subtaskAgentRunner：和后台长任务的一轮一样，用发起人这条
// 消息的权限建工具表，跑一次完整的回复 agent，返回它最后 send 的正文。
func (r *Runtime) runSubtaskAgent(ctx context.Context, event MessageEvent, finish bool, request string) (string, error) {
	system := subtaskAgentWorkerPrompt
	if finish {
		system = subtaskAgentFinishPrompt
	}
	return r.runTaskQueryWithSlot(ctx, func(ctx context.Context) (string, error) {
		cfg := r.effectiveConfigForEvent(event)
		if !cfg.AgentEnabled {
			return "", errors.New("Agent 已禁用")
		}
		ctx = context.WithValue(ctx, subtaskAgentRunKey{}, true)
		ctx = context.WithValue(ctx, subtaskAgentRawResultKey{}, !finish)
		ctx = withLLMUsageContext(ctx, event)
		relationship := r.relationshipPolicy(ctx, event)
		messages := []llm.Message{
			{Role: llm.RoleSystem, Content: r.systemPromptWithRelationship(event, nil, false, relationship) + "\n" + system},
			{Role: llm.RoleUser, Content: request},
		}
		registry, err := r.newReplyAgentRegistry(ctx, cfg, event, relationship, nil)
		if err != nil {
			return "", err
		}
		if registry != nil {
			defer registry.Close()
		}
		return r.generateReply(withLLMUsagePurpose(ctx, "subtask"), cfg, event, relationship, messages, registry)
	})
}

// runSubtaskAgentBatch 并行跑完这一批子 agent，再跑一步收尾，收尾写的话就是发回会话的
// 回复。子 agent 的完整结果都存进工作目录：收尾那一步出错时结果也还在。
func runSubtaskAgentBatch(ctx context.Context, batch subtaskAgentBatch, report func(PluginTaskProgress)) (PluginTaskResult, error) {
	if report == nil {
		report = func(PluginTaskProgress) {}
	}
	outcomes := make([]subtaskAgentOutcome, len(batch.tasks))
	sem := make(chan struct{}, max(1, batch.concurrency))
	var wg sync.WaitGroup
	var mu sync.Mutex
	done := 0
	for index := range batch.tasks {
		index := index
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer recoverGoroutinePanic("subtask.agent")
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				outcomes[index].err = ctx.Err()
				return
			}
			workerCtx, cancel := context.WithTimeout(ctx, subtaskAgentWorkerLimit)
			output, err := batch.run(workerCtx, batch.event, false, "【子 agent 任务】\n"+batch.tasks[index].Objective)
			cancel()
			// 模型照聊天习惯写了分条标记时还原成换行，文件里不留标记。
			output = strings.TrimSpace(strings.ReplaceAll(restoreExplicitReplyLines(output), notificationSplitMarker, "\n\n"))
			if err == nil && output == "" {
				err = errors.New("子 agent 没有交回结果")
			}
			outcome := subtaskAgentOutcome{output: output, err: err}
			if output != "" && batch.save != nil {
				outcome.path = batch.save(fmt.Sprintf("%02d-%s.md", index+1, subtaskAgentFileStem(batch.tasks[index].ID)), []byte(output+"\n"))
			}
			outcomes[index] = outcome
			mu.Lock()
			done++
			progress := PluginTaskProgress{Phase: "running", Completed: done, Total: len(batch.tasks)}
			mu.Unlock()
			report(progress)
		}()
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return PluginTaskResult{}, err
	}
	reply, err := batch.run(ctx, batch.event, true, renderSubtaskAgentResults(batch, outcomes, agent.ToolOutputMaxChars))
	if reply = strings.TrimSpace(reply); err == nil && reply != "" {
		return PluginTaskResult{Reply: reply}, nil
	}
	// 收尾没跑成：子 agent 的结果已经在工作目录里，照实说，不让它们白做。
	var lines []string
	for index, outcome := range outcomes {
		switch {
		case outcome.err != nil && outcome.path == "":
			lines = append(lines, fmt.Sprintf("%s：没做成（%s）", batch.tasks[index].ID, publicTaskErrorMessage(outcome.err)))
		case outcome.path != "":
			lines = append(lines, fmt.Sprintf("%s：结果在 %s", batch.tasks[index].ID, outcome.path))
		default:
			lines = append(lines, fmt.Sprintf("%s：已完成，但结果没能存进工作目录", batch.tasks[index].ID))
		}
	}
	reason := "没有写出回复"
	if err != nil {
		reason = publicTaskErrorMessage(err)
	}
	return PluginTaskResult{Reply: fmt.Sprintf("子 agent 都结束了，但最后合并交付那一步没做成（%s）。各部分的情况：\n%s", reason, strings.Join(lines, "\n"))}, nil
}

// renderSubtaskAgentResults 是交给收尾那一步的请求。各子 agent 的结果合起来装得进
// budget 字时原文全给；装不下时一律只给路径和字数，由收尾按路径读或直接合并发出，
// 不掐头去尾。
func renderSubtaskAgentResults(batch subtaskAgentBatch, outcomes []subtaskAgentOutcome, budget int) string {
	total := 0
	for _, outcome := range outcomes {
		total += utf8.RuneCountInString(outcome.output)
	}
	inline := total <= budget
	var builder strings.Builder
	fmt.Fprintf(&builder, "【子 agent 结果】\n你派出的 %d 个子 agent 都结束了。\n收尾要求：%s\n", len(outcomes), batch.then)
	if !inline {
		fmt.Fprintf(&builder, "各子 agent 的结果合起来有 %d 字，超过单次上限 %d 字，下面没有贴正文：按路径用文件工具读，或者直接按顺序合并发出。\n", total, budget)
	}
	for index, outcome := range outcomes {
		task := batch.tasks[index]
		status := "已完成"
		if outcome.err != nil {
			status = "没做成：" + publicTaskErrorMessage(outcome.err)
		}
		fmt.Fprintf(&builder, "\n子 agent %d（id=%s）：%s\n任务：%s\n", index+1, task.ID, status, task.Objective)
		if outcome.path != "" {
			fmt.Fprintf(&builder, "完整结果已存到工作目录：%s（%d 字）\n", outcome.path, utf8.RuneCountInString(outcome.output))
		}
		if outcome.output != "" && (inline || outcome.path == "") {
			builder.WriteString("结果：\n" + outcome.output + "\n")
		}
	}
	return builder.String()
}

func subtaskAgentFileStem(id string) string {
	var builder strings.Builder
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			builder.WriteRune(r)
		default:
			builder.WriteRune('_')
		}
	}
	if builder.Len() == 0 {
		return "task"
	}
	return builder.String()
}

// startAgents 把这次调用里的 agent 任务登记成一个后台批次，马上返回。
func (t *dianaSubtaskTool) startAgents(ctx context.Context, tasks []subtaskRequest, then string) []subtaskResult {
	results := make([]subtaskResult, len(tasks))
	fail := func(message string) []subtaskResult {
		for index, task := range tasks {
			results[index] = subtaskResult{ID: task.ID, Status: "failed", Error: message}
		}
		return results
	}
	if isSubtaskAgentRun(ctx) || isBackgroundAgentRun(ctx) {
		return fail("子 agent 和后台任务里不能再派子 agent，这部分自己做完")
	}
	if !t.runtime.effectiveConfigForEvent(t.event).AgentEnabled {
		return fail("Agent 已禁用，派不了子 agent")
	}
	session := sessionKey(t.event)
	if !t.runtime.reserveSubtaskAgentBatch(session) {
		return fail(fmt.Sprintf("这个会话已有 %d 批子 agent 在后台跑，等它们结束再派；进度用 tasks 查", subtaskAgentPerSession))
	}
	_, concurrency := t.limits()
	event := t.event
	dir := agent.WorkspaceOutputsDir + "/subtask-" + uuid.NewString()[:8]
	batch := subtaskAgentBatch{event: event, tasks: tasks, then: then, concurrency: concurrency, run: t.agentRun}
	if batch.run == nil {
		batch.run = t.runtime.runSubtaskAgent
	}
	batch.save = func(name string, data []byte) string {
		saved, err := t.runtime.saveWorkspacePayload(event, workspaceMediaPayload{data: data, kind: "file", generated: true}, dir+"/"+name, false, time.Now())
		if err != nil {
			return ""
		}
		return saved.Path
	}
	reservation := t.runtime.reservePluginTasksForTurn(ctx, event, []PluginTask{{
		Kind:        subtaskAgentKind,
		Name:        fmt.Sprintf("子 agent ×%d：%s", len(tasks), truncateRunes(then, 24)),
		Request:     then,
		Key:         "subtask-agent-" + uuid.NewString(),
		Timeout:     subtaskAgentBatchTimeout,
		Unthrottled: true,
		Run: func(taskCtx context.Context, services PluginTaskServices) (PluginTaskResult, error) {
			return runSubtaskAgentBatch(taskCtx, batch, services.Report)
		},
		Finish: func() { t.runtime.releaseSubtaskAgentBatch(session) },
	}})
	if len(reservation.reserved) == 0 {
		t.runtime.releaseSubtaskAgentBatch(session)
		return fail("后台任务没能登记")
	}
	t.runtime.startPluginTaskReservation(reservation)
	note := fmt.Sprintf("已派出，在后台运行（任务号 %s）。%d 个子 agent 全部结束后会自动按 then 收尾，把结果发到这个会话。这一轮不要自己再做一遍，只告诉对方已经开始、做完会自己发出来；进度用 tasks 查，不要自己估。", reservation.reserved[0].id, len(tasks))
	for index, task := range tasks {
		results[index] = subtaskResult{ID: task.ID, Status: "started", Output: note}
	}
	return results
}

func (r *Runtime) reserveSubtaskAgentBatch(session string) bool {
	r.subagentMu.Lock()
	defer r.subagentMu.Unlock()
	if r.subtaskAgentBatches[session] >= subtaskAgentPerSession {
		return false
	}
	if r.subtaskAgentBatches == nil {
		r.subtaskAgentBatches = map[string]int{}
	}
	r.subtaskAgentBatches[session]++
	return true
}

func (r *Runtime) releaseSubtaskAgentBatch(session string) {
	r.subagentMu.Lock()
	defer r.subagentMu.Unlock()
	if r.subtaskAgentBatches[session] <= 1 {
		delete(r.subtaskAgentBatches, session)
		return
	}
	r.subtaskAgentBatches[session]--
}
