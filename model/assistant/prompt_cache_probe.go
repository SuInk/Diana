// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"sync"

	"github.com/SuInk/diana/model/applog"
	"github.com/SuInk/diana/model/llm"
)

// 供应商的前缀缓存只认逐字节相同的前缀：从第一个不一样的字节起，后面全部要重算。
// 命中率掉下来时，`cached_input_tokens` 只告诉你「没命中」，不告诉你「哪里开始不一样」——
// 而能改的恰恰是后者。这个探针在每次请求前把 payload 拆成 system / tools / 逐条消息，
// 各算一个哈希，和同一会话同一用途的上一次请求逐段比对，定位到首个分叉的段、消息下标
// 和字节偏移。
//
// 它只读不写：不碰 GenerateRequest，不改变发给供应商的任何字节，因此自身不会影响命中率。
// 顺利追加（上一次是这一次的严格前缀）时什么都不记——那是期望状态，用量日志里的
// cached_input_tokens 已经能说明问题；只有前缀真的断了才留一条 debug 记录。

const (
	// promptCacheProbeMaxTracked 限制同时跟踪的会话数。每条记录只有若干哈希字符串，
	// 但群数量没有上限，必须有个天花板；超出时整体清空而不是逐个淘汰——探针是诊断
	// 工具，丢掉几次比对只是少一条日志，不值得为它维护一个 LRU。
	promptCacheProbeMaxTracked = 512
	// promptCacheProbeMaxHashedMessages 限制逐条哈希的消息数。超长历史下逐条哈希会
	// 变成每次请求的固定开销，而分叉点几乎总在靠前的位置——真正要盯的是稳定前缀。
	promptCacheProbeMaxHashedMessages = 200
)

// promptCachePayloadObservation 是一次请求的分段指纹。
type promptCachePayloadObservation struct {
	SystemHash string
	ToolsHash  string
	// MessageHashes 只覆盖非 system 消息，顺序与请求一致。
	MessageHashes []string
	// SegmentBytes 与 MessageHashes 等长，记录每段的字节数，用于估算可复用前缀规模。
	SegmentBytes []int
	SystemBytes  int
	ToolsBytes   int
	// 明文快照只活在内存里，用来算字节偏移；不落库、不进日志。
	systemText  string
	toolsText   string
	messageText []string
}

// promptCacheDivergence 描述两次请求之间第一个不同的位置。
type promptCacheDivergence struct {
	// Segment 是 system / tools / messages 之一；为空表示上一次是这一次的严格前缀。
	Segment string
	// MessageIndex 只在 Segment == "messages" 时有意义。
	MessageIndex int
	// ByteOffset 是该段内首个不同的字节位置，-1 表示一段整体缺失或新增。
	ByteOffset int
	// ReusablePrefixBytes 是分叉之前仍然逐字节相同的部分，用来衡量「还剩多少能复用」。
	ReusablePrefixBytes int
	// PreviousTotalBytes 是上一次请求的分段总字节，配合上一项看损失比例。
	PreviousTotalBytes int
}

// Clean 表示上一次请求是这一次的严格前缀，前缀缓存理论上可以完整复用。
func (d promptCacheDivergence) Clean() bool { return d.Segment == "" }

func promptCacheHash(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:8])
}

// promptCacheCanonicalMessage 把一条消息压成确定性文本。只取真正会进 payload 的字段：
// Priority、ContextGroup 这些是本地编排元数据，不发给供应商，跟着它们变会误报分叉。
func promptCacheCanonicalMessage(message llm.Message) string {
	var builder strings.Builder
	builder.WriteString(string(message.Role))
	builder.WriteString("\x1f")
	builder.WriteString(message.Content)
	for _, part := range message.Parts {
		builder.WriteString("\x1f")
		if encoded, err := json.Marshal(part); err == nil {
			builder.Write(encoded)
		}
	}
	for _, call := range message.ToolCalls {
		builder.WriteString("\x1f")
		if encoded, err := json.Marshal(call); err == nil {
			builder.Write(encoded)
		}
	}
	if message.ToolCallID != "" || message.ToolName != "" {
		builder.WriteString("\x1f")
		builder.WriteString(message.ToolName)
		builder.WriteString("\x1e")
		builder.WriteString(message.ToolCallID)
	}
	return builder.String()
}

