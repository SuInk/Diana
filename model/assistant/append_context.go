// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync"
	"time"

	"github.com/SuInk/diana/model/agent"
	"github.com/SuInk/diana/model/applog"
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
// 前缀里只留聊天本身：头部、聊天历史行、机器人自己发出的话和历轮的当前消息。
//   - 新群消息和当前消息追加进前缀；
//   - 历史区之后、当前消息之前的整段尾部（时间、发言者、对话对象、群约定、语气、
//     记忆、跨群参考……）只用于本轮（llm.Message.TurnOnly）：每轮按完整组装的结果
//     原样发出，不进前缀。重新组装的那一轮同样标记（markAppendContextTail）；
//   - 一轮里的工具调用和结果不进前缀，只在发言后面留一行「调用过哪些工具」
//     （见 agent 的 turnTranscript 和 appendContextPrefix）。
//
// 以前尾部留在前缀里，靠一套去重控制体积（带标题的段和上文最近一版相同就不发、固定
// 说明发过不再发、记忆按条去重）。生产统计每轮续接追加的内容里聊天只占三分之一，
// 其余是这些每轮在变的状态，约 5 轮就撑到上限被迫重新组装，那一轮缓存不命中、慢约
// 2 秒。用生产请求实测（swe-2，同群连续 8 轮）：标成只用于本轮不影响缓存命中
// （93.6%，严格追加是 93.9%）。
//
// 请求仍逐轮变长，超过这一轮完整组装的两倍或单次请求上限就重新开一份；头部（人设、
// 配置）或 Runner 的系统提示、固定说明、工具定义变了也重开。记录落盘，进程重启后
// 接着续（见 append_context_persist.go）。

// appendContextGrowthLimit 是续接请求相对这一轮完整组装的长度上限倍数。
//
// 「完整组装」要算上 Runner 自己加的系统提示、已加载工具契约和发言者工具段（记在
// overhead 里）。以前只拿助手这一侧的消息比，而续接前缀是整份请求：11-02 生产里
// 这两块各占一半，前缀续一两轮就超过「两倍」，46% 的主回复因 too_long 重新组装，
// 首步命中 swe-2 从续接时的 53% 掉到 19%、terra 从 74% 掉到 34%。
const appendContextGrowthLimit = 2

type appendContextLog struct {
	prefix  []llm.Message
	key     string
	headKey string
	// seen 是前缀里出现过的整条消息正文，判断历史行是不是新的；sent 是机器人
	// 已经发过的回复。
	seen map[string]bool
	sent map[string]bool
	// pieces 是前缀里历轮当前消息的每个段落（按空行切）：钉在当前消息末尾的固定注解
	// （群聊篇幅）上文有过就不再带。
	pieces map[string]bool
	// overhead 是 Runner 在助手消息之外加的部分（估算 token），在重新组装的那一轮
	// 由两边之差得出，续接期间沿用。
	overhead int64
	// historyDate 是前缀里最后一条带时间的历史行的日期。续接追加的历史行接在它后面，
	// 同一天只写时分秒、换天写全日期，和完整组装时的规则一样（见 history_line_dates.go）。
	historyDate string
}

// appendContextVerifyReminder 见 appendContextContinuation 里的说明。
const appendContextVerifyReminder = "【本轮提醒】对方提到以前说过、做过的事，或者涉及你拿不准的事实时，先用聊天记录、记忆或搜索查一下再回答，别凭印象。"

type appendContextStore struct {
	mu   sync.Mutex
	logs map[string]*appendContextLog
	// loaded 记着哪些会话已经试过从磁盘读回，同一个会话只读一次。
	loaded map[string]bool
	// disk 落盘续接记录，见 append_context_persist.go。零值按 APP_DB_PATH 定位。
	disk appendContextDisk
}

func (s *appendContextStore) get(key string) *appendContextLog {
	s.mu.Lock()
	defer s.mu.Unlock()
	if log, ok := s.logs[key]; ok || s.loaded[key] {
		return log
	}
	if s.loaded == nil {
		s.loaded = map[string]bool{}
	}
	s.loaded[key] = true
	// 进程重启后这个会话第一次回复：记录只在内存里的话只能整份重新组装，首步缓存
	// 基本落空（11-07～11-09 生产 141 次，四分之三没命中）。从磁盘读回上一次的。
	log := s.disk.load(key)
	if log != nil {
		if s.logs == nil {
			s.logs = map[string]*appendContextLog{}
		}
		s.logs[key] = log
	}
	return log
}

