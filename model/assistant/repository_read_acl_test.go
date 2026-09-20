// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// repositoryReadACLTestGitHub 是 #565 读权限矩阵的夹具：按仓库名区分公开/私有/
// 不存在，记录请求供「拒绝时什么都没发出去」一类断言使用。
type repositoryReadACLTestGitHub struct {
	mu       sync.Mutex
	requests []string
}

func (s *repositoryReadACLTestGitHub) handler(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.requests = append(s.requests, r.Method+" "+r.URL.Path)
	s.mu.Unlock()
	switch r.URL.Path {
	case "/repos/acme/public-repo":
		_ = json.NewEncoder(w).Encode(map[string]any{"private": false, "full_name": "acme/public-repo"})
	case "/repos/acme/private-repo":
		_ = json.NewEncoder(w).Encode(map[string]any{"private": true, "full_name": "acme/private-repo"})
	case "/repos/acme/public-repo/contents/README.md", "/repos/acme/private-repo/contents/README.md":
		content := base64.StdEncoding.EncodeToString([]byte("package main\n\nfunc main() {}\n"))
		_ = json.NewEncoder(w).Encode(map[string]any{
			"type": "file", "path": "README.md", "encoding": "base64", "content": content,
		})
	default:
		http.NotFound(w, r)
	}
}

func (s *repositoryReadACLTestGitHub) count(pred func(string) bool) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, req := range s.requests {
		if pred(req) {
			n++
		}
	}
	return n
}