func promptCacheCanonicalTools(tools []llm.ToolDefinition) string {
	if len(tools) == 0 {
		return ""
	}
	// 工具顺序本身就是缓存可见的一部分，这里不排序：顺序变了就该报分叉。
	encoded, err := json.Marshal(tools)
	if err != nil {
		return ""
	}
	return string(encoded)
}

// observePromptCachePayload 计算一次请求的分段指纹。
func observePromptCachePayload(req llm.GenerateRequest) promptCachePayloadObservation {
	var systemBuilder strings.Builder
	observation := promptCachePayloadObservation{}
	for _, message := range req.Messages {
		if message.Role == llm.RoleSystem {
			if systemBuilder.Len() > 0 {
				systemBuilder.WriteString("\x1d")
			}
			systemBuilder.WriteString(promptCacheCanonicalMessage(message))
			continue
		}
		if len(observation.MessageHashes) >= promptCacheProbeMaxHashedMessages {
			continue
		}
		text := promptCacheCanonicalMessage(message)
		observation.MessageHashes = append(observation.MessageHashes, promptCacheHash(text))
		observation.SegmentBytes = append(observation.SegmentBytes, len(text))
		observation.messageText = append(observation.messageText, text)
	}
	observation.systemText = systemBuilder.String()
	observation.toolsText = promptCacheCanonicalTools(req.Tools)
	observation.SystemHash = promptCacheHash(observation.systemText)
	observation.ToolsHash = promptCacheHash(observation.toolsText)
	observation.SystemBytes = len(observation.systemText)
	observation.ToolsBytes = len(observation.toolsText)
	return observation
}

// firstByteDifference 返回两段文本首个不同的字节位置；完全相同返回 -1。
func firstByteDifference(left, right string) int {
	limit := len(left)
	if len(right) < limit {
		limit = len(right)
	}
	for index := 0; index < limit; index++ {
		if left[index] != right[index] {
			return index
		}
	}
	if len(left) == len(right) {
		return -1
	}
	return limit
}

// comparePromptCachePayload 找出上一次与这一次之间第一个分叉的位置。
//
// 判定顺序刻意和供应商匹配前缀的顺序一致：system 在最前，其次是工具声明，最后才是
// 逐条消息。前面的段一旦不同，后面比对就没有意义——那部分本来就已经失效了。
func comparePromptCachePayload(previous, current promptCachePayloadObservation) promptCacheDivergence {
	divergence := promptCacheDivergence{
		MessageIndex:       -1,
		ByteOffset:         -1,
		PreviousTotalBytes: previous.totalBytes(),
	}
	if previous.SystemHash != current.SystemHash {
		divergence.Segment = "system"
		divergence.ByteOffset = firstByteDifference(previous.systemText, current.systemText)
		return divergence
	}
	divergence.ReusablePrefixBytes = previous.SystemBytes
	if previous.ToolsHash != current.ToolsHash {
		divergence.Segment = "tools"
		divergence.ByteOffset = firstByteDifference(previous.toolsText, current.toolsText)
		return divergence
	}
	divergence.ReusablePrefixBytes += previous.ToolsBytes
	for index := range previous.MessageHashes {
		if index >= len(current.MessageHashes) {
			// 这一次比上一次短：历史被裁剪或压缩过，上一次的前缀不再完整存在。
			divergence.Segment = "messages"
			divergence.MessageIndex = index
			return divergence
		}
		if previous.MessageHashes[index] == current.MessageHashes[index] {
			divergence.ReusablePrefixBytes += previous.SegmentBytes[index]
			continue
		}
		divergence.Segment = "messages"
		divergence.MessageIndex = index
		divergence.ByteOffset = firstByteDifference(previous.messageText[index], current.messageText[index])
		return divergence
	}
	return divergence
}

func (o promptCachePayloadObservation) totalBytes() int {
	total := o.SystemBytes + o.ToolsBytes
	for _, size := range o.SegmentBytes {
		total += size
	}
	return total
}

// promptCacheProbeStore 按「会话 + 用途」记住上一次请求的指纹。
//
// 用途必须进键：意图路由、记忆抽取、主回复用的是完全不同的提示词，混在一起比对
// 只会每次都报分叉，等于没有信号。
type promptCacheProbeStore struct {
	mu   sync.Mutex
	last map[string]promptCachePayloadObservation
}

