// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	dianaCodingToolName = "coding"
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
	return `把编码工作交给外部编码 CLI（Claude Code/Codex/ACP）在持久工作区后台执行，仅主人可用。submit 派活，没很快结束就返回任务号，跑完会主动汇报；status 查进度和结果（长时按 detail_line 续读），tail 看最近动作（更早的按 before_line 往前翻），cancel 终止，followup 在原会话追加指令；agents 列代理配置；decide 在主人对等待点头的操作表态后裁决（怎么说都行，不必让主人报任务号）；approvals 查看或清空常驻放行。适合改代码、修 Bug、跑构建等长任务。`
}

func (t *dianaCodingTool) InputSchema() map[string]any {
	return toolObjectSchema([]string{"operation"}, map[string]any{
		"operation":   toolEnumParam("操作。", "submit", "status", "tail", "cancel", "followup", "list", "workspaces", "decide", "approvals", "agents"),
		"agent":       toolStringParam("代理配置名，submit 默认用默认配置，followup 沿用原任务。"),
		"workspace":   toolStringParam("设置里登记的工作区名，submit 必填；只登记一个时可省。"),
		"instruction": toolStringParam("完整指令，submit/followup 必填。CLI 看不到聊天记录，写清改什么、为什么、验收标准。"),
		"context":     toolStringParam("相关聊天原文摘录，作为背景附在指令前。"),
		"job_id":      toolStringParam("任务号；status 省略时取最近的任务。"),
		"tail_lines":  toolIntParam("tail 行数，默认 10；一次放不下时按整行给最近的部分。", 1, 100000),
		"before_line": toolIntParam("tail 往前翻：只看这一行之前的动作，传上次返回的 tail_first_line。", 1, 100000000),
		"detail_line": toolIntParam("status 续读：指令、最近动作、结果连起来从第几行开始，传上次返回的 detail_next_line。", 1, 100000000),
		"offset":      toolIntParam("list 翻页：跳过前几个任务，传上次返回的 next_offset。", 0, 100000),
		"clear":       toolBoolParam("approvals 专用：清空主人说过「以后都同意」的操作类别。"),
		"decision":    toolEnumParam("decide 专用：allow 同意这一次，always 同意并以后这类都不再问，deny 拒绝。", "allow", "always", "deny"),
	})
}

type dianaCodingResult struct {
	OK         bool     `json:"ok"`
	Operation  string   `json:"operation"`
	Message    string   `json:"message,omitempty"`
	Workspaces []string `json:"workspaces,omitempty"`
	// AlwaysAllowed 是主人说过「以后都同意」的操作类别。
	AlwaysAllowed []string         `json:"always_allowed,omitempty"`
	Job           *dianaCodingJob  `json:"job,omitempty"`
	Jobs          []dianaCodingJob `json:"jobs,omitempty"`
	Tail          []string         `json:"tail,omitempty"`
	// TailFirstLine/TailTotalLines 是 tail 这一页从第几行动作开始、日志一共几行动作，
	// 往前翻传 before_line=TailFirstLine。
	TailFirstLine  int `json:"tail_first_line,omitempty"`
	TailTotalLines int `json:"tail_total_lines,omitempty"`
	// NextOffset 不为 0 时 list 还有更早的任务没列出。
	NextOffset int `json:"next_offset,omitempty"`
}

