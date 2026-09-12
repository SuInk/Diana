// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/SuInk/diana/model/applog"
)

const (
	// codingApprovalMode* 决定哪些操作要问过主人才能做。
	codingApprovalModeOff       = "off"
	codingApprovalModeDangerous = "dangerous"
	codingApprovalModeAllWrites = "all_writes"

	defaultCodingApprovalTimeoutMinutes = 10
	maxCodingApprovalTimeoutMinutes     = 120

	codingApprovalCodeLength = 6
	codingApprovalPollWait   = time.Second
)

// defaultCodingApprovalPatterns 是「危险操作」默认盯的命令片段。
//
// 收进来的标准是：做完就收不回来，或者影响跑到工作区外面去。改本地文件、跑测试、
// 本地提交都不在其中——那些是派活时就默许的事，每一步都问一遍等于这个功能没法用。
func defaultCodingApprovalPatterns() []string {
	return []string{
		"git push",
		"git reset --hard",
		"git clean -f",
		"git tag -d",
		"gh pr merge",
		"gh pr create",
		"gh release",
		"npm publish",
		"docker push",
		"rm -rf",
		"sudo",
	}
}

// codingApprovalRequest 是 hook 进程写给 Diana 的一次询问。
type codingApprovalRequest struct {
	ID        string    `json:"id"`
	JobID     string    `json:"job_id"`
	Tool      string    `json:"tool"`
	Detail    string    `json:"detail"`
	CreatedAt time.Time `json:"created_at"`
}

// codingApprovalResponse 是 Diana 写回去的裁决。
type codingApprovalResponse struct {
	Allow  bool   `json:"allow"`
	Reason string `json:"reason,omitempty"`
}

// codingApprovalPolicy 是 hook 进程要用的判断依据。它落成文件而不是塞进命令行：
// 模式列表是用户可配的多行文本，进 argv 只会在引号上出问题。
type codingApprovalPolicy struct {
	JobID          string   `json:"job_id"`
	Mode           string   `json:"mode"`
	Patterns       []string `json:"patterns"`
	ApprovalDir    string   `json:"approval_dir"`
	TimeoutSeconds int      `json:"timeout_seconds"`
}

func codingApprovalDir() string {
	return filepath.Join(codingJobRecordDir(), "approvals")
}

func codingApprovalPolicyPath(jobID string) string {
	return filepath.Join(codingJobRecordDir(), jobID+".hook.json")
}

func codingApprovalRequestPath(dir, id string) string {
	return filepath.Join(dir, id+".req")
}

func codingApprovalResponsePath(dir, id string) string {
	return filepath.Join(dir, id+".res")
}

// codingApprovalCodes 派生这次询问的放行码和拒绝码。
//
// 用确认码而不是认「同意」「可以」这类词：判断只剩一次结构匹配，不涉及任何语义
// 推断，而且码只出现在主人自己那条消息里——网页正文、工具输出或者别人的发言即使
// 写着「同意」也放行不了任何东西。这和扩展变更确认码是同一个思路。
func codingApprovalCodes(request codingApprovalRequest) (string, string) {
	seed := strings.Join([]string{request.ID, request.JobID, request.Tool, request.Detail}, "\x00")
	allow := sha256.Sum256([]byte("allow\x00" + seed))
	deny := sha256.Sum256([]byte("deny\x00" + seed))
	return hex.EncodeToString(allow[:])[:codingApprovalCodeLength],
		hex.EncodeToString(deny[:])[:codingApprovalCodeLength]
}

type codingApprovalWait struct {
	request   codingApprovalRequest
	allowCode string
	denyCode  string
	decided   chan codingApprovalResponse
}

func (g *codingJobRegistry) registerApproval(wait *codingApprovalWait) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.approvals == nil {
		g.approvals = map[string]*codingApprovalWait{}
	}
	g.approvals[wait.allowCode] = wait
	g.approvals[wait.denyCode] = wait
}

func (g *codingJobRegistry) resolveApproval(code string) (*codingApprovalWait, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	wait, ok := g.approvals[code]
	if !ok {
		return nil, false
	}
	delete(g.approvals, wait.allowCode)
	delete(g.approvals, wait.denyCode)
	return wait, true
}

func (g *codingJobRegistry) dropApproval(wait *codingApprovalWait) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.approvals, wait.allowCode)
	delete(g.approvals, wait.denyCode)
}

func (g *codingJobRegistry) pendingApprovals() []codingApprovalRequest {
	g.mu.Lock()
	defer g.mu.Unlock()
	seen := map[string]bool{}
	out := make([]codingApprovalRequest, 0, len(g.approvals))
	for _, wait := range g.approvals {
		if seen[wait.request.ID] {
			continue
		}
		seen[wait.request.ID] = true
		out = append(out, wait.request)
	}
	return out
}

func (g *codingJobRegistry) approvalPending(id string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, wait := range g.approvals {
		if wait.request.ID == id {
			return true
		}
	}
	return false
}

