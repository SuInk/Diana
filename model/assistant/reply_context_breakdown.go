// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/SuInk/diana/model/llm"
)

// 机器人设置「上下文」页的请求构成：按某个会话上一轮主回复第一个实际送到供应商的请求
// （replyRequestRecorder 在装饰链最里层记下，含隐私代理加的说明、工具定义和只用于本轮
// 的段）分块算 token。续接的轮次还要和上一轮实际请求对账，对不上就报错，不画一张和
// 真实组装对不上的图（见 noteReplyRequest）。

// ReplyContextSegment 是请求里的一块。Kind 按这条消息在真实请求里的身份分：
//   - prefix：工具定义和开头的 system 消息，会话里锁定不变；
//   - history：群消息行和机器人的发言（send 调用和「已发送」），逐条追加；
//   - state：其余进前缀的段（状态块、附注、名单……），留在前缀里给下一轮复用；
//   - turn_only：只用于这一轮的段（Message.TurnOnly），下一轮撤掉、每轮重发。
//
// NewTokens 是这块里这一轮新发的部分：请求开头和上一轮实际请求逐条相同的消息（工具
// 定义也相同时）不算，其余都算。供应商的前缀缓存最多只命中沿用的那部分。
type ReplyContextSegment struct {
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	Tokens    int64  `json:"tokens"`
	NewTokens int64  `json:"new_tokens"`
}

// replyContextHistoryName 是聊天历史那一块：群消息行和机器人的发言都只追加在这里。
const replyContextHistoryName = "聊天历史（只追加）"

// ReplyContextBreakdown 是请求构成：一个会话的，或这台机器人所有会话平均下来的一次请求。
type ReplyContextBreakdown struct {
	// Sessions 是平均了几个会话；单个会话时为 1。
	Sessions int    `json:"sessions"`
	Session  string `json:"session"`
	GroupID  string `json:"group_id,omitempty"`
	UserID   string `json:"user_id,omitempty"`
	// SavedAt 是这份请求记下的时间；零值表示进程里的记录没带时间。
	SavedAt time.Time `json:"saved_at,omitzero"`
	// TotalTokens 是各块估算之和，等于整份请求的估算。InputTokens 和 CachedTokens 是
	// 供应商对这次请求报回的输入用量和其中命中缓存的部分，0 表示没报。
	TotalTokens  int64                 `json:"total_tokens"`
	NewTokens    int64                 `json:"new_tokens"`
	InputTokens  int64                 `json:"input_tokens,omitempty"`
	CachedTokens int64                 `json:"cached_tokens,omitempty"`
	Segments     []ReplyContextSegment `json:"segments"`
	// ContextWindow 是模型窗口；CompactionThreshold 是请求超过它就重开的上限。
	ContextWindow       int64 `json:"context_window"`
	CompactionThreshold int64 `json:"compaction_threshold"`
}

// ReplyContextSession 是有续接记录、能画占比条的一个会话。
type ReplyContextSession struct {
	Session string    `json:"session"`
	GroupID string    `json:"group_id,omitempty"`
	UserID  string    `json:"user_id,omitempty"`
	SavedAt time.Time `json:"saved_at,omitzero"`
}

var replyContextBlockTitle = regexp.MustCompile(`^【([^】]{1,24})】`)

// replyContextBlockName 把请求里的一条消息归到一个块。index 是它在消息里的下标，
// headEnd 是开头连续 system 消息的条数（头部到此为止）。
func replyContextBlockName(index int, message llm.Message, headEnd int) string {
	content := appendContextText(message)
	switch {
	case index == 0:
		return "Runner 系统提示（协议、按需目录、规则）"
	case index < headEnd:
		return "头部（人设、回复规则、群风格）"
	case message.Role == llm.RoleAssistant || message.Role == llm.RoleTool, isHistoryLine(content):
		// 机器人的发言（send 调用和「已发送」）和群消息一样逐条追加，同属聊天历史。
		return replyContextHistoryName
	case strings.HasPrefix(content, currentMessageNotesTitle):
		return "【下一条消息的附注】"
	}
	if match := replyContextBlockTitle.FindStringSubmatch(content); match != nil {
		return "【" + match[1] + "】"
	}
	if message.Role == llm.RoleSystem {
		return "其他 system 段"
	}
	return "其他 user 段"
}