type dianaCodingJob struct {
	Agent       string `json:"agent"`
	ID          string `json:"id"`
	Status      string `json:"status"`
	Workspace   string `json:"workspace"`
	Backend     string `json:"backend"`
	Instruction string `json:"instruction"`
	Elapsed     string `json:"elapsed"`
	SessionID   string `json:"session_id,omitempty"`
	LastAction  string `json:"last_action,omitempty"`
	Steps       int    `json:"steps,omitempty"`
	Result      string `json:"result,omitempty"`
	// DetailNextLine 不为 0 时 instruction、last_action、result 三段连起来只给到前面的
	// 整行，从这一行起用 status 的 detail_line 续读；DetailTotalLines 是三段一共的行数。
	DetailNextLine   int     `json:"detail_next_line,omitempty"`
	DetailTotalLines int     `json:"detail_total_lines,omitempty"`
	Error            string  `json:"error,omitempty"`
	Turns            int     `json:"turns,omitempty"`
	CostUSD          float64 `json:"cost_usd,omitempty"`
	CanFollowUp      bool    `json:"can_follow_up"`
	// AwaitingApproval 是任务正停着等确认的那个操作。查进度时这条比「最近动作」
	// 重要：任务没在跑，是在等人。
	AwaitingApproval string `json:"awaiting_approval,omitempty"`
	// ApprovalPattern 是这个操作所属的类别，主人说以后都同意时 decide 传 always。
	ApprovalPattern string `json:"approval_pattern,omitempty"`
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
	operation := strings.ToLower(strings.TrimSpace(configToolString(input, "operation")))
	switch operation {
	case "", "submit":
		cfg, err := codingAgentConfigFor(t.settings, configToolString(input, "agent"))
		if err != nil {
			return "", err
		}
		return t.submit(ctx, cfg, input, "")
	case "followup":
		return t.followUp(ctx, input)
	case "status":
		return t.status(ctx, input)
	case "tail":
		return t.tail(ctx, input)
	case "cancel":
		return t.cancel(ctx, input)
	case "list":
		return t.list(ctx, input)
	case "agents":
		return t.agents()
	case "workspaces":
		cfg, err := codingAgentConfigFor(t.settings, configToolString(input, "agent"))
		if err != nil {
			return "", err
		}
		return codingToolJSON(dianaCodingResult{
			OK:         true,
			Operation:  "workspaces",
			Workspaces: cfg.workspaceNames(),
			Message:    codingWorkspaceHint(cfg),
		})
	case "decide":
		return t.decide(ctx, input)
	case "approvals":
		// 主人回过「以后都同意」之后，得有地方看见记了什么、并且能收回。清单按
		// 机器人分开，看到和清掉的都只是这台自己的。
		path, ok := t.runtime.codingAlwaysAllowPathFor(t.event.ProfileID)
		if !ok {
			return "", fmt.Errorf("认不出这条消息属于哪台机器人，没法查常驻放行")
		}
		if boolInput(input, "clear") {
			removed, err := forgetCodingAlwaysAllow(path)
			if err != nil {
				return "", fmt.Errorf("清空常驻放行失败：%w", err)
			}
			return codingToolJSON(dianaCodingResult{
				OK: true, Operation: "approvals",
				Message: fmt.Sprintf("已清空 %d 条常驻放行，这些操作以后会重新问你。", removed),
			})
		}
		allowed := loadCodingAlwaysAllow(path)
		message := "还没有常驻放行，危险操作每次都会问。"
		if len(allowed) > 0 {
			message = "这些类别以后不再问；要恢复就带 clear=true 再调一次。"
		}
		return codingToolJSON(dianaCodingResult{
			OK: true, Operation: "approvals", AlwaysAllowed: allowed, Message: message,
		})
	}
	return "", fmt.Errorf("operation 必须是 submit、status、tail、cancel、followup、list、workspaces、agents 或 approvals")
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
	task := instruction
	if background := strings.TrimSpace(configToolString(input, "context")); background != "" {
		instruction = "背景（来自聊天记录）：\n" + background + "\n\n任务：\n" + instruction
	}
	if runes := []rune(instruction); len(runes) > maxCodingInstructionRunes {
		return "", fmt.Errorf("指令超过 %d 字，请把长材料写进工作区文件里让 CLI 自己读", maxCodingInstructionRunes)
	}
	job, handedOff, err := t.runtime.startCodingJob(ctx, t.event, cfg, workspace, instruction, resumeSession, true)
	if err != nil {
		return "", err
	}
	operation := "submit"
	if resumeSession != "" {
		operation = "followup"
	}
	if finished, ok := t.awaitQuickFinish(ctx, job.ID, handedOff); ok {
		view := codingJobView(finished, nil)
		message := "任务已经跑完，这就是最终结果，不会再单独汇报，直接照实告诉用户。"
		if finished.Status != codingJobStatusSucceeded {
			message = "任务刚启动就结束了，没有成功，不会再单独汇报。直接把 error 照实告诉用户，不要说还在后台运行。"
		}
		result := dianaCodingResult{OK: true, Operation: operation, Job: &view, Message: message}
		body, complete, err := codingToolPage(ctx, result, 1)
		// 结果一次放不下时模型要分几次才读得完，也不该让它逐段转述：全文由这里直接
		// 发出去，跟后台汇报一样走合并转发。
		if err == nil && !complete && t.postCodingResultBody(ctx, finished.Result) {
			result.Message = "任务已经跑完，结果全文已经由系统发到聊天里了，不会再单独汇报。不要重贴结果，用一两句说清结论就行。"
			body, _, err = codingToolPage(ctx, result, 1)
		}
		return body, err
	}
	message := fmt.Sprintf("任务已在后台启动，跑完我会主动汇报。期间可以用 status %s 查进度。", job.ID)
	if resumeSession != "" {
		message = fmt.Sprintf("已在原会话上追加指令，任务号 %s。", job.ID)
	}
	if t.announceCodingJob(ctx, renderCodingAcceptCard(job, cfg, task, resumeSession != "")) {
		message = "受理消息已经由系统发到聊天里了，任务号、代理、工作区和指令摘要都在里面，不要再复述。" +
			"跑完会自动汇报，别自己编进度或结果；没有别的要说就不用回复。"
	}
	view := codingJobView(job, nil)
	return codingToolJSON(dianaCodingResult{OK: true, Operation: operation, Job: &view, Message: message})
}

