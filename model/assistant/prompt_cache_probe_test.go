// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"strings"
	"testing"

	"github.com/SuInk/diana/model/llm"
)

func promptCacheRequest(system string, tools []llm.ToolDefinition, contents ...string) llm.GenerateRequest {
	messages := []llm.Message{{Role: llm.RoleSystem, Content: system}}
	for index, content := range contents {
		role := llm.RoleUser
		if index%2 == 1 {
			role = llm.RoleAssistant
		}
		messages = append(messages, llm.Message{Role: role, Content: content})
	}
	return llm.GenerateRequest{Messages: messages, Tools: tools}
}

// 正常的一轮对话只在末尾追加，上一次是这一次的严格前缀，探针不该报任何分叉。
func TestPromptCacheProbeSilentOnCleanAppend(t *testing.T) {
	previous := observePromptCachePayload(promptCacheRequest("人设", nil, "你好", "你好呀"))
	current := observePromptCachePayload(promptCacheRequest("人设", nil, "你好", "你好呀", "在吗", "在的"))

	divergence := comparePromptCachePayload(previous, current)
	if !divergence.Clean() {
		t.Fatalf("干净追加不该报分叉：%#v", divergence)
	}
	// 整个上一轮都还能复用。
	if divergence.ReusablePrefixBytes != previous.totalBytes() {
		t.Fatalf("可复用字节 = %d，期望 %d", divergence.ReusablePrefixBytes, previous.totalBytes())
	}
}

// 系统提示词是最长也最该稳定的一段；它变了要第一时间指出来，并给出首个不同的字节位置。
func TestPromptCacheProbeDetectsSystemDrift(t *testing.T) {
	previous := observePromptCachePayload(promptCacheRequest("你是 Diana，说话简洁。", nil, "你好"))
	current := observePromptCachePayload(promptCacheRequest("你是 Diana，说话啰嗦。", nil, "你好"))

	divergence := comparePromptCachePayload(previous, current)
	if divergence.Segment != "system" {
		t.Fatalf("段 = %q，期望 system", divergence.Segment)
	}
	if divergence.ByteOffset <= 0 {
		t.Fatalf("字节偏移 = %d，期望指向「简/啰」那个字", divergence.ByteOffset)
	}
	// system 就断了，后面什么都复用不了。
	if divergence.ReusablePrefixBytes != 0 {
		t.Fatalf("可复用字节 = %d，期望 0", divergence.ReusablePrefixBytes)
	}
}

// 历史消息被事后改写是最隐蔽的一种：前面几条都对得上，从某一条起全部作废。
func TestPromptCacheProbeLocatesRewrittenHistoryMessage(t *testing.T) {
	previous := observePromptCachePayload(promptCacheRequest("人设", nil, "第一条", "回应一", "第二条", "回应二"))
	current := observePromptCachePayload(promptCacheRequest("人设", nil, "第一条", "回应一", "第二条[图片: 补上的描述]", "回应二"))

	divergence := comparePromptCachePayload(previous, current)
	if divergence.Segment != "messages" {
		t.Fatalf("段 = %q，期望 messages", divergence.Segment)
	}
	if divergence.MessageIndex != 2 {
		t.Fatalf("消息下标 = %d，期望 2", divergence.MessageIndex)
	}
	// 分叉之前的 system 与前两条消息仍然可复用。
	if divergence.ReusablePrefixBytes <= 0 || divergence.ReusablePrefixBytes >= previous.totalBytes() {
		t.Fatalf("可复用字节 = %d，应当是部分复用（总计 %d）", divergence.ReusablePrefixBytes, previous.totalBytes())
	}
}

// 工具描述改一个字，整份工具声明之后的内容全部失效——这类改动最容易被当成无害。
func TestPromptCacheProbeDetectsToolDrift(t *testing.T) {
	before := []llm.ToolDefinition{{Name: "search", Description: "联网搜索"}}
	after := []llm.ToolDefinition{{Name: "search", Description: "联网搜索（可读网页）"}}
	previous := observePromptCachePayload(promptCacheRequest("人设", before, "你好"))
	current := observePromptCachePayload(promptCacheRequest("人设", after, "你好"))

	divergence := comparePromptCachePayload(previous, current)
	if divergence.Segment != "tools" {
		t.Fatalf("段 = %q，期望 tools", divergence.Segment)
	}
	// system 相同，所以它那一段仍然算可复用。
	if divergence.ReusablePrefixBytes != previous.SystemBytes {
		t.Fatalf("可复用字节 = %d，期望 %d（仅 system）", divergence.ReusablePrefixBytes, previous.SystemBytes)
	}
}

// 工具顺序本身就是缓存可见的一部分，换个顺序同样要报。
func TestPromptCacheProbeDetectsToolReordering(t *testing.T) {
	before := []llm.ToolDefinition{{Name: "search"}, {Name: "image"}}
	after := []llm.ToolDefinition{{Name: "image"}, {Name: "search"}}
	previous := observePromptCachePayload(promptCacheRequest("人设", before, "你好"))
	current := observePromptCachePayload(promptCacheRequest("人设", after, "你好"))

	if divergence := comparePromptCachePayload(previous, current); divergence.Segment != "tools" {
		t.Fatalf("段 = %q，期望 tools", divergence.Segment)
	}
}

