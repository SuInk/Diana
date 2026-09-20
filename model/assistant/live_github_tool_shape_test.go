// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SuInk/diana/model/agent"
	"github.com/SuInk/diana/model/llm"
)

// github 工具的描述砍到了一句话：操作语义进了 operation 枚举说明，「别用网页渲染读代码」
// 进了 read_file 的 path 说明，草稿审批和确认码进了 user_confirmed_write 说明与工具结果的
// message。这三处都是行为约束，改了位置就得拿真实模型验一遍还认不认。
//
// 这里只连真实模型，GitHub 侧走 httptest 夹具：断言的是「模型选了什么」，不需要真实仓库，
// 也不会向 GitHub 写入任何东西。

// liveGitHubShapeProbe 记下模型每一步选了哪个工具、哪个 operation，tools_execute 信封
// 也拆开记，否则常驻与非常驻两条路径记出来的名字对不上。
type liveGitHubShapeProbe struct {
	llm.LLMClient
	mu    sync.Mutex
	steps []string
}

func (p *liveGitHubShapeProbe) Generate(ctx context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	resp, err := p.LLMClient.Generate(ctx, req)
	if err != nil || resp == nil {
		return resp, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, call := range resp.ToolCalls {
		name, arguments := call.Name, call.Arguments
		if name == agent.ToolsExecuteToolName {
			if inner := strings.TrimSpace(configToolString(arguments, "name")); inner != "" {
				name = inner
				if nested, ok := arguments["input"].(map[string]any); ok {
					arguments = nested
				}
			}
		}
		if operation := strings.TrimSpace(configToolString(arguments, "operation")); operation != "" {
			name += ":" + operation
		}
		p.steps = append(p.steps, name)
	}
	return resp, err
}

func (p *liveGitHubShapeProbe) snapshot() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.steps...)
}

func (p *liveGitHubShapeProbe) used(step string) bool {
	for _, got := range p.snapshot() {
		if got == step {
			return true
		}
	}
	return false
}

// liveGitHubRenderStub 站在 browser_render 的位置上：模型真要拿网页渲染去读 GitHub 时，
// 这里会记下来，而不是让它静悄悄地成功。
type liveGitHubRenderStub struct {
	mu   sync.Mutex
	urls []string
}

func (s *liveGitHubRenderStub) Name() string { return "browser_render" }

func (s *liveGitHubRenderStub) Description() string {
	return "把网页渲染成可读文本：传 url，返回正文。"
}

func (s *liveGitHubRenderStub) InputSchema() map[string]any {
	return toolObjectSchema([]string{"url"}, map[string]any{
		"url": toolStringParam("要渲染的网页地址。"),
	})
}

func (s *liveGitHubRenderStub) Run(_ context.Context, input map[string]any) (string, error) {
	url := strings.TrimSpace(configToolString(input, "url"))
	s.mu.Lock()
	s.urls = append(s.urls, url)
	s.mu.Unlock()
	return `{"ok":true,"text":"(页面正文略)"}`, nil
}

func (s *liveGitHubRenderStub) gitHubURLs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, url := range s.urls {
		if strings.Contains(url, "github.com") {
			out = append(out, url)
		}
	}
	return out
}

// liveGitHubShapeRun 按线上的样子跑一轮：github 和 browser_render 都是常驻工具，
// 模型手里拿到的就是这两份完整声明。
func liveGitHubShapeRun(t *testing.T, tool *dianaGitHubTool, render *liveGitHubRenderStub, userText string) (string, *liveGitHubShapeProbe) {
	t.Helper()
	probe := &liveGitHubShapeProbe{LLMClient: liveLLMClient(t)}
	runner, err := agent.NewRunner(probe, agent.Config{
		WorkDir:   t.TempDir(),
		CoreTools: []string{dianaGitHubToolName, render.Name()},
		MaxSteps:  8,
	}, agent.NewToolRegistry(tool, render))
	if err != nil {
		t.Fatal(err)
	}
	defer runner.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	response, err := runner.Run(ctx, agent.Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: userText}}})
	if err != nil {
		t.Fatalf("agent run: %v", err)
	}
	t.Logf("工具调用顺序：%s", strings.Join(probe.snapshot(), " → "))
	t.Logf("最终回复：%q", response.Text)
	return response.Text, probe
}

