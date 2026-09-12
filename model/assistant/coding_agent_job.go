// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/SuInk/diana/model/applog"

	"github.com/google/uuid"
)

const (
	codingJobStatusRunning     = "running"
	codingJobStatusSucceeded   = "succeeded"
	codingJobStatusFailed      = "failed"
	codingJobStatusCancelled   = "cancelled"
	codingJobStatusInterrupted = "interrupted"

	// codingJobStaleAfter 是重启接回时判断「PID 还活着但已经不是当初那个进程」的
	// 阈值。PID 会被系统复用，光靠 kill 0 判活会把别人的进程当成自己的任务。日志
	// 在正常运行时一直有输出，安静这么久基本只剩两种可能：任务早就没了，或者它
	// 卡死了——两种都该按中断收尾，而不是继续等。
	codingJobStaleAfter = 30 * time.Minute
	// codingJobPollInterval 是接回的任务轮询判活的间隔。自己启动的任务不走轮询，
	// 直接 Wait。
	codingJobPollInterval = 10 * time.Second
	codingJobTailLines    = 20
	codingJobMaxLineBytes = 8 << 10
	codingJobResultRunes  = 1500
	codingJobRetainCount  = 60
)

// CodingJob 是一次编码 CLI 调用的持久记录。
//
// 它落在工作区目录里而不是数据库：进程的输出本来就要写成日志文件，记录和日志放
// 在一起只有一个真相来源，重启接回就是扫一遍目录。这样没配数据库的部署也能用。
type CodingJob struct {
	ID          string    `json:"id"`
	Backend     string    `json:"backend"`
	Workspace   string    `json:"workspace"`
	Dir         string    `json:"dir"`
	Instruction string    `json:"instruction"`
	SessionID   string    `json:"session_id,omitempty"`
	ResumedFrom string    `json:"resumed_from,omitempty"`
	PID         int       `json:"pid,omitempty"`
	LogPath     string    `json:"log_path"`
	Status      string    `json:"status"`
	ExitCode    int       `json:"exit_code,omitempty"`
	StartedAt   time.Time `json:"started_at"`
	FinishedAt  time.Time `json:"finished_at,omitempty"`
	Deadline    time.Time `json:"deadline,omitempty"`
	Result      string    `json:"result,omitempty"`
	Error       string    `json:"error,omitempty"`
	CostUSD     float64   `json:"cost_usd,omitempty"`
	Turns       int       `json:"turns,omitempty"`
	// ApprovalMode 记下这次任务按哪档审批跑的：查任务时要能看出「它当时是不是
	// 会来问我」，事后改了设置也不影响已经跑过的任务怎么被解释。
	ApprovalMode string `json:"approval_mode,omitempty"`
	// ApprovalTimeoutSeconds 跟着任务存下来，重启接回时才能按当初配的时长等，
	// 而不是退回默认值。
	ApprovalTimeoutSeconds int `json:"approval_timeout_seconds,omitempty"`
	// Reported 标记结果已经发给用户了。重启接回要靠它避免把同一个结果汇报两次。
	Reported bool `json:"reported,omitempty"`
	// Target 是派活那条消息的来源，用来在任务结束后找回该往哪个会话汇报。
	Target codingJobTarget `json:"target"`
}

type codingJobTarget struct {
	Platform         string `json:"platform,omitempty"`
	ProfileID        string `json:"profile_id,omitempty"`
	ContextNamespace string `json:"context_namespace,omitempty"`
	GroupID          string `json:"group_id,omitempty"`
	UserID           string `json:"user_id,omitempty"`
}

func codingJobTargetFromEvent(event MessageEvent) codingJobTarget {
	return codingJobTarget{
		Platform:         event.Platform,
		ProfileID:        event.ProfileID,
		ContextNamespace: event.ContextNamespace,
		GroupID:          event.GroupID,
		UserID:           event.UserID,
	}
}

func (t codingJobTarget) event() MessageEvent {
	event := MessageEvent{
		Kind:             EventKindPrivate,
		Platform:         t.Platform,
		ProfileID:        t.ProfileID,
		ContextNamespace: t.ContextNamespace,
		UserID:           t.UserID,
	}
	if t.GroupID != "" {
		event.Kind = EventKindGroup
		event.GroupID = t.GroupID
	}
	return event
}