func (s *appendContextStore) put(key string, log *appendContextLog) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.setLocked(key, log)
}

// restore 把 key 的记录从 written 换回 previous；记录已经被别的轮次换掉时不动。
func (s *appendContextStore) restore(key string, written, previous *appendContextLog) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.logs[key] != written {
		return
	}
	s.setLocked(key, previous)
}

func (s *appendContextStore) setLocked(key string, log *appendContextLog) {
	if s.logs == nil {
		s.logs = map[string]*appendContextLog{}
	}
	if s.loaded == nil {
		s.loaded = map[string]bool{}
	}
	s.loaded[key] = true
	if log == nil {
		delete(s.logs, key)
	} else {
		s.logs[key] = log
	}
	s.disk.save(key, log)
}

// appendContextUndo 记下一次回复尝试写进续接记录的那份和写之前的那份。记录在 Agent
// 跑完时就写，这时回复还没过审核、也没发出；这一版最后被丢掉的话（发送前收到同一个人
// 的补充要重来、被新消息取代、审核拦下……），记录里那条草稿就成了「已经回过」。
// 11-04 生产：「虹夏是什么模型」答好了正在审核，对方补了个表情，重来那一版续接时看到
// 自己「刚答过」，静默收尾，问题就这么漏了。
type appendContextUndo struct {
	mu       sync.Mutex
	key      string
	previous *appendContextLog
	written  *appendContextLog
	noted    bool
}

type appendContextUndoKey struct{}

func withAppendContextUndo(ctx context.Context) (context.Context, *appendContextUndo) {
	undo := &appendContextUndo{}
	return context.WithValue(ctx, appendContextUndoKey{}, undo), undo
}

func appendContextUndoFromContext(ctx context.Context) *appendContextUndo {
	undo, _ := ctx.Value(appendContextUndoKey{}).(*appendContextUndo)
	return undo
}