// announceCodingJob 由代码发一条受理消息。「派出去了没有」不能只凭模型一句话：模型
// 说过「交给 Claude Code 了」却根本没调工具，群里看着和真派了一模一样，等来的只有
// 沉默。受理消息只在进程真起来之后由这里发，看不到它就是没派。发不出去不影响任务，
// 退回让模型自己报任务号。
func (t *dianaCodingTool) announceCodingJob(ctx context.Context, text string) bool {
	defer typingIndicatorFromContext(ctx).resume()
	if err := t.runtime.sendOutgoing(ctx, t.event, routeOutgoingToEvent(t.event, OutgoingMessage{Text: text})); err != nil {
		log.Printf("diana coding accept card not sent: %v", err)
		return false
	}
	// 话已经出去了：这一轮不能再被合并重来，否则重新生成时会把受理消息再发一遍。
	t.runtime.sealDirectReply(ctx)
	return true
}

// postCodingResultBody 在这一轮里把结果全文发出去，走和汇报全文一样的合并转发。
func (t *dianaCodingTool) postCodingResultBody(ctx context.Context, body string) bool {
	defer typingIndicatorFromContext(ctx).resume()
	if err := t.runtime.deliverCodingReportBody(ctx, t.event, body); err != nil {
		log.Printf("diana coding result body not posted: %v", err)
		return false
	}
	t.runtime.sealDirectReply(ctx)
	return true
}

// renderCodingAcceptCard 是受理消息的正文，格式固定，不经过模型。
func renderCodingAcceptCard(job CodingJob, cfg codingAgentConfig, task string, followUp bool) string {
	title := "已派出编码任务 " + job.ID
	if followUp {
		title = "已在原会话上追加指令，任务号 " + job.ID
	}
	agent := codingBackendLabel(cfg)
	if name := strings.TrimSpace(job.Agent); name != "" && name != codingDefaultAgent {
		agent += "（" + name + "）"
	}
	task = strings.Join(strings.Fields(task), " ")
	return title + "\n代理：" + agent + " · 工作区 " + job.Workspace +
		"\n指令：" + truncateRunes(task, 120) +
		"\n跑完会在这里汇报结果。"
}

