// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const (
	dianaCodingToolName = "diana.coding"
	// maxCodingInstructionRunes 限制单次指令长度。指令是要落进 argv 的，系统对单个
	// 参数有长度上限；真需要更多上下文的活该写进工作区里的文件，让 CLI 自己去读。
	maxCodingInstructionRunes = 8000
)

type dianaCodingTool struct {
	runtime  *Runtime
	event    MessageEvent
	settings SettingValues
}

func newDianaCodingTool(runtime *Runtime, event MessageEvent, settings SettingValues) *dianaCodingTool {
	return &dianaCodingTool{runtime: runtime, event: event, settings: settings}
}

func (t *dianaCodingTool) Name() string { return dianaCodingToolName }

func (t *dianaCodingTool) Description() string {
	return `把一件编码工作交给外部编码 CLI（Claude Code / Codex）在持久工作区里长时间执行。submit 派活后立刻返回任务号，进程在后台独立运行，跑完 Diana 会主动汇报；期间用 status 查进度、tail 看最近动作、cancel 终止、followup 在原会话上追加指令。适合「改代码、修 Bug、加测试、跑构建」这类要几分钟到几小时的活。只有机器人主人能用。`
}

func (t *dianaCodingTool) InputSchema() map[string]any {
	return toolObjectSchema([]string{"operation"}, map[string]any{
		"operation": toolEnumParam("要执行的操作。", "submit", "status", "tail", "cancel", "followup", "list", "workspaces"),
		"workspace": toolStringParam("工作区名字，必须是设置里登记过的。只登记了一个时可以省略。submit 必填。"),
		"instruction": toolStringParam("交给编码 CLI 的完整指令。它看不到这里的聊天记录，" +
			"所以要改什么、为什么改、验收标准都要写进来。submit 和 followup 必填。"),
		"context": toolStringParam("相关聊天记录原文摘录。CLI 拿不到对话历史，需要依据聊天内容改代码时，" +
			"把原话挑出来放这里，会作为背景附在指令前面。"),
		"job_id":     toolStringParam("要查询、追加或取消的任务号。status / tail / cancel / followup 用；status 省略时返回最近的任务。"),
		"tail_lines": toolIntParam("tail 返回的最近动作行数，默认 10。", 1, codingJobTailLines),
	})
}

type dianaCodingResult struct {
	OK         bool             `json:"ok"`
	Operation  string           `json:"operation"`
	Message    string           `json:"message,omitempty"`
	Workspaces []string         `json:"workspaces,omitempty"`
	Job        *dianaCodingJob  `json:"job,omitempty"`
	Jobs       []dianaCodingJob `json:"jobs,omitempty"`
	Tail       []string         `json:"tail,omitempty"`
}

type dianaCodingJob struct {
	ID          string  `json:"id"`
	Status      string  `json:"status"`
	Workspace   string  `json:"workspace"`
	Backend     string  `json:"backend"`
	Instruction string  `json:"instruction"`
	Elapsed     string  `json:"elapsed"`
	SessionID   string  `json:"session_id,omitempty"`
	LastAction  string  `json:"last_action,omitempty"`
	Steps       int     `json:"steps,omitempty"`
	Result      string  `json:"result,omitempty"`
	Error       string  `json:"error,omitempty"`
	Turns       int     `json:"turns,omitempty"`
	CostUSD     float64 `json:"cost_usd,omitempty"`
	CanFollowUp bool    `json:"can_follow_up"`
	// AwaitingApproval 是任务正停着等确认的那个操作。查进度时这条比「最近动作」
	// 重要：任务没在跑，是在等人。
	AwaitingApproval  string `json:"awaiting_approval,omitempty"`
	ApprovalAllowCode string `json:"approval_allow_code,omitempty"`
	ApprovalDenyCode  string `json:"approval_deny_code,omitempty"`
}