// 公开仓库读取：用户既不是主人也不在任何授权名单里，仓库也不在白名单里。
//
// 描述里那句「公开仓库全员可查……不能拿『不在清单里』去拒绝读取公开仓库」删掉之后，
// 这条最容易复发——#576 当初就是模型自己编了个权限理由把读请求挡了。服务端的放行
// 由 TestRepositoryReadPublicRepoOpenToEveryone 保证，这里管的是模型会不会自己先认输。
func TestLiveAgentReadsPublicRepoWithoutInventingPermissionExcuse(t *testing.T) {
	github := &repositoryReadACLTestGitHub{}
	server := httptest.NewServer(http.HandlerFunc(github.handler))
	defer server.Close()
	tool := repositoryReadACLTool(server, "stranger", SettingValues{
		repositoryPublishSettingToken:   repositoryPublishTestToken,
		repositoryPublishSettingTimeout: 5,
	})
	render := &liveGitHubRenderStub{}

	reply, probe := liveGitHubShapeRun(t, tool, render,
		"帮我看一下 acme/public-repo 这个仓库里 README.md 的内容，讲讲它是干嘛的")

	if !probe.used(dianaGitHubToolName + ":read_file") {
		t.Fatalf("没有用 github read_file 去读公开仓库：%v", probe.snapshot())
	}
	if urls := render.gitHubURLs(); len(urls) > 0 {
		t.Errorf("有 read_file 还改用网页渲染读 GitHub：%v", urls)
	}
	// 读到的内容里有 func main，模型复述得出来就说明它真的读了、也没有拿权限当借口。
	if !strings.Contains(reply, "main") {
		t.Errorf("回复里看不出读到了文件内容，疑似被模型自己挡下：%q", reply)
	}
	for _, excuse := range []string{"白名单", "未授权", "没有权限", "无权"} {
		if strings.Contains(reply, excuse) {
			t.Errorf("公开仓库读取被模型编了个权限理由拒绝（%s）：%q", excuse, reply)
		}
	}
}

// review 之前必须先 pull_files：这句原来在描述正文，现在只写在 operation 枚举说明里。
// 同时盯住写操作停在草稿，以及确认码有没有被复述——确认码的说明现在只剩
// user_confirmed_write 的参数说明和草稿结果的 message 两处。
func TestLiveAgentReadsPullFilesBeforeReviewAndRecitesConfirmationCode(t *testing.T) {
	github := newRepositoryPullRequestTestGitHub()
	github.files = append(github.files,
		githubPullRequestFile{Filename: "cmd/app.go", Status: "modified", Additions: 12, Patch: "@@ -1,3 +1,5 @@\n func main() {\n-\tlog.Print(\"hi\")\n+\tpassword := \"hunter2\"\n+\tlog.Print(password)\n }"},
	)
	server := httptest.NewServer(http.HandlerFunc(github.handler))
	defer server.Close()
	tool := repositoryPublishTestTool(server, "review acme/demo PR 85", nil)
	render := &liveGitHubRenderStub{}

	reply, probe := liveGitHubShapeRun(t, tool, render,
		"帮我 review 一下 acme/demo 的 PR 85，把你的意见作为 review 提交上去")

	steps := probe.snapshot()
	indexOf := func(step string) int {
		for i, got := range steps {
			if got == step {
				return i
			}
		}
		return -1
	}
	filesAt := indexOf(dianaGitHubToolName + ":pull_files")
	reviewAt := indexOf(dianaGitHubToolName + ":review")
	if filesAt < 0 {
		t.Fatalf("review 之前没有 pull_files 读改动：%v", steps)
	}
	if reviewAt >= 0 && reviewAt < filesAt {
		t.Fatalf("先提交了 review 才去读改动：%v", steps)
	}
	if urls := render.gitHubURLs(); len(urls) > 0 {
		t.Errorf("有 pull_files 还改用网页渲染读 GitHub：%v", urls)
	}
	if reviewAt < 0 {
		t.Skipf("这一轮模型没有走到 review，只验证了读的顺序：%v", steps)
	}
	// 草稿的确认码必须原样出现在回复里，否则有权限的人无从确认，整条审批链断在这。
	drafts := runRepositoryPublishToolOnce(t, tool, map[string]any{"operation": "list_drafts"})
	if len(drafts.Drafts) == 0 {
		t.Fatalf("review 应该停在待审批草稿：%#v", drafts)
	}
	code := drafts.Drafts[0].ConfirmationCode
	if code == "" {
		t.Fatalf("草稿没有确认码：%#v", drafts.Drafts[0])
	}
	if !strings.Contains(reply, code) {
		t.Errorf("确认码 %s 没有写进回复，对方无从确认：%q", code, reply)
	}
}
