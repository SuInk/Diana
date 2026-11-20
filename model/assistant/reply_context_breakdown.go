// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"hash/fnv"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/SuInk/diana/model/llm"
)

// 机器人设置「上下文」卡片的柱形图：每个会话上一轮主回复第一步实际送到供应商的请求
// （replyRequestRecorder 在装饰链最里层记下，隐私代理改写过、带工具定义），按块算
// token，各会话平均成一次。只存每块的 token 数和每条消息的指纹，不存正文。

// ReplyContextSegment 是请求里的一块。Kind 只分三类，前端按它上色：
//   - prefix：工具定义和开头的 system 消息（Runner 系统提示、人设、规则）；
//   - history：群消息行和机器人的历史发言；
//   - changing：其余每轮或按人变化的段（尾部的状态段、记忆、当前消息……）。
//
// NewTokens 是这块里这一轮新发的部分：从请求开头起和同一会话上一轮请求逐条相同的
// 消息（工具定义也相同时）不算。供应商的前缀缓存最多只命中沿用的那部分。
type ReplyContextSegment struct {
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	Tokens    int64  `json:"tokens"`
	NewTokens int64  `json:"new_tokens"`
}

// ReplyContextBreakdown 是这台机器人各会话平均下来的一次主回复请求。
type ReplyContextBreakdown struct {
	// Sessions 是平均了几个会话。
	Sessions int `json:"sessions"`
	// SavedAt 是最新一份请求记下的时间。
	SavedAt time.Time `json:"saved_at,omitzero"`
	// TotalTokens 是各块估算之和。InputTokens 和 CachedTokens 是供应商报回的输入用量
	// 和其中命中缓存的部分，0 表示没报。
	TotalTokens  int64                 `json:"total_tokens"`
	NewTokens    int64                 `json:"new_tokens"`
	InputTokens  int64                 `json:"input_tokens,omitempty"`
	CachedTokens int64                 `json:"cached_tokens,omitempty"`
	Segments     []ReplyContextSegment `json:"segments"`
	// ContextWindow 是单次请求上下文上限生效后的窗口，各会话里取最小的。
	ContextWindow int64 `json:"context_window"`
}

const (
	replyContextToolsName   = "工具定义"
	replyContextHistoryName = "聊天历史"
	replyContextCurrentName = "当前消息"
	// replyContextMaxAge 之前的记录不进图：设置页看的是最近的样子。
	replyContextMaxAge = 24 * time.Hour
)

var replyContextBlockTitle = regexp.MustCompile(`^【([^】]{1,24})】`)

// replyContextBlock 把请求里的一条消息归到一个块。headEnd 是开头连续 system 消息的条数。
func replyContextBlock(index, last int, message llm.Message, headEnd int) (name, kind string) {
	content := appendContextText(message)
	switch {
	case index == 0 && headEnd > 0:
		return "Runner 系统提示", "prefix"
	case index < headEnd:
		return "头部（人设、规则）", "prefix"
	case index == last:
		return replyContextCurrentName, "changing"
	case isHistoryLine(content), llm.IsHistoryReplyMessage(message), message.Priority == llm.MessagePriorityHistory:
		return replyContextHistoryName, "history"
	}
	if match := replyContextBlockTitle.FindStringSubmatch(content); match != nil {
		return "【" + match[1] + "】", "changing"
	}
	if message.Role == llm.RoleAssistant || message.Role == llm.RoleTool {
		return "工具调用与结果", "changing"
	}
	return "其他段落", "changing"
}

// replyContextSegments 按请求顺序分块：同名同类的块并到第一次出现的位置。reused 是从头
// 起和上一轮相同的消息条数，sameTools 说明工具定义是否沿用。
func replyContextSegments(messages []llm.Message, tools []llm.ToolDefinition, reused int, sameTools bool) []ReplyContextSegment {
	headEnd := leadingSystemMessages(messages)
	var segments []ReplyContextSegment
	at := map[string]int{}
	add := func(name, kind string, tokens int64, fresh bool) {
		if tokens <= 0 {
			return
		}
		key := kind + "\x00" + name
		index, ok := at[key]
		if !ok {
			index = len(segments)
			at[key] = index
			segments = append(segments, ReplyContextSegment{Name: name, Kind: kind})
		}
		segments[index].Tokens += tokens
		if fresh {
			segments[index].NewTokens += tokens
		}
	}
	add(replyContextToolsName, "prefix", llm.EstimateToolDefinitionsTokens(tools), !sameTools)
	for index, message := range messages {
		name, kind := replyContextBlock(index, len(messages)-1, message, headEnd)
		add(name, kind, llm.EstimateMessageTokens(message), !sameTools || index >= reused)
	}
	return segments
}

