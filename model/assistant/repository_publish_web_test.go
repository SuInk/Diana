package assistant

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRepositoryIssueWebCreateUsesSecurePublishingPlugin(t *testing.T) {
	github := newRepositoryPublishTestGitHub()
	server := httptest.NewServer(http.HandlerFunc(github.handler))
	defer server.Close()
	plugin := newRepositoryPublishPlugin(server.Client(), server.URL)

	result := plugin.CreateIssueFromWeb(context.Background(), SettingValues{
		repositoryPublishSettingToken:     repositoryPublishTestToken,
		repositoryPublishSettingAllowlist: "acme/demo",
	}, RepositoryIssueCreateInput{
		Repository: "https://github.com/acme/demo",
		Title:      "Login failure dev@example.com",
		Body:       "Steps to reproduce",
		Labels:     []string{"bug", "ui"},
	})

	if !result.OK || result.Outcome != "created" || result.Repository != "acme/demo" || result.Issue == nil || result.Issue.Number != 42 {
		t.Fatalf("result=%#v", result)
	}
	if result.Redactions == 0 || strings.Contains(result.Issue.Title, "dev@example.com") {
		t.Fatalf("sensitive title was not redacted: %#v", result)
	}
	request := github.last(http.MethodPost)
	if request.Path != "/repos/acme/demo/issues" || strings.Contains(stringMapValue(request.Payload, "title"), "dev@example.com") {
		t.Fatalf("request=%#v", request)
	}
}

func TestRepositoryIssueWebCreateRequiresTokenAndExactAllowlist(t *testing.T) {
	github := newRepositoryPublishTestGitHub()
	server := httptest.NewServer(http.HandlerFunc(github.handler))
	defer server.Close()
	plugin := newRepositoryPublishPlugin(server.Client(), server.URL)
	input := RepositoryIssueCreateInput{Repository: "acme/demo", Title: "Exact allowlist"}

	if result := plugin.CreateIssueFromWeb(context.Background(), SettingValues{
		repositoryPublishSettingAllowlist: "acme/demo",
	}, input); result.FailureCode != "token_required" {
		t.Fatalf("missing token result=%#v", result)
	}
	if result := plugin.CreateIssueFromWeb(context.Background(), SettingValues{
		repositoryPublishSettingToken:     repositoryPublishTestToken,
		repositoryPublishSettingAllowlist: "acme/demo-extra",
	}, input); result.FailureCode != "repository_not_allowed" {
		t.Fatalf("wrong allowlist result=%#v", result)
	}
	if github.count(http.MethodGet)+github.count(http.MethodPost) != 0 {
		t.Fatalf("rejected requests reached GitHub: %#v", github.requests)
	}
}

func TestRepositoryIssueWebDuplicateNeedsSecondConfirmation(t *testing.T) {
	github := newRepositoryPublishTestGitHub()
	github.issues = []githubRepositoryIssue{{
		Number: 12, Title: "Login fails after password reset", State: "open",
		HTMLURL: "https://github.com/acme/demo/issues/12", UpdatedAt: time.Now().UTC(),
	}}
	server := httptest.NewServer(http.HandlerFunc(github.handler))
	defer server.Close()
	plugin := newRepositoryPublishPlugin(server.Client(), server.URL)
	settings := SettingValues{
		repositoryPublishSettingToken:     repositoryPublishTestToken,
		repositoryPublishSettingAllowlist: "acme/demo",
	}
	input := RepositoryIssueCreateInput{Repository: "acme/demo", Title: "Login fails after password reset"}

	candidate := plugin.CreateIssueFromWeb(context.Background(), settings, input)
	if candidate.OK || !candidate.RequiresConfirmation || candidate.ConfirmationToken == "" || len(candidate.Candidates) != 1 || candidate.Candidates[0].Number != 12 {
		t.Fatalf("candidate result=%#v", candidate)
	}
	if github.count(http.MethodPost) != 0 {
		t.Fatalf("candidate check created an Issue: %#v", github.requests)
	}

	input.AllowDuplicate = true
	input.ConfirmationToken = candidate.ConfirmationToken
	input.CandidateNumber = candidate.Candidates[0].Number
	created := plugin.CreateIssueFromWeb(context.Background(), settings, input)
	if !created.OK || created.Outcome != "created" || created.Issue == nil || created.Issue.Number != 42 {
		t.Fatalf("confirmed result=%#v", created)
	}
	if github.count(http.MethodPost) != 1 {
		t.Fatalf("confirmed create issued %d POSTs", github.count(http.MethodPost))
	}
}
