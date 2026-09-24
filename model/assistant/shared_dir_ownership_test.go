// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 这组用例都是同一个场景：两台机器人 bot-a、bot-b 跑在同一个 Runtime 里，共用一个
// 数据目录，主人分别是 10001 和 10002。每一处按机器人归属的状态都要互不影响。

func sharedDirRuntime(t *testing.T, store ReminderStore) *Runtime {
	t.Helper()
	rt := NewRuntime(BotConfig{ID: "bot-a", BotAccount: "42", OwnerID: "10001"}, nilChannel{}, NewPluginManager(NewCodingAgentPlugin()), nil, store, nil, nil)
	rt.mu.Lock()
	rt.profileConfigs["bot-b"] = BotConfig{ID: "bot-b", BotAccount: "43", OwnerID: "10002"}
	rt.mu.Unlock()
	return rt
}

func ownerEventA() MessageEvent {
	return MessageEvent{Kind: EventKindPrivate, ProfileID: "bot-a", UserID: "10001"}
}

func ownerEventB() MessageEvent {
	return MessageEvent{Kind: EventKindPrivate, ProfileID: "bot-b", UserID: "10002"}
}

func saveSettledCodingJob(t *testing.T, id, profileID string, startedAt time.Time, reported bool) {
	t.Helper()
	job := CodingJob{
		ID: id, Backend: codingBackendClaude, Workspace: "demo", Instruction: id,
		Status: codingJobStatusSucceeded, StartedAt: startedAt, FinishedAt: startedAt.Add(time.Minute),
		LogPath: codingJobLogPath(id), Reported: reported,
		Target: codingJobTarget{ProfileID: profileID, UserID: "10001"},
	}
	if err := os.WriteFile(job.LogPath, []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("write log: %v", err)
	}
	if err := saveCodingJob(job); err != nil {
		t.Fatalf("save: %v", err)
	}
}