// replyContextToolsName 是工具定义那一块的名字。
const replyContextToolsName = "工具定义"

// replyContextSegments 按请求顺序分块：同名同类的块并到第一次出现的位置，所以从上到下
// 就是请求里的先后。
func replyContextSegments(messages []llm.Message, tools []llm.ToolDefinition, reused int) []ReplyContextSegment {
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
	// 工具定义续接时锁定沿用；重新组装那一轮和消息一样算新的。
	add(replyContextToolsName, "prefix", llm.EstimateToolDefinitionsTokens(tools), reused == 0)
	for index, message := range messages {
		name := replyContextBlockName(index, message, headEnd)
		kind := "state"
		switch {
		case index < headEnd:
			kind = "prefix"
		case message.TurnOnly:
			kind = "turn_only"
		case name == replyContextHistoryName:
			kind = "history"
		}
		add(name, kind, llm.EstimateMessageTokens(message), index >= reused)
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

// replyContextSessionEvent 从续接记录的键（配置档 \x00 会话）还原出会话。
func replyContextSessionEvent(profileID, session string) MessageEvent {
	event := MessageEvent{ProfileID: profileID}
	rest := session
	if index := strings.LastIndex(rest, "group:"); index >= 0 && (index == 0 || rest[index-1] == ':') {
		event.ContextNamespace = strings.TrimSuffix(rest[:index], ":")
		event.Kind, event.GroupID = EventKindGroup, rest[index+len("group:"):]
	} else if index := strings.LastIndex(rest, "private:"); index >= 0 && (index == 0 || rest[index-1] == ':') {
		event.ContextNamespace = strings.TrimSuffix(rest[:index], ":")
		event.Kind, event.UserID = EventKindPrivate, rest[index+len("private:"):]
	}
	return event
}

// ReplyContextSessions 列出这台机器人有续接记录的会话，最近的在前：进程里的，加上
// 落盘还没过期、这次进程还没读回来的。
func (r *Runtime) ReplyContextSessions(profileID string) []ReplyContextSession {
	profileID = strings.TrimSpace(profileID)
	prefix := profileID + "\x00"
	found := map[string]time.Time{}
	r.appendContexts.mu.Lock()
	for key, log := range r.appendContexts.logs {
		if log != nil && strings.HasPrefix(key, prefix) {
			found[strings.TrimPrefix(key, prefix)] = log.savedAt
		}
	}
	r.appendContexts.mu.Unlock()
	for key, savedAt := range appendContextDiskKeys() {
		if session, ok := strings.CutPrefix(key, prefix); ok {
			if _, seen := found[session]; !seen {
				found[session] = savedAt
			}
		}
	}
	sessions := make([]ReplyContextSession, 0, len(found))
	for session, savedAt := range found {
		event := replyContextSessionEvent(profileID, session)
		sessions = append(sessions, ReplyContextSession{Session: session, GroupID: event.GroupID, UserID: event.UserID, SavedAt: savedAt})
	}
	sort.Slice(sessions, func(a, b int) bool {
		if !sessions[a].SavedAt.Equal(sessions[b].SavedAt) {
			return sessions[a].SavedAt.After(sessions[b].SavedAt)
		}
		return sessions[a].Session < sessions[b].Session
	})
	return sessions
}

// ReplyContextBreakdownFor 算一个会话上一轮主回复第一步请求的构成。没有记录，或记录
// 是没存实际请求的老格式时 ok 为 false；请求和续接前缀对不上时返回错误。
func (r *Runtime) ReplyContextBreakdownFor(profileID, session string) (ReplyContextBreakdown, bool, error) {
	profileID, session = strings.TrimSpace(profileID), strings.TrimSpace(session)
	log := r.appendContexts.get(profileID + "\x00" + session)
	if log == nil || len(log.request.Messages) == 0 {
		return ReplyContextBreakdown{}, false, nil
	}
	if log.requestMismatch != "" {
		return ReplyContextBreakdown{}, false, fmt.Errorf("会话 %s：%s", session, log.requestMismatch)
	}
	event := replyContextSessionEvent(profileID, session)
	cfg := r.effectiveConfigForEvent(event)
	segments := replyContextSegments(log.request.Messages, log.request.Tools, log.requestReused)
	var total, fresh int64
	for _, segment := range segments {
		total += segment.Tokens
		fresh += segment.NewTokens
	}
	whole := llm.EstimateToolDefinitionsTokens(log.request.Tools) + estimateMessagesTokens(log.request.Messages)
	if total != whole {
		return ReplyContextBreakdown{}, false, fmt.Errorf("会话 %s：各块合计 %d token，整份请求 %d token，有消息没归进块", session, total, whole)
	}
	return ReplyContextBreakdown{
		Sessions: 1, Session: session, GroupID: event.GroupID, UserID: event.UserID, SavedAt: log.savedAt,
		TotalTokens: total, NewTokens: fresh, InputTokens: log.requestInputTokens, CachedTokens: log.requestCachedTokens, Segments: segments,
		ContextWindow:       r.promptContextWindowTokens(event, cfg),
		CompactionThreshold: compactionThreshold(r.promptCompactionWindow(event, cfg)),
	}, true, nil
}

// noteReplyRequest 把这一轮实际送到供应商的请求记进续接记录，并和上一轮实际请求对账：
// 续接的轮次，上一轮请求里不是只用于本轮的消息必须原样排在这一轮请求的最前面，工具
// 定义也不能变——这就是供应商前缀缓存能命中的前提。对不上记下原因，设置页如实报错。
// reused 是两份请求从头开始逐条相同的消息条数，按它区分沿用的和这一轮新发的。
func (log *appendContextLog) noteReplyRequest(request llm.GenerateRequest, usage llm.Usage, continued bool, previous *appendContextLog) {
	log.request, log.requestInputTokens, log.requestCachedTokens = request, usage.InputTokens, usage.CachedInputTokens
	if previous == nil || len(previous.request.Messages) == 0 {
		return
	}
	var carried []llm.Message
	for _, message := range previous.request.Messages {
		if !message.TurnOnly {
			carried = append(carried, message)
		}
	}
	reused := 0
	for reused < len(carried) && reused < len(request.Messages) && sameAppendContextMessage(carried[reused], request.Messages[reused]) {
		reused++
	}
	before, _ := json.Marshal(previous.request.Tools)
	after, _ := json.Marshal(request.Tools)
	sameTools := string(before) == string(after)
	if sameTools {
		log.requestReused = reused
	}
	if !continued {
		return
	}
	switch {
	case !sameTools:
		log.requestMismatch = "续接的这一轮实际请求的工具定义和上一轮不同"
	case reused < len(carried):
		now := "（这一轮请求到此结束）"
		if reused < len(request.Messages) {
			now = fmt.Sprintf("%.60q", appendContextText(request.Messages[reused]))
		}
		log.requestMismatch = fmt.Sprintf("续接的这一轮实际请求从第 %d 条起和上一轮不同：上一轮 %.60q，这一轮 %s", reused+1, appendContextText(carried[reused]), now)
	}
}

func sameAppendContextMessage(a, b llm.Message) bool {
	if a.Role != b.Role || appendContextText(a) != appendContextText(b) || a.ToolCallID != b.ToolCallID || len(a.ToolCalls) != len(b.ToolCalls) {
		return false
	}
	for index := range a.ToolCalls {
		if a.ToolCalls[index].ID != b.ToolCalls[index].ID || a.ToolCalls[index].Name != b.ToolCalls[index].Name {
			return false
		}
	}
	return true
}

// ReplyContextOverview 把这台机器人各会话上一轮的请求平均成一次：每块取平均 token，块
// 按第一次出现的先后排（头部各会话都一样，排在前面）。上限取各会话里最小的。哪个会话
// 对账不过就整体报错。
func (r *Runtime) ReplyContextOverview(profileID string) (ReplyContextBreakdown, bool, error) {
	var overview ReplyContextBreakdown
	at := map[string]int{}
	for _, session := range r.ReplyContextSessions(profileID) {
		breakdown, ok, err := r.ReplyContextBreakdownFor(profileID, session.Session)
		if err != nil {
			return ReplyContextBreakdown{}, false, err
		}
		if !ok {
			continue
		}
		overview.Sessions++
		overview.InputTokens += breakdown.InputTokens
		overview.CachedTokens += breakdown.CachedTokens
		if overview.SavedAt.IsZero() || breakdown.SavedAt.After(overview.SavedAt) {
			overview.SavedAt = breakdown.SavedAt
		}
		if overview.CompactionThreshold == 0 || breakdown.CompactionThreshold < overview.CompactionThreshold {
			overview.CompactionThreshold, overview.ContextWindow = breakdown.CompactionThreshold, breakdown.ContextWindow
		}
		for _, segment := range breakdown.Segments {
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
	if overview.Sessions == 0 {
		return ReplyContextBreakdown{}, false, nil
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
	return overview, true, nil
}

// appendContextDiskKeys 读出落盘记录的键和保存时间。文件开头就是版本和键，只解到
// 保存时间为止，不读整份前缀。
func appendContextDiskKeys() map[string]time.Time {
	dir := appendContextDir()
	if dir == "" {
		return nil
	}
	paths, _ := filepath.Glob(filepath.Join(dir, "*.json.gz"))
	keys := map[string]time.Time{}
	for _, path := range paths {
		key, savedAt, ok := readAppendContextFileHeader(path)
		if ok && time.Since(savedAt) <= appendContextFileMaxAge {
			keys[key] = savedAt
		}
	}
	return keys
}

func readAppendContextFileHeader(path string) (string, time.Time, bool) {
	file, err := os.Open(path)
	if err != nil {
		return "", time.Time{}, false
	}
	defer file.Close()
	zr, err := gzip.NewReader(file)
	if err != nil {
		return "", time.Time{}, false
	}
	defer zr.Close()
	decoder := json.NewDecoder(zr)
	if token, err := decoder.Token(); err != nil || token != json.Delim('{') {
		return "", time.Time{}, false
	}
	var key string
	var savedAt time.Time
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return "", time.Time{}, false
		}
		switch token {
		case "version":
			var version int
			if decoder.Decode(&version) != nil || version != appendContextFileVersion {
				return "", time.Time{}, false
			}
		case "key":
			if decoder.Decode(&key) != nil {
				return "", time.Time{}, false
			}
		case "saved_at":
			if decoder.Decode(&savedAt) != nil {
				return "", time.Time{}, false
			}
			return key, savedAt, key != ""
		default:
			return "", time.Time{}, false
		}
	}
	return "", time.Time{}, false
}

// replyRequestRecorder 记下主回复这一轮第一个送到供应商面前的请求和它的用量。它挂在
// provider 装饰链里紧挨着真正供应商的那一层（隐私代理、标记、预算这些改写请求的层都
// 在它外面），记下的就是模型实际收到的那份，不是 Runner 组装出来、还会被改写的中间稿。
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
	if r == nil {
		return llm.GenerateRequest{}, llm.Usage{}, false
	}
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