// note 记一次写入。同一次尝试里写了几回（例如识图被拒后重跑）时，previous 留第一回
// 写之前的那份。
func (u *appendContextUndo) note(key string, previous, written *appendContextLog) {
	if u == nil {
		return
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if !u.noted || u.key != key {
		u.key, u.previous, u.noted = key, previous, true
	}
	u.written = written
}

// rollback 撤回这次尝试写的记录，下一轮从写之前那份接着续，这期间真正发出去的
// 消息会作为新历史追加进去。
func (r *Runtime) rollbackAppendContext(undo *appendContextUndo) {
	if undo == nil {
		return
	}
	undo.mu.Lock()
	key, previous, written, noted := undo.key, undo.previous, undo.written, undo.noted
	undo.noted, undo.written = false, nil
	undo.mu.Unlock()
	if noted {
		r.appendContexts.restore(key, written, previous)
	}
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

// newAppendContextLog 记下这一轮的请求供下一轮续接。fullTokens 是这一轮助手侧完整
// 组装的估算；previous 是续接所接的那份记录，用来沿用 overhead。
func newAppendContextLog(resp *agent.Response, headKey string, fullTokens int64, previous *appendContextLog) *appendContextLog {
	if resp == nil || len(resp.Transcript) == 0 || resp.ConversationKey == "" {
		return nil
	}
	log := &appendContextLog{prefix: appendContextPrefix(resp), key: resp.ConversationKey, headKey: headKey}
	switch {
	case !resp.Continued:
		log.overhead = max(0, resp.PromptTokens-fullTokens)
	case previous != nil:
		log.overhead = previous.overhead
	}
	log.index()
	return log
}

// appendContextPrefix 是下一轮续接的前缀：Runner 记下的这一轮（第一步请求里不是只用于
// 本轮的消息，加机器人实际发出的每句话），再补一行这一轮调用过哪些工具。
//
// 工具调用和结果本身不进前缀，但模型下一轮要知道上一轮查过什么（见 tool_call_memory.go
// 开头的两次线上事故）。这一行和重新组装时历史里的那行用同一个函数生成，位置也一样：
// 重新组装时记录挂在这一轮跑完后发出的第一条消息上，也就是收尾那句的第一条后面；
// 中途发言是跑的过程中发的，排在它前面。收尾没说话、只有中途发言时放在最后一句
// 后面；整轮没说话就不加——「你发上面这条回复前」无从说起，重新组装时也没有这一行。
func appendContextPrefix(resp *agent.Response) []llm.Message {
	transcript := resp.Transcript
	calls := replyToolCalls(resp.Steps)
	if len(calls) > replyToolTraceLimit {
		calls = calls[len(calls)-replyToolTraceLimit:]
	}
	note, ok := replyToolTraceNote(calls)
	if !ok {
		return transcript
	}
	// 这一轮的发言在末尾，每句是 send 调用加「已发送」两条。
	spoken := 0
	for spoken < len(transcript) && llm.IsHistoryReplyMessage(transcript[len(transcript)-1-spoken]) {
		spoken++
	}
	if spoken == 0 {
		return transcript
	}
	at := len(transcript)
	if interim := 2 * len(agent.InterimSendTexts(resp.Steps)); interim < spoken {
		at = len(transcript) - spoken + interim + 2
	}
	out := make([]llm.Message, 0, len(transcript)+1)
	out = append(out, transcript[:at]...)
	out = append(out, note)
	return append(out, transcript[at:]...)
}

// markAppendContextTail 把完整组装的请求里历史区之后、当前消息之前的消息标成只用于
// 本轮，这一轮记下的前缀就不含尾部，和续接的轮次一样。current 是当前消息的下标。
// 历史区的终点是缓存断点；没标断点（没有历史）时分不清哪些是尾部，不标。
func markAppendContextTail(messages []llm.Message, current int) {
	historyEnd := -1
	for index, message := range messages {
		if message.CacheBreakpoint {
			historyEnd = index
		}
	}
	if historyEnd < 0 {
		return
	}
	for index := historyEnd + 1; index < current && index < len(messages); index++ {
		messages[index].TurnOnly = true
	}
}

// index 从 prefix 算出 seen、sent、pieces 这几张表。它们完全由 prefix 决定，
// 落盘时只存 prefix，读回来再算一遍。
func (log *appendContextLog) index() {
	log.seen, log.sent, log.pieces = map[string]bool{}, map[string]bool{}, map[string]bool{}
	var dates historyLineDates
	for _, message := range log.prefix {
		content := appendContextText(message)
		log.seen[content] = true
		if message.Role == llm.RoleUser {
			// 历史行按写全日期的形态记：同一条消息在前缀里可能只写了时分秒，到下一轮完整
			// 组装里成了第一行、写全了日期，按原文比会当成新消息再追加一遍。
			full := message
			full.Content = dates.expand(message.Content)
			log.seen[appendContextText(full)] = true
		}
		for _, call := range message.ToolCalls {
			if call.Name == agent.FinalizeToolName {
				if text, _ := call.Arguments["text"].(string); strings.TrimSpace(text) != "" {
					log.sent[appendContextSentKey(text)] = true
				}
			}
		}
		if message.Role == llm.RoleAssistant && strings.TrimSpace(message.Content) != "" {
			log.sent[appendContextSentKey(message.Content)] = true
		}
		if isHistoryLine(content) || message.Role == llm.RoleAssistant {
			continue
		}
		for _, paragraph := range strings.Split(content, "\n\n") {
			log.pieces[strings.TrimSpace(paragraph)] = true
		}
	}
	log.historyDate = dates.last
}

// appendContextSentKey 是判断机器人回复是否已在记录里的键，去掉开头的引用标记。
// 记录里那一轮的回复是不带标记的正文，下一轮历史区把同一条出站消息按入库的
// reply 段渲染成「<diana-quote id=…/>正文」，原样比对不上就会再追加一遍：11-01～02
// 生产主回复请求里约一成带着这种一句两遍的机器人发言，同期还有一轮把 76 秒前的
// 旧回复加上引用原样重发到了群里。
// 只按语法去掉，不校验 ID：请求里的 ID 可能已经换成了 im_message_ 别名。
func appendContextSentKey(text string) string {
	_, rest, _ := consumeOutgoingReplyControl(text)
	return strings.TrimSpace(rest)
}

// isHistoryLine 认出群聊历史行（stableGroupHistory 渲染的每条消息都以它开头）。
func isHistoryLine(content string) bool {
	return strings.HasPrefix(content, "[历史")
}

// appendContextContinuation 从这一轮完整组装的消息里挑出要接在记录后面的部分。
// 历史区（缓存断点及之前）只取记录里还没有的群消息；之后的尾部整段原样带上、标成
// 只用于本轮；当前消息放最后。返回 nil 表示这一轮不能续接、应当重新组装。
//
// contextLimit 是续接请求的长度上限（token，0 表示不限；主回复传的是
// appendContextLimit(requestContextLimit())，即单次请求上限减去预留）：续接后的
// 请求超过它就重新组装。currentNotes 是完整组装时钉在当前消息末尾的固定注解（群聊
// 篇幅），续接时上文有过就去掉。
func appendContextContinuation(log *appendContextLog, messages []llm.Message, contextLimit int64, currentNotes ...string) (*agent.Continuation, appendContextDecision) {
	decision := appendContextDecision{fullTokens: estimateMessagesTokens(messages), contextLimit: contextLimit}
	switch {
	case len(messages) == 0:
		decision.reason = "empty"
		return nil, decision
	case log == nil:
		// 进程刚启动，或这个会话第一次回复。
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
	// fullDates 沿这一轮完整组装推出每条历史行的全日期，用来和前缀比对；appendDates 从
	// 前缀最后一条历史行的日期接着推，决定追加的行省不省日期。完整组装里的写法是相对它
	// 自己那份历史的前一行定的，接到前缀后面时前一行变了，要重写。
	var fullDates historyLineDates
	appendDates := historyLineDates{last: log.historyDate}
	// keepReply 记着上一条历史发言调用追加了没有：它的「已发送」结果跟着走，两半要么
	// 都追加、要么都不追加，否则留下不成对的调用，供应商整轮拒绝。
	keepReply := false
	for index, message := range messages[head:] {
		index += head
		message.CacheBreakpoint = false
		if index == len(messages)-1 {
			appended = append(appended, message)
			continue
		}
		if index <= historyEnd && llm.IsHistoryReplyMessage(message) {
			if message.Role == llm.RoleAssistant {
				// 机器人自己的发言：记录里已经有了（上一轮真实的 send，或重新组装时拼的
				// 发言调用，或老记录里的普通发言）就不再追加。
				keepReply = !log.sent[appendContextSentKey(llm.HistoryReplyText(message))]
			}
			if keepReply {
				appended = append(appended, message)
			}
			continue
		}
		if index > historyEnd {
			// 尾部每轮按完整组装的结果原样带上，只用于本轮：上一轮那份没有留在前缀里，
			// 所以哪怕和上一轮一字不差也照发，不做「上文已有就不发」的去重。
			message.TurnOnly = true
			appended = append(appended, message)
			continue
		}
		if message.Role == llm.RoleUser {
			message.Content = fullDates.expand(message.Content)
		}
		content := appendContextText(message)
		if message.Role == llm.RoleSystem || log.seen[content] {
			continue
		}
		switch {
		case isHistoryLine(content):
			if message.Role == llm.RoleUser {
				message.Content = appendDates.compact(message.Content)
			}
		case message.Role == llm.RoleAssistant && log.sent[appendContextSentKey(content)]:
			// 机器人自己的回复已经在记录里了。历史发言现在是 send 调用，在上面处理；
			// 这里接的是不带调用的老形态。
			continue
		case message.Role == llm.RoleAssistant:
		default:
			// 历史区里的摘要、媒体索引这类块随窗口滑动变化，续接时历史不滑动，用不着。
			continue
		}
		appended = append(appended, message)
	}
	// 当前消息末尾的固定注解（群聊篇幅）上文有过就不再带：当前消息留在前缀里，注解
	// 每轮都带就一路累积。11-11 回放去掉后回复中位字数 37 → 59、人设分 3.50 → 2.67，
	// 当时因此保留；11-12 用户决定去掉，请求体积优先。
	// 查证提醒每轮贴在当前消息前面，只用于本轮、不进前缀。查证规则写在头部，续接后
	// 离当前消息隔着好几轮对话，模型容易凭印象答。11-12 一度去掉：当晚同一时段主回复
	// 联网的轮次从前六天的 9%～37% 掉到 3%（89 轮里 3 轮）。
	appended = append(appended[:len(appended)-1:len(appended)-1],
		llm.Message{Role: llm.RoleSystem, Content: appendContextVerifyReminder, TurnOnly: true}, appended[len(appended)-1])
	last := &appended[len(appended)-1]
	for _, note := range currentNotes {
		note = strings.TrimSpace(note)
		if note == "" || !log.pieces[note] {
			continue
		}
		last.Content = strings.TrimSuffix(last.Content, "\n\n"+note)
	}
	decision.prefixTokens, decision.appendTokens = estimateMessagesTokens(log.prefix), estimateMessagesTokens(appended)
	decision.overhead = log.overhead
	if decision.fullTokens > 0 && decision.prefixTokens+decision.appendTokens > appendContextGrowthLimit*(decision.fullTokens+log.overhead) {
		decision.reason = "too_long"
		return nil, decision
	}
	// 上限必须管住续接。以前续接只看上面那道两倍，配置的上限只在完整组装时参与预算
	// 分配：11-12 生产 354 轮续接，同一轮完整组装中位 3.1 万 token，实际发出中位
	// 6.8 万、最大 13 万。超了交给发送层去裁会从中间丢消息，前缀照样断；在这里直接
	// 重新组装，得到的是一份按预算排好的请求。
	if contextLimit > 0 && decision.prefixTokens+decision.appendTokens > contextLimit {
		decision.reason = "context_limit"
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
	overhead     int64
	// contextLimit 是这一轮的单次请求上下文上限，0 表示不限。
	contextLimit int64
}

// recordAppendContextDecision 在 Runner 跑完后补上 Runner 那一侧的结论：assistant 交了
// 续接但 Runner 的校验键（系统提示、契约、工具定义三段指纹）对不上时，记下新旧两把键，
// 一眼看出是哪一段变了。
func (r *Runtime) recordAppendContextDecision(ctx context.Context, event MessageEvent, decision appendContextDecision, continuation *agent.Continuation, resp *agent.Response, runErr error) {
	metadata := map[string]any{
		"phase":           "append_context",
		"continued":       false,
		"reason":          decision.reason,
		"full_tokens":     decision.fullTokens,
		"prefix_tokens":   decision.prefixTokens,
		"append_tokens":   decision.appendTokens,
		"overhead_tokens": decision.overhead,
		"context_limit":   decision.contextLimit,
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
	r.recordAppendContextLog(ctx, event, metadata)
}

// recordAppendContextLog 把这一轮续接还是重组记进运行日志（调试记录不一定开着，也不进
// 库）。事件页「上下文占比」按群统计最近的实际请求时，靠它说清楚每轮是接着上一轮发的
// 还是整份重新组装、为什么重组。
func (r *Runtime) recordAppendContextLog(ctx context.Context, event MessageEvent, decision map[string]any) {
	writer := r.appLogWriter()
	if writer == nil {
		return
	}
	entry := applog.Entry{
		Kind:    applog.KindOperation,
		Level:   applog.LevelInfo,
		Action:  "append_context",
		Message: "主回复上下文续接结论",
		Actor:   oneBotEventActor(event),
		Target:  event.MessageID,
		Metadata: map[string]any{
			"group_id":      event.GroupID,
			"profile_id":    event.ProfileID,
			"message_id":    event.MessageID,
			"session_kind":  string(event.Kind),
			"continued":     decision["continued"],
			"reason":        decision["reason"],
			"prefix_tokens": decision["prefix_tokens"],
			"append_tokens": decision["append_tokens"],
			"full_tokens":   decision["full_tokens"],
		},
	}
	logCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	_ = writer.AppendLog(logCtx, entry)
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