func codingBackendLabel(cfg codingAgentConfig) string {
	switch cfg.Backend {
	case codingBackendClaude:
		return "Claude Code"
	case codingBackendCodex:
		return "Codex"
	case codingBackendACP:
		if name := filepath.Base(strings.TrimSpace(cfg.Command)); name != "" && name != "." {
			return name + "（ACP）"
		}
		return "ACP 代理"
	}
	if name := filepath.Base(strings.TrimSpace(cfg.Command)); name != "" && name != "." {
		return name
	}
	return "编码 CLI"
}

// awaitQuickFinish 等任务头几秒：这期间结束的结果由这一轮的回复带出去，免得单独
// 推的汇报抢在「已启动」之前到。拿到结果却没法交给这一轮（请求被取消、记录写不
// 进去）就退回正常汇报，结果不会丢。
func (t *dianaCodingTool) awaitQuickFinish(ctx context.Context, jobID string, handedOff <-chan struct{}) (CodingJob, bool) {
	registry := t.runtime.codingJobs()
	timer := time.NewTimer(registry.timing().handOff)
	defer timer.Stop()
	select {
	case <-handedOff:
	case <-timer.C:
	case <-ctx.Done():
	}
	if !registry.releaseReportHold(jobID) {
		return CodingJob{}, false
	}
	if ctx.Err() == nil {
		if job, ok := t.runtime.claimHandedOffReport(jobID); ok {
			return job, true
		}
	}
	if job, err := loadCodingJob(jobID); err == nil {
		t.runtime.reportCodingJob(t.runtime.subagentRootContext(), job)
	}
	return CodingJob{}, false
}

// followUp 在一个已经结束的任务的会话上继续。运行中的任务不接受追加：非交互模式的
// 编码 CLI 没有中途收指令的通道，硬插只会变成另一个进程同时改同一份检出。
func (t *dianaCodingTool) followUp(ctx context.Context, input map[string]any) (string, error) {
	job, err := t.resolveJob(input)
	if err != nil {
		return "", err
	}
	if !job.finished() {
		return "", fmt.Errorf("任务 %s 还在跑，没法中途追加指令。等它结束，或者先 cancel", job.ID)
	}
	agent := job.Agent
	if agent == "" {
		agent = codingDefaultAgent
	}
	if requested := strings.TrimSpace(configToolString(input, "agent")); requested != "" && !strings.EqualFold(requested, agent) {
		return "", fmt.Errorf("续跑必须使用原编码代理 %s", agent)
	}
	cfg, err := codingAgentConfigFor(t.settings, agent)
	if err != nil {
		return "", err
	}
	if cfg.Backend != job.Backend || (job.AgentFingerprint != "" && job.AgentFingerprint != codingAgentFingerprint(cfg)) {
		return "", fmt.Errorf("原编码代理配置已改变，请恢复配置或提交新任务")
	}
	if !cfg.StreamJSON || job.SessionID == "" {
		return "", fmt.Errorf("任务 %s 没有可续跑的会话 ID，改用 submit 重新派活", job.ID)
	}
	if requested := strings.TrimSpace(configToolString(input, "workspace")); requested != "" && !strings.EqualFold(requested, job.Workspace) {
		return "", fmt.Errorf("续跑必须使用原工作区 %s", job.Workspace)
	}
	workspace, ok := cfg.workspace(job.Workspace)
	if !ok || workspace.Dir != job.Dir {
		return "", fmt.Errorf("原工作区配置已改变，请恢复配置或提交新任务")
	}
	if input == nil {
		input = map[string]any{}
	}
	input["workspace"] = job.Workspace
	return t.submit(ctx, cfg, input, job.SessionID)
}

