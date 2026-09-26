// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SuInk/diana/model/agent"
	"github.com/SuInk/diana/model/llm"
)

// handoffBrowser 是一个支持交接的内置浏览器桩，记下请求并留着回调，测试里手动给结果。
type handoffBrowser struct {
	mu      sync.Mutex
	reasons []string
	waits   []time.Duration
	done    func(outcome string)
}

func (b *handoffBrowser) Endpoint(context.Context) (string, error)     { return "http://127.0.0.1:1", nil }
func (b *handoffBrowser) BrowserFor(string) agent.BuiltinBrowserBridge { return b }
func (b *handoffBrowser) RequestHandoff(reason string, timeout time.Duration, done func(string)) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.reasons = append(b.reasons, reason)
	b.waits = append(b.waits, timeout)
	b.done = done
	return "handoff-1", nil
}

func newHandoffTestRuntime(t *testing.T, browser agent.BuiltinBrowserBridge) *Runtime {
	t.Helper()
	runtime := NewRuntime(BotConfig{OwnerID: "owner", AgentEnabled: true}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.SetBrowserBox(browser.(BuiltinBrowserProvider))
	return runtime
}

// 请主人接手：登记到内置浏览器，这一轮以固定说明收尾，说明里写清去哪、点什么。
func TestBrowserHandoffToolRequestsAndEndsTurn(t *testing.T) {
	browser := &handoffBrowser{}
	runtime := newHandoffTestRuntime(t, browser)
	tool := newDianaBrowserHandoffTool(runtime, MessageEvent{UserID: "owner", RawMessage: "帮我把小红书收藏导出来"}, DefaultBotConfig())

	output, err := tool.Run(context.Background(), map[string]any{
		"reason": "登录小红书。",
		"task":   "登录后打开收藏页，把收藏列表导出成表格发给主人",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(browser.reasons) != 1 || browser.reasons[0] != "登录小红书" || browser.waits[0] != 15*time.Minute {
		t.Fatalf("应当按默认 15 分钟登记，句号去掉：%v %v", browser.reasons, browser.waits)
	}
	notice, done := tool.TerminalResult(output)
	if !done || !strings.Contains(notice, "登录小红书") || !strings.Contains(notice, "开始处理") || !strings.Contains(notice, "「完成」") {
		t.Fatalf("这一轮应当以交接说明收尾：%q %v", notice, done)
	}

	if _, err := tool.Run(context.Background(), map[string]any{"reason": "登录", "task": "x", "wait_minutes": 99}); err != nil {
		t.Fatal(err)
	}
	if browser.waits[1] != 30*time.Minute {
		t.Fatalf("最多等 30 分钟，实际 %v", browser.waits[1])
	}
}

func TestBrowserHandoffToolRejectsBadInput(t *testing.T) {
	runtime := newHandoffTestRuntime(t, &handoffBrowser{})
	tool := newDianaBrowserHandoffTool(runtime, MessageEvent{UserID: "owner"}, DefaultBotConfig())
	for _, input := range []map[string]any{
		{"reason": "登录小红书"},
		{"task": "接着做"},
		{"reason": strings.Repeat("长", browserHandoffReasonMaxRunes+1), "task": "接着做"},
	} {
		if _, err := tool.Run(context.Background(), input); err == nil {
			t.Fatalf("%v 应当被拒", input)
		}
	}
	// 外接 CDP、或内置浏览器不支持交接时，说清楚办不到，而不是假装请了。
	plain := newHandoffTestRuntime(t, stubBuiltinBrowser{url: "http://127.0.0.1:1"})
	tool = newDianaBrowserHandoffTool(plain, MessageEvent{UserID: "owner"}, DefaultBotConfig())
	if _, err := tool.Run(context.Background(), map[string]any{"reason": "登录", "task": "接着做"}); err == nil {
		t.Fatal("内置浏览器不支持交接时应当报错")
	}
}

// 交接只给主人：它请的是主人在带登录态的浏览器里动手。
func TestBrowserHandoffStaysOwnerOnly(t *testing.T) {
	if (RelationshipPolicy{}).allowedAgentToolNames()[dianaBrowserHandoffToolName] {
		t.Fatal("browser_handoff 不该开给非主人")
	}
}

// 主人点了「完成」：回到原来的对话跑一轮，带着原话、待办和结果，回复发回原对话；
// 超时只说一声、不跑模型；被顶掉的不叫醒。
func TestResumeAfterBrowserHandoff(t *testing.T) {
	var mu sync.Mutex
	var sent []string
	var prompts []string
	previousSend, previousReply := browserHandoffSend, browserHandoffReply
	t.Cleanup(func() { browserHandoffSend, browserHandoffReply = previousSend, previousReply })
	browserHandoffSend = func(_ *Runtime, _ context.Context, _ MessageEvent, text string) error {
		mu.Lock()
		defer mu.Unlock()
		sent = append(sent, text)
		return nil
	}
	browserHandoffReply = func(_ *Runtime, _ context.Context, _ BotConfig, _ MessageEvent, relationship RelationshipPolicy, messages []llm.Message, tools ...agent.Tool) (string, error) {
		if !relationship.Owner {
			t.Error("接着做的那一轮应当是主人身份")
		}
		mu.Lock()
		defer mu.Unlock()
		prompts = append(prompts, messages[len(messages)-1].Content)
		return "收藏导出好了，一共 42 条", nil
	}

	runtime := newHandoffTestRuntime(t, &handoffBrowser{})
	job := browserHandoffJob{
		event:    MessageEvent{UserID: "owner"},
		reason:   "登录小红书",
		task:     "打开收藏页导出",
		original: "帮我把小红书收藏导出来",
		wait:     15 * time.Minute,
	}
	runtime.resumeAfterBrowserHandoff(job, browserHandoffDone)
	runtime.resumeAfterBrowserHandoff(job, browserHandoffCancelled)
	runtime.resumeAfterBrowserHandoff(job, browserHandoffExpired)

	mu.Lock()
	defer mu.Unlock()
	if len(prompts) != 1 {
		t.Fatalf("只有「完成」该跑模型，实际跑了 %d 次", len(prompts))
	}
	for _, want := range []string{"帮我把小红书收藏导出来", "打开收藏页导出", "做完了「登录小红书」"} {
		if !strings.Contains(prompts[0], want) {
			t.Fatalf("接着做的提示里缺 %q：%s", want, prompts[0])
		}
	}
	if len(sent) != 2 || sent[0] != "收藏导出好了，一共 42 条" || !strings.Contains(sent[1], "15 分钟没人处理") {
		t.Fatalf("发出去的不对：%q", sent)
	}
}

// 交接说明要能被操作记录认出来，浏览器页的记录里看得到「请你接管：登录小红书」。
func TestBrowserHandoffShowsInActivity(t *testing.T) {
	input, _ := json.Marshal(map[string]any{"reason": "登录小红书", "task": "x"})
	var toolInput map[string]any
	_ = json.Unmarshal(input, &toolInput)
	entry, ok := browserActionEntry(MessageEvent{ProfileID: "bot"}, agent.RunEvent{
		Phase: agent.RunPhaseToolCompleted, Tool: dianaBrowserHandoffToolName, ToolInput: toolInput,
	})
	if !ok || !strings.Contains(entry.Message, "请你接管") || entry.Target != "登录小红书" {
		t.Fatalf("操作记录不对：%+v %v", entry, ok)
	}
}