// 历史被压缩后这一次比上一次短，上一次的前缀不再完整存在，同样要报出来。
func TestPromptCacheProbeDetectsTruncatedHistory(t *testing.T) {
	previous := observePromptCachePayload(promptCacheRequest("人设", nil, "第一条", "回应一", "第二条", "回应二"))
	current := observePromptCachePayload(promptCacheRequest("人设", nil, "第一条", "回应一"))

	divergence := comparePromptCachePayload(previous, current)
	if divergence.Segment != "messages" || divergence.MessageIndex != 2 {
		t.Fatalf("段 = %q 下标 = %d，期望 messages/2", divergence.Segment, divergence.MessageIndex)
	}
}

// 本地编排元数据不会发给供应商，跟着它变会误报分叉。
func TestPromptCacheProbeIgnoresLocalOrchestrationFields(t *testing.T) {
	base := llm.Message{Role: llm.RoleUser, Content: "你好"}
	tagged := base
	tagged.Priority = llm.MessagePrioritySystem
	tagged.ContextGroup = "memory"
	tagged.CacheBreakpoint = true

	previous := observePromptCachePayload(llm.GenerateRequest{Messages: []llm.Message{{Role: llm.RoleSystem, Content: "人设"}, base}})
	current := observePromptCachePayload(llm.GenerateRequest{Messages: []llm.Message{{Role: llm.RoleSystem, Content: "人设"}, tagged}})

	if divergence := comparePromptCachePayload(previous, current); !divergence.Clean() {
		t.Fatalf("本地元数据不该被当成分叉：%#v", divergence)
	}
}

// 不同用途（意图路由 / 主回复）的提示词天然不同，必须各记各的，否则每次都报分叉。
// 工具集同理：真机上主人拿到 44 个工具、普通成员 21 个，这是两条并存的缓存前缀，
// 互相比较必然报「从头就不同」，而两条线各自都在正常复用。
func TestPromptCacheProbeKeysByPurposeAndToolSet(t *testing.T) {
	event := MessageEvent{Kind: EventKindGroup, GroupID: "123", UserID: "10001"}
	if promptCacheProbeKey(event, "chat", "t1") == promptCacheProbeKey(event, "intent", "t1") {
		t.Fatal("不同用途应当得到不同的键")
	}
	other := MessageEvent{Kind: EventKindGroup, GroupID: "456", UserID: "10001"}
	if promptCacheProbeKey(event, "chat", "t1") == promptCacheProbeKey(other, "chat", "t1") {
		t.Fatal("不同会话应当得到不同的键")
	}
	if promptCacheProbeKey(event, "chat", "owner-tools") == promptCacheProbeKey(event, "chat", "member-tools") {
		t.Fatal("不同工具集应当得到不同的键")
	}
}

// 群数量没有上限，跟踪表必须有天花板，否则常驻内存会一直涨。
func TestPromptCacheProbeStoreIsBounded(t *testing.T) {
	store := &promptCacheProbeStore{}
	observation := observePromptCachePayload(promptCacheRequest("人设", nil, "你好"))
	for index := 0; index < promptCacheProbeMaxTracked+50; index++ {
		store.swap("group:"+itoa(index), observation)
	}
	store.mu.Lock()
	tracked := len(store.last)
	store.mu.Unlock()
	if tracked > promptCacheProbeMaxTracked {
		t.Fatalf("跟踪数 = %d，超过上限 %d", tracked, promptCacheProbeMaxTracked)
	}
}

// 同一个键第二次调用要能拿到上一次的观测，这是整个探针的前提。
func TestPromptCacheProbeStoreReturnsPrevious(t *testing.T) {
	store := &promptCacheProbeStore{}
	first := observePromptCachePayload(promptCacheRequest("人设", nil, "你好"))
	if _, ok := store.swap("k", first); ok {
		t.Fatal("首次调用不该有上一次")
	}
	second := observePromptCachePayload(promptCacheRequest("人设", nil, "你好", "你好呀"))
	previous, ok := store.swap("k", second)
	if !ok || previous.SystemHash != first.SystemHash || len(previous.MessageHashes) != 1 {
		t.Fatalf("第二次应当拿到第一次的观测：ok=%v previous=%#v", ok, previous)
	}
}

// 日志里的段落标签要指到人能看懂的位置。
func TestPromptCacheSegmentLabel(t *testing.T) {
	if label := promptCacheSegmentLabel(promptCacheDivergence{Segment: "system"}); label != "系统提示词" {
		t.Fatalf("label = %q", label)
	}
	label := promptCacheSegmentLabel(promptCacheDivergence{Segment: "messages", MessageIndex: 7})
	if !strings.Contains(label, "第 7 条") {
		t.Fatalf("label = %q，应当带上消息下标", label)
	}
}