func (t *dianaCodingTool) Run(ctx context.Context, input map[string]any) (string, error) {
	if t == nil || t.runtime == nil {
		return "", fmt.Errorf("编码代理未就绪")
	}
	// 装配工具表时已经按主人身份筛过一遍，这里再挡一次：这个工具能在白名单仓库里
	// 不受限地跑命令，权限判断不该只靠「工具有没有被挂上」这一个地方。
	if !t.runtime.relationshipPolicy(ctx, t.event).Owner {
		return "", fmt.Errorf("编码代理只对机器人主人开放")
	}
	cfg, err := codingAgentConfigFromSettings(t.settings)
	if err != nil {
		return "", err
	}
	operation := strings.ToLower(strings.TrimSpace(configToolString(input, "operation")))
	switch operation {
	case "", "submit":
		return t.submit(ctx, cfg, input, "")
	case "followup":
		return t.followUp(ctx, cfg, input)
	case "status":
		return t.status(input)
	case "tail":
		return t.tail(input)
	case "cancel":
		return t.cancel(ctx, input)
	case "list":
		return t.list()
	case "workspaces":
		return codingToolJSON(dianaCodingResult{
			OK:         true,
			Operation:  "workspaces",
			Workspaces: cfg.workspaceNames(),
			Message:    codingWorkspaceHint(cfg),
		})
	}
	return "", fmt.Errorf("operation 必须是 submit、status、tail、cancel、followup、list 或 workspaces")
}

func (t *dianaCodingTool) submit(ctx context.Context, cfg codingAgentConfig, input map[string]any, resumeSession string) (string, error) {
	if len(cfg.Workspaces) == 0 {
		return "", fmt.Errorf("还没有登记任何工作区，请先在插件设置里填写工作区白名单")
	}
	workspace, ok := cfg.workspace(configToolString(input, "workspace"))
	if !ok {
		return "", fmt.Errorf("工作区必须是登记过的其中一个：%s", strings.Join(cfg.workspaceNames(), "、"))
	}
	instruction := strings.TrimSpace(configToolString(input, "instruction"))
	if instruction == "" {
		return "", fmt.Errorf("instruction 不能为空")
	}
	if background := strings.TrimSpace(configToolString(input, "context")); background != "" {
		instruction = "背景（来自聊天记录）：\n" + background + "\n\n任务：\n" + instruction
	}
	if runes := []rune(instruction); len(runes) > maxCodingInstructionRunes {
		return "", fmt.Errorf("指令超过 %d 字，请把长材料写进工作区文件里让 CLI 自己读", maxCodingInstructionRunes)
	}
	job, err := t.runtime.launchCodingJob(ctx, t.event, cfg, workspace, instruction, resumeSession)
	if err != nil {
		return "", err
	}
	operation := "submit"
	message := fmt.Sprintf("任务已在后台启动，跑完我会主动汇报。期间可以用 status %s 查进度。", job.ID)
	if resumeSession != "" {
		operation = "followup"
		message = fmt.Sprintf("已在原会话上追加指令，任务号 %s。", job.ID)
	}
	view := codingJobView(job, nil)
	return codingToolJSON(dianaCodingResult{OK: true, Operation: operation, Job: &view, Message: message})
}

// followUp 在一个已经结束的任务的会话上继续。运行中的任务不接受追加：非交互模式的
// 编码 CLI 没有中途收指令的通道，硬插只会变成另一个进程同时改同一份检出。
func (t *dianaCodingTool) followUp(ctx context.Context, cfg codingAgentConfig, input map[string]any) (string, error) {
	job, err := t.resolveJob(input)
	if err != nil {
		return "", err
	}
	if !job.finished() {
		return "", fmt.Errorf("任务 %s 还在跑，没法中途追加指令。等它结束，或者先 cancel", job.ID)
	}
	if !cfg.StreamJSON || job.SessionID == "" {
		return "", fmt.Errorf("任务 %s 没有可续跑的会话 ID，改用 submit 重新派活", job.ID)
	}
	if input == nil {
		input = map[string]any{}
	}
	if strings.TrimSpace(configToolString(input, "workspace")) == "" {
		input["workspace"] = job.Workspace
	}
	return t.submit(ctx, cfg, input, job.SessionID)
}

func (t *dianaCodingTool) status(input map[string]any) (string, error) {
	job, err := t.resolveJob(input)
	if err != nil {
		return "", err
	}
	snapshot := parseCodingLog(job.LogPath)
	view := codingJobView(job, &snapshot)
	t.attachPendingApproval(&view)
	message := "任务已结束。"
	switch {
	case view.AwaitingApproval != "":
		message = "任务停在一个要你点头的操作上，回放行码它才继续。"
	case !job.finished():
		message = "任务还在跑，跑完我会主动汇报。"
	}
	return codingToolJSON(dianaCodingResult{OK: true, Operation: "status", Job: &view, Message: message})
}

func (t *dianaCodingTool) tail(input map[string]any) (string, error) {
	job, err := t.resolveJob(input)
	if err != nil {
		return "", err
	}
	lines := 10
	if value := intFromAny(input["tail_lines"]); value > 0 {
		lines = value
	}
	view := codingJobView(job, nil)
	return codingToolJSON(dianaCodingResult{
		OK:        true,
		Operation: "tail",
		Job:       &view,
		Tail:      codingLogTail(job, lines),
	})
}

