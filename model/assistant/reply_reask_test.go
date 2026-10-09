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

func runReask(t *testing.T, relation string) (*reaskProvider, []string) {
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
	waitForCondition(t, 5*time.Second, func() bool {
		p := runtime.earlierReplyProgress(MessageEvent{Kind: EventKindGroup, GroupID: "123456", UserID: "10001"})
		return p != nil && strings.Contains(p.detail(), "用了 1 次其他工具")
	})
	_ = runtime.HandleEvent(withOutboundTurn(context.Background(), "turn-2"), directedGroupMessage("20002", "10001", "在吗"))
	if relation == "independent" {
		waitForCondition(t, 5*time.Second, func() bool { return len(channel.sentSnapshot()) > 0 })
	} else if sent := channel.sentSnapshot(); len(sent) != 0 {
		t.Fatalf("merged reask sent progress: %+v", sent)
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
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(strings.Join(texts(), "\n"), "调查结果") {
		if time.Now().After(deadline) {
			t.Fatalf("调查结果没发出来: %q", texts())
		}
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(200 * time.Millisecond)
	return provider, texts()
}

// 重复追问合并后只等待最终结果，不再额外发送固定进度。
func TestReaskMergedIntoLongReplyStaysSilent(t *testing.T) {
	_, texts := runReask(t, "repeat")
	if len(texts) != 1 || texts[0] != "调查结果：确有其事" {
		t.Fatalf("sent = %q", texts)
	}
}

// 独立追问仍知道前一轮在执行，不重复发起调查。
func TestIndependentReaskKnowsEarlierReplyStillRunning(t *testing.T) {
	provider, texts := runReask(t, "independent")
	if strings.Join(texts, "|") != "还在查|调查结果：确有其事" {
		t.Fatalf("sent = %q", texts)
	}
	provider.mu.Lock()
	prompt := provider.reaskPrompt
	provider.mu.Unlock()
	if !strings.Contains(prompt, "上一条消息你还在处理") || !strings.Contains(prompt, "不要自己回答那个问题") {
		t.Fatal("missing running-reply context")
	}
}
