// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/google/uuid"
)

// CodingACPSessionCommand 是 ACP 会话进程的内部子命令名。
//
// ACP 的传输是 stdio：客户端一退出，代理这一轮就没了。编码任务要扛得住 Diana 重启，
// 所以不让 Diana 自己当客户端，而是拉起这个脱离进程组的会话进程来当。在 Diana 眼里
// 它就是一个编码 CLI：按 PID 接回、按日志收尾、取消和超时发信号，全都照旧。设计见
// docs/coding-agent-acp.md。
const CodingACPSessionCommand = "__coding-acp"

const (
	codingACPProtocolVersion = 1
	codingACPInitTimeout     = time.Minute
	codingACPSessionTimeout  = 2 * time.Minute
	// codingACPCancelGrace 是取消后等代理回 stopReason 的时间。Diana 发 SIGTERM 之后
	// 5 秒就会 SIGKILL 整个进程组，这里要赶在那之前写完结果行。
	codingACPCancelGrace = 3 * time.Second
	codingACPExitGrace   = 2 * time.Second
	// codingACPMaxMessageBytes 是单条 ACP 消息的上限。工具调用里可能带着整段 diff，
	// 放宽到几十兆，再大的整条丢掉，不让一条消息吃光内存。
	codingACPMaxMessageBytes = 64 << 20
	codingACPStderrTailBytes = 4 << 10
	codingACPDetailRunes     = 300
	// codingACPExitCancelled 是被取消时的退出码，和 shell 被 SIGINT 打断的惯例一致。
	codingACPExitCancelled = 130

	codingACPErrMethodNotFound = -32601
	// codingACPErrAuthRequired 是 ACP 约定的「需要先认证」错误码。
	codingACPErrAuthRequired = -32000
)

// codingACPSpec 是会话进程的启动参数。写成文件不走 argv：指令可能很长，argv 有长度
// 上限；放进任务目录也方便事后核对这个任务到底是怎么启动的。
type codingACPSpec struct {
	JobID       string   `json:"job_id"`
	Command     string   `json:"command"`
	Args        []string `json:"args,omitempty"`
	Dir         string   `json:"dir"`
	Instruction string   `json:"instruction"`
	// PolicyPath 是审批策略文件，和 Claude Code hook 用的是同一份。为空表示不审批。
	PolicyPath string `json:"policy_path,omitempty"`
}

func codingACPSpecPath(jobID string) string {
	return filepath.Join(codingJobRecordDir(), jobID+".acp.json")
}

// codingACPCommand 写好启动参数，返回要以编码 CLI 身份拉起的会话进程命令。
func codingACPCommand(cfg codingAgentConfig, jobID string, workspace codingWorkspace, instruction, policyPath string) (*exec.Cmd, error) {
	executable, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("找不到 Diana 自己的可执行文件，无法启动 ACP 会话：%w", err)
	}
	spec := codingACPSpec{
		JobID:   jobID,
		Command: cfg.Command,
		// 指令走 session/prompt，参数里只剩代理自己的开关，比如 --acp。
		Args:        buildCodingArgs(cfg.Template, map[string]string{"model": cfg.Model, "workspace": workspace.Dir}),
		Dir:         workspace.Dir,
		Instruction: instruction,
		PolicyPath:  policyPath,
	}
	body, err := json.MarshalIndent(spec, "", "  ")
	if err != nil {
		return nil, err
	}
	path := codingACPSpecPath(jobID)
	if err := os.WriteFile(path, body, 0o600); err != nil {
		return nil, err
	}
	return exec.Command(executable, CodingACPSessionCommand, path), nil
}

// RunCodingACPSession 是会话进程的入口，stdout 就是任务日志。
func RunCodingACPSession(specPath string, stdout, stderr io.Writer) int {
	log := &codingACPLog{w: stdout}
	body, err := os.ReadFile(strings.TrimSpace(specPath))
	if err != nil {
		log.fail("读不到 ACP 会话参数：" + err.Error())
		return 1
	}
	var spec codingACPSpec
	if err := json.Unmarshal(body, &spec); err != nil {
		log.fail("ACP 会话参数格式不对：" + err.Error())
		return 1
	}
	// 取消和超时都是 Diana 对整个进程组发 SIGTERM：这里接住它，先让代理按协议
	// 停下来并交代结果，再退出。
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()
	return runCodingACPSession(ctx, spec, log)
}

