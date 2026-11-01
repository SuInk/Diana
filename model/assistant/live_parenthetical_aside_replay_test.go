package assistant

import (
	"context"
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/SuInk/diana/model/llm"
)

// Replay a private production request without executing any returned tools.
// DIANA_REPLAY_ASIDE_REQUEST holds the request object, not the entire trace.
func TestLiveParentheticalAsideReplay(t *testing.T) {
	path := os.Getenv("DIANA_REPLAY_ASIDE_REQUEST")
	if path == "" {
		t.Skip("set DIANA_REPLAY_ASIDE_REQUEST and DIANA_TEST_LLM_CONFIG")
	}
	client := liveLLMClient(t)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var req llm.GenerateRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		t.Fatal(err)
	}
	const oldRule = "不要用只开不合的「（」接一段补充的话，例如「这叫声控开关（摔一个来一个，百试百灵」；要补充就另起一句或把括号写完整，也不要照抄聊天记录里这种写法。"
	replaced := 0
	for i := range req.Messages {
		if req.Messages[i].Role != llm.RoleSystem {
			continue
		}
		replaced += strings.Count(req.Messages[i].Content, oldRule)
		req.Messages[i].Content = strings.ReplaceAll(req.Messages[i].Content, oldRule, replyUnclosedBracketRule)
	}
	if replaced != 1 {
		t.Fatalf("expected one old rule, got %d", replaced)
	}
	// Rebuild the current request hook exactly as the runtime does, without
	// retaining a second copy of its old execution-status note.
	last := len(req.Messages) - 1
	if last >= 0 && req.Messages[last].Role == llm.RoleUser {
		last--
	}
	if last < 0 || !strings.HasPrefix(req.Messages[last].Content, "【本轮工具执行状态，由运行时生成】") {
		t.Fatal("expected the current execution-status note at the end of this fixture")
	}
	req.Messages = append(req.Messages[:last], req.Messages[last+1:]...)
	req, err = newReplyRunHooks(BotConfig{}).PrepareRequest(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		resp, err := client.Generate(ctx, req)
		cancel()
		if err != nil {
			t.Fatal(err)
		}
		text := resp.Text
		for _, call := range resp.ToolCalls {
			if value, ok := call.Arguments["text"].(string); ok {
				text += value
			}
		}
		t.Logf("sample=%d model=%s reply=%s", i+1, resp.Model, text)
		if strings.TrimSpace(text) == "" {
			t.Fatal("no reply to inspect")
		}
		// This football-chat fixture needs no annotations or roleplay actions.
		if replayHasParentheticalAside(text) {
			t.Errorf("football reply still contains a parenthetical aside: %s", text)
		}
	}
	for _, literal := range []string{"缓存（cache）", "f(x) = (x + 1) * 2"} {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		resp, err := client.Generate(ctx, llm.GenerateRequest{Messages: []llm.Message{
			{Role: llm.RoleSystem, Content: defaultSystemPrompt + "\n" + replyUnclosedBracketRule},
			{Role: llm.RoleUser, Content: "只原样输出下面这段原文，不添加说明：" + literal},
		}})
		cancel()
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("literal=%q model=%s reply=%q", literal, resp.Model, resp.Text)
		if strings.TrimSpace(resp.Text) != literal {
			t.Errorf("required parentheses or quoted text changed: %q", resp.Text)
		}
	}
}

// A terminal opening bracket is a tone marker, not an aside.
func replayHasParentheticalAside(text string) bool {
	return regexp.MustCompile(`[（(]\s*[^（）()\s]`).MatchString(text)
}

func TestReplayParentheticalAsideBoundary(t *testing.T) {
	for _, tc := range []struct {
		text string
		want bool
	}{
		{"行吧（", false}, {"行吧( ", false}, {"行吧", false},
		{"行吧（别翻车）", true}, {"行吧（别翻车", true},
		{"行吧( 别翻车)", true},
	} {
		if got := replayHasParentheticalAside(tc.text); got != tc.want {
			t.Errorf("%q: got %v want %v", tc.text, got, tc.want)
		}
	}
}
