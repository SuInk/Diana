// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/SuInk/diana/model/llm"
)

// enableAppendContextPersist 在这条用例里打开落盘（整包默认关着），用例结束时关回去。
func enableAppendContextPersist(t *testing.T) {
	t.Helper()
	appendContextPersistDisabled.Store(false)
	t.Cleanup(func() { appendContextPersistDisabled.Store(true) })
}

// 落盘的结构必须覆盖 llm.Message 的每个字段：漏掉一个，读回的前缀就和重启前发出去的
// 不一样，供应商缓存从那里作废。llm.Message 加了字段时这里会失败，提醒同步。
func TestAppendContextMessageCoversEveryField(t *testing.T) {
	if got, want := reflect.TypeOf(appendContextMessage{}).NumField(), reflect.TypeOf(llm.Message{}).NumField(); got != want {
		t.Fatalf("appendContextMessage 有 %d 个字段，llm.Message 有 %d 个", got, want)
	}
	if got, want := reflect.TypeOf(appendContextCall{}).NumField(), reflect.TypeOf(llm.ToolCall{}).NumField(); got != want {
		t.Fatalf("appendContextCall 有 %d 个字段，llm.ToolCall 有 %d 个", got, want)
	}
}

// 写下去再读回来，前缀逐字段相同，连平时不进 JSON 的推理续接状态、缓存断点也在；
// 由前缀算出的几张表也一样。
func TestAppendContextLogRoundTripsThroughDisk(t *testing.T) {
	enableAppendContextPersist(t)
	t.Setenv("APP_DB_PATH", filepath.Join(t.TempDir(), "diana.db"))
	reasoning := "opaque"
	prefix := []llm.Message{
		{Role: llm.RoleSystem, Content: "人设", Priority: llm.MessagePrioritySystem},
		{Role: llm.RoleUser, Content: "[历史 1] 甲: 你好", CacheBreakpoint: true, Priority: llm.MessagePriorityHistory, ContextGroup: "g", AtomicText: true},
		{Role: llm.RoleUser, Content: "【关系、权限与分层长期记忆】\n- [偏好｜甲] 喜欢猫"},
		{Role: llm.RoleAssistant, ContinuationScope: "responses:model", ReasoningContent: &reasoning,
			ResponsesOutput:   []json.RawMessage{json.RawMessage(`{"type":"reasoning","encrypted_content":"abc"}`)},
			AnthropicThinking: []json.RawMessage{json.RawMessage(`{"type":"thinking"}`)},
			ToolCalls:         []llm.ToolCall{{ID: "c1", Name: "send", Arguments: map[string]any{"text": "在的"}, ThoughtSignature: []byte{1, 2, 3}}}},
		{Role: llm.RoleTool, Content: "已发送", ToolCallID: "c1", ToolName: "send"},
		{Role: llm.RoleUser, Parts: []llm.ContentPart{{Type: llm.ContentPartText, Text: "看图"}, {Type: llm.ContentPartImageURL, ImageURL: "data:image/png;base64,AA"}}},
	}
	original := &appendContextLog{prefix: prefix, key: "run-key", headKey: "head-key", overhead: 1234}
	original.index()

	var store appendContextStore
	store.put("bot\x00group:1", original)
	store.disk.waitIdle()

	// 新进程：内存里什么都没有，从磁盘读回。
	var restarted appendContextStore
	loaded := restarted.get("bot\x00group:1")
	if loaded == nil {
		t.Fatal("没有读回续接记录")
	}
	if !reflect.DeepEqual(loaded.prefix, original.prefix) {
		t.Fatalf("读回的前缀和原来不一样：\n got %#v\nwant %#v", loaded.prefix, original.prefix)
	}
	if loaded.key != original.key || loaded.headKey != original.headKey || loaded.overhead != original.overhead {
		t.Fatalf("键或开销没读回：%q %q %d", loaded.key, loaded.headKey, loaded.overhead)
	}
	if !reflect.DeepEqual(loaded.seen, original.seen) || !reflect.DeepEqual(loaded.sent, original.sent) ||
		!reflect.DeepEqual(loaded.latest, original.latest) || !reflect.DeepEqual(loaded.memory, original.memory) ||
		!reflect.DeepEqual(loaded.pieces, original.pieces) {
		t.Fatal("由前缀算出的表和原来不一样")
	}
	// 同一个会话只读一次盘：读回后就在内存里。
	if again := restarted.get("bot\x00group:1"); again != loaded {
		t.Fatal("第二次 get 应当直接用内存里那份")
	}
	if restarted.get("bot\x00group:2") != nil {
		t.Fatal("别的会话不该读到记录")
	}
}