func runCodingACPSession(ctx context.Context, spec codingACPSpec, log *codingACPLog) int {
	cmd := exec.Command(spec.Command, spec.Args...)
	cmd.Dir = spec.Dir
	stdin, err := cmd.StdinPipe()
	if err != nil {
		log.fail("启动 ACP 代理失败：" + err.Error())
		return 1
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		log.fail("启动 ACP 代理失败：" + err.Error())
		return 1
	}
	// 代理的 stderr 不进任务日志：各家都往里打一堆启动信息，混进进度只是噪音。只留
	// 最后一段，出错时附在错误说明后面。
	stderrTail := &codingTailBuffer{limit: codingACPStderrTailBytes}
	cmd.Stderr = stderrTail
	if err := cmd.Start(); err != nil {
		log.fail(fmt.Sprintf("启动 ACP 代理 %s 失败：%v", spec.Command, err))
		return 1
	}
	conn := newCodingACPConn(stdin)
	session := &codingACPSession{spec: spec, log: log, conn: conn, ctx: ctx, stderr: stderrTail, tools: map[string]*codingACPTool{}}
	conn.onNotify = session.handleNotification
	conn.onRequest = session.handleRequest
	go conn.readLoop(stdout)
	exited := make(chan struct{})
	go func() {
		// 管道要等读完再 Wait：Wait 会关掉 stdout，读到一半的消息就丢了。
		<-conn.closed
		_ = cmd.Wait()
		close(exited)
	}()
	code := session.run()
	_ = stdin.Close()
	select {
	case <-exited:
	case <-time.After(codingACPExitGrace):
		_ = cmd.Process.Kill()
		<-exited
	}
	return code
}

// codingACPSession 是一次会话的状态：把 ACP 的流式更新规整成日志里的事件，并记下
// 最终结果该取哪一段。
type codingACPSession struct {
	spec      codingACPSpec
	log       *codingACPLog
	conn      *codingACPConn
	ctx       context.Context
	stderr    *codingTailBuffer
	sessionID string
	cancelled atomic.Bool

	mu      sync.Mutex
	message strings.Builder
	// sinceTool 是最后一次工具调用之后说的话：代理的结论总在收工前最后说，前面边干
	// 边说的属于进度。
	sinceTool   []string
	lastMessage string
	tools       map[string]*codingACPTool
}

type codingACPTool struct {
	Title  string
	Kind   string
	Status string
	Detail string
}

