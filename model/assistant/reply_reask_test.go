// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SuInk/diana/model/llm"
)

// reaskProvider 模拟一次慢调查：先调一次工具，然后卡住直到放行才交结果。
// 同一个人追问「在吗」的那一轮直接回一句，并记下它看到的提示词。
type reaskProvider struct {
	t           *testing.T
	mu          sync.Mutex
	relation    string
	release     chan struct{}
	reaskPrompt string
}

func (p *reaskProvider) Generate(ctx context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	body := requestText(req)
	last := ""
	// 回复风格提示可能追加在工具结果之后，按最近的业务消息识别测试阶段。
	for i := len(req.Messages) - 1; i >= 0; i-- {
		content := req.Messages[i].Content
		if strings.Contains(content, "chat_history 执行成功") || strings.Contains(content, "@42 在吗") || strings.Contains(content, "@42 查一下") {
			last = content
			break
		}
	}
	finalize := func(text string) (*llm.GenerateResponse, error) {
		return &llm.GenerateResponse{ToolCalls: []llm.ToolCall{{ID: "final", Name: "agent_finalize", Arguments: map[string]any{"content": text}}}}, nil
	}
	switch {
	case strings.Contains(body, directReplyTopicPrompt):
		return &llm.GenerateResponse{Text: `{"relation":"` + p.relation + `","confidence":0.9}`}, nil
	case strings.Contains(body, "send_confidence"):
		return &llm.GenerateResponse{Text: `{"send_confidence":0.99,"account_safe":true,"count_refusal":false}`}, nil
	case len(req.Tools) == 0:
		return &llm.GenerateResponse{Text: `{}`}, nil
	case strings.Contains(last, "@42 在吗"):
		p.mu.Lock()
		p.reaskPrompt = body
		p.mu.Unlock()
		return finalize("还在查")
	case strings.Contains(last, "chat_history 执行成功"):
		select {
		case <-p.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return finalize("调查结果：确有其事")
	case strings.Contains(last, "@42 查一下"):
		return &llm.GenerateResponse{ToolCalls: []llm.ToolCall{{ID: "t1", Name: "chat_history", Arguments: map[string]any{}}}}, nil
	}
	p.t.Errorf("unexpected research request: last=%q", last)
	return finalize("意外的请求")
}

func runReask(t *testing.T, relation string, waitProgress bool) (*reaskProvider, []string) {
	t.Helper()
	disabled := false
	provider := &reaskProvider{t: t, relation: relation, release: make(chan struct{})}
	channel := &recordingChannel{}
	runtime := NewRuntime(BotConfig{BotAccount: "42", AgentEnabled: true, BotReplyLoopDetectionEnabled: &disabled}.WithDefaults(),
		channel, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) { return provider, nil })
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = runtime.HandleEvent(withOutboundTurn(context.Background(), "turn-1"), directedGroupMessage("20001", "10001", "查一下某发布会是不是真的"))
	}()
	if waitProgress {
		waitForCondition(t, 5*time.Second, func() bool { return len(channel.sentSnapshot()) > 0 })
	} else {
		waitForCondition(t, 5*time.Second, func() bool {
			return runtime.earlierReplyProgress(MessageEvent{Kind: EventKindGroup, GroupID: "123456", UserID: "10001"}) != nil
		})
		time.Sleep(100 * time.Millisecond)
	}
	_ = runtime.HandleEvent(withOutboundTurn(context.Background(), "turn-2"), directedGroupMessage("20002", "10001", "在吗"))
	// 并进去的那条要当场报进度；单独成轮的那条要先回出来——都不能等到结果出来。
	want := map[bool]int{false: 1, true: 2}[waitProgress]
	deadline := time.Now().Add(5 * time.Second)
	for len(channel.sentSnapshot()) < want {
		if time.Now().After(deadline) {
			t.Fatalf("追问后没有及时出声: %#v", channel.sentSnapshot())
		}
		time.Sleep(10 * time.Millisecond)
	}
	close(provider.release)
	<-done
	texts := func() []string {
		out := []string{}
		for _, message := range channel.sentSnapshot() {
			out = append(out, message.Text)
		}
		return out
	}
	deadline = time.Now().Add(5 * time.Second)
	for !strings.Contains(strings.Join(texts(), "\n"), "调查结果") {
		if time.Now().After(deadline) {
			t.Fatalf("调查结果没发出来: %q", texts())
		}
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(200 * time.Millisecond)
	return provider, texts()
}

func withReaskTiming(t *testing.T, first time.Duration) {
	t.Helper()
	oldFirst, oldEvery, oldNudge := replyProgressFirst, replyProgressEvery, replyProgressNudgeAfter
	replyProgressFirst, replyProgressEvery, replyProgressNudgeAfter = first, first, 0
	t.Cleanup(func() { replyProgressFirst, replyProgressEvery, replyProgressNudgeAfter = oldFirst, oldEvery, oldNudge })
}

// 长调查还没开口，同一个人又 @ 一次被判成重复：并进同一轮，但要当场报一句进度。
func TestReaskMergedIntoLongReplyGetsProgressNow(t *testing.T) {
	withReaskTiming(t, time.Hour)
	_, texts := runReask(t, "repeat", false)
	if len(texts) != 2 || !strings.HasPrefix(texts[0], "⏳ 还在弄") || texts[1] != "调查结果：确有其事" {
		t.Fatalf("sent = %q", texts)
	}
}

// 进度已经发过、这一轮不再接受合并时，追问单独成轮：提示词要说前一轮还在跑，
// 让它只报进度，不能再说「马上就会发出去」「合并回复」。
func TestReaskAfterProgressIsToldEarlierReplyStillRunning(t *testing.T) {
	for _, relation := range []string{"repeat", "independent"} {
		t.Run(relation, func(t *testing.T) {
			withReaskTiming(t, 200*time.Millisecond)
			provider, texts := runReask(t, relation, true)
			if !strings.HasPrefix(texts[0], "⏳ 还在弄") || strings.Join(withoutProgress(texts), "|") != "还在查|调查结果：确有其事" {
				t.Fatalf("sent = %q", texts)
			}
			provider.mu.Lock()
			prompt := provider.reaskPrompt
			provider.mu.Unlock()
			if !strings.Contains(prompt, "上一条消息你还在处理") || !strings.Contains(prompt, "用了 1 次其他工具") || !strings.Contains(prompt, "不要自己回答那个问题") {
				t.Fatalf("追问那一轮没被告知前一轮还在跑: %s", prompt)
			}
			if strings.Contains(prompt, "马上就会发出去") {
				t.Fatal("追问那一轮还在被告知「马上就会发出去」")
			}
		})
	}
}

func withoutProgress(texts []string) []string {
	out := []string{}
	for _, text := range texts {
		if !strings.HasPrefix(text, "⏳") {
			out = append(out, text)
		}
	}
	return out
}
