// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SuInk/diana/model/llm"
)

// 真实 GitHub 测试默认跳过：DIANA_LIVE_GITHUB=1 且本机 gh 已登录。只读公开仓库、只生成草稿，
// 不向 GitHub 写入任何东西。目标 PR 可用 DIANA_LIVE_GITHUB_REPO / DIANA_LIVE_GITHUB_PR 指定。
func liveGitHubPullRequestTarget(t *testing.T) (string, int) {
	t.Helper()
	if os.Getenv("DIANA_LIVE_GITHUB") != "1" {
		t.Skip("set DIANA_LIVE_GITHUB=1 (with gh logged in) to read a real pull request")
	}
	repository := strings.TrimSpace(os.Getenv("DIANA_LIVE_GITHUB_REPO"))
	if repository == "" {
		repository = "MilkSU-Official/milksu"
	}
	number, _ := strconv.Atoi(strings.TrimSpace(os.Getenv("DIANA_LIVE_GITHUB_PR")))
	if number <= 0 {
		number = 85
	}
	return repository, number
}

func liveGitHubSettings(repository string) SettingValues {
	return SettingValues{
		repositoryPublishSettingAuthMode:   repositoryPublishAuthGH,
		repositoryPublishSettingAllowlist:  repository,
		repositoryPublishSettingUserAccess: "owner = " + repository,
		repositoryPublishSettingTimeout:    30,
	}
}

// 真实接口：get 读到 PR 的分支和统计，pull_files 分页读全文件并带回 patch。
func TestLiveRepositoryPullRequestReadsRealGitHub(t *testing.T) {
	repository, number := liveGitHubPullRequestTarget(t)
	runtime := NewRuntime(BotConfig{OwnerID: "owner"}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	tool := newDianaGitHubTool(runtime, MessageEvent{Kind: EventKindPrivate, UserID: "owner", RawMessage: "看 PR"},
		newRepositoryPublishPlugin(&http.Client{Timeout: 60 * time.Second}, "https://api.github.com"), liveGitHubSettings(repository))

	get := runRepositoryPublishToolOnce(t, tool, map[string]any{"operation": "get", "repository": repository, "number": number})
	if !get.OK || get.PullRequest == nil || get.PullRequest.HeadRef == "" || get.PullRequest.ChangedFiles == 0 {
		t.Fatalf("get=%#v", get)
	}
	t.Logf("get：%s；head=%s base=%s +%d -%d files=%d reviews=%d comments=%d", get.Message, get.PullRequest.HeadRef, get.PullRequest.BaseRef, get.PullRequest.Additions, get.PullRequest.Deletions, get.PullRequest.ChangedFiles, len(get.Reviews), len(get.Comments))

	files := runRepositoryPublishToolOnce(t, tool, map[string]any{"operation": "pull_files", "repository": repository, "number": number})
	if !files.OK || len(files.Files) != get.PullRequest.ChangedFiles {
		t.Fatalf("pull_files ok=%v files=%d want=%d msg=%q", files.OK, len(files.Files), get.PullRequest.ChangedFiles, files.Message)
	}
	withPatch := 0
	for _, file := range files.Files {
		if file.Patch != "" {
			withPatch++
		}
	}
	if withPatch == 0 {
		t.Fatal("没有任何文件带回 patch")
	}
	focus := files.Files[0].Path
	focused := runRepositoryPublishToolOnce(t, tool, map[string]any{"operation": "pull_files", "repository": repository, "number": number, "paths": []any{focus}})
	if !focused.OK || len(focused.Files) != 1 || focused.Files[0].Path != focus {
		t.Fatalf("focused=%#v", focused.Files)
	}
	t.Logf("pull_files：%d 个文件，%d 个带 patch；paths=%s 时 patch %d 字符", len(files.Files), withPatch, focus, len([]rune(focused.Files[0].Patch)))

	readable := ""
	for _, candidate := range files.Files {
		if candidate.Status != "removed" && candidate.Patch != "" {
			readable = candidate.Path
			break
		}
	}
	file := runRepositoryPublishToolOnce(t, tool, map[string]any{"operation": "read_file", "repository": repository, "number": number, "path": readable})
	if !file.OK || file.File == nil || file.File.Ref != get.PullRequest.HeadSHA || !strings.HasPrefix(file.File.Content, "1| ") {
		t.Fatalf("read_file=%#v msg=%q", file.File, file.Message)
	}
	t.Logf("read_file：%s", file.Message)

	issue := runRepositoryPublishToolOnce(t, tool, map[string]any{"operation": "review", "repository": repository, "number": number, "body": "live test draft"})
	if issue.Outcome != "draft_pending" {
		t.Fatalf("review 应只生成草稿：%#v", issue)
	}
}

type liveToolCallProbe struct {
	llm.LLMClient
	mu    sync.Mutex
	calls []llm.ToolCall
}

func (p *liveToolCallProbe) Generate(ctx context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	resp, err := p.LLMClient.Generate(ctx, req)
	if err == nil && resp != nil {
		p.mu.Lock()
		p.calls = append(p.calls, resp.ToolCalls...)
		p.mu.Unlock()
	}
	return resp, err
}

func (p *liveToolCallProbe) snapshot() []llm.ToolCall {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]llm.ToolCall(nil), p.calls...)
}