func (t *dianaCodingTool) status(ctx context.Context, input map[string]any) (string, error) {
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
		message = "任务停在一个要主人点头的操作上。主人表态后用 decide 裁决，不要让主人回复任务号或固定句式。"
	case !job.finished():
		message = "任务还在跑，跑完我会主动汇报。"
	}
	body, _, err := codingToolPage(ctx, dianaCodingResult{OK: true, Operation: "status", Job: &view, Message: message}, intFromAny(input["detail_line"]))
	return body, err
}

// decide 按主人这条消息的表态裁决当前会话里在等的操作。同意怎么说由模型判断；
// 这里只保证是主人本人、在询问发出之后的消息里、在发出询问的那个会话里。
func (t *dianaCodingTool) decide(ctx context.Context, input map[string]any) (string, error) {
	if t.runtime == nil || t.runtime.codingJobRegistry == nil {
		return "", fmt.Errorf("编码代理未启用")
	}
	if backgroundRunContext(ctx) {
		return "", fmt.Errorf("后台任务不能替主人裁决编码操作")
	}
	jobID := strings.TrimSpace(configToolString(input, "job_id"))
	var candidates []codingApprovalRequest
	for _, request := range t.runtime.codingJobs().pendingApprovals() {
		if !t.runtime.codingApprovalInScope(t.event, request) || (jobID != "" && request.JobID != jobID) {
			continue
		}
		candidates = append(candidates, request)
	}
	switch {
	case len(candidates) == 0:
		return "", fmt.Errorf("当前会话没有在等主人点头的编码操作")
	case len(candidates) > 1:
		lines := make([]string, 0, len(candidates))
		for _, request := range candidates {
			lines = append(lines, request.JobID+"："+codingApprovalDetail(request))
		}
		return "", fmt.Errorf("有多条在等：%s。按主人说的是哪个带 job_id 再调；分不清就问主人指的是哪个操作，不要让主人报任务号", strings.Join(lines, "；"))
	}
	request := candidates[0]
	if t.event.Time > 0 && request.CreatedAt.After(time.Unix(t.event.Time, 0).Add(time.Second)) {
		return "", fmt.Errorf("这条询问是在主人这条消息之后才发出的，等主人看到后再表态")
	}
	allowCode, alwaysCode, denyCode := codingApprovalCodes(request)
	code, allow, always := allowCode, true, false
	switch strings.TrimSpace(configToolString(input, "decision")) {
	case "allow":
	case "always":
		if strings.TrimSpace(request.Pattern) == "" {
			return "", fmt.Errorf("这个操作不属于可以常驻放行的类别，只能同意这一次")
		}
		code, always = alwaysCode, true
	case "deny":
		code, allow = denyCode, false
	default:
		return "", fmt.Errorf("decision 必须是 allow、always 或 deny")
	}
	reply, ok := t.runtime.resolveCodingApproval(t.event, request, code, allow, always)
	if !ok {
		return "", fmt.Errorf("这个确认已经处理过或超时了")
	}
	return codingToolJSON(dianaCodingResult{OK: true, Operation: "decide", Message: reply})
}

func (t *dianaCodingTool) tail(ctx context.Context, input map[string]any) (string, error) {
	job, err := t.resolveJob(input)
	if err != nil {
		return "", err
	}
	lines := 10
	if value := intFromAny(input["tail_lines"]); value > 0 {
		lines = value
	}
	view := codingJobView(job, nil)
	// tail 只给动作；指令和结果可能很长，由 status 按整行分页，这里不重复塞进来。
	view.Instruction, view.Result = "", ""
	result := dianaCodingResult{OK: true, Operation: "tail", Job: &view}
	actions := parseCodingLog(job.LogPath).Actions
	end := len(actions)
	if before := intFromAny(input["before_line"]); before > 0 {
		end = min(end, before-1)
	}
	start := max(0, end-lines)
	// 照 Pi 的做法从最新的一行往前按整行装，装不下的更早几行留给 before_line 续看。
	// 以前最多 20 行、每行截到 300 字，命令和报错的后半截看不到。
	base, err := codingToolJSON(dianaCodingResult{OK: true, Operation: "tail", Job: &view, Message: strings.Repeat("。", 120), TailFirstLine: end, TailTotalLines: len(actions)})
	if err != nil {
		return "", err
	}
	window := takeTailLines(actions[start:end], toolResultBudget(ctx)-utf8.RuneCountInString(base))
	first := end - len(window.lines) + 1
	result.Tail, result.TailTotalLines = window.lines, len(actions)
	if len(window.lines) > 0 {
		result.TailFirstLine = first
	}
	switch {
	case len(actions) == 0:
		result.Message = "日志里还没有读出动作。"
	case window.cutRunes > 0:
		result.Message = fmt.Sprintf("第 %d 行动作超过单次上限，只给了开头，后面 %d 字没有给出。", first, window.cutRunes)
	}
	if len(window.lines) > 0 && first > 1 {
		result.Message += fmt.Sprintf("这是第 %d–%d 行动作，日志共 %d 行；更早的传 before_line=%d 往前翻。", first, end, len(actions), first)
	}
	result.Message += "指令和结果用 status 看。"
	return codingToolJSON(result)
}