func (j CodingJob) finished() bool {
	return j.Status != codingJobStatusRunning
}

// CodingWorkspaceRoot 是编码代理的工作区根目录。和 Agent 工作目录同一套约定：
// 跟着数据库走，不做成配置项。
func CodingWorkspaceRoot() string {
	return filepath.Join(AgentWorkspaceDir(), "coding")
}

func codingJobRecordDir() string {
	return filepath.Join(CodingWorkspaceRoot(), ".jobs")
}

func codingJobRecordPath(id string) string {
	return filepath.Join(codingJobRecordDir(), id+".json")
}

func codingJobLogPath(id string) string {
	return filepath.Join(codingJobRecordDir(), id+".log")
}

func saveCodingJob(job CodingJob) error {
	if err := os.MkdirAll(codingJobRecordDir(), 0o700); err != nil {
		return err
	}
	body, err := json.MarshalIndent(job, "", "  ")
	if err != nil {
		return err
	}
	path := codingJobRecordPath(job.ID)
	// 先写临时文件再改名：任务记录可能在进程被杀的瞬间被读到，半截 JSON 会让
	// 重启接回直接跳过这个任务，等于丢掉一次汇报。
	temp := path + ".tmp"
	if err := os.WriteFile(temp, body, 0o600); err != nil {
		return err
	}
	return os.Rename(temp, path)
}

func loadCodingJob(id string) (CodingJob, error) {
	body, err := os.ReadFile(codingJobRecordPath(id))
	if err != nil {
		return CodingJob{}, err
	}
	var job CodingJob
	if err := json.Unmarshal(body, &job); err != nil {
		return CodingJob{}, err
	}
	return job, nil
}

// listCodingJobs 按开始时间倒序返回全部任务记录，并顺手清掉超出保留数量的旧记录。
func listCodingJobs() []CodingJob {
	entries, err := os.ReadDir(codingJobRecordDir())
	if err != nil {
		return nil
	}
	jobs := make([]CodingJob, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		job, err := loadCodingJob(strings.TrimSuffix(entry.Name(), ".json"))
		if err != nil {
			continue
		}
		jobs = append(jobs, job)
	}
	sort.Slice(jobs, func(a, b int) bool { return jobs[a].StartedAt.After(jobs[b].StartedAt) })
	if len(jobs) > codingJobRetainCount {
		for _, stale := range jobs[codingJobRetainCount:] {
			if !stale.finished() {
				continue
			}
			_ = os.Remove(codingJobRecordPath(stale.ID))
			_ = os.Remove(stale.LogPath)
			_ = os.Remove(codingApprovalPolicyPath(stale.ID))
			_ = os.Remove(filepath.Join(codingJobRecordDir(), stale.ID+".settings.json"))
		}
		jobs = jobs[:codingJobRetainCount]
	}
	return jobs
}

func runningCodingJobs() []CodingJob {
	out := make([]CodingJob, 0, 4)
	for _, job := range listCodingJobs() {
		if !job.finished() {
			out = append(out, job)
		}
	}
	return out
}

// codingJobSnapshot 是从日志里读出来的实时进度。任务状态的真相在日志里，记录文件
// 只负责身份和收尾结论，两边不重复存一份进度。
type codingJobSnapshot struct {
	SessionID  string
	Lines      int
	LastAction string
	Tools      int
	Result     string
	IsError    bool
	Done       bool
	CostUSD    float64
	Turns      int
	Tail       []string
	UpdatedAt  time.Time
}