func leadingSystemMessages(messages []llm.Message) int {
	for index, message := range messages {
		if message.Role != llm.RoleSystem {
			return index
		}
	}
	return len(messages)
}

// replyContextRecord 是一个会话上一轮请求的构成。fingerprints 是每条消息的指纹，
// 下一轮拿来算沿用了多少；tools 是工具定义的指纹。
type replyContextRecord struct {
	savedAt      time.Time
	segments     []ReplyContextSegment
	fingerprints []uint64
	tools        uint64
	inputTokens  int64
	cachedTokens int64
	window       int64
}

// replyContextStore 按「配置档 \x00 会话」存每个会话最近一轮的构成，只在内存里。
type replyContextStore struct {
	mu      sync.Mutex
	records map[string]replyContextRecord
}

func replyContextFingerprint(message llm.Message) uint64 {
	hash := fnv.New64a()
	hash.Write([]byte(message.Role))
	hash.Write([]byte{0})
	hash.Write([]byte(appendContextText(message)))
	hash.Write([]byte{0})
	hash.Write([]byte(message.ToolCallID))
	for _, call := range message.ToolCalls {
		hash.Write([]byte{0})
		hash.Write([]byte(call.ID))
		hash.Write([]byte(call.Name))
	}
	return hash.Sum64()
}

func replyContextToolsFingerprint(tools []llm.ToolDefinition) uint64 {
	raw, _ := json.Marshal(tools)
	hash := fnv.New64a()
	hash.Write(raw)
	return hash.Sum64()
}

// note 记下一个会话这一轮实际发出的请求。
func (s *replyContextStore) note(key string, request llm.GenerateRequest, usage llm.Usage, window int64, now time.Time) {
	fingerprints := make([]uint64, len(request.Messages))
	for index, message := range request.Messages {
		fingerprints[index] = replyContextFingerprint(message)
	}
	tools := replyContextToolsFingerprint(request.Tools)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.records == nil {
		s.records = map[string]replyContextRecord{}
	}
	previous, ok := s.records[key]
	reused := 0
	if ok {
		for reused < len(previous.fingerprints) && reused < len(fingerprints) && previous.fingerprints[reused] == fingerprints[reused] {
			reused++
		}
	}
	s.records[key] = replyContextRecord{
		savedAt:      now,
		segments:     replyContextSegments(request.Messages, request.Tools, reused, ok && previous.tools == tools),
		fingerprints: fingerprints,
		tools:        tools,
		inputTokens:  usage.InputTokens,
		cachedTokens: usage.CachedInputTokens,
		window:       window,
	}
	for other, record := range s.records {
		if now.Sub(record.savedAt) > replyContextMaxAge {
			delete(s.records, other)
		}
	}
}