// 端到端：真实模型收到「review 这个 PR 并把建议评论上去」，要先 get、再 pull_files 读实际改动，
// 最后落成 review 或 comment 草稿；行内评论必须指向这个 PR 真实改动过的文件。
func TestLiveAgentReviewsPullRequestThroughTool(t *testing.T) {
	repository, number := liveGitHubPullRequestTarget(t)
	client := liveLLMClient(t)
	withFastSendTiming(t)
	probe := &liveToolCallProbe{LLMClient: client}
	channel := &recordingChannel{}
	plugins := NewDefaultPluginManager()
	settings := map[string]any{}
	for key, value := range liveGitHubSettings(repository) {
		settings[key] = value
	}
	if _, err := plugins.UpdateSettings(repositoryPublishPluginID, settings); err != nil {
		t.Fatal(err)
	}
	cfg := BotConfig{GroupTriggers: []string{"Diana"}, BotAccount: "42", OwnerID: "owner", AgentEnabled: true}.WithDefaults()
	rt := NewRuntime(cfg, channel, plugins, nil, nil, nil, func() (LLMProvider, error) { return probe, nil })
	text := "Diana 帮我 review 一下 " + repository + " 的 PR" + strconv.Itoa(number) + "，把你的建议评论到 PR 上"
	event := MessageEvent{
		Kind: EventKindPrivate, SelfID: "42", UserID: "owner", MessageID: "live-review", Time: time.Now().Unix(),
		RawMessage: text, Segments: []MessageSegment{{Type: "text", Data: map[string]string{"text": text}}},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	if err := rt.HandleEvent(ctx, event); err != nil {
		t.Fatalf("HandleEvent: %v", err)
	}
	waitForCondition(t, 9*time.Minute, func() bool { return len(channel.sentSnapshot()) > 0 })
	time.Sleep(5 * time.Second)

	var operations []string
	var reviewDraftInput map[string]any
	var renderedGitHub []string
	for _, call := range probe.snapshot() {
		if call.Name != dianaGitHubToolName {
			if call.Name == "browser_render" {
				target := configToolString(call.Arguments, "url")
				t.Logf("browser_render url=%s", target)
				if strings.Contains(target, "github.com") {
					renderedGitHub = append(renderedGitHub, target)
				}
			}
			if call.Name != "agent_finalize" {
				operations = append(operations, call.Name)
			}
			continue
		}
		operation := configToolString(call.Arguments, "operation")
		operations = append(operations, dianaGitHubToolName+":"+operation)
		if operation == "review" {
			reviewDraftInput = call.Arguments
		}
	}
	t.Logf("工具调用顺序：%s", strings.Join(operations, " → "))
	for i, msg := range channel.sentSnapshot() {
		t.Logf("sent[%d]=%q", i, msg.Text)
	}
	indexOf := func(name string) int {
		for i, operation := range operations {
			if operation == name {
				return i
			}
		}
		return -1
	}
	filesAt := indexOf(dianaGitHubToolName + ":pull_files")
	writeAt := indexOf(dianaGitHubToolName + ":review")
	if writeAt < 0 {
		writeAt = indexOf(dianaGitHubToolName + ":comment")
	}
	if filesAt < 0 {
		t.Fatal("没有调用 pull_files 读实际改动")
	}
	if writeAt < 0 || writeAt < filesAt {
		t.Fatalf("应在读过 pull_files 之后再落 review/comment 草稿，pull_files=%d write=%d", filesAt, writeAt)
	}
	if len(renderedGitHub) > 0 {
		t.Errorf("有了 PR 工具仍然改用网页渲染去读 GitHub：%v", renderedGitHub)
	}
	if reviewDraftInput != nil {
		tool := newDianaGitHubTool(rt, event, newRepositoryPublishPlugin(&http.Client{Timeout: 60 * time.Second}, "https://api.github.com"), liveGitHubSettings(repository))
		files := runRepositoryPublishToolOnce(t, tool, map[string]any{"operation": "pull_files", "repository": repository, "number": number})
		changed := map[string]bool{}
		for _, file := range files.Files {
			changed[file.Path] = true
		}
		comments, _, _, _ := repositoryPullRequestReviewComments(reviewDraftInput)
		encoded, _ := json.Marshal(comments)
		t.Logf("review 行内评论：%s", encoded)
		for _, comment := range comments {
			if !changed[comment.Path] {
				t.Errorf("行内评论指向了 PR 没有改动的文件：%s", comment.Path)
			}
		}
	}
}
