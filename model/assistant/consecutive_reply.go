// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"strings"
	"time"
)

// 同一个人连着发两条、机器人答两遍的问题。
//
// 「内容是什么？」后面紧跟一个「@Diana」，是很常见的说法——第二条只是把机器人叫
// 过来，不是新问题。但两条各自触发一轮回复，而回复的生成是并发的（投递才串行，
// 见 replyBatchGate），所以第二轮开跑时第一轮的答案还没写进历史：两轮看到的输入
// 一模一样，于是把同一件事完整答了两遍。
//
// 修法不是把第二轮掐掉——第二条有时确实带了新信息，掐掉就成了「问了不理」。这里
// 只告诉模型「同一个人刚问过、你已经在答」，让它把第二条当追问接住：已经说过的
// 不重说，没有新内容就一句话确认。
//
// 只按「会话 + 发言人」计，不按会话：群里两个人先后问不同的问题，本来就该各答各的。

const (
	// consecutiveReplyWindow 是「算作连着说」的时间窗。
	//
	// 取 45 秒：补一个 @、把话说完整、贴个链接补充，都发生在这个尺度上；再长就会
	// 把「过一分钟想起来又问一句」也算进去，那是新问题，不该被要求从简。
	consecutiveReplyWindow = 45 * time.Second

	// consecutiveReplyExcerptRunes 是上一轮答案带进提示词的长度。
	//
	// 只带开头：目的是让模型认出「这件事我答过」，不是让它重读一遍全文。带全文
	// 既费预算，又会诱导它照着改写一遍——那正是要避免的结果。
	consecutiveReplyExcerptRunes = 120
)

// replyTurnRecord 是一次回复轮次的痕迹。
type replyTurnRecord struct {
	startedAt time.Time
	// finishedAt 为零表示这一轮还在生成中。并发的第二轮看到的正是这种状态。
	finishedAt time.Time
	reply      string
}

// inFlight 判断这一轮是不是还没答完。
func (t replyTurnRecord) inFlight() bool { return t.finishedAt.IsZero() }

// consecutiveReplyKey 把痕迹绑到「会话 + 发言人」。取不到任一半就不记：那会退回
// 一个人人都命中的公共桶，反而让不相干的两个人互相压制。
func consecutiveReplyKey(event MessageEvent) string {
	session := strings.TrimSpace(sessionKey(event))
	userID := strings.TrimSpace(event.UserID)
	if session == "" || userID == "" {
		return ""
	}
	return session + "\x00" + userID
}

// beginReplyTurn 登记这一轮开始，并返回同一个人上一轮的痕迹。
//
// 返回的 ok 只在「上一轮落在时间窗内」时为真。过期痕迹顺手清掉：一个热闹的群
// 会留下很多把键，只在被读到时才删的话，不再开口的人会一直挂着。
func (r *Runtime) beginReplyTurn(event MessageEvent, now time.Time) (replyTurnRecord, bool) {
	key := consecutiveReplyKey(event)
	if key == "" || r == nil {
		return replyTurnRecord{}, false
	}
	r.replyTurnMu.Lock()
	defer r.replyTurnMu.Unlock()
	if r.replyTurns == nil {
		r.replyTurns = map[string]replyTurnRecord{}
	}
	for existing, record := range r.replyTurns {
		if now.Sub(record.startedAt) > consecutiveReplyWindow {
			delete(r.replyTurns, existing)
		}
	}
	previous, found := r.replyTurns[key]
	r.replyTurns[key] = replyTurnRecord{startedAt: now}
	if !found || now.Sub(previous.startedAt) > consecutiveReplyWindow {
		return replyTurnRecord{}, false
	}
	return previous, true
}

// finishReplyTurn 记下这一轮实际说了什么。
//
// 只记有正文的：空回复说明这一轮最后没开口（被拦下、或只发了媒体），把它当成
// 「刚答过」会让下一条消息被无故要求从简。
func (r *Runtime) finishReplyTurn(event MessageEvent, reply string, now time.Time) {
	key := consecutiveReplyKey(event)
	if key == "" || r == nil || strings.TrimSpace(reply) == "" {
		return
	}
	r.replyTurnMu.Lock()
	defer r.replyTurnMu.Unlock()
	if r.replyTurns == nil {
		return
	}
	record, ok := r.replyTurns[key]
	if !ok {
		return
	}
	record.finishedAt = now
	record.reply = strings.TrimSpace(reply)
	r.replyTurns[key] = record
}

// consecutiveReplyContext 把「刚答过同一个人」写成提示词段落。
func consecutiveReplyContext(previous replyTurnRecord) string {
	var builder strings.Builder
	builder.WriteString("【同一个人的连续消息】\n")
	if previous.inFlight() {
		// 并发的那一路：答案还没生成完，只能告诉它「正在答」。
		builder.WriteString("这个人上一条消息你正在回答，那条回复马上就会发出去。")
	} else {
		builder.WriteString("你刚刚已经回答过这个人的上一条消息")
		if excerpt := truncateReplyExcerpt(previous.reply, consecutiveReplyExcerptRunes); excerpt != "" {
			builder.WriteString("，说的是：")
			builder.WriteString(excerpt)
		}
		builder.WriteString("。")
	}
	builder.WriteString("\n这条多半是同一件事的追问，或者只是补一个 @、把话说完整。")
	builder.WriteString("已经答过的内容不要再说一遍——哪怕换个说法也不行；只回应这条新增的部分。")
	builder.WriteString("如果它没带来新问题，就用一句话接住（例如确认一声），不要重新组织一遍完整答案。")
	builder.WriteString("确实是另一件事时照常正常回答。")
	return builder.String()
}

// truncateReplyExcerpt 取回复开头的一段，压成一行。
func truncateReplyExcerpt(reply string, limit int) string {
	text := strings.Join(strings.Fields(reply), " ")
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return strings.TrimSpace(string(runes[:limit])) + "…"
}