// watchCodingApprovals 盯着信箱目录，把 hook 进程的询问转成一条发给主人的消息。
//
// 用目录轮询而不是本地端口：任务记录和日志本来就在这个目录里，信箱放在一起不引入
// 端口、令牌和鉴权，而且 Diana 在主人还没回复时重启也不丢——hook 进程还在等，请求
// 文件还在，下次启动继续接着问。
func (r *Runtime) watchCodingApprovals(ctx context.Context, job CodingJob, timeout time.Duration) {
	dir := codingApprovalDir()
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(codingApprovalPollWait):
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".req") {
				continue
			}
			id := strings.TrimSuffix(entry.Name(), ".req")
			if r.codingJobs().approvalPending(id) {
				continue
			}
			if _, err := os.Stat(codingApprovalResponsePath(dir, id)); err == nil {
				continue
			}
			body, err := os.ReadFile(codingApprovalRequestPath(dir, id))
			if err != nil {
				continue
			}
			var request codingApprovalRequest
			if err := json.Unmarshal(body, &request); err != nil || request.JobID != job.ID {
				continue
			}
			// 登记放在轮询这一轮里同步做完，再交给协程去问：登记如果留在协程
			// 里，下一轮轮询可能在它跑起来之前又看到同一个请求，主人就会收到
			// 两条一样的询问。
			allowCode, denyCode := codingApprovalCodes(request)
			wait := &codingApprovalWait{
				request:   request,
				allowCode: allowCode,
				denyCode:  denyCode,
				decided:   make(chan codingApprovalResponse, 1),
			}
			r.codingJobs().registerApproval(wait)
			go func() {
				defer recoverGoroutinePanic("coding.askApproval")
				r.askCodingApproval(ctx, job, wait, timeout)
			}()
		}
	}
}

// askCodingApproval 把一次询问发给主人并等他回码。等不到就拒绝：让一个待确认的
// 危险操作因为没人看见而默认放行，比让任务失败糟得多。
func (r *Runtime) askCodingApproval(ctx context.Context, job CodingJob, wait *codingApprovalWait, timeout time.Duration) {
	request := wait.request
	allowCode, denyCode := wait.allowCode, wait.denyCode
	defer r.codingJobs().dropApproval(wait)

	message := fmt.Sprintf(
		"编码任务 %s（工作区 %s）要执行一个需要你点头的操作：\n%s\n\n同意就回 %s，不同意回 %s。%s内没人回我就当拒绝。",
		job.ID, job.Workspace, codingApprovalDetail(request),
		allowCode, denyCode, formatCodingDuration(timeout),
	)
	if err := r.sendSubscriberNotice(ctx, job.Target.event(), message); err != nil {
		r.setError(err.Error())
		// 问都问不出去就别让 CLI 一直干等：它等到 hook 超时才会知道出了事。
		writeCodingApprovalResponse(request, codingApprovalResponse{Reason: "无法把确认请求发给主人：" + err.Error()})
		return
	}
	r.recordCodingJobLog(ctx, job, applog.KindOperation, applog.LevelInfo, "编码任务等待主人确认", codingApprovalDetail(request))

	select {
	case decision := <-wait.decided:
		writeCodingApprovalResponse(request, decision)
	case <-time.After(timeout):
		writeCodingApprovalResponse(request, codingApprovalResponse{Reason: "等待确认超时"})
	case <-ctx.Done():
		// 任务已经结束或者 Diana 要退出了，不写裁决：请求文件留在信箱里，
		// 下次启动如果 CLI 还在等就继续问。
	}
}

func codingApprovalDetail(request codingApprovalRequest) string {
	detail := strings.TrimSpace(request.Detail)
	if detail == "" {
		return request.Tool
	}
	return request.Tool + "：" + truncateRunes(detail, 400)
}

func writeCodingApprovalResponse(request codingApprovalRequest, response codingApprovalResponse) {
	dir := codingApprovalDir()
	body, err := json.Marshal(response)
	if err != nil {
		return
	}
	path := codingApprovalResponsePath(dir, request.ID)
	// 和任务记录一样先写临时文件再改名：hook 进程在轮询这个路径，读到半截 JSON
	// 会把它当成解析失败。
	temp := path + ".tmp"
	if err := os.WriteFile(temp, body, 0o600); err != nil {
		return
	}
	_ = os.Rename(temp, path)
}

// handleCodingApprovalReply 认出主人回过来的确认码。匹配是结构性的：码必须作为一
// 个独立片段出现，前后不接同类字符，避免被更长的哈希串意外命中。
func (r *Runtime) handleCodingApprovalReply(event MessageEvent, text string) (string, bool) {
	if r == nil || r.codingJobRegistry == nil {
		return "", false
	}
	pending := r.codingJobs().pendingApprovals()
	if len(pending) == 0 {
		return "", false
	}
	lowered := strings.ToLower(text)
	for _, request := range pending {
		allowCode, denyCode := codingApprovalCodes(request)
		for _, item := range []struct {
			code  string
			allow bool
		}{{allowCode, true}, {denyCode, false}} {
			if !containsStandaloneCode(lowered, item.code) {
				continue
			}
			wait, ok := r.codingJobs().resolveApproval(item.code)
			if !ok {
				continue
			}
			decision := codingApprovalResponse{Allow: item.allow}
			if !item.allow {
				decision.Reason = "主人拒绝了这个操作"
			}
			select {
			case wait.decided <- decision:
			default:
				// 已经有裁决了（超时或者重复回复），不覆盖。
				return "这个确认已经处理过了。", true
			}
			if item.allow {
				return fmt.Sprintf("好，任务 %s 继续。", request.JobID), true
			}
			return fmt.Sprintf("已拒绝，任务 %s 会绕过这一步或者报错收尾。", request.JobID), true
		}
	}
	return "", false
}

