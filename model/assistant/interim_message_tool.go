// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
)

// 一轮里中途说一句：先说「我去查一下」，接着真的去查，查完再给结果；长任务每做完
// 一段报一次进度。
//
// 以前一轮只在收尾那一下发话，工具调用旁边的文字不会发出去。于是模型要么
// 闷头跑完十几步才开口（群里看着像没反应），要么先回一句「我去查」就收工——这句话发
// 出去，这一轮也结束了，承诺的「查」从没发生。这个工具把「说」和「结束」分开：说完
// 这一轮照常继续。现在它不再单独暴露给模型：send 和其他工具同一步调用时由
// agent.Config.Interim 转到这里，名字和描述只留给测试与日志。
//
// 它不是分条发答案用的：一轮最多 interimMessageMaxPerTurn 次，每次一句短话。和工具同一
// 步的那句不过发送前审核（那要多等一次模型，「我去查一下」用不着），靠长度和次数兜着。
//
// 一步里只调 send、填了 continue=true 的那句不一样（agent.Config.InterimReply 转到
// RunReply）：11-07 历史发言改成 send 调用后，swe-2 回放约三成闲聊把一整条回复这样发，
// 不审就等于完整回复绕过了审核。所以这句和最终回复一样先审再发，也不限 80 字。试过把
// 中途发言拆回独立的 say 工具，静默从 9/84 涨到 25/84，没用。

const (
	dianaInterimMessageToolName = "say"
	// interimMessageMaxPerTurn 是一轮里中途最多说几次：开个头、报两次进度就够了，
	// 再多就是在把答案拆开发。
	interimMessageMaxPerTurn = 3
	// interimMessageMaxRunes 是中途一句话的长度上限：进度和开场白都是短话。
	interimMessageMaxRunes = 80
)

// interimMessageLedger 记这一轮中途说过的话：收尾时要知道已经说过什么，
// 别再兜底补一句「没有生成有效回复」，也别把同一句再发一遍。
type interimMessageLedger struct {
	mu   sync.Mutex
	sent []string
	// input、proactive 是这一轮发送前审核要的上下文，replyTo 算出来后记在这里，
	// RunReply 审核时用（见 setInterimReplyAudit）。
	input     string
	proactive bool
}

type interimMessageContextKey struct{}

// withInterimMessages 给这一轮挂上中途发言的账本。已经挂过就沿用。
func withInterimMessages(ctx context.Context) context.Context {
	if _, ok := ctx.Value(interimMessageContextKey{}).(*interimMessageLedger); ok {
		return ctx
	}
	return context.WithValue(ctx, interimMessageContextKey{}, &interimMessageLedger{})
}

// setInterimReplyAudit 记下这一轮发送前审核要的输入和是否主动接话。
func setInterimReplyAudit(ctx context.Context, input string, proactive bool) {
	if ledger, ok := ctx.Value(interimMessageContextKey{}).(*interimMessageLedger); ok {
		ledger.mu.Lock()
		ledger.input, ledger.proactive = input, proactive
		ledger.mu.Unlock()
	}
}

// interimMessagesSent 返回这一轮中途已经发出的话。
func interimMessagesSent(ctx context.Context) []string {
	if ctx == nil {
		return nil
	}
	ledger, ok := ctx.Value(interimMessageContextKey{}).(*interimMessageLedger)
	if !ok {
		return nil
	}
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	return append([]string(nil), ledger.sent...)
}

func interimMessageDedupKey(text string) string {
	return strings.TrimSpace(strings.TrimRight(strings.TrimSpace(stripChatPeriods(text)), "。."))
}

// repeatsInterimMessage 报告最终回复是不是就是中途已经说过的某一句。
func repeatsInterimMessage(ctx context.Context, reply string) bool {
	key := interimMessageDedupKey(reply)
	if key == "" {
		return false
	}
	for _, sent := range interimMessagesSent(ctx) {
		if interimMessageDedupKey(sent) == key {
			return true
		}
	}
	return false
}

type dianaInterimMessageTool struct {
	runtime *Runtime
	event   MessageEvent
}

func newDianaInterimMessageTool(r *Runtime, event MessageEvent) *dianaInterimMessageTool {
	return &dianaInterimMessageTool{runtime: r, event: event}
}

type interimMessageResult struct {
	OK        bool   `json:"ok"`
	Sent      string `json:"sent,omitempty"`
	Remaining int    `json:"remaining"`
	Message   string `json:"message"`
}

func (t *dianaInterimMessageTool) Run(ctx context.Context, input map[string]any) (string, error) {
	return t.run(ctx, input, false)
}

// RunReply 发单独 send、填了 continue=true 的那句：和最终回复一样先过发送前审核，
// 不限 80 字。见文件头。
func (t *dianaInterimMessageTool) RunReply(ctx context.Context, input map[string]any) (string, error) {
	return t.run(ctx, input, true)
}

