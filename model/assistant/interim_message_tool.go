// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
)

// 一轮里中途说一句：先说「我去查一下」，接着真的去查，查完再给结果；长任务每做完
// 一段报一次进度。
//
// 以前一轮只在 agent_finalize 那一下发话，工具调用旁边的文字不会发出去。于是模型要么
// 闷头跑完十几步才开口（群里看着像没反应），要么先回一句「我去查」就收工——这句话发
// 出去，这一轮也结束了，承诺的「查」从没发生。这个工具把「说」和「结束」分开：说完
// 这一轮照常继续。
//
// 它不是分条发答案用的：一轮最多 interimMessageMaxPerTurn 次，每次一句短话。中途的话
// 不过发送前审核（那要多等一次模型，「我去查一下」用不着），靠长度和次数兜着。

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
}

type interimMessageContextKey struct{}

// withInterimMessages 给这一轮挂上中途发言的账本。已经挂过就沿用。
func withInterimMessages(ctx context.Context) context.Context {
	if _, ok := ctx.Value(interimMessageContextKey{}).(*interimMessageLedger); ok {
		return ctx
	}
	return context.WithValue(ctx, interimMessageContextKey{}, &interimMessageLedger{})
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

// repeatsInterimMessage 报告最终回复是不是就是中途已经说过的某一句。
func repeatsInterimMessage(ctx context.Context, reply string) bool {
	reply = strings.TrimSpace(stripChatPeriods(reply))
	if reply == "" {
		return false
	}
	for _, sent := range interimMessagesSent(ctx) {
		if sent == reply {
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

func (t *dianaInterimMessageTool) Name() string { return dianaInterimMessageToolName }

func (t *dianaInterimMessageTool) Description() string {
	return fmt.Sprintf(`立刻在当前对话里发一句话，本轮不结束，发完可以接着调用工具。`+
		`用在两种地方：要先查、先做几步才能回答时，动手前说一句正在做什么（比如「我去查一下」）；长任务每做完一个阶段，报一句进度。`+
		`说了「去做」就必须接着真的去做，不能说完就收工。它不是用来把答案拆开发的：一轮最多 %d 次，每次一句不超过 %d 字的短话。`+
		`发出去的话对方已经看到了，最后 agent_finalize 只写还没说过的内容；该说的都说完了就 silent=true。`, interimMessageMaxPerTurn, interimMessageMaxRunes)
}

func (t *dianaInterimMessageTool) InputSchema() map[string]any {
	return toolObjectSchema([]string{"text"}, map[string]any{
		"text": toolStringParam("要立刻发出去的一句话，照平常说话的口吻写。"),
	})
}

type interimMessageResult struct {
	OK        bool   `json:"ok"`
	Sent      string `json:"sent,omitempty"`
	Remaining int    `json:"remaining"`
	Message   string `json:"message"`
}

func (t *dianaInterimMessageTool) Run(ctx context.Context, input map[string]any) (string, error) {
	text, _ := input["text"].(string)
	text = strings.TrimSpace(text)
	if text == "" {
		return "", fmt.Errorf("text 不能为空")
	}
	if strings.Contains(text, notificationSplitMarker) || strings.ContainsAny(text, "\r\n") {
		return "", fmt.Errorf("一次只说一句，不要换行或分条")
	}
	if n := len([]rune(text)); n > interimMessageMaxRunes {
		return "", fmt.Errorf("这句有 %d 字，超过中途发言上限 %d 字；答案本身写进 agent_finalize", n, interimMessageMaxRunes)
	}
	ledger, ok := ctx.Value(interimMessageContextKey{}).(*interimMessageLedger)
	if !ok {
		return "", fmt.Errorf("这一轮不能中途发言")
	}
	ledger.mu.Lock()
	if len(ledger.sent) >= interimMessageMaxPerTurn {
		ledger.mu.Unlock()
		return marshalInterimMessageResult(interimMessageResult{Message: fmt.Sprintf("这一轮已经中途说过 %d 次，不能再说了；继续做完，把结果写进 agent_finalize。", interimMessageMaxPerTurn)})
	}
	ledger.mu.Unlock()

	text = strings.TrimSpace(stripChatPeriods(normalizeReply(text, 0, markdownToPlainForConfig(t.runtime.effectiveConfigForEvent(t.event)))))
	if text == "" {
		return "", fmt.Errorf("text 不能为空")
	}
	if err := t.runtime.sendOutgoing(ctx, t.event, routeOutgoingToEvent(t.event, OutgoingMessage{Text: text})); err != nil {
		return "", fmt.Errorf("发送失败: %w", err)
	}
	// 话已经出去了：这一轮不能再被合并重来，否则重新生成时会把这句再说一遍。
	t.runtime.sealDirectReply(ctx)
	// 发送会暂停「正在输入」；后面还要接着干活，把它重新点亮。
	if typing := typingIndicatorFromContext(ctx); typing != nil {
		typing.resume()
	}
	ledger.mu.Lock()
	ledger.sent = append(ledger.sent, text)
	remaining := interimMessageMaxPerTurn - len(ledger.sent)
	ledger.mu.Unlock()
	return marshalInterimMessageResult(interimMessageResult{
		OK:        true,
		Sent:      text,
		Remaining: remaining,
		Message:   "已经发出去了，对方看得到。接着调用工具把事情做完；最后 agent_finalize 只写还没说过的内容，都说完了就 silent=true。",
	})
}

func marshalInterimMessageResult(result interimMessageResult) (string, error) {
	data, err := json.Marshal(result)
	if err != nil {
		return "", err
	}
	return string(data), nil
}
