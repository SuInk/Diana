// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

// codingOwnerRuntime 起一台只认 profileIDs 这几台机器人的 Runtime，主人都是 1。
func codingOwnerRuntime(t *testing.T, profileIDs ...string) (*Runtime, *concurrentRecordingChannel) {
	t.Helper()
	channel := &concurrentRecordingChannel{}
	rt := NewRuntime(BotConfig{ID: profileIDs[0], BotAccount: "42", OwnerID: "1"}, channel, NewPluginManager(NewCodingAgentPlugin()), nil, nil, nil, nil)
	rt.mu.Lock()
	for _, id := range profileIDs[1:] {
		rt.profileConfigs[id] = BotConfig{ID: id, BotAccount: "43", OwnerID: "1"}
	}
	rt.mu.Unlock()
	return rt, channel
}

// saveLeftoverCodingJobs 在记录目录里放两条「上次留下来」的任务：一条已经收尾但没汇
// 报，一条还标着在跑但进程早就没了。
func saveLeftoverCodingJobs(t *testing.T, target codingJobTarget) (finished, dead CodingJob) {
	t.Helper()
	if err := os.MkdirAll(codingJobRecordDir(), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	finished = CodingJob{
		ID: "code-done", Backend: codingBackendClaude, Workspace: "demo",
		Instruction: "B 派的活", Status: codingJobStatusSucceeded,
		StartedAt: time.Now().Add(-time.Hour), FinishedAt: time.Now().Add(-time.Minute),
		LogPath: codingJobLogPath("code-done"), Result: "改好了", Target: target,
	}
	dead = CodingJob{
		ID: "code-dead", Backend: codingBackendClaude, Workspace: "other",
		Instruction: "B 没跑完的活", Status: codingJobStatusRunning,
		StartedAt: time.Now().Add(-time.Hour), LogPath: codingJobLogPath("code-dead"),
		Target: target,
	}
	for _, job := range []CodingJob{finished, dead} {
		if err := os.WriteFile(job.LogPath, nil, 0o600); err != nil {
			t.Fatalf("write log: %v", err)
		}
		if err := saveCodingJob(job); err != nil {
			t.Fatalf("save: %v", err)
		}
	}
	return finished, dead
}

func TestResumeCodingJobsLeavesOtherBotsJobsAlone(t *testing.T) {
	useTempCodingWorkspace(t)
	finished, dead := saveLeftoverCodingJobs(t, codingJobTarget{ProfileID: "bot-b", UserID: "7"})

	// A 和 B 共用一个记录目录。A 重启时扫到的全是 B 的活，一条都不该碰。
	botA, channelA := codingOwnerRuntime(t, "bot-a")
	botA.ResumeCodingJobs(context.Background())
	time.Sleep(200 * time.Millisecond)
	if channelA.count() != 0 {
		t.Fatalf("A 替 B 汇报了：%#v", channelA.messages())
	}
	for _, want := range []CodingJob{finished, dead} {
		got, err := loadCodingJob(want.ID)
		if err != nil {
			t.Fatalf("load %s: %v", want.ID, err)
		}
		// 标上 Reported 或改成中断，B 以后就不会再汇报了。
		if got.Reported || got.Status != want.Status {
			t.Fatalf("A 改写了 B 的任务 %s：reported=%v status=%q", want.ID, got.Reported, got.Status)
		}
	}

	// 真正的主人重启时照常接回：已结束的补汇报，进程没了的按中断收尾。
	botB, channelB := codingOwnerRuntime(t, "bot-b")
	botB.ResumeCodingJobs(context.Background())
	waitForCondition(t, 5*time.Second, func() bool { return channelB.count() >= 2 })
	for _, msg := range channelB.messages() {
		if msg.UserID != "7" {
			t.Fatalf("汇报发错了人：%#v", msg)
		}
	}
	waitForCondition(t, 5*time.Second, func() bool {
		a, errA := loadCodingJob(finished.ID)
		b, errB := loadCodingJob(dead.ID)
		return errA == nil && errB == nil && a.Reported && b.Reported && b.Status == codingJobStatusInterrupted
	})
}

func TestResumeCodingJobsLegacyRecordWithoutProfile(t *testing.T) {
	useTempCodingWorkspace(t)
	// 没有档案 ID 的旧记录认不出主人。几台机器人共用一个 Runtime 时不去猜是哪一台。
	finished, _ := saveLeftoverCodingJobs(t, codingJobTarget{UserID: "1"})
	multi, multiChannel := codingOwnerRuntime(t, "bot-a", "bot-b")
	multi.ResumeCodingJobs(context.Background())
	time.Sleep(200 * time.Millisecond)
	if multiChannel.count() != 0 {
		t.Fatalf("多机器人时不该替没有归属的旧任务汇报：%#v", multiChannel.messages())
	}
	if got, err := loadCodingJob(finished.ID); err != nil || got.Reported {
		t.Fatalf("旧任务被改写了：%#v err=%v", got, err)
	}

	// 单机器人部署照旧接回：只有一台，旧记录就是它的。
	single, singleChannel := codingOwnerRuntime(t, "bot-a")
	single.ResumeCodingJobs(context.Background())
	waitForCondition(t, 5*time.Second, func() bool { return singleChannel.count() >= 2 })
	waitForCondition(t, 5*time.Second, func() bool {
		got, err := loadCodingJob(finished.ID)
		return err == nil && got.Reported
	})
}

func TestLaunchCodingJobRecordsOwningProfile(t *testing.T) {
	useTempCodingWorkspace(t)
	workspace := t.TempDir()
	cli := fakeCodingCLI(t, `echo '{"type":"result","is_error":false,"result":"ok"}'`)
	settings := codingSettings(map[string]any{
		codingAgentSettingCommand:    cli,
		codingAgentSettingWorkspaces: "demo=" + workspace,
	})
	cfg, err := codingAgentConfigFromSettings(settings)
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	ws, _ := cfg.workspace("demo")

	// 事件没带档案 ID 时，归属补成这台 Runtime 唯一的那台机器人。
	rt, channel := codingOwnerRuntime(t, "bot-a")
	event := MessageEvent{Kind: EventKindPrivate, UserID: "1", MessageID: "m1"}
	job, err := rt.launchCodingJob(context.Background(), event, cfg, ws, "改点东西", "")
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	if job.Target.ProfileID != "bot-a" {
		t.Fatalf("归属 = %q", job.Target.ProfileID)
	}
	waitForCondition(t, 10*time.Second, func() bool {
		saved, err := loadCodingJob(job.ID)
		return err == nil && saved.finished() && saved.Reported
	})
	if saved, _ := loadCodingJob(job.ID); saved.Target.ProfileID != "bot-a" {
		t.Fatalf("落盘的归属 = %q", saved.Target.ProfileID)
	}
	if channel.count() != 1 {
		t.Fatalf("sends = %d", channel.count())
	}

	// 几台机器人时认不出是哪一台收到的，就不派：派出去之后没人能接回。
	multi, _ := codingOwnerRuntime(t, "bot-a", "bot-b")
	if _, err := multi.launchCodingJob(context.Background(), event, cfg, ws, "改点东西", ""); err == nil || !strings.Contains(err.Error(), "哪台机器人") {
		t.Fatalf("err = %v", err)
	}
}

func TestCodingToolOnlySeesOwnBotsJobs(t *testing.T) {
	useTempCodingWorkspace(t)
	finished, dead := saveLeftoverCodingJobs(t, codingJobTarget{ProfileID: "bot-b", UserID: "1"})
	// 同一个 Runtime 里的两台机器人，主人恰好是同一个人，也各看各的。
	rt, _ := codingOwnerRuntime(t, "bot-a", "bot-b")
	settings := codingSettings(nil)
	asA := newDianaCodingTool(rt, MessageEvent{Kind: EventKindPrivate, ProfileID: "bot-a", UserID: "1"}, settings)

	output, err := asA.Run(context.Background(), map[string]any{"operation": "list"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var listed dianaCodingResult
	if err := json.Unmarshal([]byte(output), &listed); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(listed.Jobs) != 0 {
		t.Fatalf("A 列出了 B 的任务：%#v", listed.Jobs)
	}
	for _, input := range []map[string]any{
		{"operation": "status", "job_id": finished.ID},
		{"operation": "tail", "job_id": finished.ID},
		{"operation": "followup", "job_id": finished.ID, "instruction": "再改一点"},
		{"operation": "cancel", "job_id": dead.ID},
	} {
		if _, err := asA.Run(context.Background(), input); err == nil || !strings.Contains(err.Error(), "找不到任务") {
			t.Fatalf("%v: err = %v", input["operation"], err)
		}
	}
	// 省略 job_id 时也不能落到别人的任务上。
	if _, err := asA.Run(context.Background(), map[string]any{"operation": "status"}); err == nil || !strings.Contains(err.Error(), "还没有任何编码任务") {
		t.Fatalf("status without id: err = %v", err)
	}
	if _, err := asA.Run(context.Background(), map[string]any{"operation": "cancel"}); err == nil {
		t.Fatalf("cancel without id 应当找不到可取消的任务")
	}
	if got, err := loadCodingJob(dead.ID); err != nil || got.Status != codingJobStatusRunning {
		t.Fatalf("A 取消了 B 的任务：%#v err=%v", got, err)
	}

	asB := newDianaCodingTool(rt, MessageEvent{Kind: EventKindPrivate, ProfileID: "bot-b", UserID: "1"}, settings)
	output, err = asB.Run(context.Background(), map[string]any{"operation": "status", "job_id": finished.ID})
	if err != nil {
		t.Fatalf("B status: %v", err)
	}
	var status dianaCodingResult
	if err := json.Unmarshal([]byte(output), &status); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if status.Job == nil || status.Job.ID != finished.ID {
		t.Fatalf("B 查不到自己的任务：%#v", status.Job)
	}
}

// 修复前种子机器人每次重启都换档案 ID，按旧 ID 记下的任务靠本实例登记的旧号认回来：
// 旧号对到哪台就由哪台汇报，记录里的归属顺手改成现在的 ID；没登记过的陌生 ID 即使
// 只有一台机器人也不认。
func TestResumeCodingJobsClaimsRegisteredLegacyProfileID(t *testing.T) {
	useTempCodingWorkspace(t)
	finished, dead := saveLeftoverCodingJobs(t, codingJobTarget{ProfileID: "old-boot", UserID: "7"})

	stranger, strangerChannel := codingOwnerRuntime(t, "bot-a")
	stranger.ResumeCodingJobs(context.Background())
	time.Sleep(200 * time.Millisecond)
	if strangerChannel.count() != 0 {
		t.Fatalf("没登记过的旧号不该被认领：%#v", strangerChannel.messages())
	}

	rt, channel := codingOwnerRuntime(t, "bot-a", "bot-b")
	rt.SetProfileAliases(map[string]string{"old-boot": "bot-a"})
	asA := newDianaCodingTool(rt, MessageEvent{Kind: EventKindPrivate, ProfileID: "bot-a", UserID: "1"}, codingSettings(nil))
	if _, err := asA.Run(context.Background(), map[string]any{"operation": "status", "job_id": finished.ID}); err != nil {
		t.Fatalf("A 看不到自己旧号下的任务：%v", err)
	}
	asB := newDianaCodingTool(rt, MessageEvent{Kind: EventKindPrivate, ProfileID: "bot-b", UserID: "1"}, codingSettings(nil))
	if _, err := asB.Run(context.Background(), map[string]any{"operation": "status", "job_id": finished.ID}); err == nil {
		t.Fatal("B 看到了 A 旧号下的任务")
	}

	rt.ResumeCodingJobs(context.Background())
	waitForCondition(t, 5*time.Second, func() bool { return channel.count() >= 2 })
	for _, msg := range channel.messages() {
		if msg.UserID != "7" {
			t.Fatalf("汇报发错了人：%#v", msg)
		}
	}
	waitForCondition(t, 5*time.Second, func() bool {
		a, errA := loadCodingJob(finished.ID)
		b, errB := loadCodingJob(dead.ID)
		return errA == nil && errB == nil && a.Reported && b.Reported &&
			a.Target.ProfileID == "bot-a" && b.Target.ProfileID == "bot-a"
	})

	// 旧号指向的机器人已经不在了，就不再认领。
	orphan, _ := codingOwnerRuntime(t, "bot-b")
	orphan.SetProfileAliases(map[string]string{"old-boot": "bot-a"})
	if _, ok := orphan.codingJobOwner("old-boot"); ok {
		t.Fatal("旧号指向已删除的机器人时不该被认领")
	}
}