func (s *codingACPSession) run() int {
	var initialized struct {
		AgentCapabilities struct {
			LoadSession         bool `json:"loadSession"`
			SessionCapabilities struct {
				Resume *json.RawMessage `json:"resume"`
			} `json:"sessionCapabilities"`
		} `json:"agentCapabilities"`
		AuthMethods []codingACPAuthMethod `json:"authMethods"`
		AgentInfo   struct {
			Name string `json:"name"`
		} `json:"agentInfo"`
	}
	initCtx, cancelInit := context.WithTimeout(s.ctx, codingACPInitTimeout)
	err := s.conn.call(initCtx, "initialize", map[string]any{
		"protocolVersion": codingACPProtocolVersion,
		// 不声明 fs 和 terminal：代理用自己的工具在工作区里干活，Diana 不替它读写文件。
		"clientCapabilities": map[string]any{
			"fs":       map[string]any{"readTextFile": false, "writeTextFile": false},
			"terminal": false,
		},
		"clientInfo": map[string]any{"name": "diana"},
	}, &initialized)
	cancelInit()
	if err != nil {
		return s.abort("ACP 代理没有完成握手", err)
	}
	sessionID, err := s.newSession(initialized.AuthMethods)
	if err != nil {
		return s.abort("ACP 代理建不了会话", err)
	}
	s.sessionID = sessionID
	s.log.event(map[string]any{
		"type":       "acp.session",
		"session_id": sessionID,
		"agent":      initialized.AgentInfo.Name,
		"can_load":   initialized.AgentCapabilities.LoadSession,
		"can_resume": initialized.AgentCapabilities.SessionCapabilities.Resume != nil,
	})

	type promptOutcome struct {
		stopReason string
		err        error
	}
	done := make(chan promptOutcome, 1)
	go func() {
		var result struct {
			StopReason string `json:"stopReason"`
		}
		// prompt 不设期限：单次最长运行时间由 Diana 管，到点会发 SIGTERM 过来。
		err := s.conn.call(context.Background(), "session/prompt", map[string]any{
			"sessionId": sessionID,
			"prompt":    []any{map[string]any{"type": "text", "text": s.spec.Instruction}},
		}, &result)
		done <- promptOutcome{stopReason: result.StopReason, err: err}
	}()
	select {
	case outcome := <-done:
		if outcome.err != nil {
			return s.abort("ACP 代理执行出错", outcome.err)
		}
		return s.finish(outcome.stopReason)
	case <-s.ctx.Done():
		s.cancelled.Store(true)
		_ = s.conn.notify("session/cancel", map[string]any{"sessionId": sessionID})
		select {
		case <-done:
		case <-time.After(codingACPCancelGrace):
		}
		s.writeResult("cancelled", s.result(), true)
		return codingACPExitCancelled
	}
}

type codingACPAuthMethod struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// newSession 建会话。代理要求先认证时，只试不需要人在场的方式（按名字认 API 密钥、
// 环境变量这类），浏览器登录在无人值守的会话里走不通，直接说清楚要去哪里登录。
func (s *codingACPSession) newSession(methods []codingACPAuthMethod) (string, error) {
	create := func() (string, error) {
		var created struct {
			SessionID string `json:"sessionId"`
		}
		ctx, cancel := context.WithTimeout(s.ctx, codingACPSessionTimeout)
		defer cancel()
		err := s.conn.call(ctx, "session/new", map[string]any{"cwd": s.spec.Dir, "mcpServers": []any{}}, &created)
		if err == nil && strings.TrimSpace(created.SessionID) == "" {
			err = errors.New("代理没有返回会话 ID")
		}
		return created.SessionID, err
	}
	id, err := create()
	var rpcErr *codingACPRPCError
	if err == nil || !errors.As(err, &rpcErr) || rpcErr.Code != codingACPErrAuthRequired {
		return id, err
	}
	for _, method := range methods {
		if !codingACPUnattendedAuth(method) {
			continue
		}
		ctx, cancel := context.WithTimeout(s.ctx, codingACPSessionTimeout)
		authErr := s.conn.call(ctx, "authenticate", map[string]any{"methodId": method.ID}, nil)
		cancel()
		if authErr != nil {
			continue
		}
		if id, err = create(); err == nil {
			return id, nil
		}
	}
	names := make([]string, 0, len(methods))
	for _, method := range methods {
		names = append(names, firstNonEmpty(method.Name, method.ID))
	}
	return "", fmt.Errorf("代理需要先登录（可用方式：%s）。在运行 Diana 的环境里完成登录，或配好代理自己的 API 密钥环境变量：%w",
		firstNonEmpty(strings.Join(names, "、"), "代理没有列出"), err)
}

func codingACPUnattendedAuth(method codingACPAuthMethod) bool {
	id := strings.ToLower(method.ID)
	if strings.Contains(id, "oauth") || strings.Contains(id, "login") || strings.Contains(id, "browser") {
		return false
	}
	return strings.Contains(id, "key") || strings.Contains(id, "env") || strings.Contains(id, "token")
}

// finish 按代理给的停止原因写结果行。拒绝、超长、轮数到顶都算没做完，汇报按失败说。
func (s *codingACPSession) finish(stopReason string) int {
	result := s.result()
	isError := stopReason != "end_turn"
	if isError {
		reason := codingACPStopReasonText(stopReason)
		if result == "" {
			result = reason
		} else {
			result += "\n\n（" + reason + "）"
		}
	}
	s.writeResult(stopReason, result, isError)
	return 0
}

