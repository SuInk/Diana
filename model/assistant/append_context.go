// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"math"
	"sort"
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
// 续接时只追加对话和变了的东西：
//   - 新群消息和当前消息照发；
//   - 尾部每一段（以「【标题】」开头）和上文里同一标题最近的那一版比，一样就不发。
//     对话对象、本轮路由说明这类状态段在切换时会补上，连着几轮一样就不重复；
//   - 长期记忆按条、跨群历史按条，只发上文里还没有的；同一发言者的画像不重发；
//   - 可提及成员候选不发：按最近发言排序、几乎每轮都变，同样的 ID 历史行里都有。
//   - 实时时钟那条按句比：时间每轮都变，后面「这是可信实时时间……」「按他的当地时间说……」
//     这些说明逐字不变，发过就只发带时间的那几句（见 unsentClock）。
// 段和条目都整块保留，不按行删，免得读起来缺头少尾。10-27 生产三个群各 12 轮模拟，
// 每轮增长从约 1.05 万字降到约 5000 字，主要是新记忆、新群消息和新跨群条目。
//
// 请求仍逐轮变长，超过这一轮完整组装的两倍就重新开一份；头部（人设、配置）或
// Runner 的系统提示、契约、工具定义变了也重开。记录只在内存里，进程重启后第一轮重新组装。

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
	// 已经发过的回复；latest 是每个段标题在前缀里最近的那一版；memory 是前缀里
	// 出现过的记忆条目和记忆块头。
	seen   map[string]bool
	sent   map[string]bool
	latest map[string]string
	memory map[string]bool
	// pieces 是前缀里出现过的每个段（按「【」切）和每个段落（按空行切），续接时
	// 不带标题的固定说明逐字一样就不再发。
	pieces map[string]bool
	// clockSentences 是前缀里实时时钟那条消息按句拆开的每一句，见 unsentClock。
	clockSentences map[string]bool
	// sentClauses 是已发回复按行、再按断句拆开的每一小句：一次 send 写了几行，或者
	// 纯文本平台照 MaiBot 在逗号、空格处断开，发出去拆成几条，下一轮历史里就是几句
	// 各自的发言，整段比对不上。
	sentClauses map[string]bool
	// answered 是前缀里当作【当前需要回复的消息】或同轮补充答过的聊天消息（历史
	// 去重键）。续接时它们不再作为历史行追加：追加的东西都排在上一轮回复后面，
	// 模型读到的就是「答完以后对方又问了一遍」，换个说法再答一次。11-21 生产
	// Telegram 群 13:02 量子计算、13:49 F1 排位赛两次重复都是这样来的。
	answered map[string]bool
	// overhead 是 Runner 在助手消息之外加的部分（估算 token），在重新组装的那一轮
	// 由两边之差得出，续接期间沿用。
	overhead int64
	// at、model 是写下这份记录的那一轮什么时候发的第一次请求、实际由哪个模型答的：
	// 下一轮据此估前缀还在不在缓存里，见 append_context_warmth.go。
	at    time.Time
	model string
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

// clockSentences 把实时时钟那条消息按行、按句号拆成句子，句号留在句尾。
func clockSentences(content string) [][]string {
	var lines [][]string
	for _, line := range strings.Split(content, "\n") {
		var sentences []string
		for rest := line; rest != ""; {
			end := strings.Index(rest, "。")
			if end < 0 {
				sentences = append(sentences, rest)
				break
			}
			end += len("。")
			sentences = append(sentences, rest[:end])
			rest = rest[end:]
		}
		lines = append(lines, sentences)
	}
	return lines
}

// unsentClock 只留实时时钟里上文没有的句子。时钟每轮都变，整段比永远对不上，以前
// 每轮续接都把「这是机器人所在机器提供的可信实时时间……」和发言者时区的换算说明
// 整段重发一遍：11-21 生产 swe-2 主回复里平均每条请求叠着两三份。句子是从模板逐字
// 渲染出来的，带时间、时区这些值的句子每轮不同照发，固定说明发过一次就不再发。
func unsentClock(log *appendContextLog, content string) string {
	var kept []string
	for _, sentences := range clockSentences(content) {
		var line strings.Builder
		for _, sentence := range sentences {
			if !log.clockSentences[strings.TrimSpace(sentence)] {
				line.WriteString(sentence)
			}
		}
		if strings.TrimSpace(line.String()) != "" {
			kept = append(kept, line.String())
		}
	}
	return strings.Join(kept, "\n")
}