// parseCodingLog 单遍扫描日志。Claude Code 的 stream-json 能被精确解析出会话 ID、
// 每一步动作和结构化结果；别的后端认不出格式时退化成保留尾巴当结果。
func parseCodingLog(path string) codingJobSnapshot {
	snapshot := codingJobSnapshot{}
	if info, err := os.Stat(path); err == nil {
		snapshot.UpdatedAt = info.ModTime()
	}
	file, err := os.Open(path)
	if err != nil {
		return snapshot
	}
	defer file.Close()

	tail := make([]string, 0, codingJobTailLines)
	reader := bufio.NewReaderSize(file, 64<<10)
	for {
		line, err := readBoundedLine(reader)
		if line != "" {
			snapshot.Lines++
			if action, ok := applyCodingLogLine(&snapshot, line); ok && action != "" {
				snapshot.LastAction = action
				tail = append(tail, action)
				if len(tail) > codingJobTailLines {
					tail = tail[1:]
				}
			}
		}
		if err != nil {
			break
		}
	}
	snapshot.Tail = tail
	if snapshot.Result == "" && len(tail) > 0 {
		snapshot.Result = strings.Join(tail, "\n")
	}
	return snapshot
}

// readBoundedLine 读一行并限制长度。工具输出整段落进一行 JSON 很常见，几十兆一行
// 的日志不该把内存吃掉；超长的部分直接丢，解析只要开头的字段。
func readBoundedLine(reader *bufio.Reader) (string, error) {
	var builder strings.Builder
	for {
		chunk, err := reader.ReadString('\n')
		if builder.Len() < codingJobMaxLineBytes {
			remaining := codingJobMaxLineBytes - builder.Len()
			if len(chunk) > remaining {
				builder.WriteString(chunk[:remaining])
			} else {
				builder.WriteString(chunk)
			}
		}
		if err != nil {
			return strings.TrimRight(builder.String(), "\r\n"), err
		}
		if strings.HasSuffix(chunk, "\n") {
			return strings.TrimRight(builder.String(), "\r\n"), nil
		}
	}
}

// applyCodingLogLine 把一行日志并进快照，返回这一行对应的「人能看懂的动作」。
func applyCodingLogLine(snapshot *codingJobSnapshot, line string) (string, bool) {
	line = strings.TrimSpace(line)
	if line == "" {
		return "", false
	}
	if !strings.HasPrefix(line, "{") {
		return line, true
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(line), &payload); err != nil {
		return line, true
	}
	if id := jsonString(payload, "session_id"); id != "" {
		snapshot.SessionID = id
	}
	switch jsonString(payload, "type") {
	case "system":
		if jsonString(payload, "subtype") == "init" {
			return "会话已建立", true
		}
		return "", false
	case "assistant":
		return codingAssistantAction(payload), true
	case "user":
		// 工具结果回填，逐条报出来只会把进度刷成噪音。
		return "", false
	case "result":
		snapshot.Done = true
		snapshot.IsError = jsonBool(payload, "is_error")
		if text := jsonString(payload, "result"); text != "" {
			snapshot.Result = text
		}
		snapshot.CostUSD = jsonFloat(payload, "total_cost_usd")
		snapshot.Turns = int(jsonFloat(payload, "num_turns"))
		return "", false
	}
	// 认不出来的 JSON 后端：挑几个常见的正文字段，都没有就留原文。
	for _, key := range []string{"text", "message", "msg", "content"} {
		if text := jsonString(payload, key); text != "" {
			return text, true
		}
	}
	return line, true
}

func codingAssistantAction(payload map[string]any) string {
	message, _ := payload["message"].(map[string]any)
	parts, _ := message["content"].([]any)
	texts := make([]string, 0, len(parts))
	for _, raw := range parts {
		part, _ := raw.(map[string]any)
		switch jsonString(part, "type") {
		case "tool_use":
			name := jsonString(part, "name")
			if name == "" {
				name = "工具"
			}
			if detail := codingToolDetail(part); detail != "" {
				texts = append(texts, name+"："+detail)
				continue
			}
			texts = append(texts, name)
		case "text":
			if text := strings.TrimSpace(jsonString(part, "text")); text != "" {
				texts = append(texts, truncateRunes(text, 200))
			}
		}
	}
	return strings.Join(texts, "；")
}