// overview 把一台机器人最近 24 小时各会话的记录平均成一次：每块取平均，块按第一次
// 出现的先后排（头部各会话都一样，排在最前面）。
func (s *replyContextStore) overview(profileID string, now time.Time) (ReplyContextBreakdown, bool) {
	prefix := strings.TrimSpace(profileID) + "\x00"
	s.mu.Lock()
	keys := make([]string, 0, len(s.records))
	for key, record := range s.records {
		if strings.HasPrefix(key, prefix) && now.Sub(record.savedAt) <= replyContextMaxAge {
			keys = append(keys, key)
		}
	}
	// 最近的会话排前面，块的先后跟着它。
	sort.Slice(keys, func(a, b int) bool {
		left, right := s.records[keys[a]].savedAt, s.records[keys[b]].savedAt
		if !left.Equal(right) {
			return left.After(right)
		}
		return keys[a] < keys[b]
	})
	records := make([]replyContextRecord, 0, len(keys))
	for _, key := range keys {
		records = append(records, s.records[key])
	}
	s.mu.Unlock()
	if len(records) == 0 {
		return ReplyContextBreakdown{}, false
	}
	var overview ReplyContextBreakdown
	at := map[string]int{}
	for _, record := range records {
		overview.Sessions++
		overview.InputTokens += record.inputTokens
		overview.CachedTokens += record.cachedTokens
		if record.savedAt.After(overview.SavedAt) {
			overview.SavedAt = record.savedAt
		}
		if record.window > 0 && (overview.ContextWindow == 0 || record.window < overview.ContextWindow) {
			overview.ContextWindow = record.window
		}
		for _, segment := range record.segments {
			key := segment.Kind + "\x00" + segment.Name
			if index, ok := at[key]; ok {
				overview.Segments[index].Tokens += segment.Tokens
				overview.Segments[index].NewTokens += segment.NewTokens
				continue
			}
			at[key] = len(overview.Segments)
			overview.Segments = append(overview.Segments, segment)
		}
	}
	sessions := int64(overview.Sessions)
	overview.InputTokens /= sessions
	overview.CachedTokens /= sessions
	for index := range overview.Segments {
		overview.Segments[index].Tokens /= sessions
		overview.Segments[index].NewTokens /= sessions
		overview.TotalTokens += overview.Segments[index].Tokens
		overview.NewTokens += overview.Segments[index].NewTokens
	}
	return overview, true
}

// ReplyContextOverview 是设置页柱形图的数据：这台机器人最近 24 小时各会话上一轮主回复
// 第一步请求的平均构成。没有记录时 ok 为 false。
func (r *Runtime) ReplyContextOverview(profileID string) (ReplyContextBreakdown, bool) {
	return r.replyContexts.overview(profileID, time.Now())
}

// replyRequestRecorder 记下主回复这一轮第一个送到供应商面前的请求和它的用量。它挂在
// provider 装饰链里紧挨着真正供应商的那一层（隐私代理、标记、预算这些改写请求的层都
// 在它外面），记下的就是模型实际收到的那份。
type replyRequestRecorder struct {
	mu      sync.Mutex
	request llm.GenerateRequest
	usage   llm.Usage
	done    bool
}

type replyRequestRecorderKey struct{}

func withReplyRequestRecorder(ctx context.Context) (context.Context, *replyRequestRecorder) {
	recorder := &replyRequestRecorder{}
	return context.WithValue(ctx, replyRequestRecorderKey{}, recorder), recorder
}

// first 返回记下的请求和用量；还没有成功的请求时 ok 为 false。
func (r *replyRequestRecorder) first() (llm.GenerateRequest, llm.Usage, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.request, r.usage, r.done
}

func withReplyRequestRecordRun(ctx context.Context, run llmProviderRunFunc) llmProviderRunFunc {
	if _, ok := ctx.Value(replyRequestRecorderKey{}).(*replyRequestRecorder); !ok {
		return run
	}
	return func(provider LLMProvider) (string, error) {
		return run(&replyRequestRecordProvider{provider: provider})
	}
}

type replyRequestRecordProvider struct {
	provider LLMProvider
}

func (p *replyRequestRecordProvider) Generate(ctx context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	response, err := p.provider.Generate(ctx, req)
	recorder, _ := ctx.Value(replyRequestRecorderKey{}).(*replyRequestRecorder)
	if err == nil && response != nil && recorder != nil {
		recorder.mu.Lock()
		if !recorder.done {
			recorder.request, recorder.usage, recorder.done = req, response.Usage, true
		}
		recorder.mu.Unlock()
	}
	return response, err
}

// recordReplyContext 把这一轮记下的请求存进柱形图的记录。
func (r *Runtime) recordReplyContext(event MessageEvent, cfg BotConfig, recorder *replyRequestRecorder) {
	request, usage, ok := recorder.first()
	if !ok {
		return
	}
	r.replyContexts.note(appendContextKey(event), request, usage, r.promptContextWindowTokens(event, cfg), time.Now())
}
