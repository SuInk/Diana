// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/SuInk/diana/model/agent"
)

func runVersionTool(t *testing.T, runtime *Runtime) dianaVersionResult {
	t.Helper()
	return runVersionToolWithDisclosure(t, runtime, true)
}

func runVersionToolWithDisclosure(t *testing.T, runtime *Runtime, discloseRepository bool) dianaVersionResult {
	t.Helper()
	raw, err := newDianaVersionTool(runtime, discloseRepository).Run(context.Background(), nil)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	var result dianaVersionResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return result
}

func TestDianaVersionToolReportsInjectedBuild(t *testing.T) {
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.SetBuildInfo(BuildInfo{
		Version:   "v0.8.57",
		BuildType: "release",
		StartedAt: time.Now().Add(-25 * time.Hour),
	})

	result := runVersionTool(t, runtime)
	if result.Version != "v0.8.57" || result.BuildType != "正式发布版" {
		t.Fatalf("result = %+v", result)
	}
	if result.Uptime != "1 天 1 小时" {
		t.Fatalf("uptime = %q", result.Uptime)
	}
	// 更新时间取的是可执行文件落盘时间，测试二进制也有，所以必然报得出来。
	if result.UpdatedAt == "" || result.UpdatedAgo == "" {
		t.Fatalf("updated_at/ago missing: %+v", result)
	}
	if result.Message != "" {
		t.Fatalf("有版本号时不该带兜底说明: %+v", result)
	}
	if result.Platform == "" || result.GoVersion == "" {
		t.Fatalf("platform/go version missing: %+v", result)
	}
	// 没注入更新检查入口时不谈新版本，而不是猜一个。
	if result.UpdateState != "" || result.LatestVersion != "" {
		t.Fatalf("没有更新器却给出了更新结论: %+v", result)
	}
}

type stubReleaseStatusProvider struct {
	status ReleaseStatus
	err    error
	calls  int
}

func (s *stubReleaseStatusProvider) ReleaseStatus(context.Context) (ReleaseStatus, error) {
	s.calls++
	return s.status, s.err
}

func TestDianaVersionToolReportsReleaseStatus(t *testing.T) {
	provider := &stubReleaseStatusProvider{status: ReleaseStatus{
		RepositoryURL:     "https://github.com/SuInk/Diana",
		DeploymentMode:    "release",
		CurrentVersion:    "v0.8.57",
		LatestVersion:     "v0.8.58",
		LatestPublishedAt: time.Now().Add(-50 * time.Hour),
		CheckedAt:         time.Now().Add(-3 * time.Minute),
		UpdateAvailable:   true,
		UpdateSupported:   true,
	}}
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.SetBuildInfo(BuildInfo{Version: "v0.8.57", BuildType: "release"})
	runtime.SetReleaseStatusProvider(provider)

	result := runVersionTool(t, runtime)
	if provider.calls != 1 {
		t.Fatalf("calls = %d", provider.calls)
	}
	if result.LatestVersion != "v0.8.58" || result.DeploymentMode != "Release 包部署" {
		t.Fatalf("result = %+v", result)
	}
	if result.LatestReleasedAgo != "2 天 2 小时" || result.CheckedAt == "" {
		t.Fatalf("release timing = %+v", result)
	}
	if result.UpdateState != "有新版本可以更新。" {
		t.Fatalf("update state = %q", result.UpdateState)
	}
	// 「你源码在哪」不给真链接，模型就会编一个像模像样的 GitHub 地址。
	if result.RepositoryURL != "https://github.com/SuInk/Diana" {
		t.Fatalf("repository url = %q", result.RepositoryURL)
	}
}

