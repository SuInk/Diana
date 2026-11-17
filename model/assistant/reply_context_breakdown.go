// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"compress/gzip"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/SuInk/diana/model/llm"
)

// 机器人设置「上下文」页的占比条：按某个会话上一轮实际发给模型的主回复请求（续接
// 记录里的前缀，加工具定义），从头到尾分块算 token。数字来自真实请求，不是按配置
// 估出来的模板；会话还没回复过就没有。

// ReplyContextSegment 是请求里的一块。Part 是 head（工具定义、系统提示和固定说明，
// 会话里锁定不变）或 tail（历史、状态块、机器人发言，逐轮往后追加）。
type ReplyContextSegment struct {
	Name   string `json:"name"`
	Part   string `json:"part"`
	Tokens int64  `json:"tokens"`
}

// ReplyContextBreakdown 是一个会话的请求构成。
type ReplyContextBreakdown struct {
	Session string `json:"session"`
	GroupID string `json:"group_id,omitempty"`
	UserID  string `json:"user_id,omitempty"`
	// SavedAt 是这份请求记下的时间；零值表示进程里的记录没带时间。
	SavedAt     time.Time             `json:"saved_at,omitzero"`
	TotalTokens int64                 `json:"total_tokens"`
	Segments    []ReplyContextSegment `json:"segments"`
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
	case message.Role == llm.RoleAssistant || message.Role == llm.RoleTool:
		return "机器人发言（send 调用和「已发送」）"
	case strings.HasPrefix(content, currentMessageNotesTitle):
		return "【下一条消息的附注】"
	case isHistoryLine(content):
		return "聊天消息行"
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

// replyContextSegments 按请求顺序分块：同名的块并到第一次出现的位置，所以条从左到右
// 就是头部在前、尾部按出现先后排。
func replyContextSegments(messages []llm.Message, tools []llm.ToolDefinition) []ReplyContextSegment {
	headEnd := leadingSystemMessages(messages)
	var segments []ReplyContextSegment
	at := map[string]int{}
	add := func(name, part string, tokens int64) {
		if tokens <= 0 {
			return
		}
		if index, ok := at[name]; ok {
			segments[index].Tokens += tokens
			return
		}
		at[name] = len(segments)
		segments = append(segments, ReplyContextSegment{Name: name, Part: part, Tokens: tokens})
	}
	add(replyContextToolsName, "head", llm.EstimateToolDefinitionsTokens(tools))
	for index, message := range messages {
		part := "tail"
		if index < headEnd {
			part = "head"
		}
		add(replyContextBlockName(index, message, headEnd), part, llm.EstimateMessageTokens(message))
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

// ReplyContextBreakdownFor 算一个会话上一轮主回复请求的构成；没有记录时 ok 为 false。
func (r *Runtime) ReplyContextBreakdownFor(profileID, session string) (ReplyContextBreakdown, bool) {
	profileID, session = strings.TrimSpace(profileID), strings.TrimSpace(session)
	log := r.appendContexts.get(profileID + "\x00" + session)
	if log == nil || len(log.prefix) == 0 {
		return ReplyContextBreakdown{}, false
	}
	event := replyContextSessionEvent(profileID, session)
	cfg := r.effectiveConfigForEvent(event)
	segments := replyContextSegments(log.prefix, log.definitions)
	var total int64
	for _, segment := range segments {
		total += segment.Tokens
	}
	return ReplyContextBreakdown{
		Session: session, GroupID: event.GroupID, UserID: event.UserID, SavedAt: log.savedAt,
		TotalTokens: total, Segments: segments,
		ContextWindow:       r.promptContextWindowTokens(event, cfg),
		CompactionThreshold: compactionThreshold(r.promptCompactionWindow(event, cfg)),
	}, true
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
