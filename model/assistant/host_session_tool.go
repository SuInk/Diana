package assistant

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/SuInk/diana/model/agent"
)

// host_access 的交互会话操作。exec 一次跑完、60 秒收工；dev server、REPL、
// 要来回输入的安装程序得一直开着，就走会话：session_start 启动并留在后台，
// session_send 往 stdin 写一行，session_read 取新输出，session_stop 结束。
//
// 审核和 exec 是同一套：启动审命令本身，每次输入单独再审一次——往 shell 会话里
// 写一行字就是执行一条 shell 命令，只审启动等于开了个不受审核的后门。读输出、
// 列会话、结束会话不产生新动作，不审。

const (
	hostSessionDefaultWait = 2 * time.Second
	hostSessionReadWait    = time.Second
)

func (r *Runtime) ownerSessions() *agent.OwnerSessions {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.hostSessions == nil {
		r.hostSessions = agent.NewOwnerSessions()
	}
	return r.hostSessions
}

func (r *Runtime) closeHostSessions() {
	r.mu.Lock()
	sessions := r.hostSessions
	r.hostSessions = nil
	r.mu.Unlock()
	if sessions != nil {
		sessions.Close()
	}
}

func hostSessionWait(input map[string]any, fallback time.Duration) time.Duration {
	raw, ok := input["wait_ms"]
	if !ok || raw == nil {
		return fallback
	}
	ms := intFromAny(raw)
	if ms <= 0 {
		return 0
	}
	return min(time.Duration(ms)*time.Millisecond, agent.OwnerSessionMaxWait)
}

func (t *hostAccessTool) session(ctx context.Context, cfg agent.Config, op string, input map[string]any) (string, error) {
	sessions := t.runtime.ownerSessions()
	owner := t.runtime.eventProfileID(t.event)
	id := configToolString(input, "session_id")
	switch op {
	case "session_list":
		return hostAccessJSON(map[string]any{"sessions": sessions.List(owner)})
	case "session_start":
		request, purpose, err := hostExecRequest(cfg, input)
		if err != nil {
			return "", err
		}
		validated, err := agent.ValidateOwnerCommand(cfg, request)
		if err != nil {
			return "", err
		}
		review, err := t.runtime.passHostReview(t.event, func() (hostCommandReviewDecision, error) {
			return t.runtime.reviewHostAction(ctx, t.event, purpose, "请审核这条要在交互会话里启动的命令：", map[string]any{
				"session": map[string]any{
					"mode":         "start",
					"idle_limit":   agent.OwnerSessionIdleTimeout.String(),
					"max_lifetime": agent.OwnerSessionMaxLifetime.String(),
				},
				"command": validated.Command,
				"args":    validated.Args,
				"cwd":     validated.CWD,
			})
		})
		if err != nil {
			return "", err
		}
		result, err := sessions.Start(cfg, owner, validated, hostSessionWait(input, hostSessionDefaultWait), ctx.Done())
		if err != nil {
			return "", err
		}
		return hostAccessJSON(map[string]any{"session": result, "review": review})
	case "session_send":
		if id == "" {
			return "", fmt.Errorf("session_send 需要 session_id")
		}
		text, _ := input["input"].(string)
		if strings.TrimSpace(text) == "" {
			return "", fmt.Errorf("session_send 需要 input")
		}
		purpose := configToolString(input, "purpose")
		if purpose == "" {
			return "", fmt.Errorf("purpose 必填：写清这次输入对应用户的哪项请求")
		}
		// 先确认会话存在，免得为一个不存在的会话白跑一次审核。
		current, err := findOwnerSession(sessions, owner, id)
		if err != nil {
			return "", err
		}
		review, err := t.runtime.passHostReview(t.event, func() (hostCommandReviewDecision, error) {
			return t.runtime.reviewHostAction(ctx, t.event, purpose, "请审核这段要写进交互会话的输入：", map[string]any{
				"session": map[string]any{
					"mode":    "send",
					"command": current.Command.Command,
					"args":    current.Command.Args,
					"cwd":     current.Command.CWD,
				},
				"input": truncateRunes(text, 4000),
			})
		})
		if err != nil {
			return "", err
		}
		result, err := sessions.Send(owner, id, text, hostSessionWait(input, hostSessionDefaultWait), ctx.Done())
		if err != nil {
			return "", err
		}
		return hostAccessJSON(map[string]any{"session": result, "review": review})
	case "session_read":
		if id == "" {
			return "", fmt.Errorf("session_read 需要 session_id")
		}
		result, err := sessions.Read(owner, id, hostSessionWait(input, hostSessionReadWait), ctx.Done())
		if err != nil {
			return "", err
		}
		return hostAccessJSON(map[string]any{"session": result})
	case "session_stop":
		if id == "" {
			return "", fmt.Errorf("session_stop 需要 session_id")
		}
		result, err := sessions.Stop(owner, id)
		if err != nil {
			return "", err
		}
		return hostAccessJSON(map[string]any{"session": result})
	}
	return "", fmt.Errorf("不支持的 operation %q", op)
}

func findOwnerSession(sessions *agent.OwnerSessions, owner, id string) (agent.OwnerSessionResult, error) {
	for _, session := range sessions.List(owner) {
		if session.SessionID == strings.TrimSpace(id) {
			if !session.Running {
				return session, fmt.Errorf("会话已经结束，不能再输入；用 session_read 取剩余输出")
			}
			return session, nil
		}
	}
	return agent.OwnerSessionResult{}, agent.ErrOwnerSessionNotFound
}
