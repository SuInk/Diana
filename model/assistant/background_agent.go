// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/SuInk/diana/model/llm"
)

// 后台长任务：一轮回复最多几十次工具调用，调研、多来源核查、跑脚本这类活常常不够，
// 而且整个过程聊天里干等。这里把活挪到后台分轮做：每轮是一次完整的 Agent 运行，
// 结束时要么报【进度】（做了什么、下一轮做什么），要么交【完成】的结果。进度发进
// 会话，下一轮带着之前所有轮的记录接着做，所以能先查再核、反复核对。复用插件子任务
// 的框架：并发登记、运行状态、关停取消和结果投递都是现成的。
const (
	backgroundAgentKind       = "background_agent"
	backgroundAgentMaxRounds  = 6
	backgroundAgentTimeout    = 90 * time.Minute
	backgroundAgentRoundLimit = 15 * time.Minute
	backgroundAgentPerSession = 2
	backgroundAgentKeepDone   = time.Hour
	backgroundAgentGoalRunes  = 2000
	backgroundAgentNoteRunes  = 600

	backgroundAgentProgressTag = "【进度】"
	backgroundAgentDoneTag     = "【完成】"
)

type backgroundAgentState struct {
	ID        string
	Session   string
	Event     MessageEvent
	Goal      string
	Round     int
	MaxRounds int
	Notes     []string
	Phase     string // queued / running / completed / failed / cancelled
	Result    string
	StartedAt time.Time
	UpdatedAt time.Time
}

// backgroundAgentRunKey 标记「这是后台任务自己的运行」：里面不再挂开任务的工具，
// 免得任务套任务。
type backgroundAgentRunKey struct{}

func isBackgroundAgentRun(ctx context.Context) bool {
	value, _ := ctx.Value(backgroundAgentRunKey{}).(bool)
	return value
}

// roundRunner 跑一轮，返回模型这一轮的原始输出；测试替换它。
type backgroundRoundRunner func(ctx context.Context, state backgroundAgentState) (string, error)

// startBackgroundAgent 预约并启动一个后台任务，返回任务号。
func (r *Runtime) startBackgroundAgent(ctx context.Context, event MessageEvent, goal string, maxRounds int, run backgroundRoundRunner) (string, error) {
	goal = strings.TrimSpace(goal)
	if goal == "" {
		return "", errors.New("goal is required")
	}
	goal = truncateRunes(goal, backgroundAgentGoalRunes)
	if maxRounds <= 0 || maxRounds > backgroundAgentMaxRounds {
		maxRounds = backgroundAgentMaxRounds
	}
	session := sessionKey(event)
	now := time.Now()
	r.subagentMu.Lock()
	r.pruneBackgroundAgentsLocked(now)
	running := 0
	for _, state := range r.backgroundAgents {
		if state.Session == session && !backgroundAgentFinished(state.Phase) {
			running++
		}
	}
	r.subagentMu.Unlock()
	if running >= backgroundAgentPerSession {
		return "", fmt.Errorf("这个会话已有 %d 个后台任务在跑，等它们结束或先取消一个", running)
	}
	if run == nil {
		run = r.runBackgroundAgentRound
	}
	state := &backgroundAgentState{Session: session, Event: event, Goal: goal, MaxRounds: maxRounds, Phase: "queued", StartedAt: now, UpdatedAt: now}
	task := PluginTask{
		Kind:        backgroundAgentKind,
		Name:        "后台任务：" + truncateRunes(goal, 24),
		Key:         "bg-" + fmt.Sprint(now.UnixNano()),
		Timeout:     backgroundAgentTimeout,
		Unthrottled: true,
		Run: func(taskCtx context.Context, services PluginTaskServices) (PluginTaskResult, error) {
			return r.runBackgroundAgent(taskCtx, state, services, run)
		},
	}
	reservation := r.reservePluginTasksForTurn(ctx, event, []PluginTask{task})
	if len(reservation.reserved) == 0 {
		return "", errors.New("后台任务没能登记")
	}
	id := reservation.reserved[0].id
	r.subagentMu.Lock()
	state.ID = id
	if r.backgroundAgents == nil {
		r.backgroundAgents = map[string]*backgroundAgentState{}
	}
	r.backgroundAgents[id] = state
	r.subagentMu.Unlock()
	r.startPluginTaskReservation(reservation)
	return id, nil
}