func codingACPStopReasonText(stopReason string) string {
	switch stopReason {
	case "refusal":
		return "代理拒绝继续执行"
	case "max_tokens":
		return "代理输出达到长度上限，没有说完"
	case "max_turn_requests":
		return "代理达到单轮请求次数上限，没有做完"
	case "cancelled":
		return "代理自己取消了这一轮"
	}
	return "代理停止：" + firstNonEmpty(stopReason, "没有给出原因")
}

func (s *codingACPSession) writeResult(stopReason, result string, isError bool) {
	s.log.event(map[string]any{"type": "acp.result", "stop_reason": stopReason, "result": result, "is_error": isError})
}

// abort 写一条错误并以失败退出。代理自己先挂了的话，它的 stderr 最后一段往往就是
// 原因，附在后面。
func (s *codingACPSession) abort(what string, err error) int {
	if s.ctx.Err() != nil {
		s.writeResult("cancelled", s.result(), true)
		return codingACPExitCancelled
	}
	s.flushMessage()
	message := what + "：" + err.Error()
	if tail := strings.TrimSpace(s.stderr.String()); tail != "" {
		message += "\n代理输出：" + tail
	}
	s.log.fail(message)
	return 1
}

func (s *codingACPSession) result() string {
	s.flushMessage()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.sinceTool) > 0 {
		return strings.Join(s.sinceTool, "\n\n")
	}
	return s.lastMessage
}

// flushMessage 把攒着的消息分片落成一行。代理一个字一个字地流式发，原样写进日志会
// 把日志撑大，进度里也看不出一句完整的话。
func (s *codingACPSession) flushMessage() {
	s.mu.Lock()
	text := strings.TrimSpace(s.message.String())
	s.message.Reset()
	if text != "" {
		s.sinceTool = append(s.sinceTool, text)
		s.lastMessage = text
	}
	s.mu.Unlock()
	if text != "" {
		s.log.event(map[string]any{"type": "acp.message", "text": text})
	}
}

type codingACPToolCall struct {
	ToolCallID string         `json:"toolCallId"`
	Title      string         `json:"title"`
	Kind       string         `json:"kind"`
	Status     string         `json:"status"`
	RawInput   map[string]any `json:"rawInput"`
	Locations  []struct {
		Path string `json:"path"`
	} `json:"locations"`
}

func (s *codingACPSession) handleNotification(method string, params json.RawMessage) {
	if method != "session/update" {
		return
	}
	var payload struct {
		Update json.RawMessage `json:"update"`
	}
	if json.Unmarshal(params, &payload) != nil {
		return
	}
	var head struct {
		SessionUpdate string `json:"sessionUpdate"`
		Content       struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		Entries []struct {
			Status string `json:"status"`
		} `json:"entries"`
	}
	if json.Unmarshal(payload.Update, &head) != nil {
		return
	}
	switch head.SessionUpdate {
	case "agent_message_chunk":
		if head.Content.Type == "text" && head.Content.Text != "" {
			s.mu.Lock()
			s.message.WriteString(head.Content.Text)
			s.mu.Unlock()
		}
	case "tool_call", "tool_call_update":
		var call codingACPToolCall
		if json.Unmarshal(payload.Update, &call) != nil {
			return
		}
		s.flushMessage()
		s.trackTool(call, head.SessionUpdate == "tool_call")
	case "plan":
		completed := 0
		for _, entry := range head.Entries {
			if entry.Status == "completed" {
				completed++
			}
		}
		s.flushMessage()
		s.log.event(map[string]any{"type": "acp.plan", "total": len(head.Entries), "completed": completed})
	}
	// agent_thought_chunk 是模型的思考过程，既不是进度也不是结论，不落日志。
}

