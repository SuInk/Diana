// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRepositoryWatchTestCredentialsReportsAccountPerCredential(t *testing.T) {
	logins := map[string]string{"Bearer public-token": "DianaAgent", "Bearer org-token": "OrgBot", "Bearer gh-token": "SuInk"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		login, ok := logins[r.Header.Get("Authorization")]
		if !ok {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/user":
			_, _ = w.Write([]byte(`{"login":"` + login + `"}`))
		case "/repos/acme/demo":
			_, _ = w.Write([]byte(`{"full_name":"acme/demo","permissions":{"push":true}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	plugin := newRepositoryWatchPlugin(server.Client(), server.URL)
	plugin.ghAuthToken = func(context.Context) (string, error) { return "gh-token", nil }
	settings := SettingValues{
		repositoryWatchSettingToken:           "public-token",
		repositoryPublishSettingAuthMode:      "token",
		repositoryCredentialSettingList:       `[{"id":"org","name":"组织","auth":"token"},{"id":"cli","name":"本机","auth":"gh"},{"id":"bad","name":"坏的","auth":"token"},{"id":"empty","auth":"token"}]`,
		repositoryCredentialSettingTokens:     `{"org":"org-token","bad":"expired"}`,
		repositoryCredentialSettingBindings:   `{"acme/demo":"org","acme/secret":"org"}`,
		repositoryCredentialSettingConfigured: `["org","bad"]`,
	}
	checks := plugin.TestCredentials(context.Background(), settings)
	byKey := map[string]CredentialCheck{}
	for _, check := range checks {
		byKey[check.Key] = check
	}
	if len(checks) != 5 || checks[0].Key != RepositoryCredentialDefaultKey {
		t.Fatalf("checks=%#v", checks)
	}
	if got := byKey[RepositoryCredentialDefaultKey]; got.State != CredentialValid || got.Account != "DianaAgent" {
		t.Fatalf("default=%#v", got)
	}
	if got := byKey["org"]; got.State != CredentialValid || got.Account != "OrgBot" || got.Message != "绑定仓库：acme/demo 可写、acme/secret 看不到。" {
		t.Fatalf("org=%#v", got)
	}
	if got := byKey["cli"]; got.State != CredentialValid || got.Account != "SuInk" {
		t.Fatalf("gh=%#v", got)
	}
	if got := byKey["bad"]; got.State != CredentialInvalid {
		t.Fatalf("bad=%#v", got)
	}
	if got := byKey["empty"]; got.State != CredentialUnconfigured {
		t.Fatalf("empty=%#v", got)
	}

	// 默认凭据选 gh 时检测的是 gh 登录的账号，而不是公共 Token。
	settings[repositoryPublishSettingAuthMode] = "gh"
	if got := plugin.TestCredentials(context.Background(), settings)[0]; got.Account != "SuInk" {
		t.Fatalf("default gh=%#v", got)
	}
}
