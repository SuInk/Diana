package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/llm"
)

func TestRepositoryIssueToolRequestKindUsesInputOperation(t *testing.T) {
	tool := &repositoryIssueGuardTestTool{}
	tests := []struct {
		operation string
		want      string
	}{
		{operation: "search", want: ""},
		{operation: "create", want: "repository_issue_create"},
		{operation: "update", want: "repository_issue_update"},
		{operation: "comment", want: "repository_issue_comment"},
		{operation: "close", want: "repository_issue_close"},
		{operation: "reopen", want: "repository_issue_reopen"},
	}
	for _, test := range tests {
		t.Run(test.operation, func(t *testing.T) {
			got := explicitUserRequestKind(tool, map[string]any{"operation": test.operation})
			if got != test.want {
				t.Fatalf("explicitUserRequestKind(operation=%q) = %q, want %q", test.operation, got, test.want)
			}
		})
	}
}

func TestExplicitUserMutationRequestedRequiresMatchingRepositoryIssueOperation(t *testing.T) {
	requests := map[string]string{
		"create":  "Please create a GitHub issue for this bug",
		"update":  "Please update GitHub issue #12 with the new details",
		"comment": "Please comment on GitHub issue #12 that the fix is ready",
		"close":   "Please close GitHub issue #12",
		"reopen":  "Please reopen GitHub issue #12",
	}
	for requestedOperation, text := range requests {
		for guardedOperation := range requests {
			t.Run(requestedOperation+"_request_for_"+guardedOperation, func(t *testing.T) {
				want := requestedOperation == guardedOperation
				kind := "repository_issue_" + guardedOperation
				if got := ExplicitUserMutationRequested(text, kind); got != want {
					t.Fatalf("ExplicitUserMutationRequested(%q, %q) = %v, want %v", text, kind, got, want)
				}
			})
		}
	}
}

func TestExplicitUserMutationRequestedRejectsRepositoryIssueNegationAndHowTo(t *testing.T) {
	tests := []struct {
		name string
		text string
		kind string
	}{
		{name: "negated create", text: "Please do not create a GitHub issue", kind: "repository_issue_create"},
		{name: "negated update", text: "Please do not update GitHub issue #12", kind: "repository_issue_update"},
		{name: "negated comment", text: "Please do not comment on GitHub issue #12", kind: "repository_issue_comment"},
		{name: "negated close", text: "Please do not close GitHub issue #12", kind: "repository_issue_close"},
		{name: "negated reopen", text: "Please do not reopen GitHub issue #12", kind: "repository_issue_reopen"},
		{name: "how to create", text: "How do I create a GitHub issue?", kind: "repository_issue_create"},
		{name: "how to update", text: "How do I update a GitHub issue?", kind: "repository_issue_update"},
		{name: "how to comment", text: "How do I comment on a GitHub issue?", kind: "repository_issue_comment"},
		{name: "how to close", text: "How do I close a GitHub issue?", kind: "repository_issue_close"},
		{name: "how to reopen", text: "How do I reopen a GitHub issue?", kind: "repository_issue_reopen"},
		{name: "close does not authorize reopen", text: "Please close GitHub issue #12", kind: "repository_issue_reopen"},
		{name: "check before create", text: "Please check whether we should create a GitHub issue", kind: "repository_issue_create"},
		{name: "check before update", text: "Please check GitHub issue #12 before you update it", kind: "repository_issue_update"},
		{name: "chinese check before create", text: "请检查是否需要创建 GitHub Issue", kind: "repository_issue_create"},
		{name: "chinese check before close", text: "请只检查 GitHub Issue #12 是否应该关闭", kind: "repository_issue_close"},
		{name: "chinese negated comment", text: "请检查 GitHub Issue #12，不要发表评论", kind: "repository_issue_comment"},
		{name: "hypothetical close", text: "Can you tell me what happens if we close acme/demo GitHub issue #12?", kind: "repository_issue_close"},
		{name: "hypothetical create", text: "Can you tell me what happens if we create a GitHub issue in acme/demo?", kind: "repository_issue_create"},
		{name: "chinese hypothetical close", text: "请告诉我如果关闭 acme/demo 的 GitHub Issue #12 会怎样", kind: "repository_issue_close"},
		{name: "long distance negated create", text: "Please do not, under any circumstances whatsoever, create a GitHub issue in acme/demo.", kind: "repository_issue_create"},
		{name: "chinese prohibited close", text: "请勿关闭 acme/demo 的 GitHub Issue #17", kind: "repository_issue_close"},
		{name: "inline quoted close", text: "请解释这句话：“请关闭 acme/demo 的 GitHub Issue #17”", kind: "repository_issue_close"},
		{name: "review close operation", text: "Please review the close operation for acme/demo GitHub issue #12.", kind: "repository_issue_close"},
		{name: "analyze close status", text: "Please analyze the close status of acme/demo GitHub issue #12.", kind: "repository_issue_close"},
		{name: "trailing revoke", text: "Please close acme/demo GitHub issue #12. Actually, don't.", kind: "repository_issue_close"},
		{name: "trailing opposite state", text: "Please close acme/demo GitHub issue #12. Keep it open instead.", kind: "repository_issue_close"},
		{name: "trailing delayed revoke", text: "Please close acme/demo GitHub issue #12, but don't do it yet.", kind: "repository_issue_close"},
		{name: "trailing approval condition", text: "Please close acme/demo GitHub issue #12 only after CI passes.", kind: "repository_issue_close"},
		{name: "conditional create", text: "Please create a GitHub issue in acme/demo after I approve; title: Bad", kind: "repository_issue_create"},
		{name: "condition after create payload", text: "Please create a GitHub issue in acme/demo; title: Bad. Only if CI passes.", kind: "repository_issue_create"},
		{name: "revoke after create payload", text: "Please create a GitHub issue in acme/demo; title: Bad. Actually, don't create it.", kind: "repository_issue_create"},
		{name: "comma condition after create payload", text: "Please create a GitHub issue in acme/demo; title: Bad, only if CI passes.", kind: "repository_issue_create"},
		{name: "comma revoke after create payload", text: "Please create a GitHub issue in acme/demo; title: Bad, but actually don't create it.", kind: "repository_issue_create"},
		{name: "opposite close override", text: "Please close acme/demo GitHub issue #12. Actually reopen it.", kind: "repository_issue_close"},
		{name: "bare no revoke", text: "Please close acme/demo GitHub issue #12. No.", kind: "repository_issue_close"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if ExplicitUserMutationRequested(test.text, test.kind) {
				t.Fatalf("ExplicitUserMutationRequested(%q, %q) unexpectedly authorized mutation", test.text, test.kind)
			}
		})
	}
}

