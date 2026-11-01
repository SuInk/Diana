// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/SuInk/diana/model/agent"
	"github.com/SuInk/diana/model/llm"
)

// Isolated real-model tool replay: no channel sends and no production room writes.
func TestLiveRoomIntent(t *testing.T) {
	if os.Getenv("DIANA_TEST_LLM_CONFIG") == "" {
		t.Skip("requires explicit current-model config")
	}
	client := liveLLMClient(t)
	r, cfg, store := roomTestRuntime()
	event := MessageEvent{ProfileID: cfg.ID}
	for _, turn := range []struct{ text, pose string }{
		{"去你的小窝书桌前坐会儿。", "desk"},
		{"回床边休息吧。", "rest"},
	} {
		runner, err := agent.NewRunner(client, agent.Config{WorkDir: t.TempDir(), MaxSteps: 6, ToolTimeoutMS: 30000}, agent.NewToolRegistry(&dianaRoomTool{runtime: r, event: event}))
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		ctx, finish := r.beginRoomReply(ctx, cfg, event)
		response, err := runner.Run(ctx, agent.Request{Messages: []llm.Message{
			{Role: llm.RoleSystem, Content: "你有一个虚拟小窝。用户要求调整房间活动时请使用可用工具，然后简短回复。不要把场景动作说成真的执行电脑操作。"},
			{Role: llm.RoleUser, Content: turn.text},
		}, Observer: r.agentRunObserver(event)})
		finish(err)
		cancel()
		_ = runner.Close()
		if err != nil {
			t.Fatal(err)
		}
		if store.values[cfg.ID].Pose != turn.pose {
			t.Fatalf("expected %s, got %+v; reply=%s", turn.pose, store.values[cfg.ID], response.Text)
		}
		t.Logf("%s -> %s; reply: %s", turn.text, turn.pose, response.Text)
	}
}

// Current-model replay uses a recording channel, never a production chat.
func TestLiveRoomSharing(t *testing.T) {
	if os.Getenv("DIANA_TEST_LLM_CONFIG") == "" {
		t.Skip("requires explicit current-model config")
	}
	client := liveLLMClient(t)
	r, _, _ := roomTestRuntime()
	ch := &recordingChannel{}
	r.channel = ch
	r.SetRoomSnapshotRenderer(testRoomRenderer())
	event := MessageEvent{ProfileID: "one", Platform: PlatformOneBotV11, Kind: EventKindGroup, GroupID: "isolated-test", UserID: "test-user"}
	for _, turn := range []struct{ text, operation string }{
		{"发一下你现在的小窝状态。", "report"},
		{"发一张你当前默认小窝的状态图给我看看。", "snapshot"},
	} {
		runner, err := agent.NewRunner(client, agent.Config{WorkDir: t.TempDir(), MaxSteps: 6, ToolTimeoutMS: 30000}, agent.NewToolRegistry(&dianaRoomTool{runtime: r, event: event}))
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		before := len(ch.sent)
		response, err := runner.Run(ctx, agent.Request{Messages: []llm.Message{{Role: llm.RoleSystem, Content: "你有虚拟小窝。按用户要求使用可用工具，成功后简短回复。不要声称操作了真实电脑。"}, {Role: llm.RoleUser, Content: turn.text}}})
		cancel()
		_ = runner.Close()
		if err != nil {
			t.Fatal(err)
		}
		if len(ch.sent) != before+1 {
			t.Fatalf("expected one delivery for %s; got %d: %s", turn.operation, len(ch.sent)-before, response.Text)
		}
		msg := ch.sent[len(ch.sent)-1]
		if turn.operation == "snapshot" && len(msg.ImageURLs) != 1 {
			t.Fatal("no actual image delivery")
		}
		if turn.operation == "report" && msg.Text == "" {
			t.Fatal("no actual status delivery")
		}
		t.Logf("%s: delivered through isolated channel; reply=%s", turn.operation, response.Text)
	}
}
