// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync"

	"github.com/SuInk/diana/model/agent"
	"github.com/SuInk/diana/model/llm"
)

// 只追加的主回复上下文：每个会话记住上一轮实际发给模型的请求，下一轮在它后面接上
// 这期间的新群消息、这一轮的尾部和当前消息，而不是按窗口重新组装。
//
// 重新组装时尾部每轮都变、上一轮的当前消息被改写成历史行，新请求从来不是上一条的
// 纯追加：DeepSeek 只能复用到分叉点，经 sub2api 的 gpt 更只认「instructions 加工具
// 末尾」和「上一次完整请求末尾」两个检查点，主回复命中只有三到四成。10-27 生产
// 同群连续 8 轮实测：gpt-6.1-sol 47.4% → 90.9%，deepseek-flash 72.2% → 91.2%。
//
// 续接时只追加对话和变了的东西：
//   - 新群消息和当前消息照发；
//   - 尾部每一段（以「【标题】」开头）和上文里同一标题最近的那一版比，一样就不发。
//     对话对象、本轮路由说明这类状态段在切换时会补上，连着几轮一样就不重复；
//   - 长期记忆按条、跨群历史按条，只发上文里还没有的；同一发言者的画像不重发；
//   - 可提及成员候选不发：按最近发言排序、几乎每轮都变，同样的 ID 历史行里都有。
// 段和条目都整块保留，不按行删，免得读起来缺头少尾。10-27 生产三个群各 12 轮模拟，
// 每轮增长从约 1.05 万字降到约 5000 字，主要是新记忆、新群消息和新跨群条目。
//
// 请求仍逐轮变长，超过这一轮完整组装的两倍就重新开一份；头部（人设、配置）或
// Runner 的系统提示、契约、工具定义变了也重开。记录只在内存里，进程重启后第一轮重新组装。

// appendContextGrowthLimit 是续接请求相对这一轮完整组装的长度上限倍数。
const appendContextGrowthLimit = 2

type appendContextLog struct {
	prefix  []llm.Message
	key     string
	headKey string
	// seen 是前缀里出现过的整条消息正文，判断历史行是不是新的；sent 是机器人
	// 已经发过的回复；latest 是每个段标题在前缀里最近的那一版；memory 是前缀里
	// 出现过的记忆条目和记忆块头。
	seen   map[string]bool
	sent   map[string]bool
	latest map[string]string
	memory map[string]bool
}

// appendContextVerifyReminder 每轮续接都插在当前消息前面，不参与「和上一版一样就
// 不发」。查证规则写在头部，续接后离当前消息隔着好几轮对话，前几轮又多是直接回答，
// 模型容易照着接着凭印象答。10-27 回放「我当时不是说过…」那一轮：现布局 5 次查了
// 2 次，续接不加提醒 3 次查 1 次，加了提醒 6 次都先查聊天记录。
const appendContextVerifyReminder = "【本轮提醒】对方提到以前说过、做过的事，或者涉及你拿不准的事实时，先用聊天记录、记忆或搜索查一下再回答，别凭印象。"

// appendContextMemoryTitle 是长期记忆块的开头，见 formatStructuredMemoryContextWithTokenBudgetDetailed。
const appendContextMemoryTitle = "【关系、权限与分层长期记忆"

// appendContextMentionTitle 是可提及成员候选，见 replyMentionPrompt。
const appendContextMentionTitle = "【可提及成员候选 JSON】"

// blockTitle 取段开头的「【…】」作为同一段的标识；不以「【」开头的段没有标题。
func blockTitle(block string) string {
	if !strings.HasPrefix(block, "【") {
		return ""
	}
	if end := strings.Index(block, "】"); end > 0 {
		return block[:end+len("】")]
	}
	return ""
}

// isCrossGroupLine 认出跨群历史（historyLinePrefix 给跨群来源打的标记）。
func isCrossGroupLine(content string) bool {
	return strings.HasPrefix(content, "[跨群历史")
}