func (r *Runtime) runBackgroundAgent(ctx context.Context, state *backgroundAgentState, services PluginTaskServices, run backgroundRoundRunner) (PluginTaskResult, error) {
	ctx = context.WithValue(ctx, backgroundAgentRunKey{}, true)
	for round := 1; round <= state.MaxRounds; round++ {
		snapshot := r.updateBackgroundAgent(state, func(s *backgroundAgentState) {
			s.Round = round
			s.Phase = "running"
		})
		output, err := run(ctx, snapshot)
		if ctx.Err() != nil {
			// 主人取消或到了总时限：交出已有进度，不当失败报。
			return PluginTaskResult{Reply: r.finishBackgroundAgent(state, "cancelled", "")}, nil
		}
		if err != nil {
			r.updateBackgroundAgent(state, func(s *backgroundAgentState) { s.Phase = "failed" })
			return PluginTaskResult{}, fmt.Errorf("第 %d 轮出错：%w", round, err)
		}
		kind, body := parseBackgroundAgentOutput(output)
		if kind == backgroundAgentDoneTag || round == state.MaxRounds {
			return PluginTaskResult{Reply: r.finishBackgroundAgent(state, "completed", body)}, nil
		}
		note := truncateRunes(body, backgroundAgentNoteRunes)
		r.updateBackgroundAgent(state, func(s *backgroundAgentState) { s.Notes = append(s.Notes, note) })
		if services.Report != nil {
			services.Report(PluginTaskProgress{
				Phase:     "running",
				Message:   fmt.Sprintf("🔄 后台任务 %s 第 %d/%d 轮：%s", state.ID, round, state.MaxRounds, note),
				Completed: round,
				Total:     state.MaxRounds,
			})
		}
	}
	return PluginTaskResult{Reply: r.finishBackgroundAgent(state, "completed", "")}, nil
}

func (r *Runtime) finishBackgroundAgent(state *backgroundAgentState, phase, result string) string {
	snapshot := r.updateBackgroundAgent(state, func(s *backgroundAgentState) {
		s.Phase = phase
		s.Result = truncateRunes(result, backgroundAgentNoteRunes)
	})
	elapsed := formatCodingDuration(time.Since(snapshot.StartedAt))
	switch {
	case phase == "cancelled":
		text := fmt.Sprintf("⏹ 后台任务 %s 已停止（%s，做了 %d 轮）。", snapshot.ID, elapsed, len(snapshot.Notes))
		if len(snapshot.Notes) > 0 {
			text += "\n停下前的进度：" + snapshot.Notes[len(snapshot.Notes)-1]
		}
		return text
	case strings.TrimSpace(result) == "":
		text := fmt.Sprintf("✅ 后台任务 %s 跑满 %d 轮，没有给出最终结论（%s）。", snapshot.ID, snapshot.MaxRounds, elapsed)
		if len(snapshot.Notes) > 0 {
			text += "\n最后一轮：" + snapshot.Notes[len(snapshot.Notes)-1]
		}
		return text
	default:
		return fmt.Sprintf("✅ 后台任务 %s 完成（%s，%d 轮）：\n%s", snapshot.ID, elapsed, snapshot.Round, strings.TrimSpace(result))
	}
}

func (r *Runtime) updateBackgroundAgent(state *backgroundAgentState, change func(*backgroundAgentState)) backgroundAgentState {
	r.subagentMu.Lock()
	defer r.subagentMu.Unlock()
	change(state)
	state.UpdatedAt = time.Now()
	snapshot := *state
	snapshot.Notes = append([]string(nil), state.Notes...)
	return snapshot
}

func (r *Runtime) pruneBackgroundAgentsLocked(now time.Time) {
	for id, state := range r.backgroundAgents {
		if backgroundAgentFinished(state.Phase) && now.Sub(state.UpdatedAt) > backgroundAgentKeepDone {
			delete(r.backgroundAgents, id)
		}
	}
}

