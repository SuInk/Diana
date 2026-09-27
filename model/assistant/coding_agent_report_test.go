// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// gatedDeliveryChannel 模拟反向 WebSocket：接入端没连上时发送直接报「未连接」，
// 每次重新连上 ConnectionEpoch 加一。
type gatedDeliveryChannel struct {
	mu        sync.Mutex
	connected bool
	epoch     uint64
	failNext  int
	attempts  int
	sent      []OutgoingMessage
}

func (c *gatedDeliveryChannel) Connect(ctx context.Context, _ EventHandler) error {
	<-ctx.Done()
	return ctx.Err()
}

func (c *gatedDeliveryChannel) Send(_ context.Context, msg OutgoingMessage) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.attempts++
	if !c.connected {
		return errors.New("diana: onebot reverse websocket is not connected")
	}
	if c.failNext > 0 {
		c.failNext--
		return errors.New("diana: onebot action send_private_msg failed")
	}
	c.sent = append(c.sent, msg)
	return nil
}

func (c *gatedDeliveryChannel) CallAPI(context.Context, string, map[string]any) (map[string]any, error) {
	return map[string]any{"message_id": 42}, nil
}

func (c *gatedDeliveryChannel) Status() ChannelStatus {
	c.mu.Lock()
	defer c.mu.Unlock()
	return ChannelStatus{Connected: c.connected, ConnectionEpoch: c.epoch}
}

func (c *gatedDeliveryChannel) Close() error { return nil }

func (c *gatedDeliveryChannel) setConnected(connected bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if connected && !c.connected {
		c.epoch++
	}
	c.connected = connected
}

func (c *gatedDeliveryChannel) counts() (attempts, sent int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.attempts, len(c.sent)
}

func (c *gatedDeliveryChannel) messages() []OutgoingMessage {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]OutgoingMessage(nil), c.sent...)
}

// fastCodingReports 把汇报协程的节奏调到毫秒级，并在用例结束时确认协程都退出了。
func fastCodingReports(t *testing.T, rt *Runtime) {
	t.Helper()
	registry := rt.codingJobs()
	registry.mu.Lock()
	registry.reportTiming = codingReportTiming{poll: 5 * time.Millisecond, initialDelay: 20 * time.Millisecond, maxDelay: 80 * time.Millisecond}
	registry.mu.Unlock()
	t.Cleanup(func() {
		waitForCondition(t, 5*time.Second, func() bool { return codingReportRetries(rt) == 0 })
	})
}

func codingReportRetries(rt *Runtime) int {
	registry := rt.codingJobs()
	registry.mu.Lock()
	defer registry.mu.Unlock()
	return len(registry.reportRetries)
}

