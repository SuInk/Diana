// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

func backgroundTestRuntime() (*Runtime, *concurrentRecordingChannel, MessageEvent) {
	channel := &concurrentRecordingChannel{}
	runtime := NewRuntime(BotConfig{BotAccount: "42"}, channel, NewPluginManager(), nil, nil, nil, nil)
	event := MessageEvent{Kind: EventKindPrivate, UserID: "10001", MessageID: "message-1"}
	return runtime, channel, event
}

// sentText 把发出去的消息连起来：结束通知的「最后进度：……」那行会另起一条
// （见 splitReplyBlocks），只看最后一条会漏掉前半句。
func sentText(channel *concurrentRecordingChannel) string {
	var texts []string
	for _, message := range channel.messages() {
		texts = append(texts, message.Text)
	}
	return strings.Join(texts, "\n")
}

func lastText(channel *concurrentRecordingChannel) string {
	messages := channel.messages()
	if len(messages) == 0 {
		return ""
	}
	return messages[len(messages)-1].Text
}

// 分轮推进：每轮带着之前的记录，【完成】时交结果；后台运行里认得出自己是后台任务。
func TestBackgroundAgentRunsRoundsWithNotes(t *testing.T) {
	runtime, channel, event := backgroundTestRuntime()
	var mu sync.Mutex
	var seen [][]string
	nested := false
	outputs := []string{"【进度】搜了官网，下一轮核对第三方报道", "【进度】两家媒体一致", "【完成】结论：确有其事。来源：官网、两家媒体"}
	tool := newDianaBackgroundTaskTool(runtime, event)
	tool.run = func(ctx context.Context, state backgroundAgentState) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		seen = append(seen, state.Notes)
		nested = nested || isBackgroundAgentRun(ctx)
		return outputs[state.Round-1], nil
	}
	out, err := tool.Run(context.Background(), map[string]any{"operation": "start", "goal": "核实某发布会消息"})
	if err != nil {
		t.Fatal(err)
	}
	var started struct{ ID string }
	_ = json.Unmarshal([]byte(out), &started)
	waitForCondition(t, 2*time.Second, func() bool { return strings.Contains(lastText(channel), "✅") })
	if text := lastText(channel); !strings.Contains(text, started.ID) || !strings.Contains(text, "确有其事") {
		t.Fatalf("结果 = %q", text)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 3 || len(seen[0]) != 0 || len(seen[2]) != 2 || !strings.Contains(seen[2][0], "官网") {
		t.Fatalf("各轮看到的记录 = %q", seen)
	}
	if !nested {
		t.Fatal("后台运行没有带上后台任务标记，里面会再挂开任务的工具")
	}
	status, err := tool.Run(context.Background(), map[string]any{"operation": "status", "id": started.ID})
	if err != nil || !strings.Contains(status, `"phase":"completed"`) || !strings.Contains(status, "两家媒体一致") {
		t.Fatalf("status = %s, %v", status, err)
	}
}

func TestBackgroundAgentStopsAtRoundLimit(t *testing.T) {
	runtime, channel, event := backgroundTestRuntime()
	tool := newDianaBackgroundTaskTool(runtime, event)
	tool.run = func(context.Context, backgroundAgentState) (string, error) { return "【进度】还在查", nil }
	if _, err := tool.Run(context.Background(), map[string]any{"operation": "start", "goal": "查不完的事", "max_rounds": 2}); err != nil {
		t.Fatal(err)
	}
	waitForCondition(t, 2*time.Second, func() bool {
		text := sentText(channel)
		return strings.Contains(text, "尚未完成") && strings.Contains(text, "还在查")
	})
	if !strings.Contains(sentText(channel), "还在查") {
		t.Fatalf("最后一轮的结果没交出来: %q", sentText(channel))
	}
	if states := runtime.backgroundAgentsFor(event); len(states) != 1 || states[0].Phase != "exhausted" {
		t.Fatalf("states = %+v", states)
	}
}