// codingToolDetail 从工具入参里挑一个能说明它在动什么的字段。命令和路径就够了，
// 整个入参打出来会把进度变成日志转储。
func codingToolDetail(part map[string]any) string {
	input, _ := part["input"].(map[string]any)
	for _, key := range []string{"command", "file_path", "path", "pattern", "url"} {
		if value := strings.TrimSpace(jsonString(input, key)); value != "" {
			return truncateRunes(value, 120)
		}
	}
	return ""
}

func jsonString(payload map[string]any, key string) string {
	value, _ := payload[key].(string)
	return value
}

func jsonBool(payload map[string]any, key string) bool {
	value, _ := payload[key].(bool)
	return value
}

func jsonFloat(payload map[string]any, key string) float64 {
	value, _ := payload[key].(float64)
	return value
}

// buildCodingArgs 把命令模板展开成 argv。不经过 shell：指令原文里的引号、反引号和
// 分号在这里只是普通字符。
//
// 占位符取不到值时，这个参数连同紧挨在前面的标志一起丢掉——没配模型时留一个
// 孤零零的 --model 会让 CLI 直接报参数错误。
func buildCodingArgs(template string, values map[string]string) []string {
	fields := strings.Fields(template)
	out := make([]string, 0, len(fields))
	// bare 记录已输出的参数里哪些是「不含占位符的裸标志」，只有它们能被回撤。
	bare := make([]bool, 0, len(fields))
	for _, field := range fields {
		expanded, hasPlaceholder, complete := expandCodingPlaceholders(field, values)
		if hasPlaceholder && !complete {
			if n := len(out); n > 0 && bare[n-1] && strings.HasPrefix(out[n-1], "-") {
				out = out[:n-1]
				bare = bare[:n-1]
			}
			continue
		}
		out = append(out, expanded)
		bare = append(bare, !hasPlaceholder)
	}
	return out
}

// expandCodingPlaceholders 展开一个参数里的占位符。返回展开结果、这个参数是否带
// 占位符、以及占位符是否都拿到了值。模板里写了但调用方没给的占位符和给了空值的
// 一样算「没拿到」：留一个字面量 {{model}} 传给 CLI 比丢掉这个参数更糟。
func expandCodingPlaceholders(field string, values map[string]string) (string, bool, bool) {
	if !strings.Contains(field, "{{") {
		return field, false, true
	}
	expanded := field
	for key, value := range values {
		if strings.TrimSpace(value) == "" {
			continue
		}
		expanded = strings.ReplaceAll(expanded, "{{"+key+"}}", value)
	}
	if strings.Contains(expanded, "{{") {
		return expanded, true, false
	}
	return expanded, true, true
}

var errCodingWorkspaceBusy = errors.New("该工作区已经有任务在跑")

// ensureCodingWorkspace 准备工作目录。登记为仓库地址的工作区首次使用时 clone，
// 之后常驻：工作区是持久的，不每次重新检出。
func ensureCodingWorkspace(ctx context.Context, workspace codingWorkspace) error {
	if info, err := os.Stat(workspace.Dir); err == nil {
		if !info.IsDir() {
			return fmt.Errorf("工作区 %s 的路径不是目录：%s", workspace.Name, workspace.Dir)
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if workspace.RepoURL == "" {
		return fmt.Errorf("工作区 %s 的目录不存在：%s", workspace.Name, workspace.Dir)
	}
	if err := os.MkdirAll(filepath.Dir(workspace.Dir), 0o700); err != nil {
		return err
	}
	cloneCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(cloneCtx, "git", "clone", workspace.RepoURL, workspace.Dir)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("clone 工作区 %s 失败：%s", workspace.Name, strings.TrimSpace(string(output)))
	}
	return nil
}

type codingJobRegistry struct {
	mu      sync.Mutex
	running map[string]string // 工作区名 -> 任务 ID
	watched map[string]bool
	// approvals 按放行码和拒绝码两个键指向同一个等待项，主人回哪个码都认。
	approvals map[string]*codingApprovalWait
}

func (r *Runtime) codingJobs() *codingJobRegistry {
	r.codingJobsOnce.Do(func() {
		r.codingJobRegistry = &codingJobRegistry{
			running:   map[string]string{},
			watched:   map[string]bool{},
			approvals: map[string]*codingApprovalWait{},
		}
	})
	return r.codingJobRegistry
}

// claimCodingWorkspace 占住一个工作区。同一份检出同时跑两个代理会互相覆盖，这个
// 闸门比并发上限更重要，所以按工作区而不是按总数来锁。
func (g *codingJobRegistry) claim(workspace, jobID string, limit int) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if existing, ok := g.running[workspace]; ok {
		return fmt.Errorf("%w（任务 %s）", errCodingWorkspaceBusy, existing)
	}
	if limit > 0 && len(g.running) >= limit {
		return fmt.Errorf("同时运行的编码任务已达上限 %d 个", limit)
	}
	g.running[workspace] = jobID
	return nil
}