// 撤回到「没有记录」时删掉文件；过期的、别的会话的文件不读回；没设 APP_DB_PATH 不落盘。
func TestAppendContextDiskSkipsStaleAndRemovedRecords(t *testing.T) {
	enableAppendContextPersist(t)
	dir := t.TempDir()
	t.Setenv("APP_DB_PATH", filepath.Join(dir, "diana.db"))
	record := &appendContextLog{prefix: []llm.Message{{Role: llm.RoleSystem, Content: "人设"}}, key: "k", headKey: "h"}
	record.index()

	var store appendContextStore
	store.put("a", record)
	store.restore("a", record, nil)
	store.disk.waitIdle()
	var fresh appendContextStore
	if fresh.get("a") != nil {
		t.Fatal("撤回到没有记录后，文件应当已经删掉")
	}

	store.put("b", record)
	store.disk.waitIdle()
	path := appendContextFilePath(appendContextDir(), "b")
	old := time.Now().Add(-2 * appendContextFileMaxAge)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	var later appendContextStore
	if later.get("b") != nil {
		t.Fatal("过期的记录不该读回")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("过期文件应当被清掉：%v", err)
	}

	t.Setenv("APP_DB_PATH", "")
	var memoryOnly appendContextStore
	memoryOnly.put("c", record)
	memoryOnly.disk.waitIdle()
	entries, _ := os.ReadDir(filepath.Join(dir, "append-context"))
	for _, entry := range entries {
		if entry.Name() == filepath.Base(appendContextFilePath(filepath.Join(dir, "append-context"), "c")) {
			t.Fatal("没设 APP_DB_PATH 时不该落盘")
		}
	}
}

// 端到端：第一轮回复后换一个新的 Runtime（模拟进程重启），第二轮照样接着上一轮的请求
// 续接，前缀逐条相同。
func TestReplyContinuesAcrossRestart(t *testing.T) {
	enableAppendContextPersist(t)
	t.Setenv("APP_DB_PATH", filepath.Join(t.TempDir(), "diana.db"))
	newRuntime := func(reply string) (*Runtime, *sequenceLLMProvider) {
		provider := &sequenceLLMProvider{replies: []string{`tool_calls:[{"id":"s1","name":"send","arguments":{"text":"` + reply + `"}}]`}}
		runtime := NewRuntime(BotConfig{AgentEnabled: true, BotAccount: "42"}, &recordingChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
			return provider, nil
		})
		return runtime, provider
	}
	replyRequests := func(provider *sequenceLLMProvider) []llm.GenerateRequest {
		var out []llm.GenerateRequest
		for _, request := range provider.requestsSnapshot() {
			if len(request.Tools) > 0 {
				out = append(out, request)
			}
		}
		return out
	}
	before, firstProvider := newRuntime("在的")
	first := interimTestEvent()
	first.MessageID, first.RawMessage = "restart-1", "在吗"
	first.Segments = []MessageSegment{{Type: "text", Data: map[string]string{"text": "在吗"}}}
	if _, err := before.replyTo(t.Context(), first, first.RawMessage); err != nil {
		t.Fatal(err)
	}
	before.appendContexts.disk.waitIdle()

	after, secondProvider := newRuntime("吃过了")
	// 生产里身份别名的盐落库，重启后不变（见 identityAliasSalt）；测试没有存储，手动沿用。
	before.mu.Lock()
	after.aliasSalt = before.aliasSalt
	before.mu.Unlock()
	// 新进程里内存历史是空的，把第一轮那两条补回去，和正式启动时从库里读回历史一样。
	after.remember(first)
	second := interimTestEvent()
	second.MessageID, second.RawMessage, second.Time = "restart-2", "吃了吗", first.Time+30
	second.Segments = []MessageSegment{{Type: "text", Data: map[string]string{"text": "吃了吗"}}}
	if _, err := after.replyTo(t.Context(), second, second.RawMessage); err != nil {
		t.Fatal(err)
	}
	// 后台写完再结束，免得临时目录清理时文件还在写。
	after.appendContexts.disk.waitIdle()
	firstRequests, secondRequests := replyRequests(firstProvider), replyRequests(secondProvider)
	if len(firstRequests) != 1 || len(secondRequests) != 1 {
		t.Fatalf("reply requests = %d / %d", len(firstRequests), len(secondRequests))
	}
	prefix, next := firstRequests[0].Messages, secondRequests[0].Messages
	if len(next) <= len(prefix) {
		t.Fatalf("重启后的请求没有接在上一轮后面：%d vs %d", len(next), len(prefix))
	}
	for index, message := range prefix {
		got, want := next[index], message
		got.CacheBreakpoint, want.CacheBreakpoint = false, false
		if got.Role != want.Role || appendContextText(got) != appendContextText(want) {
			t.Fatalf("重启后前缀在第 %d 条分叉：\n got %s: %.200q\nwant %s: %.200q", index, got.Role, appendContextText(got), want.Role, appendContextText(want))
		}
	}
}
