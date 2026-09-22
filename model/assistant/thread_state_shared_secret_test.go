// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func threadStateTestRuntime(t *testing.T) (*Runtime, *memoryThreadStateStore) {
	t.Helper()
	store := &memoryThreadStateStore{}
	now := time.Date(2026, 9, 15, 22, 9, 0, 0, time.Local)
	runtime := NewRuntime(BotConfig{}.WithDefaults(), nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.now = func() time.Time { return now }
	runtime.SetThreadStateStore(store)
	return runtime, store
}

func runThreadStateTool(t *testing.T, runtime *Runtime, event MessageEvent, input map[string]any) (map[string]any, error) {
	t.Helper()
	raw, err := newDianaThreadStateTool(runtime, event).Run(context.Background(), input)
	if err != nil {
		return nil, err
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatalf("decode %q: %v", raw, err)
	}
	return result, nil
}

// 回放线上那局群聊猜谜：大眼小手发起、Diana 出题，之后 Rim、Winter、轩诺轮流提问。
// 谜底存进 session 并锁定后，每个人的轮次都看得到同一个谜底，谁也改不了它。
func TestThreadStateGroupGuessingGameKeepsOneLockedSecret(t *testing.T) {
	runtime, store := threadStateTestRuntime(t)
	base := MessageEvent{ProfileID: "bot-1", Kind: EventKindGroup, GroupID: "20005"}
	starter := base
	starter.UserID, starter.MessageID = "741083048", "start"
	if _, err := runThreadStateTool(t, runtime, starter, map[string]any{
		"operation": "set", "scope": "session", "task_kind": "guess.word", "locked_keys": []any{"secret_word"},
		"state": map[string]any{"category": "日常用品", "secret_word": "耳机", "question_count": 0},
	}); err != nil {
		t.Fatal(err)
	}

	for index, userID := range []string{"30003", "30007", "934542274"} {
		player := base
		player.UserID, player.MessageID = userID, "q"+userID
		if context := runtime.privateThreadStateContext(context.Background(), player); !strings.Contains(context, `"secret_word":"耳机"`) {
			t.Fatalf("玩家 %s 的轮次看不到谜底：%q", userID, context)
		}
		listed, err := runThreadStateTool(t, runtime, player, map[string]any{"operation": "get"})
		if err != nil {
			t.Fatal(err)
		}
		if items, _ := listed["items"].([]any); len(items) != 1 || !strings.Contains(threadStateTestJSON(t, items[0]), `"secret_word":"耳机"`) {
			t.Fatalf("不传 task_kind 的 get 应列出这局游戏：%#v", listed)
		}
		// 模型想「重新出题」：换谜底必须被拒绝。
		if _, err := runThreadStateTool(t, runtime, player, map[string]any{
			"operation": "set", "scope": "session", "task_kind": "guess.word", "expected_version": index + 1,
			"state": map[string]any{"category": "日常用品", "secret_word": "钥匙", "question_count": index + 1},
		}); err == nil || !strings.Contains(err.Error(), "secret_word 已锁定") {
			t.Fatalf("玩家 %s 改写谜底没有被拦下：%v", userID, err)
		}
		// 只更新进度、省略谜底：谜底沿用原值。
		updated, err := runThreadStateTool(t, runtime, player, map[string]any{
			"operation": "set", "scope": "session", "task_kind": "guess.word", "expected_version": index + 1,
			"state": map[string]any{"category": "日常用品", "question_count": index + 1},
		})
		if err != nil {
			t.Fatal(err)
		}
		if encoded := threadStateTestJSON(t, updated); !strings.Contains(encoded, `"secret_word":"耳机"`) || !strings.Contains(encoded, `"_locked_keys":["secret_word"]`) {
			t.Fatalf("更新进度后谜底或锁定丢了：%s", encoded)
		}
	}
	if len(store.items) != 1 || store.items[0].Scope != ThreadStateScopeSession {
		t.Fatalf("一局游戏分裂成了多条状态：%#v", store.items)
	}
}