func (g *codingJobRegistry) release(workspace, jobID string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if existing, ok := g.running[workspace]; ok && existing == jobID {
		delete(g.running, workspace)
	}
	delete(g.watched, jobID)
}

func (g *codingJobRegistry) beginWatch(jobID string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.watched[jobID] {
		return false
	}
	g.watched[jobID] = true
	return true
}

// launchCodingJob 启动一次编码调用并立刻返回。进程脱离 Diana 的进程组独立运行，
// Diana 重启后按 PID 接回。
func (r *Runtime) launchCodingJob(
	ctx context.Context,
	event MessageEvent,
	cfg codingAgentConfig,
	workspace codingWorkspace,
	instruction string,
	resumeSession string,
) (CodingJob, error) {
	if err := ensureCodingWorkspace(ctx, workspace); err != nil {
		return CodingJob{}, err
	}
	if err := os.MkdirAll(codingJobRecordDir(), 0o700); err != nil {
		return CodingJob{}, err
	}
	job := CodingJob{
		ID:          "code-" + strings.ReplaceAll(uuid.NewString()[:8], "-", ""),
		Backend:     cfg.Backend,
		Workspace:   workspace.Name,
		Dir:         workspace.Dir,
		Instruction: instruction,
		ResumedFrom: resumeSession,
		Status:      codingJobStatusRunning,
		StartedAt:   time.Now(),
		Target:      codingJobTargetFromEvent(event),
	}
	job.LogPath = codingJobLogPath(job.ID)
	job.Deadline = job.StartedAt.Add(cfg.MaxRuntime)

	registry := r.codingJobs()
	if err := registry.claim(workspace.Name, job.ID, cfg.Concurrency); err != nil {
		return CodingJob{}, err
	}

	settingsPath, err := prepareCodingApproval(cfg, job.ID)
	if err != nil {
		registry.release(workspace.Name, job.ID)
		return CodingJob{}, err
	}
	job.ApprovalMode = cfg.ApprovalMode
	job.ApprovalTimeoutSeconds = int(cfg.ApprovalTimeout / time.Second)

	logFile, err := os.OpenFile(job.LogPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		registry.release(workspace.Name, job.ID)
		return CodingJob{}, err
	}
	args := buildCodingArgs(cfg.Template, map[string]string{
		"instruction": instruction,
		"model":       cfg.Model,
		"session":     resumeSession,
		"workspace":   workspace.Dir,
		"settings":    settingsPath,
	})
	cmd := exec.Command(cfg.Command, args...)
	cmd.Dir = workspace.Dir
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.Env = codingJobEnv(cfg)
	cmd.SysProcAttr = detachedProcAttr()
	if err := cmd.Start(); err != nil {
		logFile.Close()
		registry.release(workspace.Name, job.ID)
		return CodingJob{}, fmt.Errorf("启动 %s 失败：%w", cfg.Command, err)
	}
	logFile.Close()
	job.PID = cmd.Process.Pid
	if err := saveCodingJob(job); err != nil {
		// 记录写不下去就别让任务跑成孤儿进程：没有记录就没有汇报，也没法取消。
		killCodingProcess(job.PID)
		registry.release(workspace.Name, job.ID)
		return CodingJob{}, fmt.Errorf("保存任务记录失败：%w", err)
	}
	r.recordCodingJobLog(ctx, job, applog.KindOperation, applog.LevelInfo, "编码任务已启动", instruction)
	registry.beginWatch(job.ID)
	go func() {
		defer recoverGoroutinePanic("coding.watchJob")
		r.watchCodingJob(job, cmd, cfg.ApprovalTimeout)
	}()
	return job, nil
}

