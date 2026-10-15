// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SuInk/diana/model/llm"
)

// liveDampingHarness 按线上入站 worker 的方式处理消息：每条消息先走 prepareMessageEvent，
// 要回的再走 replyAndRecord，同一个群最多 3 条并发。回复生成、发送前审核都走真实模型。
type liveDampingHarness struct {
	t        *testing.T
	runtime  *Runtime
	channel  *recordingChannel
	sem      chan struct{}
	wg       sync.WaitGroup
	mu       sync.Mutex
	outcomes map[string]string
	order    []string
}

func newLiveDampingHarness(t *testing.T, client llm.LLMClient) *liveDampingHarness {
	cfg := BotConfig{GroupTriggers: []string{"Diana"}, BotAccount: "42"}.WithDefaults()
	channel := &recordingChannel{}
	runtime := NewRuntime(cfg, channel, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) { return client, nil })
	return &liveDampingHarness{t: t, runtime: runtime, channel: channel, sem: make(chan struct{}, 3), outcomes: map[string]string{}}
}

func (h *liveDampingHarness) send(ctx context.Context, userID, sender, messageID, text string) {
	event := MessageEvent{
		Kind: EventKindGroup, SelfID: "42", GroupID: "900001", UserID: userID, SenderName: sender,
		MessageID: messageID, Time: time.Now().Unix(), RawMessage: text,
		Segments: []MessageSegment{{Type: "text", Data: map[string]string{"text": text}}},
	}
	h.mu.Lock()
	h.order = append(h.order, messageID)
	h.mu.Unlock()
	h.wg.Add(1)
	go func() {
		defer h.wg.Done()
		h.sem <- struct{}{}
		defer func() { <-h.sem }()
		prepared, preparedText, handled, outcome := h.runtime.prepareMessageEvent(ctx, event)
		if handled {
			var err error
			outcome, err = h.runtime.replyAndRecord(ctx, prepared, preparedText, outcome)
			if err != nil {
				outcome = "error: " + err.Error()
			}
		}
		h.mu.Lock()
		h.outcomes[messageID] = outcome
		h.mu.Unlock()
	}()
}

func (h *liveDampingHarness) lastBotText() string {
	sent := h.channel.sentSnapshot()
	for i := len(sent) - 1; i >= 0; i-- {
		if text := strings.TrimSpace(sent[i].Text); text != "" {
			return text
		}
	}
	return ""
}

func (h *liveDampingHarness) summary() (replied int, damped int, suppressed int, lines []string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, id := range h.order {
		outcome := h.outcomes[id]
		switch {
		case strings.HasPrefix(outcome, "replied"):
			replied++
		case outcome == "ignored_reply_damping":
			damped++
		case outcome == "ignored_response_suppression":
			suppressed++
		}
		lines = append(lines, id+"="+outcome)
	}
	return
}