type codingTailWindow struct {
	lines    []string
	cutRunes int
}

// takeTailLines 从最后一行往前按整行装进 budget，返回的行保持原来的先后顺序。最后
// 一行自己就装不下时只给它的开头，免得一行超长的动作永远看不到。
func takeTailLines(lines []string, budget int) codingTailWindow {
	used := 0
	start := len(lines)
	window := codingTailWindow{}
	for index := len(lines) - 1; index >= 0; index-- {
		cost := jsonStringRunes(lines[index]) + 3
		if used+cost > budget {
			if index == len(lines)-1 && budget > 3 {
				prefix := textPrefixWithin(lines[index], budget-3, jsonStringRunes)
				window.cutRunes = utf8.RuneCountInString(lines[index]) - utf8.RuneCountInString(prefix)
				window.lines = []string{prefix}
				return window
			}
			break
		}
		used += cost
		start = index
	}
	window.lines = append([]string(nil), lines[start:]...)
	return window
}

func (t *dianaCodingTool) cancel(ctx context.Context, input map[string]any) (string, error) {
	id := strings.TrimSpace(configToolString(input, "job_id"))
	if id == "" {
		running := t.runningJobs()
		if len(running) != 1 {
			return "", fmt.Errorf("要取消哪个任务？请给出 job_id")
		}
		id = running[0].ID
	} else if _, err := t.loadJob(id); err != nil {
		return "", err
	}
	job, err := t.runtime.cancelCodingJob(ctx, id)
	if err != nil {
		return "", err
	}
	view := codingJobView(job, nil)
	body, _, err := codingToolPage(ctx, dianaCodingResult{
		OK:        true,
		Operation: "cancel",
		Job:       &view,
		Message:   "任务已终止，已完成的改动留在工作区里。",
	}, 1)
	return body, err
}

// list 列出这台机器人派过的任务，新的在前，按整条装进单次上限，装不下的更早任务
// 用 offset 翻页。结果全文不在这里给，用 status 按任务读。以前固定只列 10 个。
func (t *dianaCodingTool) list(ctx context.Context, input map[string]any) (string, error) {
	jobs := t.jobs()
	offset := min(max(0, intFromAny(input["offset"])), len(jobs))
	budget := toolResultBudget(ctx)
	result := dianaCodingResult{OK: true, Operation: "list", Jobs: []dianaCodingJob{}}
	for index := offset; index < len(jobs); index++ {
		view := codingJobView(jobs[index], nil)
		view.Result = ""
		next := result
		next.Jobs = append(append([]dianaCodingJob(nil), result.Jobs...), view)
		next.NextOffset = index + 1
		next.Message = codingListMessage(len(jobs), index+1)
		body, err := codingToolJSON(next)
		if err != nil {
			return "", err
		}
		if len(result.Jobs) > 0 && utf8.RuneCountInString(body) > budget {
			break
		}
		result = next
	}
	if result.NextOffset >= len(jobs) {
		result.NextOffset = 0
	}
	result.Message = codingListMessage(len(jobs), offset+len(result.Jobs))
	return codingToolJSON(result)
}