func isMemoryItemLine(line string) bool {
	return strings.HasPrefix(line, "- [")
}

// splitContextBlocks 按以「【」开头的行把一条消息切成段，原样拼回去和原文相同。
func splitContextBlocks(content string) []string {
	var blocks []string
	var current []string
	for _, line := range strings.Split(content, "\n") {
		if strings.HasPrefix(line, "【") && len(current) > 0 {
			blocks = append(blocks, strings.Join(current, "\n"))
			current = nil
		}
		current = append(current, line)
	}
	return append(blocks, strings.Join(current, "\n"))
}

// memoryBlockParts 把记忆块拆成块头（标题、当前发言者、画像这些非条目行）和条目。
// 分段小标题（「相关情景：」这类紧挨着条目的行）不算块头。
func memoryBlockParts(block string) (head string, items []string) {
	lines := strings.Split(block, "\n")
	var heads []string
	for index, line := range lines {
		switch {
		case isMemoryItemLine(line):
			items = append(items, line)
		case index+1 < len(lines) && isMemoryItemLine(lines[index+1]):
		default:
			heads = append(heads, line)
		}
	}
	return strings.Join(heads, "\n"), items
}

// unsentMemoryBlock 只留上文里还没有的记忆条目，小标题跟着它下面还有新条目时才留。
// 块头变了（换了发言者、画像更新）就带上块头；块头和条目都发过，整块不发。
func unsentMemoryBlock(log *appendContextLog, block string) string {
	head, _ := memoryBlockParts(block)
	lines := strings.Split(block, "\n")
	var kept []string
	fresh := 0
	for index, line := range lines {
		switch {
		case isMemoryItemLine(line):
			if !log.memory[line] {
				kept = append(kept, line)
				fresh++
			}
		case index+1 < len(lines) && isMemoryItemLine(lines[index+1]):
			for next := index + 1; next < len(lines) && isMemoryItemLine(lines[next]); next++ {
				if !log.memory[lines[next]] {
					kept = append(kept, line)
					break
				}
			}
		default:
			kept = append(kept, line)
		}
	}
	if fresh == 0 && log.memory[head] {
		return ""
	}
	if log.memory[head] {
		// 同一发言者、同一份画像已经在上文里：只留标题行，后面跟新条目。
		headLines := map[string]bool{}
		for _, line := range strings.Split(head, "\n")[1:] {
			headLines[line] = true
		}
		slim := kept[:0]
		for _, line := range kept {
			if !headLines[line] {
				slim = append(slim, line)
			}
		}
		kept = slim
	}
	return strings.Join(kept, "\n")
}