// backgroundAgentsFor 列出本会话的后台任务，新的在前。
func (r *Runtime) backgroundAgentsFor(event MessageEvent) []backgroundAgentState {
	session := sessionKey(event)
	r.subagentMu.Lock()
	defer r.subagentMu.Unlock()
	r.pruneBackgroundAgentsLocked(time.Now())
	out := make([]backgroundAgentState, 0, len(r.backgroundAgents))
	for _, state := range r.backgroundAgents {
		if state.Session != session {
			continue
		}
		snapshot := *state
		snapshot.Notes = append([]string(nil), state.Notes...)
		out = append(out, snapshot)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].StartedAt.After(out[j-1].StartedAt); j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

func backgroundAgentFinished(phase string) bool {
	switch phase {
	case "completed", "failed", "cancelled":
		return true
	}
	return false
}

// parseBackgroundAgentOutput 认【完成】/【进度】开头；都没写时按完成处理：
// 模型直接给了结果却忘了标记，比卡着再跑一轮更常见。
func parseBackgroundAgentOutput(output string) (string, string) {
	text := strings.TrimSpace(output)
	for _, tag := range []string{backgroundAgentDoneTag, backgroundAgentProgressTag} {
		if index := strings.Index(text, tag); index >= 0 && index < 8 {
			return tag, strings.TrimSpace(text[index+len(tag):])
		}
	}
	return backgroundAgentDoneTag, text
}

// runBackgroundAgentRound 用定时查询同一套方式跑一轮：完整人设与权限、全套工具，
// 只是请求换成任务目标加之前各轮的记录。每轮抢主回复的并发槽。
func (r *Runtime) runBackgroundAgentRound(ctx context.Context, state backgroundAgentState) (string, error) {
	return r.runTaskQueryWithSlot(ctx, func(ctx context.Context) (string, error) {
		event := state.Event
		cfg := r.effectiveConfigForEvent(event)
		if !cfg.AgentEnabled {
			return "", errors.New("Agent 已禁用")
		}
		roundCtx, cancel := context.WithTimeout(ctx, backgroundAgentRoundLimit)
		defer cancel()
		roundCtx = withLLMUsageContext(roundCtx, event)
		relationship := r.relationshipPolicy(roundCtx, event)
		messages := []llm.Message{
			{Role: llm.RoleSystem, Content: r.systemPromptWithRelationship(event, nil, false, relationship) + "\n" + backgroundAgentSystemPrompt},
			{Role: llm.RoleUser, Content: renderBackgroundAgentRequest(state, cfg.Location())},
		}
		registry, err := r.newReplyAgentRegistry(roundCtx, cfg, event, relationship, nil)
		if err != nil {
			return "", err
		}
		if registry != nil {
			defer registry.Close()
		}
		return r.generateReply(withLLMUsagePurpose(roundCtx, PurposeScheduledQuery), cfg, event, relationship, messages, registry)
	})
}

const backgroundAgentSystemPrompt = `【后台任务模式】你在后台分轮执行主人交代的任务，这一轮没有人在等你即时回话。
- 每一轮都是一次完整的工具调用机会：先查、再读原文、再核对，不要凭记忆下结论。
- 这一轮做完就停，用以下两种格式之一作为整条输出的开头：
  【进度】这一轮做了什么、查到了什么（带来源）、下一轮打算核实或补做什么。会原样发给主人看，写短一点。
  【完成】最终结果：结论、关键依据和来源，没核实的地方要标出来。
- 之前轮次的记录在请求里，接着往下做，不要重复已经做完的步骤；需要交叉核对时换独立来源。`

func renderBackgroundAgentRequest(state backgroundAgentState, location *time.Location) string {
	var builder strings.Builder
	builder.WriteString("【后台任务】\n目标：")
	builder.WriteString(state.Goal)
	fmt.Fprintf(&builder, "\n现在：%s；这是第 %d/%d 轮", formatZonedTime(time.Now().In(location), "2006-01-02 15:04"), state.Round, state.MaxRounds)
	if state.Round == state.MaxRounds {
		builder.WriteString("，也是最后一轮，必须用【完成】交出结果")
	}
	builder.WriteString("。")
	if len(state.Notes) > 0 {
		builder.WriteString("\n\n之前各轮的记录：")
		for index, note := range state.Notes {
			fmt.Fprintf(&builder, "\n第 %d 轮：%s", index+1, note)
		}
	}
	return builder.String()
}
