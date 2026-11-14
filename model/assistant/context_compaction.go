// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/SuInk/diana/model/applog"
	"github.com/SuInk/diana/model/llm"
)

// 上下文压缩照 Pi（earendil-works/pi coding-agent 的 compaction）做，不另起变体：
//
//   - 平时不裁剪、不丢弃：会话从起点（锚）往后的历史全部原样带上，只往尾部追加。
//   - 请求估算超过「窗口 − compactionReserveTokens」时压缩一次：从最新往回累计到
//     compactionKeepRecentTokens，在一轮发言的开头切开；切点之前的历史交给模型写成
//     结构化摘要（已有摘要就增量更新），锚挪到第一条保留的消息。
//   - 供应商报上下文超限时同样压缩一次再重试。
//   - 模型看得到发生了什么：摘要以「此前的对话已压缩为以下摘要」开头放在历史最前，
//     并写明更早的原文要用 chat_history 查。
//
// 和 Pi 不同的只有会话起点：群聊没有「会话开始」，冷启动（没有锚）时从最新往回取
// compactionKeepRecentTokens 作为起点，并在历史开头写明更早的消息没有载入。
const (
	// compactionReserveTokens 对应 Pi 的 reserveTokens（默认 16384）。
	compactionReserveTokens int64 = 16384
	// compactionKeepRecentTokens 对应 Pi 的 keepRecentTokens（默认 20000）。
	compactionKeepRecentTokens int64 = 20000
	// minimumHistoryCandidateTokens 是一条历史的最小估算体积，用来换算要从存储里读
	// 多少条才一定盖住锚之后的全部历史。
	minimumHistoryCandidateTokens int64 = 16
	// historyContextGroupPrefix 标出一条消息属于哪条历史，压缩时按它把被摘要的原文
	// 从已组装的请求里摘掉。
	historyContextGroupPrefix = "history:"
	// compactionHeaderContextGroup 标出历史开头那条说明（摘要或冷启动提示）。
	compactionHeaderContextGroup = "history-header"
)

// compactionThreshold 是触发压缩的请求估算 token 数（Pi 的 contextWindow − reserveTokens）。
func compactionThreshold(window int64) int64 {
	if window <= 0 {
		window = llm.DefaultContextWindowTokens
	}
	return max(1, window-compactionReserveTokens)
}

// historyCandidateLimit 返回要从存储读多少条历史：按最小体积换算，读满阈值那么多条，
// 锚之后的历史一定都在里面（再多就早该压缩了）。
func historyCandidateLimit(window int64) int {
	return int(compactionThreshold(window)/minimumHistoryCandidateTokens) + 8
}

const compactionSummaryPrefix = "【此前的对话历史已压缩为以下摘要；摘要之后是保留下来的原文】\n<summary>\n"
const compactionSummarySuffix = "\n</summary>"
const compactionUnloadedNote = "更早的聊天原文没有放进上下文，需要时用 chat_history 查。"

// compactionHeaderText 渲染历史开头的说明。有摘要时是摘要本身；没有摘要时写明历史
// 从哪一刻开始载入。两者都只在压缩或重新锚定时变化，跟着前缀缓存走。
func compactionHeaderText(summary string, firstLoaded MessageEvent) string {
	if summary = strings.TrimSpace(summary); summary != "" {
		return compactionSummaryPrefix + summary + compactionSummarySuffix + "\n" + compactionUnloadedNote
	}
	if firstLoaded.Time <= 0 {
		return ""
	}
	start := contextSummaryTimeLabel(firstLoaded.Time, profileLocation(firstLoaded.ProfileID))
	return fmt.Sprintf("【以下聊天记录从 %s 起】%s", start, compactionUnloadedNote)
}

