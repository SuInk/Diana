// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"strings"
	"testing"
)

func credentialSettings() SettingValues {
	return SettingValues{
		repositoryCredentialSettingList:     `[{"id":"c1","name":"个人 Token","auth":"token"},{"id":"c2","name":"组织 gh","auth":"gh"}]`,
		repositoryCredentialSettingTokens:   `{"c1":"personal-token"}`,
		repositoryCredentialSettingBindings: `{"acme/demo":"c1","Acme/Org":"c2"}`,
	}
}

func TestRepositoryCredentialForResolvesPerRepository(t *testing.T) {
	settings := credentialSettings()

	credential, token, ok := repositoryCredentialFor("acme/demo", settings)
	if !ok || token != "personal-token" || credential.label() != "个人 Token" {
		t.Fatalf("token credential = %#v token=%q ok=%v", credential, token, ok)
	}
	// 仓库名大小写不敏感，绑定时的大小写不该影响匹配。
	credential, token, ok = repositoryCredentialFor("ACME/ORG", settings)
	if !ok || token != "" || credential.authMode() != repositoryCredentialAuthGH {
		t.Fatalf("gh credential = %#v token=%q ok=%v", credential, token, ok)
	}
	// 没绑定的仓库交回调用方走公共 Token。
	if _, _, ok := repositoryCredentialFor("acme/other", settings); ok {
		t.Fatal("unbound repository should fall back to the shared token")
	}
}

// 配置残缺时宁可退回公共 Token，也不要让仓库彻底不可用。
func TestRepositoryCredentialForFallsBackOnBrokenConfig(t *testing.T) {
	cases := map[string]SettingValues{
		"绑定指向已删除的凭据": {
			repositoryCredentialSettingList:     `[{"id":"c1","name":"a","auth":"token"}]`,
			repositoryCredentialSettingTokens:   `{"c1":"x"}`,
			repositoryCredentialSettingBindings: `{"acme/demo":"gone"}`,
		},
		"token 类型但没填 Token": {
			repositoryCredentialSettingList:     `[{"id":"c1","name":"a","auth":"token"}]`,
			repositoryCredentialSettingBindings: `{"acme/demo":"c1"}`,
		},
		"列表 JSON 坏掉": {
			repositoryCredentialSettingList:     `not json`,
			repositoryCredentialSettingBindings: `{"acme/demo":"c1"}`,
		},
		"绑定 JSON 坏掉": {
			repositoryCredentialSettingList:     `[{"id":"c1","name":"a","auth":"token"}]`,
			repositoryCredentialSettingTokens:   `{"c1":"x"}`,
			repositoryCredentialSettingBindings: `not json`,
		},
	}
	for name, settings := range cases {
		if _, _, ok := repositoryCredentialFor("acme/demo", settings); ok {
			t.Fatalf("%s: should fall back", name)
		}
	}
}

func TestRepositoryFromGitHubAPIPath(t *testing.T) {
	cases := map[string]string{
		"/repos/acme/demo/commits?per_page=100": "acme/demo",
		"/repos/acme/demo":                      "acme/demo",
		"/repos/acme/demo/issues/12/comments":   "acme/demo",
		"/repos/acme/demo/compare/aaa...bbb":    "acme/demo",
		"/user/repos":                           "",
		"/repos/acme":                           "",
		"/repos//demo":                          "",
		"":                                      "",
	}
	for path, want := range cases {
		if got := repositoryFromGitHubAPIPath(path); got != want {
			t.Fatalf("path %q => %q, want %q", path, got, want)
		}
	}
}

// 仓库绑定的凭据要真的用在请求上，并盖过公共 Token。
func TestRepositoryPublishCredentialPrefersTheRepositoryBinding(t *testing.T) {
	manager := NewPluginManager(NewRepositoryWatchPlugin(nil))
	if _, err := manager.UpdateSettings(repositoryWatchPluginID, map[string]any{
		repositoryWatchSettingToken:         "shared-token",
		repositoryCredentialSettingList:     `[{"id":"c1","name":"组织 Token","auth":"token"},{"id":"c2","name":"服务器 gh","auth":"gh"}]`,
		repositoryCredentialSettingTokens:   `{"c1":"org-token"}`,
		repositoryCredentialSettingBindings: `{"acme/demo":"c1","acme/gh":"c2"}`,
	}); err != nil {
		t.Fatalf("seed credentials: %v", err)
	}
	runtime := NewRuntime(BotConfig{OwnerID: "owner"}, nilChannel{}, manager, nil, nil, nil, nil)
	event := MessageEvent{Kind: EventKindPrivate, UserID: "owner"}
	tool := newDianaRepositoryIssuesTool(runtime, event, &RepositoryPublishPlugin{}, SettingValues{repositoryPublishSettingToken: "publish-token"})

	// 绑定的仓库用绑定的凭据，而不是发布插件自己的公共 Token。
	token, apiErr := tool.repositoryPublishCredential(context.Background(), "acme/demo")
	if apiErr != nil || token != "org-token" {
		t.Fatalf("bound token=%q err=%#v", token, apiErr)
	}
	if !strings.Contains(tool.credentialSource, "组织 Token") {
		t.Fatalf("credential source = %q", tool.credentialSource)
	}
	// 没绑定的仓库仍走公共 Token。
	token, apiErr = tool.repositoryPublishCredential(context.Background(), "acme/other")
	if apiErr != nil || token != "publish-token" {
		t.Fatalf("unbound token=%q err=%#v", token, apiErr)
	}
	// 绑定到 gh 类型的凭据会走 gh CLI；这里没有 gh，应当明确报错而不是悄悄用公共 Token。
	if _, apiErr = tool.repositoryPublishCredential(context.Background(), "acme/gh"); apiErr == nil || apiErr.Code != "gh_unavailable" {
		t.Fatalf("gh credential should surface gh_unavailable, got %#v", apiErr)
	}
}