// 检查失败时说「查不到」，不要把「没查到新版本」说成「已经是最新」。
func TestDianaVersionToolAdmitsFailedUpdateCheck(t *testing.T) {
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.SetBuildInfo(BuildInfo{Version: "v0.8.57", BuildType: "release"})
	runtime.SetReleaseStatusProvider(&stubReleaseStatusProvider{err: context.DeadlineExceeded})

	result := runVersionTool(t, runtime)
	if !strings.Contains(result.UpdateState, "查不到最新版本") {
		t.Fatalf("update state = %q", result.UpdateState)
	}
	if result.LatestVersion != "" {
		t.Fatalf("failed check must not report a latest version: %+v", result)
	}
}

// 「升不了级」和「已经是最新」都表现为没得更新，但含义相反，必须分开说。
func TestDescribeUpdateStateSeparatesUnsupportedFromUpToDate(t *testing.T) {
	cases := []struct {
		name   string
		status ReleaseStatus
		want   string
	}{
		{name: "最新", status: ReleaseStatus{UpdateSupported: true}, want: "已经是最新版本。"},
		{name: "有更新", status: ReleaseStatus{UpdateAvailable: true, UpdateSupported: true}, want: "有新版本可以更新。"},
		{
			name:   "有更新但升不了",
			status: ReleaseStatus{UpdateAvailable: true, UnsupportedReason: "前端目录不在包内。"},
			want:   "有新版本，但这台机器升不了级：前端目录不在包内。",
		},
		{
			name:   "最新但也升不了",
			status: ReleaseStatus{UnsupportedReason: "容器部署由镜像负责升级。"},
			want:   "已经是最新的，但这台机器本来也升不了级：容器部署由镜像负责升级。",
		},
		{
			name:   "源码构建",
			status: ReleaseStatus{SwitchToReleaseAvailable: true},
			want:   "当前是源码构建，不参与自动更新；可以在控制台显式切换到正式 Release 包。",
		},
	}
	for _, item := range cases {
		if got := describeUpdateState(item.status); got != item.want {
			t.Fatalf("%s: describeUpdateState = %q, want %q", item.name, got, item.want)
		}
	}
}

// 没注入版本时如实说不知道，而不是让模型自己编一个像模像样的版本号。
func TestDianaVersionToolAdmitsMissingVersion(t *testing.T) {
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	result := runVersionTool(t, runtime)
	if result.Version != "" || result.BuildType != "" {
		t.Fatalf("result = %+v", result)
	}
	if !strings.Contains(result.Message, "没有拿到版本号") {
		t.Fatalf("message = %q", result.Message)
	}
	// StartedAt 没注入时按调用时刻兜底，不该是零值。
	if result.StartedAt == "" || result.Uptime == "" {
		t.Fatalf("uptime missing: %+v", result)
	}
}

func TestHumanizeChineseDurationKeepsTwoUnits(t *testing.T) {
	cases := map[time.Duration]string{
		30 * time.Second:                     "不到 1 分钟",
		90 * time.Second:                     "1 分钟",
		2*time.Hour + 5*time.Minute:          "2 小时 5 分钟",
		3 * time.Hour:                        "3 小时",
		49 * time.Hour:                       "2 天 1 小时",
		48 * time.Hour:                       "2 天",
		72*time.Hour + 30*time.Minute:        "3 天",
		-(2*time.Hour + 5*time.Minute):       "2 小时 5 分钟",
		time.Duration(0):                     "不到 1 分钟",
		25*time.Hour + 61*time.Second + 1000: "1 天 1 小时",
	}
	for input, want := range cases {
		if got := humanizeChineseDuration(input); got != want {
			t.Fatalf("humanizeChineseDuration(%v) = %q, want %q", input, got, want)
		}
	}
}

// 注册了版本工具才注入版本规则。
func TestSystemPromptInjectsVersionRuleWithTool(t *testing.T) {
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	registry := agent.NewToolRegistry(newDianaVersionTool(runtime, true))
	prompt := runtime.systemPromptWithRelationshipAndAgentTools(
		MessageEvent{Kind: EventKindGroup, GroupID: "g1", UserID: "1"},
		nil, false, RelationshipPolicy{Owner: true}, true, registry,
	)
	if !strings.Contains(prompt, promptToolVersion) {
		t.Fatalf("prompt missing the version rule: %s", prompt)
	}
	// 普通成员也能问版本，这不是机密。
	member := RelationshipPolicy{}
	if !member.allowedAgentToolNames()[dianaVersionToolName] {
		t.Fatal("version tool is hidden from non-owners")
	}
}

