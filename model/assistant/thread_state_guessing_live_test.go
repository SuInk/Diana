// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/SuInk/diana/model/agent"
	"github.com/SuInk/diana/model/llm"
)

// TestLiveGroupGuessingGameKeepsLockedSecret 回放线上那局群聊猜谜：一人发起，几个群友轮流提问。
// 不给模型写死的 JSON，只用自然语言，要求模型自己把谜底存进 session 并锁定，之后谁来问
// 谜底都不变，「是两个字吗」的回答要和谜底实际字数一致。
func TestLiveGroupGuessingGameKeepsLockedSecret(t *testing.T) {
	client := liveThreadStateClient(t, liveThreadStateModel("DIANA_TEST_LLM_MODEL", "gpt-4o-mini"), "diana-live-thread-state-test")
	store := &memoryThreadStateStore{}
	now := time.Date(2026, 9, 15, 22, 9, 0, 0, time.Local)
	runtime := NewRuntime(BotConfig{}.WithDefaults(), nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.now = func() time.Time { return now }
	runtime.SetThreadStateStore(store)

	runTurn := func(userID, sender, messageID, text string) *agent.Response {
		t.Helper()
		event := MessageEvent{ProfileID: "bot-live", Kind: EventKindGroup, GroupID: "20005", UserID: userID, SenderName: sender, MessageID: messageID}
		messages := []llm.Message{{
			Role: llm.RoleSystem,
			Content: "你是 QQ 群里的机器人然然，回复简短口语化。当前是群聊，群里多个人都可能接着和你说话。\n" +
				promptToolThreadState + "\n工具调用结束后调用 agent_finalize 给出要发到群里的话。",
		}}
		if state := runtime.privateThreadStateContext(context.Background(), event); state != "" {
			messages = append(messages, llm.Message{Role: llm.RoleUser, Content: state, Priority: llm.MessagePriorityPlugin, AtomicText: true})
		}
		messages = append(messages, llm.Message{Role: llm.RoleUser, Content: "【当前需要回复的消息】群聊 20005，发送者 " + sender + "（" + userID + "）：" + text})
		runner, err := agent.NewRunner(client, agent.Config{WorkDir: t.TempDir(), MaxSteps: 8, ToolTimeoutMS: 30_000, FinalizationReserveMS: 10_000, EvidenceLedgerAdvisory: true},
			agent.NewToolRegistry(newDianaThreadStateTool(runtime, event)))
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = runner.Close() }()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		response, err := runner.Run(ctx, agent.Request{Messages: messages, TraceID: "live-guess-" + messageID})
		if err != nil {
			t.Fatalf("turn %s failed: %v", messageID, err)
		}
		var calls []string
		for _, step := range response.Steps {
			if step.Tool == dianaThreadStateToolName {
				encoded, _ := json.Marshal(step.Input)
				calls = append(calls, string(encoded))
			}
		}
		t.Logf("[%s] %s → %q  工具：%s", sender, text, response.Text, strings.Join(calls, " | "))
		return response
	}

	activeLocked := func(label string) (ThreadState, map[string]any) {
		t.Helper()
		store.mu.Lock()
		defer store.mu.Unlock()
		var active []ThreadState
		for _, item := range store.items {
			if item.Status == ThreadStateActive {
				active = append(active, item)
			}
		}
		if len(active) != 1 {
			t.Fatalf("%s：进行中的状态应只有一条，实际 %#v", label, store.items)
		}
		item := active[0]
		if item.Scope != ThreadStateScopeSession {
			t.Fatalf("%s：群聊猜谜应存进 session，实际 %s（%s）", label, item.Scope, item.State)
		}
		keys := threadStateLockedKeys(item.State)
		if len(keys) == 0 {
			t.Fatalf("%s：谜底没有锁定：%s", label, item.State)
		}
		var payload map[string]any
		_ = json.Unmarshal(item.State, &payload)
		locked := map[string]any{}
		for _, key := range keys {
			locked[key] = payload[key]
		}
		return item, locked
	}

	runTurn("741083048", "大眼小手", "m1", "@然然 我们来玩猜东西吧，你想好一个日常物品，我们轮流问是非题，你只能答是、不是或无关")
	first, locked := activeLocked("出题后")
	lockedJSON, _ := json.Marshal(locked)
	t.Logf("锁定的谜底：%s（task_kind=%s）", lockedJSON, first.TaskKind)

	secret := ""
	for _, value := range locked {
		if text, ok := value.(string); ok && utf8.RuneCountInString(text) > 0 && (secret == "" || utf8.RuneCountInString(text) < utf8.RuneCountInString(secret)) {
			secret = text
		}
	}

	turns := []struct{ user, sender, id, text string }{
		{"30003", "Rim de Lacent", "m2", "@然然 是 winter 用过的吗"},
		{"30007", "Winter", "m3", "@然然 是两个字吗"},
		{"934542274", "轩诺", "m4", "@然然 能吃吗"},
	}
	for _, turn := range turns {
		now = now.Add(time.Minute)
		response := runTurn(turn.user, turn.sender, turn.id, turn.text)
		_, current := activeLocked(turn.sender + " 提问后")
		if currentJSON, _ := json.Marshal(current); string(currentJSON) != string(lockedJSON) {
			t.Fatalf("%s 提问后谜底变了：%s → %s", turn.sender, lockedJSON, currentJSON)
		}
		if turn.id == "m3" && secret != "" {
			twoChars := utf8.RuneCountInString(secret) == 2
			saidNo := strings.Contains(response.Text, "不是")
			if twoChars == saidNo {
				t.Fatalf("谜底「%s」有 %d 个字，回答「%s」对不上", secret, utf8.RuneCountInString(secret), response.Text)
			}
		}
	}
}