func codingJobEnv(cfg codingAgentConfig) []string {
	env := os.Environ()
	if cfg.APIKey != "" && cfg.EnvKey != "" {
		env = append(env, cfg.EnvKey+"="+cfg.APIKey)
	}
	// CLI 在非交互模式下仍可能想开分页器或彩色输出，两个都只会污染日志。
	env = append(env, "CI=1", "TERM=dumb", "NO_COLOR=1", "PAGER=cat")
	return env
}

// watchCodingJob 等自己启动的任务结束。cmd 非 nil 时直接 Wait；重启接回的任务没有
// Cmd，走轮询判活。
func (r *Runtime) watchCodingJob(job CodingJob, cmd *exec.Cmd, approvalTimeout time.Duration) {
	defer r.codingJobs().release(job.Workspace, job.ID)
	rootCtx := r.subagentRootContext()
	if job.ApprovalMode != "" && job.ApprovalMode != codingApprovalModeOff {
		// 审批信箱只在任务活着的时候盯：任务一结束就没有等着的 hook 进程了。
		approvalCtx, stopApprovals := context.WithCancel(rootCtx)
		defer stopApprovals()
		if approvalTimeout <= 0 && job.ApprovalTimeoutSeconds > 0 {
			approvalTimeout = time.Duration(job.ApprovalTimeoutSeconds) * time.Second
		}
		if approvalTimeout <= 0 {
			approvalTimeout = defaultCodingApprovalTimeoutMinutes * time.Minute
		}
		go func() {
			defer recoverGoroutinePanic("coding.watchApprovals")
			r.watchCodingApprovals(approvalCtx, job, approvalTimeout)
		}()
	}

	deadline := job.Deadline
	if deadline.IsZero() {
		deadline = job.StartedAt.Add(defaultCodingMaxRuntimeMinutes * time.Minute)
	}
	timedOut := false
	if cmd != nil {
		done := make(chan error, 1)
		go func() {
			defer recoverGoroutinePanic("coding.waitJob")
			done <- cmd.Wait()
		}()
		select {
		case err := <-done:
			job.ExitCode = codingExitCode(cmd, err)
		case <-time.After(time.Until(deadline)):
			timedOut = true
			killCodingProcess(job.PID)
			<-done
			job.ExitCode = codingExitCode(cmd, nil)
		}
	} else {
		for {
			if !codingProcessAlive(job.PID) {
				break
			}
			if time.Now().After(deadline) {
				timedOut = true
				killCodingProcess(job.PID)
				break
			}
			select {
			case <-rootCtx.Done():
				// Diana 要退出了。任务是脱离进程组跑的，不动它：下次启动再接回。
				return
			case <-time.After(codingJobPollInterval):
			}
		}
	}
	r.finalizeCodingJob(rootCtx, job, timedOut, false)
}

func codingExitCode(cmd *exec.Cmd, waitErr error) int {
	if cmd != nil && cmd.ProcessState != nil {
		return cmd.ProcessState.ExitCode()
	}
	var exitErr *exec.ExitError
	if errors.As(waitErr, &exitErr) {
		return exitErr.ExitCode()
	}
	if waitErr != nil {
		return -1
	}
	return 0
}

