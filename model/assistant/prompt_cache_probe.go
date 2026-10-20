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

// 这个探针定位本地请求结构的变化，不判定供应商是否命中缓存。它在适配器预算和协议
// 转换之前，将请求按 system / tools / 逐条消息做确定性指纹，与同会话、同用途的上次
// 观测比较，定位已观察范围内的首个分叉及其字节偏移。这里的段顺序、字节数和边界
// 不代表供应商内部序列化、token 检查点或可计费缓存规模。
//
// 指纹观测只读 payload；同一包装器另行补充稳定的匿名路由键，供兼容网关保持会话亲和。
// 已观察前缀顺利追加，或仅在本地稳定边界之后变化时不记分叉日志；没有日志不等于
// 实际缓存正常。实际命中需核对最终 wire、模型、请求间隔和原始 usage 缓存字段。

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
	// SegmentBytes 与 MessageHashes 等长，记录本地规范化段的字节数。
	SegmentBytes []int
	SystemBytes  int
	ToolsBytes   int
	// StableBoundary 是调用方标出的本地稳定历史边界，不是供应商确认的缓存位置。
	// markStablePromptPrefix 将它标在历史末尾，后面允许发言者资料、时钟与当前消息
	// 按轮变化。-1 表示没有标记，或标记位于最多 200 条消息的观测范围之外。
	StableBoundary int
	// 明文快照只活在内存里，用来算字节偏移；不落库、不进日志。
	systemText  string
	toolsText   string
	messageText []string
}

// promptCacheDivergence 描述两次请求之间第一个不同的位置。
type promptCacheDivergence struct {
	// Segment 是 system / tools / messages 之一；为空表示已观察范围未见前缀变化。
	Segment string
	// MessageIndex 只在 Segment == "messages" 时有意义。
	MessageIndex int
	// ByteOffset 是该段内首个不同的字节位置，-1 表示一段整体缺失或新增。
	ByteOffset int
	// ReusablePrefixBytes 是分叉前相同的本地规范化字节数，不是供应商可复用 token。
	ReusablePrefixBytes int
	// PreviousTotalBytes 是上次已观察部分的规范化总字节，不含未哈希的消息。
	PreviousTotalBytes int
	// ToolsHash 标出这条记录属于哪一套工具集，方便按主人/成员分别看命中率。
	ToolsHash string
}

// Clean 表示已观察范围未见前缀变化，不能证明整个请求相同或实际缓存命中。
func (d promptCacheDivergence) Clean() bool { return d.Segment == "" }

// Expected 表示没有观测到分叉，或分叉位于本地稳定边界之后。为减少逐轮噪声不记
// 这些变化；供应商仍可能受后缀大小、缓存驻留和最终适配结果影响而未命中历史。
func (d promptCacheDivergence) Expected(previous promptCachePayloadObservation) bool {
	if d.Clean() {
		return true
	}
	if d.Segment != "messages" || previous.StableBoundary < 0 {
		return false
	}
	return d.MessageIndex > previous.StableBoundary
}

func promptCacheHash(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:8])
}

// promptCacheCanonicalMessage 对角色、正文、Parts 和工具字段做本地确定性表示。
// 它不是最终 wire：不含全部 continuation 元数据，也尚未执行适配器预算和协议转换。
// Priority、ContextGroup 等本地编排元数据不参与比较。
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
//
// 只有开头连续的 system 消息算作 "system" 段——和适配层 splitSystemPrompt 的切法
// 一致。靠后的 system 消息（发言者尾部、实时时钟）按原位置进消息序列：它们本来就
// 每轮都变，混进 system 段会让探针每条消息都报「system 分叉、可复用前缀 0 字节」，
// 而真实命中率有九成。这个误报是拿真机跑一轮 QQ 群聊才发现的。
func observePromptCachePayload(req llm.GenerateRequest) promptCachePayloadObservation {
	var systemBuilder strings.Builder
	observation := promptCachePayloadObservation{StableBoundary: -1}
	leadingSystem := true
	for _, message := range req.Messages {
		if message.Role == llm.RoleSystem && leadingSystem {
			if systemBuilder.Len() > 0 {
				systemBuilder.WriteString("\x1d")
			}
			systemBuilder.WriteString(promptCacheCanonicalMessage(message))
			continue
		}
		leadingSystem = false
		if len(observation.MessageHashes) >= promptCacheProbeMaxHashedMessages {
			continue
		}
		text := promptCacheCanonicalMessage(message)
		if message.CacheBreakpoint && observation.StableBoundary < 0 {
			observation.StableBoundary = len(observation.MessageHashes)
		}
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
// 按本地诊断约定依次比较 system、tools 和 messages，遇到首次变化即返回。
// 不同供应商的实际字段层级和缓存策略可能不同，不能据此推断其缓存失效位置。
func comparePromptCachePayload(previous, current promptCachePayloadObservation) promptCacheDivergence {
	divergence := promptCacheDivergence{
		MessageIndex:       -1,
		ByteOffset:         -1,
		PreviousTotalBytes: previous.totalBytes(),
		ToolsHash:          current.ToolsHash,
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

// promptCacheProbeKey 决定「这一次该和哪一次比」。
//
// 用途必须进键：意图路由、记忆抽取、主回复用的是完全不同的提示词，混在一起比对
// 只会每次都报分叉，等于没有信号。
//
// 工具集指纹同样要进键。主人和普通成员拿到的工具不一样（真机实测 21 个 vs 44 个），
// 工具声明排在缓存层级最前面，两者本来就是两条独立的缓存前缀，供应商侧并存。
// 拿主人那轮去比成员那轮，必然报「system 从头就不同」——那不是故障，是两条线各自
// 都在正常复用。不区分的话，一个爱说话的主人能让这份日志永远在报警。
func promptCacheProbeKey(event MessageEvent, purpose string, toolsHash string) string {
	purpose = strings.TrimSpace(purpose)
	if purpose == "" {
		purpose = "unknown"
	}
	return groupPromptSessionKey(event) + "|" + purpose + "|" + toolsHash
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
	purpose := llmCallPurpose(ctx)
	if req.PromptCacheKey == "" {
		req.PromptCacheKey = promptCacheRoutingKey(p.event, purpose)
	}
	observation := observePromptCachePayload(req)
	previous, ok := p.runtime.promptCacheProbe.swap(promptCacheProbeKey(p.event, purpose, observation.ToolsHash), observation)
	response, err := p.provider.Generate(ctx, req)
	if ok {
		divergence := comparePromptCachePayload(previous, observation)
		if !divergence.Expected(previous) {
			p.runtime.recordPromptCacheDivergence(ctx, p.event, purpose, divergence, response)
		}
	}
	return response, err
}

func promptCacheRoutingKey(event MessageEvent, purpose string) string {
	// Do not put account IDs or group names on the wire. Tool additions must not
	// change routing; current tool authorization remains enforced by the registry.
	sum := sha256.Sum256([]byte(groupPromptSessionKey(event) + "\x00" + strings.TrimSpace(purpose)))
	return "diana-" + hex.EncodeToString(sum[:24])
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
		"tools_hash":            divergence.ToolsHash,
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
		Action:   "prompt_cache_divergence",
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