// trackTool 合并工具调用的状态，只在状态变化时落一行：同一个调用的输出分片会反复
// 发 tool_call_update，每次都写就是刷屏。
func (s *codingACPSession) trackTool(call codingACPToolCall, started bool) {
	s.mu.Lock()
	tool, ok := s.tools[call.ToolCallID]
	if !ok {
		tool = &codingACPTool{}
		s.tools[call.ToolCallID] = tool
	}
	if started {
		// 新的工具调用开始，之前说的话都算进度，结论要等它后面再说。
		s.sinceTool = nil
	}
	changed := !ok
	if call.Title != "" && call.Title != tool.Title {
		tool.Title = call.Title
	}
	if call.Kind != "" {
		tool.Kind = call.Kind
	}
	if detail := codingACPToolDetail(call); detail != "" {
		tool.Detail = detail
	}
	if call.Status != "" && call.Status != tool.Status {
		tool.Status = call.Status
		changed = true
	}
	snapshot := *tool
	s.mu.Unlock()
	if changed {
		s.log.event(map[string]any{
			"type": "acp.tool", "id": call.ToolCallID, "title": snapshot.Title, "started": !ok,
			"kind": snapshot.Kind, "status": snapshot.Status, "detail": snapshot.Detail,
		})
	}
}

func codingACPToolDetail(call codingACPToolCall) string {
	if len(call.RawInput) > 0 {
		if detail := codingHookDetail(codingHookPayload{ToolInput: call.RawInput}); detail != "" {
			return truncateRunes(detail, codingACPDetailRunes)
		}
	}
	for _, location := range call.Locations {
		if path := strings.TrimSpace(location.Path); path != "" {
			return path
		}
	}
	return ""
}

func (s *codingACPSession) handleRequest(method string, params json.RawMessage) (any, *codingACPRPCError) {
	if method == "session/request_permission" {
		return s.requestPermission(params), nil
	}
	// fs/*、terminal/* 这些客户端能力握手时就没声明，代理照理不该来问；真来了按
	// 「没有这个方法」回，让它退回用自己的工具。
	return nil, &codingACPRPCError{Code: codingACPErrMethodNotFound, Message: "diana 不提供 " + method}
}

type codingACPPermissionOption struct {
	OptionID string `json:"optionId"`
	Kind     string `json:"kind"`
}

// codingACPReadOnlyKinds 是不改东西的工具种类，「所有写操作」档也不拿它们去问主人。
var codingACPReadOnlyKinds = map[string]bool{"read": true, "search": true, "think": true, "fetch": true}

// requestPermission 把代理的授权请求映射到 Diana 的审批：要不要问、问谁、怎么回，
// 全照 Claude Code hook 那套来，信箱和放行码都是同一份。
func (s *codingACPSession) requestPermission(params json.RawMessage) any {
	var request struct {
		ToolCall codingACPToolCall           `json:"toolCall"`
		Options  []codingACPPermissionOption `json:"options"`
	}
	_ = json.Unmarshal(params, &request)
	if s.cancelled.Load() {
		return codingACPPermissionCancelled()
	}
	s.mu.Lock()
	known := s.tools[request.ToolCall.ToolCallID]
	s.mu.Unlock()
	title, kind, detail := request.ToolCall.Title, request.ToolCall.Kind, codingACPToolDetail(request.ToolCall)
	if known != nil {
		title, kind, detail = firstNonEmpty(title, known.Title), firstNonEmpty(kind, known.Kind), firstNonEmpty(detail, known.Detail)
	}
	title = firstNonEmpty(title, kind, "工具调用")

	allow := s.decidePermission(title, kind, detail)
	if s.cancelled.Load() {
		return codingACPPermissionCancelled()
	}
	option := codingACPPickOption(request.Options, allow)
	if option == "" {
		return codingACPPermissionCancelled()
	}
	return map[string]any{"outcome": map[string]any{"outcome": "selected", "optionId": option}}
}

func codingACPPermissionCancelled() any {
	return map[string]any{"outcome": map[string]any{"outcome": "cancelled"}}
}