// finalizeCodingJob 按日志给任务收尾并汇报。interrupted 为真表示进程在 Diana 不在
// 的时候没了，结论只能从日志里推。
func (r *Runtime) finalizeCodingJob(ctx context.Context, job CodingJob, timedOut, interrupted bool) {
	// 记录可能在运行期间被 cancel 改过，以磁盘上的为准再收尾。
	if latest, err := loadCodingJob(job.ID); err == nil {
		if latest.Status == codingJobStatusCancelled {
			latest.FinishedAt = time.Now()
			_ = saveCodingJob(latest)
			return
		}
		// 已经收过尾并汇报过了就别再来一遍：重启接回和原来的守望协程理论上不会
		// 同时活着，但收尾要写状态又要发消息，重复一次就是重复一条汇报。
		if latest.finished() && latest.Reported {
			return
		}
		latest.PID = job.PID
		latest.ExitCode = job.ExitCode
		job = latest
	}
	snapshot := parseCodingLog(job.LogPath)
	if snapshot.SessionID != "" {
		job.SessionID = snapshot.SessionID
	}
	job.Result = truncateRunes(strings.TrimSpace(snapshot.Result), codingJobResultRunes)
	job.CostUSD = snapshot.CostUSD
	job.Turns = snapshot.Turns
	job.FinishedAt = time.Now()
	switch {
	case timedOut:
		job.Status = codingJobStatusFailed
		job.Error = "超过单次最长运行时间，已终止；已完成的改动留在工作区里"
	case interrupted && !snapshot.Done:
		job.Status = codingJobStatusInterrupted
		job.Error = "Diana 重启期间进程结束，没有拿到结果"
	case snapshot.Done && snapshot.IsError:
		job.Status = codingJobStatusFailed
		if job.Error == "" {
			job.Error = firstNonEmpty(job.Result, "CLI 报告执行失败")
		}
	case job.ExitCode != 0 && !snapshot.Done:
		job.Status = codingJobStatusFailed
		job.Error = fmt.Sprintf("CLI 退出码 %d", job.ExitCode)
	default:
		job.Status = codingJobStatusSucceeded
	}
	if err := saveCodingJob(job); err != nil {
		r.setError(err.Error())
	}
	level := applog.LevelInfo
	kind := applog.KindOperation
	if job.Status != codingJobStatusSucceeded {
		level, kind = applog.LevelError, applog.KindError
	}
	r.recordCodingJobLog(ctx, job, kind, level, "编码任务已结束："+job.Status, firstNonEmpty(job.Error, job.Result))
	r.reportCodingJob(ctx, job)
}

// reportCodingJob 把结果发回派活的那个会话。汇报成功才落 Reported：发失败时下次
// 启动还会再试一次，比静默丢掉一小时的工作强。
func (r *Runtime) reportCodingJob(ctx context.Context, job CodingJob) {
	if job.Reported || !job.finished() {
		return
	}
	target := job.Target.event()
	if strings.TrimSpace(target.UserID) == "" && strings.TrimSpace(target.GroupID) == "" {
		return
	}
	if err := r.sendSubscriberNotice(ctx, target, renderCodingJobReport(job)); err != nil {
		r.setError(err.Error())
		return
	}
	job.Reported = true
	if err := saveCodingJob(job); err != nil {
		r.setError(err.Error())
	}
}

func renderCodingJobReport(job CodingJob) string {
	var builder strings.Builder
	switch job.Status {
	case codingJobStatusSucceeded:
		builder.WriteString(fmt.Sprintf("编码任务 %s 完成（工作区 %s）", job.ID, job.Workspace))
	case codingJobStatusInterrupted:
		builder.WriteString(fmt.Sprintf("编码任务 %s 被中断（工作区 %s）", job.ID, job.Workspace))
	case codingJobStatusCancelled:
		builder.WriteString(fmt.Sprintf("编码任务 %s 已取消（工作区 %s）", job.ID, job.Workspace))
	default:
		builder.WriteString(fmt.Sprintf("编码任务 %s 失败（工作区 %s）", job.ID, job.Workspace))
	}
	if !job.StartedAt.IsZero() && !job.FinishedAt.IsZero() {
		builder.WriteString("，耗时 " + formatCodingDuration(job.FinishedAt.Sub(job.StartedAt)))
	}
	builder.WriteString("\n指令：" + truncateRunes(job.Instruction, 200))
	if job.Error != "" {
		builder.WriteString("\n问题：" + truncateRunes(job.Error, 400))
	}
	if job.Result != "" && job.Result != job.Error {
		builder.WriteString("\n结果：\n" + job.Result)
	}
	if job.Turns > 0 {
		builder.WriteString(fmt.Sprintf("\n模型轮次 %d", job.Turns))
		if job.CostUSD > 0 {
			builder.WriteString(fmt.Sprintf("，花费 $%.2f", job.CostUSD))
		}
	}
	return builder.String()
}

func formatCodingDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%d 秒", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%d 分钟", int(d.Minutes()))
	}
	return fmt.Sprintf("%d 小时 %d 分钟", int(d.Hours()), int(d.Minutes())%60)
}

// cancelCodingJob 终止一个在跑的任务。先改记录再杀进程：watch 那头看到进程消失会
// 去读记录收尾，顺序反过来会被它当成正常结束。
func (r *Runtime) cancelCodingJob(ctx context.Context, id string) (CodingJob, error) {
	job, err := loadCodingJob(id)
	if err != nil {
		return CodingJob{}, fmt.Errorf("找不到任务 %s", id)
	}
	if job.finished() {
		return job, fmt.Errorf("任务 %s 已经是 %s 状态", id, job.Status)
	}
	snapshot := parseCodingLog(job.LogPath)
	job.Status = codingJobStatusCancelled
	job.FinishedAt = time.Now()
	job.Error = "已按要求取消，已完成的改动留在工作区里"
	job.Result = truncateRunes(strings.TrimSpace(snapshot.Result), codingJobResultRunes)
	if snapshot.SessionID != "" {
		job.SessionID = snapshot.SessionID
	}
	// 取消是用户当面下的指令，工具返回值就是回执，不必再推一条汇报。
	job.Reported = true
	if err := saveCodingJob(job); err != nil {
		return CodingJob{}, err
	}
	if job.PID > 0 {
		killCodingProcess(job.PID)
	}
	r.codingJobs().release(job.Workspace, job.ID)
	r.recordCodingJobLog(ctx, job, applog.KindOperation, applog.LevelInfo, "编码任务已取消", job.Instruction)
	return job, nil
}

// ResumeCodingJobs 在启动时接回上次留下的编码任务：进程还活着就继续盯，已经没了
// 就按日志收尾并把欠下的汇报补上。
func (r *Runtime) ResumeCodingJobs(ctx context.Context) {
	for _, job := range listCodingJobs() {
		if job.finished() {
			if !job.Reported {
				r.reportCodingJob(ctx, job)
			}
			continue
		}
		registry := r.codingJobs()
		alive := job.PID > 0 && codingProcessAlive(job.PID)
		if alive {
			snapshot := parseCodingLog(job.LogPath)
			// PID 会被复用，光判活会把别人的进程当成这个任务。日志安静太久就
			// 不再认这个 PID。
			if !snapshot.UpdatedAt.IsZero() && time.Since(snapshot.UpdatedAt) > codingJobStaleAfter {
				alive = false
			}
		}
		if !alive {
			r.finalizeCodingJob(ctx, job, false, true)
			continue
		}
		if err := registry.claim(job.Workspace, job.ID, 0); err != nil {
			continue
		}
		if !registry.beginWatch(job.ID) {
			continue
		}
		go func() {
			defer recoverGoroutinePanic("coding.watchJob")
			r.watchCodingJob(job, nil, 0)
		}()
	}
}

func (r *Runtime) recordCodingJobLog(ctx context.Context, job CodingJob, kind applog.Kind, level applog.Level, message, detail string) {
	writer := r.appLogWriter()
	if writer == nil {
		return
	}
	logCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	_ = writer.AppendLog(logCtx, applog.Entry{
		Kind:    kind,
		Level:   level,
		Action:  "diana.coding",
		Message: message,
		Detail:  detail,
		Actor:   oneBotEventActor(job.Target.event()),
		Target:  job.ID,
		Metadata: map[string]any{
			"backend":   job.Backend,
			"workspace": job.Workspace,
			"status":    job.Status,
			"session":   job.SessionID,
		},
	})
}

// codingLogTail 返回日志尾部若干行，供运行途中查询用。
func codingLogTail(job CodingJob, lines int) []string {
	if lines <= 0 || lines > codingJobTailLines {
		lines = codingJobTailLines
	}
	tail := parseCodingLog(job.LogPath).Tail
	if len(tail) > lines {
		tail = tail[len(tail)-lines:]
	}
	return tail
}