func (t *dianaInterimMessageTool) run(ctx context.Context, input map[string]any, audited bool) (string, error) {
	text, _ := input["text"].(string)
	text = strings.TrimSpace(text)
	if text == "" {
		return "", fmt.Errorf("text 不能为空")
	}
	if strings.Contains(text, notificationSplitMarker) || strings.ContainsAny(text, "\r\n") {
		return "", fmt.Errorf("一次只说一句，不要换行或分条")
	}
	if n := len([]rune(text)); n > interimMessageMaxRunes && !audited {
		return "", fmt.Errorf("这句有 %d 字，超过中途发言上限 %d 字；答案本身最后单独 send", n, interimMessageMaxRunes)
	}
	ledger, ok := ctx.Value(interimMessageContextKey{}).(*interimMessageLedger)
	if !ok {
		return "", fmt.Errorf("这一轮不能中途发言")
	}
	ledger.mu.Lock()
	if len(ledger.sent) >= interimMessageMaxPerTurn {
		ledger.mu.Unlock()
		return marshalInterimMessageResult(interimMessageResult{Message: fmt.Sprintf("这一轮已经中途说过 %d 次，不能再说了；继续做完，最后单独 send 结果。", interimMessageMaxPerTurn)})
	}
	ledger.mu.Unlock()

	cfg := t.runtime.effectiveConfigForEvent(t.event)
	text = applyReplyFilters(cfg.ReplyFilterRules, text)
	text = strings.TrimSpace(stripChatPeriods(normalizeReply(text, 0, markdownToPlainForConfig(cfg))))
	if text == "" {
		return "", fmt.Errorf("text 不能为空")
	}
	dedupKey := interimMessageDedupKey(text)
	ledger.mu.Lock()
	for _, prev := range ledger.sent {
		if interimMessageDedupKey(prev) == dedupKey {
			remaining := interimMessageMaxPerTurn - len(ledger.sent)
			ledger.mu.Unlock()
			return marshalInterimMessageResult(interimMessageResult{
				OK:        true,
				Remaining: remaining,
				Message:   "这句话刚才在本轮已经发过了，没有重复发送。接着把事情做完；如果都说完了就直接 send silent=true。",
			})
		}
	}
	ledger.mu.Unlock()
	if t.runtime != nil && t.runtime.sessionSends.repeatsRecentSibling(ctx, t.event, text, time.Now()) {
		ledger.mu.Lock()
		ledger.sent = append(ledger.sent, text)
		remaining := interimMessageMaxPerTurn - len(ledger.sent)
		ledger.mu.Unlock()
		return marshalInterimMessageResult(interimMessageResult{
			OK:        true,
			Sent:      text,
			Remaining: remaining,
			Message:   "同一会话里你并发的另一条回复刚刚已经发出了完全相同的话，已自动跳过重复发送。若没有其他新内容，直接调 send(silent=true) 结束本轮。",
		})
	}
	var prepared preparedReplyAudit
	if audited {
		ledger.mu.Lock()
		auditInput, proactive := ledger.input, ledger.proactive
		ledger.mu.Unlock()
		// 和最终回复一样：账号安全这类要拦当前这条的判断发送前等结果，其余挪到发出之后。
		prepared = t.runtime.prepareReplyAuditOrDefer(ctx, t.event, auditInput, text, cfg, proactive, true)
		intent, err := t.runtime.applyReplyAudit(ctx, t.event, cfg, prepared)
		if err != nil {
			return "", fmt.Errorf("审核没通过，没有发出：%w", err)
		}
		if intent.RefuseCurrent {
			return "", fmt.Errorf("审核判定这条不该回答，没有发出")
		}
	}
	defer typingIndicatorFromContext(ctx).resume()
	if err := t.runtime.sendOutgoing(ctx, t.event, routeOutgoingToEvent(t.event, OutgoingMessage{Text: text})); err != nil {
		return "", fmt.Errorf("发送失败: %w", err)
	}
	if prepared.deferred {
		t.runtime.runDeferredReplyAudit(ctx, t.event, cfg, prepared, text, false)
	}
	// 话已经出去了：这一轮不能再被合并重来，否则重新生成时会把这句再说一遍。
	t.runtime.sealDirectReply(ctx)
	ledger.mu.Lock()
	ledger.sent = append(ledger.sent, text)
	remaining := interimMessageMaxPerTurn - len(ledger.sent)
	ledger.mu.Unlock()
	return marshalInterimMessageResult(interimMessageResult{
		OK:        true,
		Sent:      text,
		Remaining: remaining,
		Message:   "已经发出去了，对方看得到。接着调用工具把事情做完；最后单独 send 还没说过的内容，都说完了就 send silent=true。",
	})
}

func marshalInterimMessageResult(result interimMessageResult) (string, error) {
	data, err := json.Marshal(result)
	if err != nil {
		return "", err
	}
	return string(data), nil
}