// 锁定只增不减：后续 set 不带 locked_keys 也保持锁定；锁定不存在的字段直接报错。
func TestThreadStateLocksCannotBeDropped(t *testing.T) {
	runtime, _ := threadStateTestRuntime(t)
	event := MessageEvent{ProfileID: "bot-1", Kind: EventKindPrivate, UserID: "u1", MessageID: "m1"}
	if _, err := runThreadStateTool(t, runtime, event, map[string]any{
		"operation": "set", "task_kind": "guess.word", "locked_keys": []any{"missing"},
		"state": map[string]any{"secret_word": "耳机"},
	}); err == nil || !strings.Contains(err.Error(), "missing 不在 state 里") {
		t.Fatalf("锁定不存在的字段应报错：%v", err)
	}
	if _, err := runThreadStateTool(t, runtime, event, map[string]any{
		"operation": "set", "task_kind": "guess.word", "locked_keys": []any{"secret_word"},
		"state": map[string]any{"secret_word": "耳机"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := runThreadStateTool(t, runtime, event, map[string]any{
		"operation": "set", "task_kind": "guess.word", "expected_version": 1,
		"state": map[string]any{"secret_word": "钥匙", "_locked_keys": []any{}},
	}); err == nil {
		t.Fatal("不带 locked_keys 或清空 _locked_keys 都不能解锁")
	}
}

// get 不传 task_kind 时列出当前会话和当前发言者的全部状态；传 scope 时只按作用域过滤。
func TestThreadStateGetWithoutTaskKindListsEverything(t *testing.T) {
	runtime, _ := threadStateTestRuntime(t)
	event := MessageEvent{ProfileID: "bot-1", Kind: EventKindGroup, GroupID: "g1", UserID: "u1", MessageID: "m1"}
	for _, input := range []map[string]any{
		{"operation": "set", "scope": "session", "task_kind": "guess.word", "state": map[string]any{"secret_word": "耳机"}},
		{"operation": "set", "task_kind": "form.signup", "state": map[string]any{"step": 2}},
	} {
		if _, err := runThreadStateTool(t, runtime, event, input); err != nil {
			t.Fatal(err)
		}
	}
	all, err := runThreadStateTool(t, runtime, event, map[string]any{"operation": "get"})
	if err != nil {
		t.Fatal(err)
	}
	if items, _ := all["items"].([]any); len(items) != 2 {
		t.Fatalf("应列出两条：%#v", all)
	}
	sessionOnly, err := runThreadStateTool(t, runtime, event, map[string]any{"operation": "get", "scope": "session"})
	if err != nil {
		t.Fatal(err)
	}
	if items, _ := sessionOnly["items"].([]any); len(items) != 1 || !strings.Contains(threadStateTestJSON(t, items[0]), "耳机") {
		t.Fatalf("按 scope 过滤不对：%#v", sessionOnly)
	}
	if _, err := runThreadStateTool(t, runtime, event, map[string]any{"operation": "set", "state": map[string]any{"x": 1}}); err == nil {
		t.Fatal("set 仍然必须提供 task_kind")
	}
}

// 结束时结果里带着结束前的最终状态，存储里照旧清空。
func TestThreadStateCompleteReturnsFinalStateButClearsStore(t *testing.T) {
	runtime, store := threadStateTestRuntime(t)
	event := MessageEvent{ProfileID: "bot-1", Kind: EventKindGroup, GroupID: "g1", UserID: "u1", MessageID: "m1"}
	if _, err := runThreadStateTool(t, runtime, event, map[string]any{
		"operation": "set", "scope": "session", "task_kind": "guess.word", "locked_keys": []any{"secret_word"},
		"state": map[string]any{"secret_word": "耳机"},
	}); err != nil {
		t.Fatal(err)
	}
	result, err := runThreadStateTool(t, runtime, event, map[string]any{"operation": "complete", "scope": "session", "task_kind": "guess.word", "expected_version": 1})
	if err != nil {
		t.Fatal(err)
	}
	if encoded := threadStateTestJSON(t, result); !strings.Contains(encoded, `"final_state":{`) || !strings.Contains(encoded, `"secret_word":"耳机"`) || !strings.Contains(encoded, `"status":"completed"`) {
		t.Fatalf("结束结果应带最终状态：%s", encoded)
	}
	if len(store.items) != 1 || len(store.items[0].State) != 0 {
		t.Fatalf("存储里应已清空：%#v", store.items)
	}
}

func TestThreadStatePromptSteersGroupGamesToLockedSession(t *testing.T) {
	for _, want := range []string{"群聊里发起的猜谜", "scope=session", "locked_keys", "不要重新出题"} {
		if !strings.Contains(promptToolThreadState, want) {
			t.Fatalf("thread state prompt missing %q", want)
		}
	}
	if description := (&dianaThreadStateTool{}).Description(); !strings.Contains(description, "locked_keys") || !strings.Contains(description, "get 不传 task_kind") {
		t.Fatalf("tool description = %q", description)
	}
}

func threadStateTestJSON(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}