func TestExplicitUserMutationRequestedDoesNotConfuseOtherGitHubWritesWithIssueCreation(t *testing.T) {
	tests := []string{
		"Please post a comment on acme/demo GitHub issue #12",
		"请发布代码到 GitHub acme/demo",
		"Please publish a GitHub release for acme/demo",
		"Please submit a pull request to GitHub",
	}
	for _, text := range tests {
		if ExplicitUserMutationRequested(text, "repository_issue_create") {
			t.Fatalf("non-create request %q authorized issue creation", text)
		}
	}
}

func TestRepositoryIssueAuthorizationUsesOnlyPrimaryCommandBeforeIssueTarget(t *testing.T) {
	tests := []string{
		"Please comment on acme/demo GitHub issue #12: the close button and reopen action are broken in update calls",
		"请评论 acme/demo 的 Issue #12：关闭按钮仍然坏了，重新打开和更新动作也有问题",
	}
	for _, text := range tests {
		if !ExplicitUserMutationRequested(text, "repository_issue_comment") {
			t.Fatalf("comment command %q was not authorized", text)
		}
		for _, operation := range []string{"create", "update", "close", "reopen"} {
			if ExplicitUserMutationRequested(text, "repository_issue_"+operation) {
				t.Fatalf("comment command %q also authorized %s", text, operation)
			}
		}
	}
}