func codingListMessage(total, shown int) string {
	message := "结果全文用 status 传 job_id 读。"
	if shown < total {
		message += fmt.Sprintf("一共 %d 个任务，这页列到第 %d 个；更早的传 offset=%d 接着看。", total, shown, shown)
	}
	return message
}

// resolveJob 找出要操作的任务。省略 job_id 时优先取在跑的那个，没有就取最近一个——
// 用户说「进度怎么样了」时指的几乎总是这两者之一。
func (t *dianaCodingTool) resolveJob(input map[string]any) (CodingJob, error) {
	if id := strings.TrimSpace(configToolString(input, "job_id")); id != "" {
		return t.loadJob(id)
	}
	if running := t.runningJobs(); len(running) == 1 {
		return running[0], nil
	}
	jobs := t.jobs()
	if len(jobs) == 0 {
		return CodingJob{}, fmt.Errorf("还没有任何编码任务")
	}
	return jobs[0], nil
}

// loadJob 按任务号读记录，只认这台机器人自己派的。别人的任务和不存在的任务回同
// 一句话，不替别人确认「有这么个任务」。
func (t *dianaCodingTool) loadJob(id string) (CodingJob, error) {
	job, err := loadCodingJob(id)
	if err != nil || !t.runtime.codingJobVisibleTo(job, t.event) {
		return CodingJob{}, fmt.Errorf("找不到任务 %s", id)
	}
	return job, nil
}

// jobs 是这台机器人自己派过的任务，按开始时间倒序。
func (t *dianaCodingTool) jobs() []CodingJob {
	all := listCodingJobs()
	out := make([]CodingJob, 0, len(all))
	for _, job := range all {
		if t.runtime.codingJobVisibleTo(job, t.event) {
			out = append(out, job)
		}
	}
	return out
}

func (t *dianaCodingTool) runningJobs() []CodingJob {
	out := make([]CodingJob, 0, 4)
	for _, job := range t.jobs() {
		if !job.finished() {
			out = append(out, job)
		}
	}
	return out
}

// attachPendingApproval 告诉模型任务停在哪个操作上，裁决走 decide。
func (t *dianaCodingTool) attachPendingApproval(view *dianaCodingJob) {
	if t.runtime == nil || t.runtime.codingJobRegistry == nil {
		return
	}
	for _, request := range t.runtime.codingJobs().pendingApprovals() {
		if request.JobID != view.ID {
			continue
		}
		view.AwaitingApproval = codingApprovalDetail(request)
		view.ApprovalPattern = strings.TrimSpace(request.Pattern)
		return
	}
}