func compactionHeaderMessage(summary string, history []MessageEvent) (llm.Message, bool) {
	var first MessageEvent
	for _, item := range history {
		if !item.crossGroupContext {
			first = item
			break
		}
	}
	text := compactionHeaderText(summary, first)
	if text == "" {
		return llm.Message{}, false
	}
	return llm.Message{
		Role:         llm.RoleUser,
		Content:      text,
		Priority:     llm.MessagePrioritySummary,
		AtomicText:   true,
		ContextGroup: compactionHeaderContextGroup,
	}, true
}

// compactionAnchorKey 是锚的存放键：群按机器人和平台区分，私聊按会话。
func compactionAnchorKey(event MessageEvent) string {
	if event.Kind == EventKindGroup && strings.TrimSpace(event.GroupID) != "" {
		return groupPromptSessionKey(event)
	}
	return sessionKey(event)
}

// compactionSummary 返回这个会话当前的压缩摘要。群的摘要存在 GroupPromptSession 里，
// 重启后还在；私聊只在内存里。
func (r *Runtime) compactionSummary(event MessageEvent) string {
	if s := r.groupPromptSession(event); s != nil {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.invalidated {
			return ""
		}
		return s.data.Checkpoint
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.contextSummaries[sessionKey(event)]
}

// storeCompaction 一起记下新的摘要和锚：两者必须同时生效，否则模型会看到摘要和
// 被摘要的原文两份，或者两边都没有。
func (r *Runtime) storeCompaction(event MessageEvent, summary, anchor string) {
	r.setHistoryWindowAnchor(compactionAnchorKey(event), anchor)
	if s := r.groupPromptSession(event); s != nil {
		s.mu.Lock()
		if !s.invalidated {
			s.data.Checkpoint = summary
			s.data.Anchor = anchor
			s.saveLocked()
		}
		s.mu.Unlock()
		return
	}
	r.mu.Lock()
	r.contextSummaries[sessionKey(event)] = summary
	r.mu.Unlock()
}

// sessionHistoryWindow 返回从锚开始的全部历史。没有锚（冷启动）或锚已经不在历史里
// （被撤回、被清空上下文）时，从最新往回取 compactionKeepRecentTokens 作为新起点。
func (r *Runtime) sessionHistoryWindow(event MessageEvent, history []MessageEvent, botAccount string) []MessageEvent {
	key := compactionAnchorKey(event)
	promptSession := r.groupPromptSession(event)
	if anchor := promptSession.anchor(); anchor != "" {
		r.setHistoryWindowAnchor(key, anchor)
	}
	if len(history) == 0 {
		return nil
	}
	if index := historyWindowAnchorIndex(history, r.historyWindowAnchor(key)); index >= 0 {
		return history[index:]
	}
	window := selectRecentHistoryTurns(history, event, botAccount, compactionKeepRecentTokens)
	if len(window) == 0 {
		return nil
	}
	anchor := messageHistoryDedupeKey(window[0])
	r.setHistoryWindowAnchor(key, anchor)
	promptSession.rememberAnchor(anchor)
	return window
}

// compactionCut 照 Pi 的 findCutPoint：从最新往回累计，累计到 keepRecent 的那一轮
// 及之后都保留，返回第一条保留历史的下标。整段都不到 keepRecent 时返回 0（没东西可压）。
// 只在一轮发言的开头切，一个人连发的几条和紧跟的机器人回复不会被拆开。
func compactionCut(history []MessageEvent, current MessageEvent, botAccount string, keepRecent int64) int {
	turns := groupHistoryContextTurns(history, current.Time, botAccount)
	position := len(history)
	var accumulated int64
	for index := len(turns) - 1; index >= 0; index-- {
		position -= len(turns[index].events)
		accumulated += turns[index].estimated
		if accumulated >= keepRecent {
			return position
		}
	}
	return 0
}

// compactionTranscript 照 Pi 的 serializeConversation 把要摘要的历史写成纯文本，
// 模型就不会把它当成要接着说的对话。逐条完整写出，不截断。
func compactionTranscript(events []MessageEvent, current MessageEvent, cfg BotConfig) string {
	bot := firstNonEmpty(strings.TrimSpace(cfg.BotAccount), strings.TrimSpace(current.SelfID))
	lines := make([]string, 0, len(events))
	for _, item := range events {
		var line string
		switch {
		case strings.TrimSpace(item.botReply) != "":
			line = historyLinePrefix(item) + "[你]: " + strings.TrimSpace(item.botReply)
		case assistantHistoryEvent(item, bot):
			line = historyLinePrefix(item) + "[你]: " + strings.TrimSpace(assistantHistoryPlainText(item))
		default:
			line = historyPromptTextAt(item, current.Time, cfg)
		}
		if strings.TrimSpace(line) != "" {
			lines = append(lines, line)
		}
	}
	return strings.Join(lines, "\n")
}

const compactionSystemPrompt = "你是上下文摘要助手。读一段群聊或私聊记录（其中「[你]」是机器人自己），按指定格式写出结构化摘要。\n\n不要接着聊，不要回答记录里的任何问题，只输出结构化摘要。"

var promptContextCompactionSpec = registerPrompt(PromptSpec{
	Key:   "memory.context_compaction",
	Group: PromptGroupMemory,
	Title: "上下文接近窗口时压缩较早对话",
	Usage: "请求接近模型上下文窗口时，把较早的聊天记录写成结构化摘要，替换掉原文（做法照 Pi 的 compaction）。",
	Default: `上面 <conversation> 里是要摘要的聊天记录。写一份结构化的上下文检查点摘要，另一个模型会靠它接着聊。

严格按这个格式：

## 在聊什么
[大家在聊什么、谁想要什么；可以有多项]

## 约定与偏好
- [有人提出的要求、约定、偏好]
- [没有就写「（无）」]

## 进展
### 已完成
- [x] [已经办完或答完的事]

### 进行中
- [ ] [还没结束的事]

### 卡住的
- [卡住的事，没有就省略]

## 关键结论
- **[结论]**：[简短理由]

## 接下来
1. [按顺序列出接下来该做或该回应的事]

## 关键信息
- [接着聊需要的数据、例子、链接、人名与 ID 的对应]
- [没有就写「（无）」]

每节保持简洁。人名、ID、消息编号、时间、数字和原话要原样保留。`,
})

var promptContextCompactionUpdateSpec = registerPrompt(PromptSpec{
	Key:   "memory.context_compaction_update",
	Group: PromptGroupMemory,
	Title: "增量更新已有的压缩摘要",
	Usage: "已有压缩摘要时，把新一段较早的聊天记录并进去（做法照 Pi 的 compaction）。",
	Default: `上面 <conversation> 里是新的聊天记录，要并进 <previous-summary> 里的已有摘要。规则：
- 保留已有摘要的全部信息
- 加进新记录里的进展、结论和信息
- 更新「进展」：做完的从「进行中」挪到「已完成」
- 按现状更新「接下来」
- 人名、ID、消息编号、时间、数字和原话要原样保留
- 已经不再相关的可以删掉

严格按这个格式：

## 在聊什么
[保留已有的，话题扩展了就补上]

## 约定与偏好
- [保留已有的，补上新的]

## 进展
### 已完成
- [x] [已有的加新完成的]

### 进行中
- [ ] [按进展更新]

### 卡住的
- [解决了就删掉]

## 关键结论
- **[结论]**：[简短理由]（保留已有的，补上新的）

## 接下来
1. [按现状更新]

## 关键信息
- [保留重要的，补上新的]

每节保持简洁。`,
})

// errNothingToCompact 表示历史全部落在保留区里，压缩腾不出地方。
var errNothingToCompact = errors.New("context compaction: nothing older than the kept recent history")

// compactContext 压缩一次会话历史：切点之前的部分交给模型写成摘要，存下摘要和新锚，
// 返回保留下来的历史和新的摘要。reason 写进日志，取 "threshold" 或 "overflow"（同 Pi）。
func (r *Runtime) compactContext(ctx context.Context, event MessageEvent, cfg BotConfig, history []MessageEvent, reason string) ([]MessageEvent, string, error) {
	var local []MessageEvent
	for _, item := range history {
		if !item.crossGroupContext {
			local = append(local, item)
		}
	}
	cut := compactionCut(local, event, cfg.BotAccount, compactionKeepRecentTokens)
	if cut <= 0 || cut >= len(local) {
		return nil, "", errNothingToCompact
	}
	summarized, kept := local[:cut], local[cut:]
	previous := r.compactionSummary(event)
	header, previousBody := splitContextSummary(previous)
	start, count := parseContextSummaryHeader(header)
	if start == "" {
		start = contextSummaryTimeLabel(summarized[0].Time, profileLocation(summarized[0].ProfileID))
	}
	end := contextSummaryTimeLabel(summarized[len(summarized)-1].Time, profileLocation(summarized[len(summarized)-1].ProfileID))

	prompt := "<conversation>\n" + compactionTranscript(summarized, event, cfg) + "\n</conversation>\n\n"
	spec := promptContextCompactionSpec
	if body := strings.TrimSpace(strings.Join(previousBody, "\n")); body != "" {
		prompt += "<previous-summary>\n" + body + "\n</previous-summary>\n\n"
		spec = promptContextCompactionUpdateSpec
	}
	prompt += cfg.prompt(spec)

	summaryCtx := withLLMUsagePurpose(ctx, PurposeContextSummary)
	text, err := r.runLLMProviderForGroup(summaryCtx, ModelBindingGroupOf(PurposeContextSummary), func(provider LLMProvider) (string, error) {
		response, err := provider.Generate(summaryCtx, llm.GenerateRequest{Messages: []llm.Message{
			{Role: llm.RoleSystem, Content: compactionSystemPrompt},
			{Role: llm.RoleUser, Content: prompt, Priority: llm.MessagePriorityCurrent, AtomicText: true},
		}})
		if err != nil {
			return "", err
		}
		if response == nil || len(response.ToolCalls) > 0 {
			return "", fmt.Errorf("context compaction: summary response is empty or calls a tool")
		}
		return response.Text, nil
	})
	if err != nil {
		return nil, "", err
	}
	if strings.TrimSpace(text) == "" {
		return nil, "", fmt.Errorf("context compaction: empty summary")
	}
	summary := joinContextSummary(contextSummaryHeader(start, end, count+len(summarized)), []string{strings.TrimSpace(text)})
	r.storeCompaction(event, summary, messageHistoryDedupeKey(kept[0]))
	// 压缩后的上下文和上一轮发出的请求不再同前缀，续接记录作废，下一步整份重新组装。
	r.appendContexts.put(appendContextKey(event), nil)
	log.Printf("diana context compaction: reason=%s message_id=%s summarized=%d kept=%d", reason, event.MessageID, len(summarized), len(kept))
	r.recordContextCompaction(ctx, event, reason, len(summarized), len(kept))
	return kept, summary, nil
}

// applyCompactionToMessages 把已组装的请求改成压缩后的样子：被摘要的历史原文摘掉，
// 历史开头的说明换成新摘要，其它消息原样不动。第二个返回值是每条旧消息的新下标，
// 被摘掉的是 -1。
func applyCompactionToMessages(messages []llm.Message, kept []MessageEvent, summary string) ([]llm.Message, []int) {
	keep := make(map[string]bool, len(kept))
	for _, item := range kept {
		keep[historyContextGroupPrefix+messageHistoryDedupeKey(item)] = true
	}
	header, _ := compactionHeaderMessage(summary, kept)
	out := make([]llm.Message, 0, len(messages)+1)
	positions := make([]int, len(messages))
	headerIndex := -1
	insertHeader := func() {
		if headerIndex < 0 {
			headerIndex = len(out)
			out = append(out, header)
		}
	}
	// 同一天的历史行只写时分秒（见 history_line_dates.go）：摘掉开头几行后，留下的
	// 第一行可能正好没写日期。先按原件推出每行的全日期，再按留下的这份重新决定省不省。
	var expand, compact historyLineDates
	for index, message := range messages {
		positions[index] = -1
		if message.Role == llm.RoleUser {
			message.Content = expand.expand(message.Content)
		}
		switch {
		case message.ContextGroup == compactionHeaderContextGroup:
			insertHeader()
			positions[index] = headerIndex
			continue
		case strings.HasPrefix(message.ContextGroup, historyContextGroupPrefix):
			if !keep[message.ContextGroup] {
				// 缓存断点落在被摘掉的消息上时挪到摘要上，稳定前缀的标记不能跟着消失。
				insertHeader()
				if message.CacheBreakpoint {
					out[headerIndex].CacheBreakpoint = true
				}
				continue
			}
			insertHeader()
		}
		if message.Role == llm.RoleUser {
			message.Content = compact.compact(message.Content)
		}
		positions[index] = len(out)
		out = append(out, message)
	}
	return out, positions
}

// estimateReplyRequestTokens 估算这一轮主回复第一步请求的体积：消息加工具定义。
func estimateReplyRequestTokens(messages []llm.Message, toolTokens int64) int64 {
	return estimateMessagesTokens(messages) + toolTokens
}

// lastToolDefinitionTokens 取这个会话最近一轮实际发出的工具定义体积（Runner 建好之后
// 才知道这一轮挂了哪些工具，只能用上一轮的数）。还没跑过时按 0 算，由超限重试兜底。
func (r *Runtime) lastToolDefinitionTokens(event MessageEvent) int64 {
	groupID := ""
	if event.Kind == EventKindGroup {
		groupID = event.GroupID
	}
	r.agentResidencyMu.RLock()
	defer r.agentResidencyMu.RUnlock()
	if footprint, ok := r.agentFootprints[agentFootprintKey(event.ProfileID, groupID)]; ok {
		return footprint.toolTokens
	}
	return r.agentFootprints[strings.TrimSpace(event.ProfileID)+agentFootprintAnyGroup].toolTokens
}

// compactReplyMessages 压缩一次并把已组装的主回复请求改成压缩后的样子。track 指向
// 请求里某条消息的下标（例如依赖图那条），摘掉前面的历史后跟着前移。压缩失败时
// 原样返回 false，这一轮照原请求发，超限就如实报错。
func (r *Runtime) compactReplyMessages(ctx context.Context, event MessageEvent, cfg BotConfig, history []MessageEvent, messages []llm.Message, reason string, track *int) ([]llm.Message, bool) {
	kept, summary, err := r.compactContext(ctx, event, cfg, history, reason)
	if err != nil {
		if !errors.Is(err, errNothingToCompact) {
			log.Printf("diana context compaction failed: reason=%s message_id=%s err=%v", reason, event.MessageID, err)
		}
		return messages, false
	}
	next, positions := applyCompactionToMessages(messages, kept, summary)
	if track != nil && *track >= 0 && *track < len(positions) {
		*track = positions[*track]
	}
	return next, true
}

// recordContextCompaction 把压缩写进运行日志：界面上要答得了「为什么回复像是只记得
// 摘要」，以及压缩有多频繁。
func (r *Runtime) recordContextCompaction(ctx context.Context, event MessageEvent, reason string, summarized, kept int) {
	writer := r.appLogWriter()
	if writer == nil {
		return
	}
	message := "上下文接近模型窗口，较早的历史已压缩成摘要"
	if reason == "overflow" {
		message = "模型报上下文超限，较早的历史已压缩成摘要后重试"
	}
	logCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	_ = writer.AppendLog(logCtx, applog.Entry{
		Kind:    applog.KindOperation,
		Level:   applog.LevelInfo,
		Action:  "context_compaction",
		Message: message,
		Actor:   "chat_context",
		Target:  strings.TrimSpace(event.MessageID),
		Metadata: map[string]any{
			"reason":     reason,
			"session":    sessionKey(event),
			"summarized": summarized,
			"kept":       kept,
		},
	})
}