// decidePermission 决定放不放行，要问主人时在这里等回复。等待期间任务被取消，就不再
// 等下去。
func (s *codingACPSession) decidePermission(title, kind, detail string) bool {
	if strings.TrimSpace(s.spec.PolicyPath) == "" {
		return true
	}
	policy, err := loadCodingApprovalPolicy(s.spec.PolicyPath)
	if err != nil {
		// 和 hook 一致：审批链路自己坏了不把任务卡死，日志里留下痕迹。
		s.log.event(map[string]any{"type": "acp.permission", "tool": title, "detail": detail, "decision": "unchecked", "reason": "读不到审批策略：" + err.Error()})
		return true
	}
	pattern, needed := "", false
	switch {
	case policy.Mode == codingApprovalModeAllWrites && codingACPReadOnlyKinds[kind]:
	default:
		pattern, needed = codingHookNeedsApproval(policy, title, detail)
	}
	if !needed {
		return true
	}
	s.log.event(map[string]any{"type": "acp.permission", "tool": title, "detail": detail, "decision": "pending"})
	request := codingApprovalRequest{
		ID:        strings.ReplaceAll(uuid.NewString(), "-", "")[:16],
		JobID:     s.spec.JobID,
		Tool:      title,
		Detail:    detail,
		Pattern:   pattern,
		CreatedAt: time.Now(),
	}
	type verdict struct {
		response codingApprovalResponse
		err      error
	}
	answered := make(chan verdict, 1)
	go func() {
		response, err := requestCodingApproval(policy, request)
		answered <- verdict{response, err}
	}()
	var got verdict
	select {
	case got = <-answered:
	case <-s.ctx.Done():
		_ = os.Remove(codingApprovalRequestPath(policy.ApprovalDir, request.ID))
		return false
	}
	allow := got.err == nil && got.response.Allow
	reason := strings.TrimSpace(got.response.Reason)
	if got.err != nil {
		reason = got.err.Error()
	}
	decision := "denied"
	if allow {
		decision = "allowed"
	}
	s.log.event(map[string]any{"type": "acp.permission", "tool": title, "detail": detail, "decision": decision, "reason": reason})
	return allow
}

// codingACPPickOption 在代理给的选项里挑一个。放行只选「这一次」：「以后都同意」由
// Diana 自己记，主人用 approvals clear 才收得回来；交给代理去记，Diana 就既看不见也
// 清不掉。
func codingACPPickOption(options []codingACPPermissionOption, allow bool) string {
	exact, prefix := "reject_once", "reject"
	if allow {
		exact, prefix = "allow_once", "allow"
	}
	for _, option := range options {
		if option.Kind == exact {
			return option.OptionID
		}
	}
	for _, option := range options {
		if strings.HasPrefix(option.Kind, prefix) && (!allow || option.Kind != "allow_always") {
			return option.OptionID
		}
	}
	return ""
}

// codingACPLog 往任务日志里写规整后的事件，一行一个 JSON。
type codingACPLog struct {
	mu sync.Mutex
	w  io.Writer
}

func (l *codingACPLog) event(payload map[string]any) {
	body, err := json.Marshal(payload)
	if err != nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	_, _ = l.w.Write(append(body, '\n'))
}

func (l *codingACPLog) fail(message string) {
	l.event(map[string]any{"type": "acp.error", "message": message})
}

// codingTailBuffer 只留写进来的最后一段。
type codingTailBuffer struct {
	mu    sync.Mutex
	limit int
	buf   []byte
}

func (b *codingTailBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf = append(b.buf, p...)
	if over := len(b.buf) - b.limit; over > 0 {
		b.buf = append([]byte(nil), b.buf[over:]...)
	}
	return len(p), nil
}

func (b *codingTailBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return strings.ToValidUTF8(string(b.buf), "")
}

// codingACPConn 是 stdio 上按行分隔的 JSON-RPC 2.0 连接。代理发来的通知按顺序在读
// 协程里处理（进度的先后不能乱），发来的请求各起一个协程，因为授权请求可能要等主人
// 几分钟。
type codingACPConn struct {
	writeMu   sync.Mutex
	w         io.Writer
	nextID    atomic.Int64
	pendingMu sync.Mutex
	pending   map[int64]chan codingACPMessage
	closed    chan struct{}
	onNotify  func(method string, params json.RawMessage)
	onRequest func(method string, params json.RawMessage) (any, *codingACPRPCError)
}