// 项目地址不公开时，收掉的只有地址这一项：版本、运行时长照答。
func TestDianaVersionToolWithholdsRepositoryURL(t *testing.T) {
	provider := &stubReleaseStatusProvider{status: ReleaseStatus{
		RepositoryURL:   "https://github.com/SuInk/Diana",
		DeploymentMode:  "release",
		LatestVersion:   "v0.8.58",
		UpdateSupported: true,
	}}
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.SetBuildInfo(BuildInfo{Version: "v0.8.57", BuildType: "release"})
	runtime.SetReleaseStatusProvider(provider)

	result := runVersionToolWithDisclosure(t, runtime, false)
	if result.RepositoryURL != "" {
		t.Fatalf("repository url leaked: %q", result.RepositoryURL)
	}
	if result.Version != "v0.8.57" || result.LatestVersion != "v0.8.58" {
		t.Fatalf("其余运行时事实不该跟着一起收掉: %+v", result)
	}
	// 说成「查不到」模型就会去帮忙找一个，必须说成「不公开」。
	if !strings.Contains(result.ReplyGuidance, "不对外公开") {
		t.Fatalf("reply guidance = %q", result.ReplyGuidance)
	}
	if strings.Contains(newDianaVersionTool(runtime, false).Description(), "项目开源地址、") {
		t.Fatal("工具说明仍在宣称能查开源地址")
	}
}

func TestRepositoryDisclosedTo(t *testing.T) {
	cases := []struct {
		mode  RepositoryDisclosure
		owner bool
		want  bool
	}{
		{"", false, false},
		{"", true, true},
		{RepositoryDisclosureOwner, false, false},
		{RepositoryDisclosureEveryone, false, true},
		{"EVERYONE", false, true},
		{"乱写", false, false},
	}
	for _, tc := range cases {
		if got := repositoryDisclosedTo(BotConfig{RepositoryDisclosure: tc.mode}, tc.owner); got != tc.want {
			t.Fatalf("repositoryDisclosedTo(%q, owner=%t) = %t", tc.mode, tc.owner, got)
		}
	}
}

// 默认收紧：普通成员拿到的是「不给地址」那份规则，主人和全公开时照旧。
func TestSystemPromptVersionRuleFollowsRepositoryDisclosure(t *testing.T) {
	cases := []struct {
		name  string
		mode  RepositoryDisclosure
		owner bool
		want  string
	}{
		{"默认对成员收紧", "", false, promptToolVersionNoRepository},
		{"主人照旧", "", true, promptToolVersion},
		{"全公开", RepositoryDisclosureEveryone, false, promptToolVersion},
	}
	for _, tc := range cases {
		runtime := NewRuntime(BotConfig{RepositoryDisclosure: tc.mode}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
		registry := agent.NewToolRegistry(newDianaVersionTool(runtime, repositoryDisclosedTo(BotConfig{RepositoryDisclosure: tc.mode}, tc.owner)))
		prompt := runtime.systemPromptWithRelationshipAndAgentTools(
			MessageEvent{Kind: EventKindGroup, GroupID: "g1", UserID: "1"},
			nil, false, RelationshipPolicy{Owner: tc.owner}, true, registry,
		)
		if !strings.Contains(prompt, tc.want) {
			t.Fatalf("%s: prompt missing the expected version rule: %s", tc.name, prompt)
		}
		if tc.want == promptToolVersionNoRepository && strings.Contains(prompt, promptToolVersion) {
			t.Fatalf("%s: 收紧时不该还带着公开地址那份规则", tc.name)
		}
	}
}