func saveFinishedCodingJob(t *testing.T, id string, target codingJobTarget) CodingJob {
	t.Helper()
	if err := os.MkdirAll(codingJobRecordDir(), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	job := CodingJob{
		ID: id, Backend: codingBackendClaude, Workspace: "demo",
		Instruction: "重启前派的活", Status: codingJobStatusSucceeded,
		StartedAt: time.Now().Add(-time.Hour), FinishedAt: time.Now().Add(-time.Minute),
		LogPath: codingJobLogPath(id), Result: "改好了", Target: target,
	}
	if err := saveCodingJob(job); err != nil {
		t.Fatalf("save: %v", err)
	}
	return job
}

func codingJobReported(t *testing.T, id string) bool {
	t.Helper()
	job, err := loadCodingJob(id)
	return err == nil && job.Reported
}

// TestResumeCodingJobsReportsOnceAfterReverseWebSocketConnects 复现 #757 实测时的
// 场景：反向 WebSocket 模式启动，接回跑在接入端连上之前。汇报要等连上再发、只发
// 一次；断线重连、再接回一轮都不能重发。
func TestResumeCodingJobsReportsOnceAfterReverseWebSocketConnects(t *testing.T) {
	useTempCodingWorkspace(t)
	channel := &gatedDeliveryChannel{}
	rt := NewRuntime(BotConfig{ID: "bot-a", BotAccount: "42", OwnerID: "1"}, channel, NewPluginManager(NewCodingAgentPlugin()), nil, nil, nil, nil)
	fastCodingReports(t, rt)
	job := saveFinishedCodingJob(t, "code-early", codingJobTarget{ProfileID: "bot-a", UserID: "7"})

	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	rt.ResumeCodingJobs(ctx)
	// 同一轮里再接回一次（比如保存配置重建了通道）也不该多起一个汇报协程。
	rt.ResumeCodingJobs(ctx)
	time.Sleep(100 * time.Millisecond)
	if attempts, sent := channel.counts(); attempts != 0 || sent != 0 {
		t.Fatalf("没连上就去发了：attempts=%d sent=%d", attempts, sent)
	}
	if codingJobReported(t, job.ID) {
		t.Fatalf("没发出去就标了 Reported")
	}
	if got := codingReportRetries(rt); got != 1 {
		t.Fatalf("汇报协程 = %d，want 1", got)
	}

	channel.setConnected(true)
	waitForCondition(t, 5*time.Second, func() bool { return codingJobReported(t, job.ID) })
	msgs := channel.messages()
	if len(msgs) != 1 || msgs[0].UserID != "7" || !strings.Contains(msgs[0].Text, job.ID) {
		t.Fatalf("汇报 = %#v", msgs)
	}
	waitForCondition(t, 5*time.Second, func() bool { return codingReportRetries(rt) == 0 })

	// 断线重连、再接回一轮：都不重发。
	channel.setConnected(false)
	time.Sleep(30 * time.Millisecond)
	channel.setConnected(true)
	rt.ResumeCodingJobs(ctx)
	time.Sleep(150 * time.Millisecond)
	if _, sent := channel.counts(); sent != 1 {
		t.Fatalf("重连后重复汇报：sent=%d", sent)
	}
}

// TestCodingReportRetriesWhileConnectedUntilDelivered 覆盖连着却发失败（接口报错、
// 被风控）的情况：按退避重试到发出去为止，发出去之前不落 Reported。
func TestCodingReportRetriesWhileConnectedUntilDelivered(t *testing.T) {
	useTempCodingWorkspace(t)
	// 单次发送内部自带 sendRetryAttempts 次重试，失败次数要多过它才轮到汇报协程。
	channel := &gatedDeliveryChannel{connected: true, epoch: 1, failNext: sendRetryAttempts + 1}
	rt := NewRuntime(BotConfig{ID: "bot-a", BotAccount: "42", OwnerID: "1"}, channel, NewPluginManager(NewCodingAgentPlugin()), nil, nil, nil, nil)
	fastCodingReports(t, rt)
	job := saveFinishedCodingJob(t, "code-flaky", codingJobTarget{ProfileID: "bot-a", UserID: "7"})

	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	rt.ResumeCodingJobs(ctx)
	if codingJobReported(t, job.ID) {
		t.Fatalf("第一次就失败了，不该标 Reported")
	}
	waitForCondition(t, 5*time.Second, func() bool { return codingJobReported(t, job.ID) })
	waitForCondition(t, 5*time.Second, func() bool { return codingReportRetries(rt) == 0 })
	if attempts, sent := channel.counts(); sent != 1 || attempts <= sendRetryAttempts {
		t.Fatalf("attempts=%d sent=%d", attempts, sent)
	}
}

// TestCodingReportRetryExitsWhenRuntimeStops 钉住协程不泄漏：一直连不上时，这一轮
// 运行一结束汇报协程就退出，任务留着没汇报，交给下次启动。
func TestCodingReportRetryExitsWhenRuntimeStops(t *testing.T) {
	useTempCodingWorkspace(t)
	channel := &gatedDeliveryChannel{}
	rt := NewRuntime(BotConfig{ID: "bot-a", BotAccount: "42", OwnerID: "1"}, channel, NewPluginManager(NewCodingAgentPlugin()), nil, nil, nil, nil)
	fastCodingReports(t, rt)
	job := saveFinishedCodingJob(t, "code-stop", codingJobTarget{ProfileID: "bot-a", UserID: "7"})

	ctx, stop := context.WithCancel(context.Background())
	rt.ResumeCodingJobs(ctx)
	waitForCondition(t, 5*time.Second, func() bool { return codingReportRetries(rt) == 1 })
	stop()
	waitForCondition(t, 5*time.Second, func() bool { return codingReportRetries(rt) == 0 })
	if codingJobReported(t, job.ID) {
		t.Fatalf("没发出去就标了 Reported")
	}

	// 下一轮运行（同一进程里重启运行时）重新接回，连上后照常补上。
	next, stopNext := context.WithCancel(context.Background())
	defer stopNext()
	rt.ResumeCodingJobs(next)
	channel.setConnected(true)
	waitForCondition(t, 5*time.Second, func() bool { return codingJobReported(t, job.ID) })
	if _, sent := channel.counts(); sent != 1 {
		t.Fatalf("sent = %d", sent)
	}
}

// TestCodingReportWaitsForItsOwnBotConnection 多机器人时等的是任务所属那台的连接：
// 合并状态里别的机器人连着不算数，汇报也只从所属那台发出去。
func TestCodingReportWaitsForItsOwnBotConnection(t *testing.T) {
	useTempCodingWorkspace(t)
	channelA := &gatedDeliveryChannel{connected: true, epoch: 1}
	channelB := &gatedDeliveryChannel{}
	multi := NewMultiChannel([]ChannelBinding{
		{ProfileID: "bot-a", Platform: PlatformOneBotV11, Name: "A", Channel: channelA},
		{ProfileID: "bot-b", Platform: PlatformOneBotV11, Name: "B", Channel: channelB},
	})
	rt := NewRuntime(BotConfig{ID: "bot-a", BotAccount: "42", OwnerID: "1"}, multi, NewPluginManager(NewCodingAgentPlugin()), nil, nil, nil, nil)
	rt.mu.Lock()
	rt.profileConfigs["bot-b"] = BotConfig{ID: "bot-b", BotAccount: "43", OwnerID: "1"}
	rt.mu.Unlock()
	fastCodingReports(t, rt)
	jobA := saveFinishedCodingJob(t, "code-a", codingJobTarget{ProfileID: "bot-a", UserID: "7"})
	jobB := saveFinishedCodingJob(t, "code-b", codingJobTarget{ProfileID: "bot-b", UserID: "8"})
	// 别的实例的任务：这台 Runtime 不认，连上之后也不碰。
	foreign := saveFinishedCodingJob(t, "code-c", codingJobTarget{ProfileID: "bot-c", UserID: "9"})

	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	rt.ResumeCodingJobs(ctx)
	waitForCondition(t, 5*time.Second, func() bool { return codingJobReported(t, jobA.ID) })
	time.Sleep(100 * time.Millisecond)
	if codingJobReported(t, jobB.ID) {
		t.Fatalf("B 还没连上就标了 Reported")
	}
	if attempts, _ := channelB.counts(); attempts != 0 {
		t.Fatalf("B 没连上就去发了 %d 次", attempts)
	}

	channelB.setConnected(true)
	waitForCondition(t, 5*time.Second, func() bool { return codingJobReported(t, jobB.ID) })
	waitForCondition(t, 5*time.Second, func() bool { return codingReportRetries(rt) == 0 })
	if msgs := channelA.messages(); len(msgs) != 1 || !strings.Contains(msgs[0].Text, jobA.ID) {
		t.Fatalf("A 的汇报 = %#v", msgs)
	}
	if msgs := channelB.messages(); len(msgs) != 1 || !strings.Contains(msgs[0].Text, jobB.ID) {
		t.Fatalf("B 的汇报 = %#v", msgs)
	}
	if got, err := loadCodingJob(foreign.ID); err != nil || got.Reported {
		t.Fatalf("别的实例的任务被改写了：%#v err=%v", got, err)
	}
}

// TestCodingApprovalWaitsForConnectionInsteadOfDenying 接回的任务一启动就在等确认时，
// 不能因为聊天客户端还没连上就直接拒掉，连上之后照常去问。
func TestCodingApprovalWaitsForConnectionInsteadOfDenying(t *testing.T) {
	useTempCodingWorkspace(t)
	channel := &gatedDeliveryChannel{}
	rt := NewRuntime(BotConfig{ID: "bot-a", BotAccount: "42", OwnerID: "1"}, channel, NewPluginManager(NewCodingAgentPlugin()), nil, nil, nil, nil)
	fastCodingReports(t, rt)
	dir := codingApprovalDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	job := CodingJob{
		ID: "code-ask-early", Workspace: "demo", Status: codingJobStatusRunning,
		StartedAt: time.Now(), ApprovalMode: codingApprovalModeDangerous,
		Target: codingJobTarget{ProfileID: "bot-a", UserID: "1"},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		rt.watchCodingApprovals(ctx, job, 30*time.Second)
	}()

	request := codingApprovalRequest{ID: "req-early", JobID: job.ID, Tool: "Bash", Detail: "git push origin main", CreatedAt: time.Now()}
	body, _ := json.Marshal(request)
	if err := os.WriteFile(codingApprovalRequestPath(dir, request.ID), body, 0o600); err != nil {
		t.Fatalf("write request: %v", err)
	}
	waitForCondition(t, 5*time.Second, func() bool { return rt.codingJobs().approvalPending(request.ID) })
	time.Sleep(100 * time.Millisecond)
	if _, err := os.Stat(codingApprovalResponsePath(dir, request.ID)); err == nil {
		t.Fatalf("没连上就写了裁决")
	}
	if attempts, _ := channel.counts(); attempts != 0 {
		t.Fatalf("没连上就去问了 %d 次", attempts)
	}

	channel.setConnected(true)
	waitForCondition(t, 5*time.Second, func() bool {
		_, sent := channel.counts()
		return sent == 1
	})
	if _, err := os.Stat(codingApprovalResponsePath(dir, request.ID)); err == nil {
		t.Fatalf("问出去之后不该立刻有裁决")
	}
	cancel()
	<-done
}

// TestDeferredCodingReportRoutesLegacyProfileToCurrentBot 等连接期间重读的记录还记着
// 旧号（#760）：汇报要改成现在的 ID 从那台机器人的连接发出，写回的归属也是新 ID。
func TestDeferredCodingReportRoutesLegacyProfileToCurrentBot(t *testing.T) {
	useTempCodingWorkspace(t)
	channelA := &gatedDeliveryChannel{}
	channelB := &gatedDeliveryChannel{connected: true, epoch: 1}
	multi := NewMultiChannel([]ChannelBinding{
		{ProfileID: "bot-a", Platform: PlatformOneBotV11, Name: "A", Channel: channelA},
		{ProfileID: "bot-b", Platform: PlatformOneBotV11, Name: "B", Channel: channelB},
	})
	rt := NewRuntime(BotConfig{ID: "bot-a", BotAccount: "42", OwnerID: "1"}, multi, NewPluginManager(NewCodingAgentPlugin()), nil, nil, nil, nil)
	rt.mu.Lock()
	rt.profileConfigs["bot-b"] = BotConfig{ID: "bot-b", BotAccount: "43", OwnerID: "1"}
	rt.mu.Unlock()
	rt.SetProfileAliases(map[string]string{"old-boot": "bot-a"})
	fastCodingReports(t, rt)
	job := saveFinishedCodingJob(t, "code-legacy", codingJobTarget{ProfileID: "old-boot", UserID: "7"})

	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	rt.ResumeCodingJobs(ctx)
	waitForCondition(t, 5*time.Second, func() bool { return codingReportRetries(rt) == 1 })
	channelA.setConnected(true)
	waitForCondition(t, 5*time.Second, func() bool { return codingJobReported(t, job.ID) })
	waitForCondition(t, 5*time.Second, func() bool { return codingReportRetries(rt) == 0 })
	if msgs := channelA.messages(); len(msgs) != 1 || msgs[0].UserID != "7" {
		t.Fatalf("A 的汇报 = %#v", msgs)
	}
	if _, sent := channelB.counts(); sent != 0 {
		t.Fatalf("汇报从 B 发出去了")
	}
	if saved, _ := loadCodingJob(job.ID); saved.Target.ProfileID != "bot-a" {
		t.Fatalf("写回的归属 = %q", saved.Target.ProfileID)
	}
}

// forwardlessChannel 让合并转发失败、其余照常记录：有的 OneBot 实现不支持合并转发。
type forwardlessChannel struct {
	*recordingChannel
}

func (c forwardlessChannel) CallAPI(ctx context.Context, action string, params map[string]any) (map[string]any, error) {
	if strings.Contains(action, "forward") {
		return nil, errors.New("unsupported action")
	}
	return c.recordingChannel.CallAPI(ctx, action, params)
}

// longCodingResult 造一份几千字的报告，开头和结尾各有记号，好判断全文有没有送到。
func longCodingResult() string {
	return "REPORT-START 结论：方向对，边界要收紧。\n\n" +
		strings.Repeat("## 分析\n这一节展开讲拦截点和审批流怎么接。\n\n", 150) +
		"REPORT-END"
}

func saveLongCodingJob(t *testing.T, rt *Runtime) CodingJob {
	t.Helper()
	job := saveFinishedCodingJob(t, "code-long", codingJobTarget{ProfileID: "bot-a", GroupID: "123", UserID: "7"})
	job.Result = longCodingResult()
	if err := saveCodingJob(job); err != nil {
		t.Fatalf("save: %v", err)
	}
	return job
}

// TestCodingReportSendsLongResultAsForward 钉住长报告的发法：以前结果截到 1500 字，
// 「整理成 md 发群里」的报告到群里只剩开头一段。现在头部带 @ 和开头先发一条，全文
// 整段进合并转发，不在群里刷十几条。
func TestCodingReportSendsLongResultAsForward(t *testing.T) {
	useTempCodingWorkspace(t)
	channel := &recordingChannel{}
	rt := NewRuntime(BotConfig{ID: "bot-a", BotAccount: "42", OwnerID: "1", ForwardReplyEnabled: boolPointer(true)}, channel, NewPluginManager(NewCodingAgentPlugin()), nil, nil, nil, nil)
	job := saveLongCodingJob(t, rt)

	if retry := rt.attemptCodingReport(context.Background(), job); retry {
		t.Fatalf("发成功了不该再重试")
	}
	sent := channel.sentSnapshot()
	if len(sent) != 1 {
		t.Fatalf("头部应当只有一条，实际 %d 条：%#v", len(sent), sent)
	}
	head := sent[0].Text
	runes := len([]rune(job.Result))
	if !strings.Contains(head, fmt.Sprintf("结果共 %d 字", runes)) || !strings.Contains(head, "REPORT-START") || strings.Contains(head, "REPORT-END") {
		t.Fatalf("头部 = %q", head)
	}
	if sent[0].MentionUserID != "7" {
		t.Fatalf("头部要点名派活的人：%#v", sent[0])
	}
	var forwards []recordingAPICall
	for _, call := range channel.callsSnapshot() {
		if call.action == "send_group_forward_msg" {
			forwards = append(forwards, call)
		}
	}
	if len(forwards) != 1 {
		t.Fatalf("全文应当走一张合并转发，实际 %d 张", len(forwards))
	}
	body, _ := json.Marshal(forwards[0].params)
	if !strings.Contains(string(body), "REPORT-START") || !strings.Contains(string(body), "REPORT-END") {
		t.Fatalf("合并转发里不是全文：%.300s", body)
	}
	if !codingJobReported(t, job.ID) {
		t.Fatalf("发完要记成已汇报")
	}
}

// TestCodingReportFallsBackToChunksWithoutForward 钉住合并转发发不出去时的退路：
// 全文分条照样送到，只有头部那条点名。
func TestCodingReportFallsBackToChunksWithoutForward(t *testing.T) {
	useTempCodingWorkspace(t)
	channel := forwardlessChannel{&recordingChannel{}}
	rt := NewRuntime(BotConfig{ID: "bot-a", BotAccount: "42", OwnerID: "1", SendChunkIntervalMS: 1, ForwardReplyEnabled: boolPointer(true)}, channel, NewPluginManager(NewCodingAgentPlugin()), nil, nil, nil, nil)
	job := saveLongCodingJob(t, rt)

	if retry := rt.attemptCodingReport(context.Background(), job); retry {
		t.Fatalf("分条发成功了不该再重试")
	}
	sent := channel.sentSnapshot()
	if len(sent) < 3 {
		t.Fatalf("应当是头部加分条的全文，实际 %d 条", len(sent))
	}
	var body strings.Builder
	for _, msg := range sent[1:] {
		if msg.MentionUserID != "" {
			t.Fatalf("全文分条不该再点名：%#v", msg)
		}
		body.WriteString(msg.Text)
	}
	if !strings.Contains(body.String(), "REPORT-START") || !strings.Contains(body.String(), "REPORT-END") {
		t.Fatalf("分条没送全文")
	}
	if !codingJobReported(t, job.ID) {
		t.Fatalf("发完要记成已汇报")
	}
}

// TestCodingReportKeepsShortResultInline 钉住短结果不变：头部和结果一条发完。
func TestCodingReportKeepsShortResultInline(t *testing.T) {
	useTempCodingWorkspace(t)
	channel := &recordingChannel{}
	rt := NewRuntime(BotConfig{ID: "bot-a", BotAccount: "42", OwnerID: "1"}, channel, NewPluginManager(NewCodingAgentPlugin()), nil, nil, nil, nil)
	job := saveFinishedCodingJob(t, "code-short", codingJobTarget{ProfileID: "bot-a", GroupID: "123", UserID: "7"})

	rt.attemptCodingReport(context.Background(), job)
	sent := channel.sentSnapshot()
	if len(sent) != 1 || !strings.Contains(sent[0].Text, "结果：\n改好了") {
		t.Fatalf("短结果应当一条发完：%#v", sent)
	}
	if calls := channel.callsSnapshot(); len(calls) != 0 {
		t.Fatalf("短结果不该走合并转发：%#v", calls)
	}
}
