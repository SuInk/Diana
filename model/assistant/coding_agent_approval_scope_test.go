// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 线上第一次真实派活，主人被连问了七八次，其中多数是只读命令：
// gh release view、git status、git branch、gh pr view、go test。
// 原因有两个：模式写到了命令族一级（"gh release" 连 view 一起命中），
// 以及整条命令做子串匹配，heredoc 里的发版说明正文也算数。
func TestCodingApprovalDoesNotAskForReadOnlyCommands(t *testing.T) {
	policy := codingApprovalPolicy{Mode: codingApprovalModeDangerous, Patterns: defaultCodingApprovalPatterns()}
	readOnly := []string{
		`cd "/w/diana" && gh release view v0.8.117 --json name,body`,
		`cd "/w/diana" && git branch --show-current && git status --short && git log --oneline v0.8.117..origin/main && git tag -l v0.8.118`,
		`cd "/w/diana" && gh pr view 549 --json mergeable,state`,
		`go test ./model/assistant/ -run Coding -count=1`,
		`git diff --stat`,
	}
	for _, command := range readOnly {
		if pattern, needed := codingHookNeedsApproval(policy, "Bash", command); needed {
			t.Errorf("只读命令被拦下了（命中 %q）：%s", pattern, command)
		}
	}

	// 真危险的仍然要问，包括藏在 && 链中间的那一段。
	dangerous := map[string]string{
		`cd "/w/diana" && git switch -c chore/release && git commit -qam bump && git push -u origin chore/release`: "git push",
		`cd "/w/diana" && gh pr view 549 --json state && gh pr merge 549 --squash`:                                 "gh pr merge",
		`gh release create v0.8.118 --notes-file /tmp/notes.md`:                                                    "gh release create",
		`sudo launchctl bootout gui/501 com.suink.diana-runtime`:                                                   "sudo",
	}
	for command, want := range dangerous {
		pattern, needed := codingHookNeedsApproval(policy, "Bash", command)
		if !needed || pattern != want {
			t.Errorf("危险命令判断错了：%s → 命中 %q needed=%v，期望 %q", command, pattern, needed, want)
		}
	}
}

// 写发版说明时 heredoc 正文里有一键安装命令 curl … | sudo sh，线上因此要主人点头。
// 正文是数据不是命令，不参与判断；但 heredoc 结束之后的命令照常判断。
func TestCodingApprovalIgnoresHeredocBody(t *testing.T) {
	policy := codingApprovalPolicy{Mode: codingApprovalModeDangerous, Patterns: defaultCodingApprovalPatterns()}
	notes := "cat > /tmp/notes.md <<'EOF'\n## 安装\ncurl -fsSL https://example.com/install.sh | sudo sh\n然后 git push 就发出去了\nEOF"
	if pattern, needed := codingHookNeedsApproval(policy, "Bash", notes); needed {
		t.Errorf("heredoc 正文触发了确认（命中 %q）", pattern)
	}
	withPush := notes + "\ncd /w/diana && git push origin main"
	if pattern, _ := codingHookNeedsApproval(policy, "Bash", withPush); pattern != "git push" {
		t.Errorf("heredoc 之后的真命令漏判了：命中 %q", pattern)
	}
}