func codingJobView(job CodingJob, snapshot *codingJobSnapshot) dianaCodingJob {
	view := dianaCodingJob{
		ID:          job.ID,
		Status:      job.Status,
		Workspace:   job.Workspace,
		Backend:     job.Backend,
		Agent:       job.Agent,
		Instruction: job.Instruction,
		SessionID:   job.SessionID,
		Result:      job.Result,
		Error:       job.Error,
		Turns:       job.Turns,
		CostUSD:     job.CostUSD,
	}
	if view.Agent == "" {
		view.Agent = codingDefaultAgent
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
	view.CanFollowUp = job.finished() && job.Backend == codingBackendClaude && view.SessionID != ""
	return view
}

func codingWorkspaceHint(cfg codingAgentConfig) string {
	if len(cfg.Workspaces) == 0 {
		return "还没有登记任何工作区，请在插件设置的工作区白名单里填写。"
	}
	return fmt.Sprintf("后端 %s，最长单次运行 %s。", cfg.Backend, formatCodingDuration(cfg.MaxRuntime))
}

// codingToolPage 把带任务的返回值装进单次上限。指令、最近动作、结果三段是长文本，
// 连起来按整行从 startLine 起给，三段各自只放落在这一页里的行；放不下的标上
// detail_next_line，message 写明用 status 的 detail_line 续读。任务还在跑时也一样
// 过预算。complete 说明三段都给到了末尾。以前结果固定只给前 1500 字、指令截到 300 字。
func codingToolPage(ctx context.Context, result dianaCodingResult, startLine int) (string, bool, error) {
	if result.Job == nil {
		body, err := codingToolJSON(result)
		return body, true, err
	}
	full := *result.Job
	fields := []*string{&full.Instruction, &full.LastAction, &full.Result}
	var lines []string
	var owner []int
	for index, field := range fields {
		if *field == "" {
			continue
		}
		for _, line := range strings.Split(*field, "\n") {
			lines, owner = append(lines, line), append(owner, index)
		}
	}
	budget := toolResultBudget(ctx)
	skeleton := full
	skeleton.Instruction, skeleton.LastAction, skeleton.Result, skeleton.DetailNextLine, skeleton.DetailTotalLines = "", "", "", 1, 1
	base := result
	base.Job = &skeleton
	base.Message += strings.Repeat("。", 160)
	raw, err := codingToolJSON(base)
	if err != nil {
		return "", false, err
	}
	reserve := utf8.RuneCountInString(raw)
	for {
		job := full
		page := result
		page.Job = &job
		window := takeLines(len(lines), func(i int) string { return lines[i] }, max(startLine, 1), len(lines), max(0, budget-reserve), jsonStringRunes)
		if len(lines) > 0 && window.Start > window.Total {
			return "", false, fmt.Errorf("任务 %s 的指令、动作和结果共 %d 行，detail_line=%d 超出范围", job.ID, window.Total, window.Start)
		}
		parts := make([][]string, len(fields))
		if window.End >= window.Start {
			for offset, line := range strings.Split(window.Text, "\n") {
				field := owner[window.Start-1+offset]
				parts[field] = append(parts[field], line)
			}
		}
		job.Instruction, job.LastAction, job.Result = strings.Join(parts[0], "\n"), strings.Join(parts[1], "\n"), strings.Join(parts[2], "\n")
		if !window.Whole() {
			job.DetailTotalLines = window.Total
			page.Message += "指令、最近动作、结果连起来给出了" + window.Range() + "。"
		}
		if !window.Complete() {
			job.DetailNextLine = window.ResumeLine()
			page.Message += fmt.Sprintf("还没给完：传 operation=status、job_id=%s、detail_line=%d 接着读，读完之前不要当成全文。", job.ID, job.DetailNextLine)
		}
		body, err := codingToolJSON(page)
		if err != nil {
			return "", false, err
		}
		over := utf8.RuneCountInString(body) - budget
		if over <= 0 || reserve >= budget {
			return body, window.Complete(), nil
		}
		reserve += over
	}
}

func codingToolJSON(result dianaCodingResult) (string, error) {
	body, err := json.Marshal(result)
	if err != nil {
		return "", err
	}
	return string(body), nil
}

func (t *dianaCodingTool) agents() (string, error) {
	profiles, err := codingProfiles(t.settings)
	if err != nil {
		return "", err
	}
	type entry struct {
		Name    string `json:"name"`
		Backend string `json:"backend"`
		Model   string `json:"model,omitempty"`
		Default bool   `json:"default"`
	}
	defaultLegacy := true
	for _, p := range profiles {
		if p.Default {
			defaultLegacy = false
		}
	}
	entries := []entry{{Name: codingDefaultAgent, Backend: t.settings.String(codingAgentSettingBackend, codingBackendClaude), Model: t.settings.String(codingAgentSettingModel, ""), Default: defaultLegacy}}
	for _, p := range profiles {
		entries = append(entries, entry{Name: p.ID, Backend: p.Backend, Model: p.Model, Default: p.Default})
	}
	data, err := json.Marshal(map[string]any{"ok": true, "operation": "agents", "agents": entries})
	return string(data), err
}