// unsentTail 去掉一条尾部消息里上文已经有的部分，返回空串表示整条都不用发。
func unsentTail(log *appendContextLog, content string) string {
	if isCrossGroupLine(content) {
		if log.seen[content] {
			return ""
		}
		return content
	}
	var kept []string
	for _, block := range splitContextBlocks(content) {
		title := blockTitle(block)
		switch {
		case title == appendContextMentionTitle:
		case strings.HasPrefix(block, appendContextMemoryTitle):
			if rest := unsentMemoryBlock(log, block); rest != "" {
				kept = append(kept, rest)
			}
		case title != "" && log.latest[title] == block:
		default:
			kept = append(kept, block)
		}
	}
	return strings.Join(kept, "\n")
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
		seen: map[string]bool{}, sent: map[string]bool{}, latest: map[string]string{}, memory: map[string]bool{}}
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
		if isHistoryLine(content) || message.Role == llm.RoleAssistant {
			continue
		}
		for _, block := range splitContextBlocks(content) {
			if title := blockTitle(block); title != "" {
				log.latest[title] = block
			}
			if strings.HasPrefix(block, appendContextMemoryTitle) {
				head, items := memoryBlockParts(block)
				log.memory[head] = true
				for _, item := range items {
					log.memory[item] = true
				}
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
// 历史区（缓存断点及之前）只取记录里还没有的群消息；之后的尾部去掉上文已有的
// 参考资料；当前消息原样放最后。返回 nil 表示这一轮不能续接、应当重新组装。
func appendContextContinuation(log *appendContextLog, messages []llm.Message) (*agent.Continuation, appendContextDecision) {
	decision := appendContextDecision{fullTokens: estimateMessagesTokens(messages)}
	switch {
	case len(messages) == 0:
		decision.reason = "empty"
		return nil, decision
	case log == nil:
		// 进程刚启动、上一轮失败或这个会话第一次回复。
		decision.reason = "no_log"
		return nil, decision
	case appendContextHeadKey(messages) != log.headKey:
		decision.reason = "head_changed"
		return nil, decision
	}
	historyEnd := -1
	for index, message := range messages {
		if message.CacheBreakpoint {
			historyEnd = index
		}
	}
	// 开头连续的 system 是头部，已经由 headKey 核对过、就在前缀里。
	head := 0
	for head < len(messages)-1 && messages[head].Role == llm.RoleSystem {
		head++
	}
	var appended []llm.Message
	for index, message := range messages[head:] {
		index += head
		message.CacheBreakpoint = false
		if index == len(messages)-1 {
			appended = append(appended, message)
			continue
		}
		if index > historyEnd {
			if len(message.Parts) == 0 {
				message.Content = unsentTail(log, message.Content)
				if strings.TrimSpace(message.Content) == "" {
					continue
				}
			}
			appended = append(appended, message)
			continue
		}
		content := appendContextText(message)
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
		appended = append(appended, message)
	}
	appended = append(appended[:len(appended)-1:len(appended)-1],
		llm.Message{Role: llm.RoleSystem, Content: appendContextVerifyReminder}, appended[len(appended)-1])
	decision.prefixTokens, decision.appendTokens = estimateMessagesTokens(log.prefix), estimateMessagesTokens(appended)
	if decision.fullTokens > 0 && decision.prefixTokens+decision.appendTokens > appendContextGrowthLimit*decision.fullTokens {
		decision.reason = "too_long"
		return nil, decision
	}
	return &agent.Continuation{Prefix: log.prefix, Append: appended, Key: log.key}, decision
}

// appendContextDecision 记下这一轮为什么续接或没续接，写进调试轨迹。10-28 上线后
// 群聊连续两轮都没续上，却看不出是哪道校验拦的，所以每轮都留一条。
type appendContextDecision struct {
	reason       string
	previousKey  string
	fullTokens   int64
	prefixTokens int64
	appendTokens int64
}

// recordAppendContextDecision 在 Runner 跑完后补上 Runner 那一侧的结论：assistant 交了
// 续接但 Runner 的校验键（系统提示、契约、工具定义三段指纹）对不上时，记下新旧两把键，
// 一眼看出是哪一段变了。
func (r *Runtime) recordAppendContextDecision(ctx context.Context, decision appendContextDecision, continuation *agent.Continuation, resp *agent.Response, runErr error) {
	metadata := map[string]any{
		"phase":         "append_context",
		"continued":     false,
		"reason":        decision.reason,
		"full_tokens":   decision.fullTokens,
		"prefix_tokens": decision.prefixTokens,
		"append_tokens": decision.appendTokens,
	}
	switch {
	case runErr != nil:
		metadata["reason"] = "run_failed"
	case continuation != nil && resp != nil && resp.Continued:
		metadata["continued"] = true
		metadata["reason"] = ""
	case continuation != nil && resp != nil:
		metadata["reason"] = "runner_key_changed"
		metadata["previous_key"] = decision.previousKey
		metadata["current_key"] = resp.ConversationKey
	}
	message := "只追加上下文：重新组装"
	if metadata["continued"] == true {
		message = "只追加上下文：续接上一轮"
	}
	r.recordDebugTrace(debugTraceFromContext(ctx), message, metadata)
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