// 主人回「以后都同意」之后，同类操作不再问；清空之后恢复询问。
func TestCodingApprovalRemembersAlwaysAllow(t *testing.T) {
	root := t.TempDir()
	t.Setenv("APP_DB_PATH", filepath.Join(root, "diana.db"))
	policy := codingApprovalPolicy{
		Mode: codingApprovalModeDangerous, Patterns: defaultCodingApprovalPatterns(),
		AlwaysAllowPath: codingAlwaysAllowPath("bot-a"),
	}
	path := policy.AlwaysAllowPath
	const command = `cd "/w/diana" && git push origin main`
	if _, needed := codingHookNeedsApproval(policy, "Bash", command); !needed {
		t.Fatal("一开始就该问")
	}
	if err := rememberCodingAlwaysAllow(path, "git push", "30007", command); err != nil {
		t.Fatal(err)
	}
	// hook 每次执行都现读清单：同一个任务里说过之后，后面的步骤就不再问。
	policy.AlwaysAllow = loadCodingAlwaysAllow(policy.AlwaysAllowPath)
	if pattern, needed := codingHookNeedsApproval(policy, "Bash", command); needed {
		t.Errorf("说过以后都同意，仍然在问（命中 %q）", pattern)
	}
	// 别的危险操作不受影响。
	if _, needed := codingHookNeedsApproval(policy, "Bash", "rm -rf /w/diana/build"); !needed {
		t.Error("只该放行说过的那一类")
	}
	// 记两次不重复。
	if err := rememberCodingAlwaysAllow(path, "GIT PUSH", "30007", command); err != nil {
		t.Fatal(err)
	}
	if entries := readCodingAlwaysAllow(path); len(entries) != 1 {
		t.Fatalf("同一类记了 %d 条", len(entries))
	}
	if !strings.HasPrefix(path, filepath.Clean(root)) {
		t.Fatalf("清单没落在工作区里：%s", path)
	}
	removed, err := forgetCodingAlwaysAllow(path)
	if err != nil || removed != 1 {
		t.Fatalf("清空返回 %d, %v", removed, err)
	}
	policy.AlwaysAllow = loadCodingAlwaysAllow(policy.AlwaysAllowPath)
	if _, needed := codingHookNeedsApproval(policy, "Bash", command); !needed {
		t.Error("清空之后应当重新问")
	}
}

// 端到端：主人回常驻放行码，这一步放行，同类操作被记住，询问消息里也确实给了这个码。
func TestCodingApprovalAlwaysCodeRemembersAndContinues(t *testing.T) {
	useTempCodingWorkspace(t)
	channel := &concurrentRecordingChannel{}
	rt := NewRuntime(BotConfig{BotAccount: "42", OwnerID: "1"}, channel, NewPluginManager(NewCodingAgentPlugin()), nil, nil, nil, nil)
	dir := codingApprovalDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	job := CodingJob{
		ID: "code-always", Workspace: "demo", Status: codingJobStatusRunning,
		StartedAt: time.Now(), ApprovalMode: codingApprovalModeDangerous,
		Target: codingJobTarget{UserID: "1"},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go rt.watchCodingApprovals(ctx, job, 30*time.Second)

	request := codingApprovalRequest{
		ID: "req-always", JobID: job.ID, Tool: "Bash",
		Detail: `cd "/w/demo" && git push origin main`, Pattern: "git push", CreatedAt: time.Now(),
	}
	body, _ := json.Marshal(request)
	if err := os.WriteFile(codingApprovalRequestPath(dir, request.ID), body, 0o600); err != nil {
		t.Fatal(err)
	}
	waitForCondition(t, 10*time.Second, func() bool { return channel.count() >= 1 })
	asked := channel.messages()[0].Text
	_, alwaysCode, _ := codingApprovalCodes(request)
	if !strings.Contains(asked, alwaysCode) || !strings.Contains(asked, "以后都同意") || !strings.Contains(asked, "git push") {
		t.Fatalf("询问消息里没给出常驻放行的说法：%q", asked)
	}

	reply, handled := rt.handleOwnerCommand(MessageEvent{Kind: EventKindPrivate, UserID: "1"}, alwaysCode)
	if !handled || !strings.Contains(reply, "不再问") {
		t.Fatalf("handled=%v reply=%q", handled, reply)
	}
	waitForCondition(t, 10*time.Second, func() bool {
		_, err := os.Stat(codingApprovalResponsePath(dir, request.ID))
		return err == nil
	})
	decision, err := os.ReadFile(codingApprovalResponsePath(dir, request.ID))
	if err != nil || !strings.Contains(string(decision), `"allow":true`) {
		t.Fatalf("这一步应当放行：%s %v", decision, err)
	}
	entries := readCodingAlwaysAllow(codingAlwaysAllowPath(""))
	if len(entries) != 1 || entries[0].Pattern != "git push" || entries[0].OwnerID != "1" {
		t.Fatalf("常驻放行没记对：%#v", entries)
	}
}
