// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync"

	"github.com/SuInk/diana/model/agent"
	"github.com/SuInk/diana/model/llm"
)

// 只追加的主回复上下文：每个会话记住上一轮实际发给模型的请求，下一轮在它后面接上
// 这期间的新群消息、尾部里变了的部分和当前消息，而不是按窗口重新组装。
//
// 重新组装时尾部每轮都变、上一轮的当前消息被改写成历史行，新请求从来不是上一条的
// 纯追加：DeepSeek 只能复用到分叉点，经 sub2api 的 gpt 更只认「instructions 加工具
// 末尾」和「上一次完整请求末尾」两个检查点，主回复命中只有三到四成。10-27 生产
// 同群连续 8 轮实测（尾部按行去重）：gpt-6.1-sol 47.4% → 90.9%，deepseek-flash
// 72.2% → 91.2%，未命中 token 分别少 70% 和 44%。
//
// 续接的代价是请求逐轮变长（去重后每轮约多 4600 字），所以超过这一轮完整组装的
// 两倍就重新开一份；头部（人设、配置）或 Runner 的系统提示、契约、工具定义变了也
// 重开。记录只在内存里，进程重启后第一轮重新组装。

// appendContextGrowthLimit 是续接请求相对这一轮完整组装的长度上限倍数。
const appendContextGrowthLimit = 2

// appendContextMinLineRunes 以下的短行不参与尾部去重：「】」「---」这类分隔行逐轮
// 都一样，去掉了反而把段落粘在一起。
const appendContextMinLineRunes = 20

type appendContextLog struct {
	prefix  []llm.Message
	key     string
	headKey string
	// seen 是前缀里出现过的整条消息正文，判断历史行是不是新的；lines 是前缀里非
	// 历史消息的行，给尾部去重。
	seen  map[string]bool
	lines map[string]bool
	sent  map[string]bool
}

type appendContextStore struct {
	mu   sync.Mutex
	logs map[string]*appendContextLog
}

func (s *appendContextStore) get(key string) *appendContextLog {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.logs[key]
}

func (s *appendContextStore) put(key string, log *appendContextLog) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.logs == nil {
		s.logs = map[string]*appendContextLog{}
	}
	if log == nil {
		delete(s.logs, key)
		return
	}
	s.logs[key] = log
}

func appendContextEnabled(cfg BotConfig) bool {
	return boolValue(cfg.AppendOnlyContext, false)
}

// appendContextKey 按配置档和会话区分记录：同一个群里两台机器人、私聊和群聊各一份。
func appendContextKey(event MessageEvent) string {
	return strings.TrimSpace(event.ProfileID) + "\x00" + sessionKey(event)
}

func appendContextHeadKey(messages []llm.Message) string {
	hash := sha256.New()
	for _, message := range messages {
		if message.Role != llm.RoleSystem {
			break
		}
		hash.Write([]byte(message.Content))
		hash.Write([]byte{0})
	}
	return hex.EncodeToString(hash.Sum(nil)[:16])
}

func newAppendContextLog(resp *agent.Response, headKey string) *appendContextLog {
	if resp == nil || len(resp.Transcript) == 0 || resp.ConversationKey == "" {
		return nil
	}
	log := &appendContextLog{prefix: resp.Transcript, key: resp.ConversationKey, headKey: headKey,
		seen: map[string]bool{}, lines: map[string]bool{}, sent: map[string]bool{}}
	for _, message := range resp.Transcript {
		content := appendContextText(message)
		log.seen[content] = true
		for _, call := range message.ToolCalls {
			if call.Name == agent.FinalizeToolName {
				if text, _ := call.Arguments["text"].(string); strings.TrimSpace(text) != "" {
					log.sent[strings.TrimSpace(text)] = true
				}
			}
		}
		if message.Role == llm.RoleAssistant && strings.TrimSpace(message.Content) != "" {
			log.sent[strings.TrimSpace(message.Content)] = true
		}
		if isHistoryLine(content) {
			continue
		}
		for _, line := range strings.Split(content, "\n") {
			if len([]rune(strings.TrimSpace(line))) >= appendContextMinLineRunes {
				log.lines[strings.TrimSpace(line)] = true
			}
		}
	}
	return log
}

// isHistoryLine 认出群聊历史行（stableGroupHistory 渲染的每条消息都以它开头）。
func isHistoryLine(content string) bool {
	return strings.HasPrefix(content, "[历史")
}

// appendContextContinuation 从这一轮完整组装的消息里挑出要接在记录后面的部分。
// 历史区（缓存断点及之前）只取记录里还没有的群消息；之后的尾部按行去掉记录里已经
// 有的；最后一条（当前消息）原样保留。返回 nil 表示这一轮不能续接、应当重新组装。
func appendContextContinuation(log *appendContextLog, messages []llm.Message) *agent.Continuation {
	if log == nil || len(messages) == 0 || appendContextHeadKey(messages) != log.headKey {
		return nil
	}
	historyEnd := -1
	for index, message := range messages {
		if message.CacheBreakpoint {
			historyEnd = index
		}
	}
	var appended []llm.Message
	for index, message := range messages {
		if index == len(messages)-1 {
			break
		}
		content := appendContextText(message)
		if index <= historyEnd {
			if message.Role == llm.RoleSystem || log.seen[content] {
				continue
			}
			switch {
			case isHistoryLine(content):
			case message.Role == llm.RoleAssistant && log.sent[strings.TrimSpace(content)]:
				// 机器人自己的回复已经以 send 调用的形式在记录里了。
				continue
			case message.Role == llm.RoleAssistant:
			default:
				// 历史区里的摘要、媒体索引这类块随窗口滑动变化，续接时历史不滑动，用不着。
				continue
			}
			message.CacheBreakpoint = false
			appended = append(appended, message)
			continue
		}
		if len(message.Parts) > 0 {
			appended = append(appended, message)
			continue
		}
		var kept []string
		for _, line := range strings.Split(message.Content, "\n") {
			if len([]rune(strings.TrimSpace(line))) >= appendContextMinLineRunes && log.lines[strings.TrimSpace(line)] {
				continue
			}
			kept = append(kept, line)
		}
		body := strings.TrimSpace(strings.Join(kept, "\n"))
		if body == "" || body == strings.TrimSpace(firstLine(message.Content)) {
			continue
		}
		message.Content = body
		appended = append(appended, message)
	}
	current := messages[len(messages)-1]
	current.CacheBreakpoint = false
	appended = append(appended, current)
	full := estimateMessagesTokens(messages)
	if full > 0 && estimateMessagesTokens(log.prefix)+estimateMessagesTokens(appended) > appendContextGrowthLimit*full {
		return nil
	}
	return &agent.Continuation{Prefix: log.prefix, Append: appended, Key: log.key}
}

// appendContextText 是比较用的消息正文：纯文本消息取 Content，分段消息拼文字段。
func appendContextText(message llm.Message) string {
	if len(message.Parts) == 0 {
		return message.Content
	}
	var builder strings.Builder
	builder.WriteString(message.Content)
	for _, part := range message.Parts {
		if part.Type == llm.ContentPartText {
			builder.WriteString(part.Text)
		}
	}
	return builder.String()
}

func estimateMessagesTokens(messages []llm.Message) int64 {
	var total int64
	for _, message := range messages {
		total += llm.EstimateMessageTokens(message)
	}
	return total
}