func (t *dianaCodingTool) cancel(ctx context.Context, input map[string]any) (string, error) {
	id := strings.TrimSpace(configToolString(input, "job_id"))
	if id == "" {
		running := runningCodingJobs()
		if len(running) != 1 {
			return "", fmt.Errorf("要取消哪个任务？请给出 job_id")
		}
		id = running[0].ID
	}
	job, err := t.runtime.cancelCodingJob(ctx, id)
	if err != nil {
		return "", err
	}
	view := codingJobView(job, nil)
	return codingToolJSON(dianaCodingResult{
		OK:        true,
		Operation: "cancel",
		Job:       &view,
		Message:   "任务已终止，已完成的改动留在工作区里。",
	})
}

func (t *dianaCodingTool) list() (string, error) {
	jobs := listCodingJobs()
	if len(jobs) > 10 {
		jobs = jobs[:10]
	}
	views := make([]dianaCodingJob, 0, len(jobs))
	for _, job := range jobs {
		views = append(views, codingJobView(job, nil))
	}
	return codingToolJSON(dianaCodingResult{OK: true, Operation: "list", Jobs: views})
}

// resolveJob 找出要操作的任务。省略 job_id 时优先取在跑的那个，没有就取最近一个——
// 用户说「进度怎么样了」时指的几乎总是这两者之一。
func (t *dianaCodingTool) resolveJob(input map[string]any) (CodingJob, error) {
	if id := strings.TrimSpace(configToolString(input, "job_id")); id != "" {
		job, err := loadCodingJob(id)
		if err != nil {
			return CodingJob{}, fmt.Errorf("找不到任务 %s", id)
		}
		return job, nil
	}
	if running := runningCodingJobs(); len(running) == 1 {
		return running[0], nil
	}
	jobs := listCodingJobs()
	if len(jobs) == 0 {
		return CodingJob{}, fmt.Errorf("还没有任何编码任务")
	}
	return jobs[0], nil
}

// attachPendingApproval 把这个任务正等着的确认填进查询结果。确认码原样给出：
// 主人可能没看到当初那条消息，问进度时顺手把码再给一次，比让他去翻记录强。
// 码本身不需要保密——唯一被检查的文本是主人自己那条消息。
func (t *dianaCodingTool) attachPendingApproval(view *dianaCodingJob) {
	if t.runtime == nil || t.runtime.codingJobRegistry == nil {
		return
	}
	for _, request := range t.runtime.codingJobs().pendingApprovals() {
		if request.JobID != view.ID {
			continue
		}
		allow, deny := codingApprovalCodes(request)
		view.AwaitingApproval = codingApprovalDetail(request)
		view.ApprovalAllowCode = allow
		view.ApprovalDenyCode = deny
		return
	}
}

func codingJobView(job CodingJob, snapshot *codingJobSnapshot) dianaCodingJob {
	view := dianaCodingJob{
		ID:          job.ID,
		Status:      job.Status,
		Workspace:   job.Workspace,
		Backend:     job.Backend,
		Instruction: truncateRunes(job.Instruction, 300),
		SessionID:   job.SessionID,
		Result:      job.Result,
		Error:       job.Error,
		Turns:       job.Turns,
		CostUSD:     job.CostUSD,
	}
	end := job.FinishedAt
	if end.IsZero() {
		end = time.Now()
	}
	view.Elapsed = formatCodingDuration(end.Sub(job.StartedAt))
	if snapshot != nil {
		view.Steps = snapshot.Lines
		view.LastAction = snapshot.LastAction
		if view.SessionID == "" {
			view.SessionID = snapshot.SessionID
		}
		if !job.finished() {
			// 运行中的结果只是「目前说了什么」，不是结论，不塞进 result 里让模型
			// 当成最终答案念出去。
			view.Result = ""
		}
	}
	view.CanFollowUp = job.finished() && view.SessionID != ""
	return view
}

func codingWorkspaceHint(cfg codingAgentConfig) string {
	if len(cfg.Workspaces) == 0 {
		return "还没有登记任何工作区，请在插件设置的工作区白名单里填写。"
	}
	return fmt.Sprintf("后端 %s，最长单次运行 %s。", cfg.Backend, formatCodingDuration(cfg.MaxRuntime))
}

func codingToolJSON(result dianaCodingResult) (string, error) {
	body, err := json.Marshal(result)
	if err != nil {
		return "", err
	}
	return string(body), nil
}