func repositoryReadACLTool(server *httptest.Server, userID string, settings SettingValues) *dianaGitHubTool {
	runtime := NewRuntime(BotConfig{OwnerID: "owner"}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	return newDianaGitHubTool(
		runtime,
		MessageEvent{Kind: EventKindPrivate, UserID: userID, RawMessage: "读一下代码"},
		newRepositoryPublishPlugin(server.Client(), server.URL),
		settings,
	)
}

func runRepositoryReadACL(t *testing.T, tool *dianaGitHubTool, repository string) repositoryIssueResult {
	t.Helper()
	return runRepositoryPublishToolOnce(t, tool, map[string]any{
		"operation":  "read_file",
		"repository": repository,
		"path":       "README.md",
	})
}

func TestRepositoryReadPublicRepoOpenToEveryone(t *testing.T) {
	github := &repositoryReadACLTestGitHub{}
	server := httptest.NewServer(http.HandlerFunc(github.handler))
	defer server.Close()

	// 无全局白名单、无任何逐用户授权，普通用户也要能读公开仓库。
	tool := repositoryReadACLTool(server, "stranger", SettingValues{
		repositoryPublishSettingToken:   repositoryPublishTestToken,
		repositoryPublishSettingTimeout: 5,
	})
	result := runRepositoryReadACL(t, tool, "acme/public-repo")
	if !result.OK {
		t.Fatalf("公开仓库全员可读未生效：%#v", result)
	}
	if !strings.Contains(result.File.Content, "func main") {
		t.Fatalf("读到的内容不对：%+v", result.File)
	}
}

func TestRepositoryReadPrivateRepoRejectsUnauthorizedUser(t *testing.T) {
	github := &repositoryReadACLTestGitHub{}
	server := httptest.NewServer(http.HandlerFunc(github.handler))
	defer server.Close()

	tool := repositoryReadACLTool(server, "stranger", SettingValues{
		repositoryPublishSettingToken:     repositoryPublishTestToken,
		repositoryPublishSettingAllowlist: "acme/private-repo",
		repositoryPublishSettingTimeout:   5,
	})
	result := runRepositoryReadACL(t, tool, "acme/private-repo")
	if result.OK || result.FailureCode != "permission_denied" {
		t.Fatalf("私有仓库未授权用户应被拒：%#v", result)
	}
	if !strings.Contains(result.Message, "私有仓库") || strings.Contains(result.Message, "func main") {
		t.Fatalf("拒绝提示不该泄露仓库内容：%q", result.Message)
	}
	if got := github.count(func(req string) bool { return strings.Contains(req, "/contents/") }); got != 0 {
		t.Fatalf("被拒后不应再请求文件内容：%v", github.requests)
	}
}

func TestRepositoryReadPrivateRepoAllowsOwnerAndAuthorizedUsers(t *testing.T) {
	cases := []struct {
		name     string
		userID   string
		settings SettingValues
	}{
		{"主人", "owner", SettingValues{
			repositoryPublishSettingToken: repositoryPublishTestToken, repositoryPublishSettingTimeout: 5,
		}},
		{"源码读取授权用户", "reader", SettingValues{
			repositoryPublishSettingToken: repositoryPublishTestToken, repositoryPublishSettingTimeout: 5,
			repositoryPublishSettingCodeUsers: "reader = acme/private-repo",
		}},
		{"Issue 管理人员", "manager", SettingValues{
			repositoryPublishSettingToken: repositoryPublishTestToken, repositoryPublishSettingTimeout: 5,
			repositoryPublishSettingManagerUsers: "manager = acme/private-repo",
			// 已按用户授权的用户读操作也要归因到本人：不配个人 Token 时必须显式
			// 声明沿用全局凭据，否则会先收到 user_token_required。
			repositoryPublishSettingUserAuth: `{"manager":"inherit"}`,
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			github := &repositoryReadACLTestGitHub{}
			server := httptest.NewServer(http.HandlerFunc(github.handler))
			defer server.Close()
			tool := repositoryReadACLTool(server, tc.userID, tc.settings)
			result := runRepositoryReadACL(t, tool, "acme/private-repo")
			if !result.OK {
				t.Fatalf("%s 读私有仓库应放行：%#v", tc.name, result)
			}
			if !strings.Contains(result.File.Content, "func main") {
				t.Fatalf("读到的内容不对：%+v", result.File)
			}
		})
	}
}

func TestRepositoryReadMissingRepoIsNotFound(t *testing.T) {
	github := &repositoryReadACLTestGitHub{}
	server := httptest.NewServer(http.HandlerFunc(github.handler))
	defer server.Close()

	tool := repositoryReadACLTool(server, "owner", SettingValues{
		repositoryPublishSettingToken: repositoryPublishTestToken, repositoryPublishSettingTimeout: 5,
	})
	result := runRepositoryReadACL(t, tool, "acme/ghost")
	if result.OK || result.FailureCode != "not_found" {
		t.Fatalf("不存在/不可见的仓库应报 not_found：%#v", result)
	}
}

// 读操作放宽不等于写操作放宽：公开仓库上无授权用户发起写操作仍应被拦在草稿之外。
func TestRepositoryReadLooseningDoesNotAffectWrites(t *testing.T) {
	github := &repositoryReadACLTestGitHub{}
	server := httptest.NewServer(http.HandlerFunc(github.handler))
	defer server.Close()

	tool := repositoryReadACLTool(server, "stranger", SettingValues{
		repositoryPublishSettingToken:     repositoryPublishTestToken,
		repositoryPublishSettingAllowlist: "acme/public-repo",
		repositoryPublishSettingTimeout:   5,
	})
	result := runRepositoryPublishToolOnce(t, tool, map[string]any{
		"operation":  "create",
		"repository": "acme/public-repo",
		"title":      "should not be created",
	})
	if result.OK || result.FailureCode != "permission_denied" {
		t.Fatalf("公开仓库的写操作权限不应随读操作放宽：%#v", result)
	}
	if got := github.count(func(req string) bool { return strings.HasPrefix(req, "POST /repos/acme/public-repo/issues") }); got != 0 {
		t.Fatalf("被拒后不应发起写入请求：%v", github.requests)
	}
}

// 「按用户授权只在私聊生效」之后，同一个人在群里就是普通成员：读公开仓库不该再被
// 「请先配置你自己的 GitHub Token」挡下——那是给已授权用户做归因用的，普通成员走
// 公共凭据。漏改这一处会把一次本来人人都能做的公开仓库读取变成报错。
func TestRepositoryReadPublicRepoInGroupNeedsNoPersonalToken(t *testing.T) {
	github := &repositoryReadACLTestGitHub{}
	server := httptest.NewServer(http.HandlerFunc(github.handler))
	defer server.Close()
	settings := SettingValues{
		repositoryPublishSettingToken: repositoryPublishTestToken,
		// 这个人在私聊里是管理员，但没有配自己的 Token。
		repositoryPublishSettingManagerUsers: "manager = acme/public-repo",
		repositoryPublishSettingTimeout:      5,
	}
	runtime := NewRuntime(BotConfig{OwnerID: "owner"}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	inGroup := newDianaGitHubTool(runtime, MessageEvent{
		Kind: EventKindGroup, GroupID: "group-1", UserID: "manager", RawMessage: "看看这个仓库",
	}, newRepositoryPublishPlugin(server.Client(), server.URL), settings)

	result := runRepositoryPublishToolOnce(t, inGroup, map[string]any{
		"operation": "read_file", "repository": "acme/public-repo", "path": "README.md",
	})
	if !result.OK || result.File == nil || !strings.Contains(result.File.Content, "func main") {
		t.Fatalf("群里读公开仓库被挡下：%#v", result)
	}

	// 同一个人在私聊里仍然按已授权用户处理：没有个人 Token 就要求先配。
	inPrivate := newDianaGitHubTool(runtime, MessageEvent{
		Kind: EventKindPrivate, UserID: "manager", RawMessage: "看看这个仓库",
	}, newRepositoryPublishPlugin(server.Client(), server.URL), settings)
	denied := runRepositoryPublishToolOnce(t, inPrivate, map[string]any{
		"operation": "read_file", "repository": "acme/public-repo", "path": "README.md",
	})
	if denied.OK || denied.FailureCode != "user_token_required" {
		t.Fatalf("私聊里的已授权用户应按本人 Token 归因：%#v", denied)
	}
}