// unsentTail 去掉一条尾部消息里上文已经有的部分，返回空串表示整条都不用发。
func unsentTail(log *appendContextLog, content string) string {
	if strings.Contains(content, agent.RuntimeClockMarker) {
		return unsentClock(log, content)
	}
	if isCrossGroupLine(content) {
		if log.seen[content] {
			return ""
		}
		return content
	}
	var kept []string
	for _, block := range splitContextBlocks(content) {
		title := blockTitle(block)
		_, items := memoryBlockParts(block)
		switch {
		case title == appendContextMentionTitle:
		case len(items) > 0 || strings.HasPrefix(block, appendContextMemoryTitle):
			if rest := unsentMemoryBlock(log, block); rest != "" {
				kept = append(kept, rest)
			}
		case title != "" && log.latest[title] == block:
			// 带标题的段是状态（对话对象、当前发言者的工具……）：和上文最近那一版一样就不发，
			// 变了就发新的一版。不能按「出现过」判断：A、B 交替说话时要发回 A 那一版。
		case title == "" && log.pieces[strings.TrimSpace(block)]:
			// 不带标题的段是固定说明：发过就不再发。
		default:
			kept = append(kept, block)
		}
	}
	return strings.Join(kept, "\n")
}

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
// answered 是这一轮回答的聊天消息（当前消息和同轮补充的历史去重键）；续接时并上
// previous 的，重新组装时前缀里以前答过的都已经是历史行，只记这一轮的。
func newAppendContextLog(resp *agent.Response, headKey string, fullTokens int64, previous *appendContextLog, answered ...string) *appendContextLog {
	if resp == nil || len(resp.Transcript) == 0 || resp.ConversationKey == "" {
		return nil
	}
	log := &appendContextLog{prefix: resp.Transcript, key: resp.ConversationKey, headKey: headKey, answered: map[string]bool{}}
	switch {
	case !resp.Continued:
		log.overhead = max(0, resp.PromptTokens-fullTokens)
	case previous != nil:
		log.overhead = previous.overhead
		for key := range previous.answered {
			log.answered[key] = true
		}
	}
	for _, key := range answered {
		if key != "" {
			log.answered[key] = true
		}
	}
	log.index()
	return log
}