func (s *promptCacheProbeStore) swap(key string, observation promptCachePayloadObservation) (promptCachePayloadObservation, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.last == nil {
		s.last = make(map[string]promptCachePayloadObservation)
	}
	if len(s.last) >= promptCacheProbeMaxTracked {
		if _, tracked := s.last[key]; !tracked {
			s.last = make(map[string]promptCachePayloadObservation)
		}
	}
	previous, ok := s.last[key]
	s.last[key] = observation
	return previous, ok
}

func promptCacheProbeKey(event MessageEvent, purpose string) string {
	purpose = strings.TrimSpace(purpose)
	if purpose == "" {
		purpose = "unknown"
	}
	return sessionKey(event) + "|" + purpose
}

// withPromptCacheProbeRun 把探针接进 provider 装饰链。
func (r *Runtime) withPromptCacheProbeRun(ctx context.Context, run llmProviderRunFunc) llmProviderRunFunc {
	state := llmUsageFromContext(ctx)
	if state == nil {
		// 没有消息事件就没有会话可归属，也就没有「上一次」可比。
		return run
	}
	return func(provider LLMProvider) (string, error) {
		return run(&promptCacheProbeLLMProvider{runtime: r, event: state.event, provider: provider})
	}
}

type promptCacheProbeLLMProvider struct {
	runtime  *Runtime
	event    MessageEvent
	provider LLMProvider
}

func (p *promptCacheProbeLLMProvider) Generate(ctx context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	purpose := llmUsagePurposeFromContext(ctx)
	if purpose == "" {
		purpose = debugModelPurpose(req)
	}
	observation := observePromptCachePayload(req)
	previous, ok := p.runtime.promptCacheProbe.swap(promptCacheProbeKey(p.event, purpose), observation)
	response, err := p.provider.Generate(ctx, req)
	if ok {
		divergence := comparePromptCachePayload(previous, observation)
		if !divergence.Clean() {
			p.runtime.recordPromptCacheDivergence(ctx, p.event, purpose, divergence, response)
		}
	}
	return response, err
}

// recordPromptCacheDivergence 记录一次前缀断裂。写 debug 类日志：它不是错误，
// 也不是用户可感知的操作，只在排查命中率时才需要翻出来。
func (r *Runtime) recordPromptCacheDivergence(
	ctx context.Context,
	event MessageEvent,
	purpose string,
	divergence promptCacheDivergence,
	response *llm.GenerateResponse,
) {
	writer := r.appLogWriter()
	if writer == nil {
		return
	}
	metadata := map[string]any{
		"group_id":              event.GroupID,
		"user_id":               event.UserID,
		"message_id":            event.MessageID,
		"purpose":               purpose,
		"segment":               divergence.Segment,
		"byte_offset":           divergence.ByteOffset,
		"reusable_prefix_bytes": divergence.ReusablePrefixBytes,
		"previous_total_bytes":  divergence.PreviousTotalBytes,
	}
	if divergence.MessageIndex >= 0 {
		metadata["message_index"] = divergence.MessageIndex
	}
	if response != nil {
		metadata["provider"] = string(response.Provider)
		metadata["model"] = response.Model
		metadata["cached_input_tokens"] = response.Usage.CachedInputTokens
		metadata["input_tokens"] = response.Usage.InputTokens
	}
	_ = writer.AppendLog(ctx, applog.Entry{
		Kind:     applog.KindDebug,
		Level:    applog.LevelInfo,
		Action:   "chatbot.prompt_cache.divergence",
		Message:  "提示词前缀在 " + promptCacheSegmentLabel(divergence) + " 处与上一次请求分叉",
		Actor:    oneBotEventActor(event),
		Target:   event.MessageID,
		Metadata: metadata,
	})
}

func promptCacheSegmentLabel(divergence promptCacheDivergence) string {
	switch divergence.Segment {
	case "system":
		return "系统提示词"
	case "tools":
		return "工具声明"
	case "messages":
		if divergence.MessageIndex >= 0 {
			return "历史消息第 " + itoa(divergence.MessageIndex) + " 条"
		}
		return "历史消息"
	default:
		return divergence.Segment
	}
}