// 取消后交出已有进度；别的会话取消不了；同一会话同时最多两个。
func TestBackgroundAgentCancelAndLimits(t *testing.T) {
	runtime, channel, event := backgroundTestRuntime()
	tool := newDianaBackgroundTaskTool(runtime, event)
	tool.run = func(ctx context.Context, state backgroundAgentState) (string, error) {
		if state.Round == 1 {
			return "【进度】读完第一份材料", nil
		}
		<-ctx.Done()
		return "", ctx.Err()
	}
	ids := make([]string, 0, 2)
	for range 2 {
		out, err := tool.Run(context.Background(), map[string]any{"operation": "start", "goal": "长活"})
		if err != nil {
			t.Fatal(err)
		}
		var started struct{ ID string }
		_ = json.Unmarshal([]byte(out), &started)
		ids = append(ids, started.ID)
	}
	if _, err := tool.Run(context.Background(), map[string]any{"operation": "start", "goal": "第三个"}); err == nil {
		t.Fatal("同一会话第三个任务没被拦")
	}
	other := newDianaBackgroundTaskTool(runtime, MessageEvent{Kind: EventKindPrivate, UserID: "10002"})
	if _, err := other.Run(context.Background(), map[string]any{"operation": "cancel", "id": ids[0]}); err == nil {
		t.Fatal("别的会话取消了这个会话的任务")
	}
	waitForCondition(t, 2*time.Second, func() bool {
		for _, state := range runtime.backgroundAgentsFor(event) {
			if state.ID == ids[0] && state.Round == 2 {
				return true
			}
		}
		return false
	})
	if _, err := tool.Run(context.Background(), map[string]any{"operation": "cancel", "id": ids[0]}); err != nil {
		t.Fatal(err)
	}
	waitForCondition(t, 2*time.Second, func() bool {
		text := sentText(channel)
		return strings.Contains(text, "⏹") && strings.Contains(text, "停下前的进度")
	})
	if text := sentText(channel); !strings.Contains(text, ids[0]) || !strings.Contains(text, "读完第一份材料") {
		t.Fatalf("取消通知 = %q", text)
	}
	_, _ = tool.Run(context.Background(), map[string]any{"operation": "cancel", "id": ids[1]})
}

// 长任务不占子任务并发槽：槽被占满时照样能跑。
func TestBackgroundAgentDoesNotTakeSubagentSlot(t *testing.T) {
	runtime, channel, event := backgroundTestRuntime()
	for range cap(runtime.subagentSem) {
		runtime.subagentSem <- struct{}{}
	}
	tool := newDianaBackgroundTaskTool(runtime, event)
	tool.run = func(context.Context, backgroundAgentState) (string, error) { return "【完成】好了", nil }
	if _, err := tool.Run(context.Background(), map[string]any{"operation": "start", "goal": "x"}); err != nil {
		t.Fatal(err)
	}
	waitForCondition(t, 2*time.Second, func() bool { return strings.Contains(lastText(channel), "好了") })
}

func TestParseBackgroundAgentOutput(t *testing.T) {
	for input, want := range map[string]string{
		"【进度】a":   backgroundAgentProgressTag,
		" 【完成】b ": backgroundAgentDoneTag,
		"直接给结果":   backgroundAgentDoneTag,
	} {
		if got, _ := parseBackgroundAgentOutput(input); got != want {
			t.Fatalf("%q => %q", input, got)
		}
	}
}

func TestBackgroundAgentConcurrentStartHonorsLimit(t *testing.T) {
	runtime, _, event := backgroundTestRuntime()
	release := make(chan struct{})
	defer close(release)
	ready := make(chan struct{})
	results := make(chan error, 20)
	for range 20 {
		go func() {
			<-ready
			_, err := runtime.startBackgroundAgent(context.Background(), event, "并发请求", 1, func(context.Context, backgroundAgentState) (string, error) {
				<-release
				return "【完成】结束", nil
			})
			results <- err
		}()
	}
	close(ready)
	started := 0
	for range 20 {
		if <-results == nil {
			started++
		}
	}
	if started != backgroundAgentPerSession {
		t.Fatalf("started %d, want %d", started, backgroundAgentPerSession)
	}
}

// 后台任务 status 带 id 时，目标、各轮记录和结果合成一份按整行分页，start_line 续读拼回全文。
func TestBackgroundAgentStatusPagesByWholeLines(t *testing.T) {
	state := backgroundAgentState{ID: "bg-1", Goal: "查清楚", Phase: "completed", Round: 3, MaxRounds: 3, StartedAt: time.Now()}
	for i := 1; i <= 3; i++ {
		state.Notes = append(state.Notes, strings.Repeat(fmt.Sprintf("第%d轮记录", i), 200))
	}
	state.Result = strings.Repeat("最终结论。", 300)
	want := "目标：查清楚"
	for i, note := range state.Notes {
		want += fmt.Sprintf("\n第 %d 轮记录：%s", i+1, note)
	}
	want += "\n结果：" + state.Result

	var lines []string
	start := 1
	for calls := 1; ; calls++ {
		if calls > 20 {
			t.Fatal("start_line 续读没有收敛")
		}
		raw, err := backgroundAgentStatusPage(state, start, 3_000)
		if err != nil {
			t.Fatal(err)
		}
		if len([]rune(raw)) > 3_000 {
			t.Fatalf("结果 %d 字超出上限", len([]rune(raw)))
		}
		var payload struct {
			Tasks []map[string]any `json:"tasks"`
		}
		if err := json.Unmarshal([]byte(raw), &payload); err != nil {
			t.Fatal(err)
		}
		item := payload.Tasks[0]
		lines = append(lines, strings.Split(item["detail"].(string), "\n")...)
		next, ok := item["next_line"].(float64)
		if !ok {
			break
		}
		if !strings.Contains(item["next"].(string), fmt.Sprintf("start_line=%d", int(next))) {
			t.Fatalf("续读提示：%q", item["next"])
		}
		start = int(next)
	}
	if strings.Join(lines, "\n") != want {
		t.Fatalf("续读拼回来的记录和原文不一致：%d 行", len(lines))
	}
}