// 清理只动自己的、已经了结的任务：A 派活再多也不能把 B 还没汇报的任务删掉，
// 自己没汇报出去的也要留着等重启补上。
func TestPruneCodingJobsOnlyTouchesOwnSettledJobs(t *testing.T) {
	useTempCodingWorkspace(t)
	if err := os.MkdirAll(codingJobRecordDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	base := time.Now().Add(-48 * time.Hour)
	// B 的两条最老的任务：一条已汇报，一条还欠着汇报。
	saveSettledCodingJob(t, "code-b-reported", "bot-b", base, true)
	saveSettledCodingJob(t, "code-b-unreported", "bot-b", base.Add(time.Second), false)
	// A 自己一条很老、还没汇报的任务，外加一批已汇报的，数量超过保留上限。
	saveSettledCodingJob(t, "code-a-unreported", "bot-a", base.Add(2*time.Second), false)
	total := codingJobRetainCount + 5
	for i := 0; i < total; i++ {
		saveSettledCodingJob(t, fmt.Sprintf("code-a-%03d", i), "bot-a", base.Add(time.Hour+time.Duration(i)*time.Minute), true)
	}

	// 只让 A 这台 Runtime 清理：B 不在这台 Runtime 上，模拟另一个实例共用目录。
	rtA := NewRuntime(BotConfig{ID: "bot-a", OwnerID: "10001"}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	rtA.pruneCodingJobs()

	for _, id := range []string{"code-b-reported", "code-b-unreported"} {
		if _, err := loadCodingJob(id); err != nil {
			t.Fatalf("A 清理时删掉了 B 的任务 %s：%v", id, err)
		}
		if _, err := os.Stat(codingJobLogPath(id)); err != nil {
			t.Fatalf("A 清理时删掉了 B 的日志 %s：%v", id, err)
		}
	}
	if _, err := loadCodingJob("code-a-unreported"); err != nil {
		t.Fatalf("还没汇报的任务不该被清掉：%v", err)
	}
	kept := 0
	for i := 0; i < total; i++ {
		if _, err := loadCodingJob(fmt.Sprintf("code-a-%03d", i)); err == nil {
			kept++
		}
	}
	// 保留数按 A 自己的任务算：最新的 60 条留着，再往前已了结的清掉，没汇报的那条仍在。
	if kept != codingJobRetainCount {
		t.Fatalf("A 保留了 %d 条已汇报的任务，应为 %d", kept, codingJobRetainCount)
	}
	for _, i := range []int{0, total - codingJobRetainCount - 1} {
		id := fmt.Sprintf("code-a-%03d", i)
		if _, err := os.Stat(codingJobLogPath(id)); !os.IsNotExist(err) {
			t.Fatalf("最老的 %s 应当连日志一起清掉：%v", id, err)
		}
	}

	// 列表只读不删：几个实例共用目录时谁列一下都不该动别人的记录。
	if got := len(listCodingJobs()); got != 2+1+codingJobRetainCount {
		t.Fatalf("清理后还剩 %d 条记录", got)
	}
}

// 常驻放行按机器人分文件：A 的主人说过「以后都同意」，B 派的任务照样要问。
func TestCodingAlwaysAllowIsPerBot(t *testing.T) {
	useTempCodingWorkspace(t)
	rt := sharedDirRuntime(t, nil)
	pathA, okA := rt.codingAlwaysAllowPathFor("bot-a")
	pathB, okB := rt.codingAlwaysAllowPathFor("bot-b")
	if !okA || !okB || pathA == pathB {
		t.Fatalf("两台机器人应当各有一份清单：%q %q", pathA, pathB)
	}
	if err := rememberCodingAlwaysAllow(pathA, "git push", "10001", "push"); err != nil {
		t.Fatal(err)
	}
	if got := loadCodingAlwaysAllow(pathB); len(got) != 0 {
		t.Fatalf("A 的常驻放行漏到了 B：%v", got)
	}
	policyB := codingApprovalPolicy{Mode: codingApprovalModeDangerous, Patterns: defaultCodingApprovalPatterns(), AlwaysAllow: loadCodingAlwaysAllow(pathB)}
	if _, needed := codingHookNeedsApproval(policyB, "Bash", "git push origin main"); !needed {
		t.Fatal("B 派的任务不该因为 A 说过以后都同意就不问")
	}
	// 认不出是哪台机器人（多机器人、没带档案 ID）就不给路径，也就记不下来。
	if _, ok := rt.codingAlwaysAllowPathFor(""); ok {
		t.Fatal("多机器人时空档案 ID 不该归到任何一台")
	}
	if err := rememberCodingAlwaysAllow("", "git push", "10001", "push"); err == nil {
		t.Fatal("没有清单路径时不该记成功")
	}

	// 编码工具的 approvals 只看得到、清得掉这台自己的。
	settings := codingSettings(map[string]any{codingAgentSettingWorkspaces: "demo=" + t.TempDir()})
	raw, err := newDianaCodingTool(rt, ownerEventB(), settings).Run(context.Background(), map[string]any{"operation": "approvals", "clear": true})
	if err != nil {
		t.Fatalf("B 清空：%v", err)
	}
	if !strings.Contains(raw, "已清空 0 条") {
		t.Fatalf("B 清空时不该碰 A 的清单：%s", raw)
	}
	if got := loadCodingAlwaysAllow(pathA); len(got) != 1 {
		t.Fatalf("B 清空后 A 的清单变了：%v", got)
	}
	raw, err = newDianaCodingTool(rt, ownerEventA(), settings).Run(context.Background(), map[string]any{"operation": "approvals"})
	if err != nil || !strings.Contains(raw, "git push") {
		t.Fatalf("A 查自己的清单：%s %v", raw, err)
	}
}

// 旧的共享清单按记录人认领：谁点的头归谁，认不出的不搬；搬过一次、清空之后不会再
// 被搬回来；旧文件本身不动，共用目录的其他实例还要从里面认领。
func TestCodingAlwaysAllowMigratesLegacySharedFile(t *testing.T) {
	useTempCodingWorkspace(t)
	legacy := []codingAlwaysAllowEntry{
		{Pattern: "git push", OwnerID: "10001"},
		{Pattern: "rm -rf", OwnerID: "10002"},
		{Pattern: "sudo"},
	}
	if err := writeCodingAlwaysAllow(codingLegacyAlwaysAllowPath(), legacy); err != nil {
		t.Fatal(err)
	}
	legacyBody, _ := os.ReadFile(codingLegacyAlwaysAllowPath())

	rt := sharedDirRuntime(t, nil)
	pathA, _ := rt.codingAlwaysAllowPathFor("bot-a")
	pathB, _ := rt.codingAlwaysAllowPathFor("bot-b")
	if got := loadCodingAlwaysAllow(pathA); len(got) != 1 || got[0] != "git push" {
		t.Fatalf("A 认领到 %v，应当只有自己主人点过头的 git push", got)
	}
	if got := loadCodingAlwaysAllow(pathB); len(got) != 1 || got[0] != "rm -rf" {
		t.Fatalf("B 认领到 %v，应当只有 rm -rf", got)
	}

	if removed, err := forgetCodingAlwaysAllow(pathA); err != nil || removed != 1 {
		t.Fatalf("清空返回 %d, %v", removed, err)
	}
	pathA, _ = rt.codingAlwaysAllowPathFor("bot-a")
	if got := loadCodingAlwaysAllow(pathA); len(got) != 0 {
		t.Fatalf("清空之后旧条目又被搬回来了：%v", got)
	}
	if body, _ := os.ReadFile(codingLegacyAlwaysAllowPath()); string(body) != string(legacyBody) {
		t.Fatalf("旧共享清单被改写了：%s", body)
	}

	// 单机器人部署：没记录人的旧条目归它，别人点过头的仍然不搬。
	useTempCodingWorkspace(t)
	if err := writeCodingAlwaysAllow(codingLegacyAlwaysAllowPath(), legacy); err != nil {
		t.Fatal(err)
	}
	single := NewRuntime(BotConfig{ID: "bot-a", OwnerID: "10001"}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	path, _ := single.codingAlwaysAllowPathFor("")
	got := strings.Join(loadCodingAlwaysAllow(path), ",")
	if got != "git push,sudo" {
		t.Fatalf("单机器人认领到 %q", got)
	}
}

// 确认码只认任务所属那台机器人的主人：B 的主人回 A 的码不算数，也不会被吞掉。
func TestCodingApprovalReplyOnlyFromOwningBot(t *testing.T) {
	useTempCodingWorkspace(t)
	rt := sharedDirRuntime(t, nil)
	request := codingApprovalRequest{ID: "req-a", JobID: "code-a", Tool: "Bash", Detail: "git push origin main", Pattern: "git push", ProfileID: "bot-a"}
	allow, always, deny := codingApprovalCodes(request)
	wait := &codingApprovalWait{request: request, allowCode: allow, alwaysCode: always, denyCode: deny, decided: make(chan codingApprovalResponse, 1)}
	rt.codingJobs().registerApproval(wait)

	if reply, handled := rt.handleOwnerCommand(ownerEventB(), always); handled && strings.Contains(reply, "code-a") {
		t.Fatalf("B 的主人替 A 的任务点了头：%q", reply)
	}
	select {
	case decision := <-wait.decided:
		t.Fatalf("B 的回复被当成了裁决：%#v", decision)
	default:
	}
	pathB, _ := rt.codingAlwaysAllowPathFor("bot-b")
	if got := loadCodingAlwaysAllow(pathB); len(got) != 0 {
		t.Fatalf("B 的清单被记上了：%v", got)
	}

	reply, handled := rt.handleOwnerCommand(ownerEventA(), always)
	if !handled || !strings.Contains(reply, "code-a") {
		t.Fatalf("A 的主人回码没被认出来：handled=%v reply=%q", handled, reply)
	}
	if decision := <-wait.decided; !decision.Allow {
		t.Fatalf("A 的主人同意了却没放行：%#v", decision)
	}
	pathA, _ := rt.codingAlwaysAllowPathFor("bot-a")
	if got := loadCodingAlwaysAllow(pathA); len(got) != 1 || got[0] != "git push" {
		t.Fatalf("常驻放行应当记在 A 的清单里：%v", got)
	}
}

func twoBotReminderStore() *stubReminderStore {
	return &stubReminderStore{items: []Reminder{
		{ID: "rem-a", Kind: ReminderKindMessage, ProfileID: "bot-a", OwnerID: "20001", UserID: "20001", Message: "A 的提醒", TriggerAt: time.Now().Add(time.Hour)},
		{ID: "rem-b", Kind: ReminderKindMessage, ProfileID: "bot-b", OwnerID: "20002", UserID: "20002", Message: "B 的提醒原文", TriggerAt: time.Now().Add(time.Hour)},
		{ID: "rss-a", Kind: ReminderKindRSSWatch, ProfileID: "bot-a", OwnerID: "webui:bot-a", GroupID: "30001", FeedURL: "https://example.com/a.xml", IntervalSeconds: 900},
		{ID: "rss-b", Kind: ReminderKindRSSWatch, ProfileID: "bot-b", OwnerID: "webui:bot-b", GroupID: "30002", FeedURL: "https://example.com/b.xml", IntervalSeconds: 900},
		{ID: "repo-a", Kind: ReminderKindRepositoryWatch, ProfileID: "bot-a", OwnerID: "webui:bot-a", Repository: "acme/a", IntervalSeconds: 300},
		{ID: "repo-b", Kind: ReminderKindRepositoryWatch, ProfileID: "bot-b", OwnerID: "webui:bot-b", Repository: "acme/b", IntervalSeconds: 300},
	}}
}

// 提醒 列表 / 提醒 删除 是主人命令，只管这台机器人名下的条目。
func TestReminderOwnerCommandsStayOnOwnBot(t *testing.T) {
	store := twoBotReminderStore()
	rt := sharedDirRuntime(t, store)

	listed, handled := rt.handleOwnerCommand(ownerEventA(), "提醒 列表")
	if !handled || !strings.Contains(listed, "rem-a") {
		t.Fatalf("A 的列表缺了自己的提醒：%q", listed)
	}
	if strings.Contains(listed, "rem-b") || strings.Contains(listed, "B 的提醒原文") {
		t.Fatalf("A 的主人看到了 B 的提醒：%q", listed)
	}

	reply, _ := rt.handleOwnerCommand(ownerEventA(), "提醒 删除 repo-b")
	if !strings.Contains(reply, "没有找到") {
		t.Fatalf("A 的主人删掉了 B 的仓库订阅：%q", reply)
	}
	if len(store.items) != 6 {
		t.Fatalf("B 的条目被删了：%#v", store.items)
	}
	reply, _ = rt.handleOwnerCommand(ownerEventB(), "提醒 删除 repo-b")
	if reply != "提醒已删除。" || len(store.items) != 5 {
		t.Fatalf("B 的主人删自己的订阅：%q items=%d", reply, len(store.items))
	}
}

// 聊天工具里的「查全部」「查指定用户」、RSS 和仓库订阅的主人视角同样只到本机器人为止。
func TestOwnerTaskToolsStayOnOwnBot(t *testing.T) {
	store := twoBotReminderStore()
	rt := sharedDirRuntime(t, store)
	ctx := context.Background()

	raw, err := newDianaTasksTool(rt, ownerEventA()).Run(ctx, map[string]any{"operation": "list", "scope": "all"})
	if err != nil {
		t.Fatal(err)
	}
	var tasks dianaTasksResult
	if err := json.Unmarshal([]byte(raw), &tasks); err != nil {
		t.Fatal(err)
	}
	for _, item := range tasks.Items {
		if strings.HasSuffix(item.ID, "-b") {
			t.Fatalf("scope=all 列出了 B 的条目：%#v", item)
		}
	}
	if len(tasks.Items) != 3 {
		t.Fatalf("scope=all 应当列出 A 的 3 条，实际 %d", len(tasks.Items))
	}
	raw, err = newDianaTasksTool(rt, ownerEventA()).Run(ctx, map[string]any{"operation": "list", "target_user_id": "20002"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(raw, "rem-b") {
		t.Fatalf("按用户查到了 B 那边的提醒：%s", raw)
	}

	rss := newDianaRSSWatchTool(rt, ownerEventA())
	listed := runRSSOwnerTool(t, rss, map[string]any{"operation": "list"})
	if len(listed.Items) != 1 || listed.Items[0].ID != "rss-a" {
		t.Fatalf("A 的主人列出的 RSS 订阅：%#v", listed.Items)
	}
	for _, op := range []string{"cancel", "delete"} {
		if _, err := rss.Run(ctx, map[string]any{"operation": op, "id": "rss-b"}); err == nil || !strings.Contains(err.Error(), "没有找到") {
			t.Fatalf("A 的主人 %s 了 B 的 RSS 订阅：%v", op, err)
		}
	}
	for _, item := range store.items {
		if item.ID == "rss-b" && !item.CancelledAt.IsZero() {
			t.Fatal("B 的 RSS 订阅被取消了")
		}
	}

	repo := newDianaRepositoryWatchTool(rt, ownerEventA(), true, nil, nil)
	raw, err = repo.Run(ctx, map[string]any{"operation": "list"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(raw, "acme/b") || !strings.Contains(raw, "acme/a") {
		t.Fatalf("A 的主人看到的仓库订阅：%s", raw)
	}
	if _, err := repo.Run(ctx, map[string]any{"operation": "delete", "id": "repo-b"}); err == nil {
		t.Fatal("A 的主人删掉了 B 的仓库订阅")
	}
}

// 休息时段的提示按会话限流，两台机器人在同一个群里各算各的。
func TestQuietNoticeRateLimitIsPerBot(t *testing.T) {
	rt := sharedDirRuntime(t, nil)
	groupA := MessageEvent{Kind: EventKindGroup, ProfileID: "bot-a", ContextNamespace: "bot-a", GroupID: "30001", UserID: "20001"}
	groupB := groupA
	groupB.ProfileID, groupB.ContextNamespace = "bot-b", "bot-b"
	if !rt.allowQuietNotice(groupA) {
		t.Fatal("A 第一次提示应当放行")
	}
	if rt.allowQuietNotice(groupA) {
		t.Fatal("A 一小时内第二次提示应当被限流")
	}
	if !rt.allowQuietNotice(groupB) {
		t.Fatal("A 发过提示不该让 B 在同一个群里闭嘴")
	}
}

// 插件后台任务的去重只在同一个会话里算：两台机器人收到同一份扫描件，各自都要有结果。
func TestPluginTaskDedupIsPerSession(t *testing.T) {
	rt := sharedDirRuntime(t, nil)
	task := PluginTask{Kind: "document_ocr", Name: "扫描件识别", Key: "document_ocr:same-file", Run: func(context.Context, PluginTaskServices) (PluginTaskResult, error) {
		return PluginTaskResult{}, nil
	}}
	eventA := MessageEvent{Kind: EventKindGroup, ProfileID: "bot-a", ContextNamespace: "bot-a", GroupID: "30001", UserID: "20001"}
	eventB := eventA
	eventB.ProfileID, eventB.ContextNamespace = "bot-b", "bot-b"

	first := rt.reservePluginTasks(eventA, []PluginTask{task})
	second := rt.reservePluginTasks(eventB, []PluginTask{task})
	defer rt.cancelPluginTaskReservation(first)
	defer rt.cancelPluginTaskReservation(second)
	if len(first.reserved) != 1 || len(second.reserved) != 1 || len(second.duplicates) != 0 {
		t.Fatalf("B 的任务被当成了 A 的重复：first=%d second=%d dup=%d", len(first.reserved), len(second.reserved), len(second.duplicates))
	}
	// 同一个会话里重复发起仍然去重。
	again := rt.reservePluginTasks(eventA, []PluginTask{task})
	if len(again.reserved) != 0 || len(again.duplicates) != 1 {
		t.Fatalf("同一会话的重复任务没去重：%#v", again)
	}
}

// 发附件、看图和 read_file 共用一份凭据名单，MCP 配置不能换个工具就发出去。
func TestLocalAttachmentRefusesRuntimeCredentialFiles(t *testing.T) {
	root := t.TempDir()
	t.Setenv("APP_DB_PATH", filepath.Join(root, "diana.db"))
	workspace := AgentWorkspaceDir()
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, ".mcp.json"), []byte(`{"mcpServers":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	rt := sharedDirRuntime(t, nil)
	for _, tool := range []*dianaLocalAttachmentTool{
		{runtime: rt, event: ownerEventB()},
		{runtime: rt, event: ownerEventB(), view: true},
	} {
		input := map[string]any{"path": ".mcp.json", "mode": "file"}
		if tool.view {
			input = map[string]any{"path": ".mcp.json"}
		}
		if _, err := tool.Run(context.Background(), input); err == nil || !strings.Contains(err.Error(), "运行时配置") {
			t.Fatalf("%s 放过了 .mcp.json：%v", tool.Name(), err)
		}
	}
}