type codingACPMessage struct {
	JSONRPC string             `json:"jsonrpc"`
	ID      json.RawMessage    `json:"id,omitempty"`
	Method  string             `json:"method,omitempty"`
	Params  json.RawMessage    `json:"params,omitempty"`
	Result  json.RawMessage    `json:"result,omitempty"`
	Error   *codingACPRPCError `json:"error,omitempty"`
}

type codingACPRPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *codingACPRPCError) Error() string {
	if len(e.Data) > 0 && string(e.Data) != "null" {
		return fmt.Sprintf("%s（%d）：%s", e.Message, e.Code, truncateRunes(string(e.Data), 300))
	}
	return fmt.Sprintf("%s（%d）", e.Message, e.Code)
}

var errCodingACPAgentGone = errors.New("ACP 代理已经退出")

func newCodingACPConn(w io.Writer) *codingACPConn {
	return &codingACPConn{w: w, pending: map[int64]chan codingACPMessage{}, closed: make(chan struct{})}
}

func (c *codingACPConn) write(message any) error {
	body, err := json.Marshal(message)
	if err != nil {
		return err
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_, err = c.w.Write(append(body, '\n'))
	return err
}

func (c *codingACPConn) call(ctx context.Context, method string, params any, result any) error {
	id := c.nextID.Add(1)
	reply := make(chan codingACPMessage, 1)
	c.pendingMu.Lock()
	c.pending[id] = reply
	c.pendingMu.Unlock()
	defer func() {
		c.pendingMu.Lock()
		delete(c.pending, id)
		c.pendingMu.Unlock()
	}()
	if err := c.write(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}); err != nil {
		return err
	}
	select {
	case message := <-reply:
		if message.Error != nil {
			return message.Error
		}
		if result != nil && len(message.Result) > 0 {
			return json.Unmarshal(message.Result, result)
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-c.closed:
		return errCodingACPAgentGone
	}
}

func (c *codingACPConn) notify(method string, params any) error {
	return c.write(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
}

func (c *codingACPConn) readLoop(r io.Reader) {
	defer close(c.closed)
	reader := bufio.NewReaderSize(r, 1<<20)
	for {
		line, oversized, err := readCodingACPLine(reader)
		if !oversized && len(strings.TrimSpace(string(line))) > 0 {
			var message codingACPMessage
			if json.Unmarshal(line, &message) == nil {
				c.dispatch(message)
			}
		}
		if err != nil {
			return
		}
	}
}

func (c *codingACPConn) dispatch(message codingACPMessage) {
	hasID := len(message.ID) > 0 && string(message.ID) != "null"
	switch {
	case message.Method != "" && hasID:
		go func() {
			result, rpcErr := c.onRequest(message.Method, message.Params)
			if rpcErr != nil {
				_ = c.write(map[string]any{"jsonrpc": "2.0", "id": message.ID, "error": rpcErr})
				return
			}
			_ = c.write(map[string]any{"jsonrpc": "2.0", "id": message.ID, "result": result})
		}()
	case message.Method != "":
		c.onNotify(message.Method, message.Params)
	case hasID:
		id, err := strconv.ParseInt(strings.Trim(string(message.ID), `"`), 10, 64)
		if err != nil {
			return
		}
		c.pendingMu.Lock()
		reply := c.pending[id]
		c.pendingMu.Unlock()
		if reply != nil {
			reply <- message
		}
	}
}

// readCodingACPLine 读一条消息，超过上限的整条丢掉并告诉调用方。
func readCodingACPLine(reader *bufio.Reader) ([]byte, bool, error) {
	var line []byte
	oversized := false
	for {
		chunk, err := reader.ReadSlice('\n')
		if !oversized {
			if len(line)+len(chunk) > codingACPMaxMessageBytes {
				oversized, line = true, nil
			} else {
				line = append(line, chunk...)
			}
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		return line, oversized, err
	}
}