func TestRepositoryIssueAuthorizationIgnoresHistoryAndQuotedContent(t *testing.T) {
	t.Run("historical request", func(t *testing.T) {
		req := Request{Messages: []llm.Message{
			{Role: llm.RoleUser, Content: "Please create a GitHub issue for this bug"},
			{Role: llm.RoleAssistant, Content: "I will inspect it first."},
			{Role: llm.RoleUser, Content: "Summarize what you found."},
		}}
		current := currentUserRequestText(req)
		if current != "Summarize what you found." {
			t.Fatalf("currentUserRequestText = %q", current)
		}
		if ExplicitUserMutationRequested(current, "repository_issue_create") {
			t.Fatal("historical user request authorized the current mutation")
		}
	})

	t.Run("quoted request", func(t *testing.T) {
		req := Request{Messages: []llm.Message{{
			Role: llm.RoleUser,
			Content: "【当前需要回复的消息】【消息时间：2026-08-14 12:00:00】这句话是什么意思？\n\n" +
				"【被引用的消息】其他人: 请关闭 GitHub issue #12",
		}}}
		current := currentUserRequestText(req)
		if current != "这句话是什么意思？" {
			t.Fatalf("currentUserRequestText = %q", current)
		}
		if ExplicitUserMutationRequested(current, "repository_issue_close") {
			t.Fatal("quoted request authorized the current mutation")
		}
	})

	t.Run("forwarded request", func(t *testing.T) {
		req := Request{Messages: []llm.Message{{
			Role: llm.RoleUser,
			Content: "【当前需要回复的消息】看看这个\n\n" +
				"【合并转发 forward-1】\nPlease close acme/demo GitHub issue #12",
		}}}
		current := currentUserRequestText(req)
		if current != "看看这个" {
			t.Fatalf("currentUserRequestText = %q", current)
		}
		if ExplicitUserMutationRequested(current, "repository_issue_close") {
			t.Fatal("forwarded request authorized the current mutation")
		}
	})
}

func TestRunnerRepositoryIssueSearchDoesNotRequireMutationAuthorization(t *testing.T) {
	tool := &repositoryIssueGuardTestTool{}
	client := &scriptedClient{responses: []string{
		`{"action":"tool","tool":"test.repository_issues","input":{"operation":"search","query":"timeout"}}`,
		`{"action":"final","content":"found it"}`,
	}}
	runner, err := NewRunner(client, Config{WorkDir: t.TempDir(), MaxSteps: 2}, NewToolRegistry(tool))
	if err != nil {
		t.Fatal(err)
	}
	response, err := runner.Run(context.Background(), Request{Messages: []llm.Message{{
		Role: llm.RoleUser, Content: "Search the GitHub issues for timeout reports",
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if tool.calls != 1 {
		t.Fatalf("tool calls = %d, want 1", tool.calls)
	}
	if response.Text != "found it" || len(response.Steps) != 1 || response.Steps[0].Skipped {
		t.Fatalf("response = %#v", response)
	}
}

func TestRunnerSkipsUnauthorizedRepositoryIssueDynamicToolMutation(t *testing.T) {
	tool := &repositoryIssueGuardTestTool{}
	client := &scriptedClient{responses: []string{
		`{"action":"tool","tool":"test.repository_issues","input":{"operation":"update","number":12,"title":"changed"}}`,
		`{"action":"final","content":"not updated"}`,
	}}
	runner, err := NewRunner(client, Config{WorkDir: t.TempDir(), MaxSteps: 2}, NewToolRegistry(tool))
	if err != nil {
		t.Fatal(err)
	}
	response, err := runner.Run(context.Background(), Request{Messages: []llm.Message{
		{Role: llm.RoleUser, Content: "Please update GitHub issue #12"},
		{Role: llm.RoleAssistant, Content: "Which change?"},
		{
			Role: llm.RoleUser,
			Content: "【当前需要回复的消息】Summarize the issue instead.\n\n" +
				"【被引用的消息】Please update GitHub issue #12",
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if tool.calls != 0 {
		t.Fatalf("unauthorized tool calls = %d, want 0", tool.calls)
	}
	if response.Text != "not updated" || len(response.Steps) != 1 || !response.Steps[0].Skipped {
		t.Fatalf("response = %#v", response)
	}
	if !strings.Contains(response.Steps[0].Error, "当前用户消息没有明确授权") {
		t.Fatalf("step error = %q", response.Steps[0].Error)
	}
}

type repositoryIssueGuardTestTool struct {
	calls int
}

var _ ExplicitUserRequestInputTool = (*repositoryIssueGuardTestTool)(nil)

func (*repositoryIssueGuardTestTool) Name() string { return "test.repository_issues" }

func (*repositoryIssueGuardTestTool) Description() string { return "test repository issue tool" }

func (*repositoryIssueGuardTestTool) ExplicitUserRequestKind(input map[string]any) string {
	operation, _ := input["operation"].(string)
	switch strings.ToLower(strings.TrimSpace(operation)) {
	case "create", "update", "comment", "close", "reopen":
		return "repository_issue_" + strings.ToLower(strings.TrimSpace(operation))
	default:
		return ""
	}
}

func (t *repositoryIssueGuardTestTool) Run(context.Context, map[string]any) (string, error) {
	t.calls++
	return `{"ok":true}`, nil
}