// containsStandaloneCode 判断确认码是否作为独立片段出现在文本里。
func containsStandaloneCode(lowered, code string) bool {
	if code == "" {
		return false
	}
	for offset := 0; ; {
		index := strings.Index(lowered[offset:], code)
		if index < 0 {
			return false
		}
		start := offset + index
		end := start + len(code)
		if (start == 0 || !isCodeRune(rune(lowered[start-1]))) &&
			(end == len(lowered) || !isCodeRune(rune(lowered[end]))) {
			return true
		}
		offset = start + 1
		if offset >= len(lowered) {
			return false
		}
	}
}

func isCodeRune(r rune) bool {
	return (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
}

// prepareCodingApproval 为一次任务写好 hook 需要的设置和策略文件，返回要传给
// CLI 的 --settings 路径。审批关闭时返回空串，模板里的 {{settings}} 会被丢掉。
func prepareCodingApproval(cfg codingAgentConfig, jobID string) (string, error) {
	if cfg.ApprovalMode == codingApprovalModeOff {
		return "", nil
	}
	if !cfg.StreamJSON {
		// 审批靠 Claude Code 的 PreToolUse hook 实现。别的后端没有等价机制，
		// 这时候安静地不审批等于骗人，直接说清楚。
		return "", fmt.Errorf("聊天里确认只支持 Claude Code 后端，当前后端是 %s；要么换后端，要么把审批模式设成关闭", cfg.Backend)
	}
	if err := os.MkdirAll(codingApprovalDir(), 0o700); err != nil {
		return "", err
	}
	executable, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("找不到 Diana 自己的可执行文件，无法安装审批 hook：%w", err)
	}
	timeout := int(cfg.ApprovalTimeout / time.Second)
	policy := codingApprovalPolicy{
		JobID:          jobID,
		Mode:           cfg.ApprovalMode,
		Patterns:       cfg.ApprovalPatterns,
		ApprovalDir:    codingApprovalDir(),
		TimeoutSeconds: timeout,
	}
	policyPath := codingApprovalPolicyPath(jobID)
	policyBody, err := json.MarshalIndent(policy, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(policyPath, policyBody, 0o600); err != nil {
		return "", err
	}
	settings := map[string]any{
		"hooks": map[string]any{
			"PreToolUse": []any{
				map[string]any{
					"matcher": codingApprovalMatcher(cfg.ApprovalMode),
					"hooks": []any{
						map[string]any{
							"type":    "command",
							"command": shellQuote(executable) + " " + CodingApprovalHookCommand + " " + shellQuote(policyPath),
							// hook 自己的超时必须比等主人的时间长，否则 CLI 会在
							// 主人还没回复的时候就把 hook 掐掉。
							"timeout": timeout + 60,
						},
					},
				},
			},
		},
	}
	settingsPath := filepath.Join(codingJobRecordDir(), jobID+".settings.json")
	settingsBody, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(settingsPath, settingsBody, 0o600); err != nil {
		return "", err
	}
	return settingsPath, nil
}

// codingApprovalMatcher 决定 hook 挂在哪些工具上。危险操作只可能从命令里发出来，
// 挂在 Bash 上就够；全部写操作要连改文件一起拦。
func codingApprovalMatcher(mode string) string {
	if mode == codingApprovalModeAllWrites {
		return "Bash|Write|Edit|MultiEdit|NotebookEdit"
	}
	return "Bash"
}

// shellQuote 因为 hook 的 command 是交给 shell 跑的：路径里有空格时不引就断成两段。
func shellQuote(value string) string {
	if value == "" {
		return "''"
	}
	if !strings.ContainsAny(value, " \t\n\"'\\$`&;|<>()*?[]{}#~!") {
		return value
	}
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

func normalizeCodingApprovalMode(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case codingApprovalModeOff:
		return codingApprovalModeOff
	case codingApprovalModeAllWrites:
		return codingApprovalModeAllWrites
	}
	return codingApprovalModeDangerous
}

// parseCodingApprovalPatterns 解析用户配的命令片段，一行一个。留空用默认列表：
// 审批开着却没有任何模式命中，等于以为开了其实全放行。
func parseCodingApprovalPatterns(raw string) []string {
	lines := strings.FieldsFunc(raw, func(r rune) bool { return r == '\n' || r == '\r' })
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out = append(out, strings.ToLower(line))
	}
	if len(out) == 0 {
		return defaultCodingApprovalPatterns()
	}
	return out
}