// answeredKeys 按落盘顺序列出 answered。
func (log *appendContextLog) answeredKeys() []string {
	keys := make([]string, 0, len(log.answered))
	for key := range log.answered {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// alreadySent 报告机器人的这句发言是不是已经在记录里：整句发过，或者它的每一小句
// 都是发过的回复里的一小句（一次 send 拆成几条发出去的情形）。
//
// 只按行比对时，MaiBot 断句在逗号处拆出的半句对不上：11-22 生产「嘿嘿，被拿去炫耀
// 了」发成「嘿嘿」「被拿去炫耀了」两条，下一轮续接把后一条又当新发言追加在整句
// send 后面，模型以为自己说了两遍，回了一句「手抖发两遍」。当天 413 轮主回复里
// 247 轮的历史里有这种重复。
func (log *appendContextLog) alreadySent(text string) bool {
	key := appendContextSentKey(text)
	if log.sent[key] {
		return true
	}
	clauses := sentReplyClauses(key)
	for _, clause := range clauses {
		if !log.sentClauses[clause] {
			return false
		}
	}
	return len(clauses) > 0
}

// sentReplyClauses 把一段回复按行、再按 MaiBot 的断句点拆成最细的小句。拆条时
// 气泡的分界只会落在这些断句点上，所以一条气泡的每一小句都在原回复的小句里。
func sentReplyClauses(text string) []string {
	var clauses []string
	for _, line := range sentReplyLines(text) {
		for _, segment := range maiBotSplitSentencesWith(line, func() float64 { return 1 }) {
			if clause := strings.TrimSpace(segment.Text); clause != "" {
				clauses = append(clauses, clause)
			}
		}
	}
	return clauses
}

// sentReplyLines 把一段回复拆成去掉首尾空白的非空行，分条标记也算换行。
func sentReplyLines(text string) []string {
	text = strings.NewReplacer(notificationSplitMarker, "\n", notificationLineMarker, "\n").Replace(text)
	var lines []string
	for _, line := range strings.Split(text, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

// index 从 prefix 算出 seen、sent、sentClauses、latest、memory、pieces、clockSentences 这几张表。它们完全由
// prefix 决定，落盘时只存 prefix，读回来再算一遍；answered 不由 prefix 决定，单独落盘。
func (log *appendContextLog) index() {
	log.seen, log.sent, log.latest, log.memory, log.pieces = map[string]bool{}, map[string]bool{}, map[string]string{}, map[string]bool{}, map[string]bool{}
	log.sentClauses, log.clockSentences = map[string]bool{}, map[string]bool{}
	if log.answered == nil {
		log.answered = map[string]bool{}
	}
	markSent := func(text string) {
		key := appendContextSentKey(text)
		log.sent[key] = true
		for _, clause := range sentReplyClauses(key) {
			log.sentClauses[clause] = true
		}
	}
	for _, message := range log.prefix {
		content := appendContextText(message)
		log.seen[content] = true
		for _, call := range message.ToolCalls {
			if call.Name == agent.FinalizeToolName {
				if text, _ := call.Arguments["text"].(string); strings.TrimSpace(text) != "" {
					markSent(text)
				}
			}
		}
		if message.Role == llm.RoleAssistant && strings.TrimSpace(message.Content) != "" {
			markSent(message.Content)
		}
		if isHistoryLine(content) || message.Role == llm.RoleAssistant {
			continue
		}
		if message.Role == llm.RoleSystem && strings.Contains(content, agent.RuntimeClockMarker) {
			for _, sentences := range clockSentences(content) {
				for _, sentence := range sentences {
					log.clockSentences[strings.TrimSpace(sentence)] = true
				}
			}
		}
		for _, paragraph := range strings.Split(content, "\n\n") {
			log.pieces[strings.TrimSpace(paragraph)] = true
		}
		for _, block := range splitContextBlocks(content) {
			log.pieces[strings.TrimSpace(block)] = true
			if title := blockTitle(block); title != "" {
				log.latest[title] = block
			}
			if head, items := memoryBlockParts(block); len(items) > 0 || strings.HasPrefix(block, appendContextMemoryTitle) {
				log.memory[head] = true
				for _, item := range items {
					log.memory[item] = true
				}
			}
		}
	}
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
// 历史区（缓存断点及之前）只取记录里还没有的群消息；之后的尾部去掉上文已有的
// 参考资料；当前消息原样放最后。返回 nil 表示这一轮不能续接、应当重新组装。
func appendContextContinuation(log *appendContextLog, messages []llm.Message) (*agent.Continuation, appendContextDecision) {
	decision := appendContextDecision{fullTokens: estimateMessagesTokens(messages)}
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
	// keepReply 记着上一条历史发言调用追加了没有：它的「已发送」结果跟着走，两半要么
	// 都追加、要么都不追加，否则留下不成对的调用，供应商整轮拒绝。
	keepReply := false
	// replyTools 攒着排在发言前面的历史工具调用和占位结果：它们跟那条发言同进退，发言
	// 追加才一起追加。
	var replyTools []llm.Message
	for index, message := range messages[head:] {
		index += head
		message.CacheBreakpoint = false
		if index == len(messages)-1 {
			appended = append(appended, message)
			continue
		}
		if index <= historyEnd && llm.IsHistoryToolMessage(message) {
			replyTools = append(replyTools, message)
			continue
		}
		if index <= historyEnd && llm.IsHistoryReplyMessage(message) {
			if message.Role == llm.RoleAssistant {
				// 机器人自己的发言：记录里已经有了（上一轮真实的 send，或重新组装时拼的
				// 发言调用，或老记录里的普通发言）就不再追加。
				keepReply = !log.alreadySent(llm.HistoryReplyText(message))
				if keepReply {
					appended = append(appended, replyTools...)
				}
				replyTools = nil
			}
			if keepReply {
				appended = append(appended, message)
			}
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
		if message.SourceKey != "" && log.answered[message.SourceKey] {
			// 上一轮的当前消息：前缀里它就在那一轮回复前面，不再排到回复后面。
			continue
		}
		content := appendContextText(message)
		if message.Role == llm.RoleSystem || log.seen[content] {
			continue
		}
		switch {
		case isHistoryLine(content):
		case message.Role == llm.RoleAssistant && log.alreadySent(content):
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
	// 当前消息带的注解（群聊篇幅等）、查证提醒和 Runner 加的输出前检查每轮照发，不走
	// 「发过的不再发」：11-11 回放（swe-2，24 条，terra 盲评）连它们也去掉后回复中位字数
	// 37 → 59、人设分 3.50 → 2.67，只多省约 540 token。贴着当前消息才压得住篇幅。
	appended = append(appended[:len(appended)-1:len(appended)-1],
		llm.Message{Role: llm.RoleSystem, Content: appendContextVerifyReminder}, appended[len(appended)-1])
	decision.prefixTokens, decision.appendTokens = estimateMessagesTokens(log.prefix), estimateMessagesTokens(appended)
	decision.overhead = log.overhead
	if decision.fullTokens > 0 && decision.prefixTokens+decision.appendTokens > appendContextGrowthLimit*(decision.fullTokens+log.overhead) {
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
	overhead     int64
	// idle 是距上一轮第一次请求多久；warmHit 是按同模型相近间隔估的续接命中，没估时为负。
	idle    time.Duration
	warmHit float64
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
	}
	if decision.idle > 0 {
		metadata["idle_seconds"] = int64(decision.idle / time.Second)
	}
	if decision.warmHit >= 0 && decision.reason == "cache_cold" {
		metadata["estimated_hit"] = math.Round(decision.warmHit*1000) / 1000
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
