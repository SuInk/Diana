// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"strings"
	"sync"

	"github.com/SuInk/diana/model/llm"
)

// 语气改写（maibot 回复器）和发送前审核以前是串行的：Agent 收尾 → 改写 → 审核 → 发送，
// 生产上这两次模型调用各要 2–3 秒。改写只动措辞，审核看的是这段话该不该发，两件事
// 都只依赖 Agent 给出的草稿，所以主回复链路把改写挪到后台，审核拿草稿同时跑，
// 放行后再换上改写稿。
//
// 只有主回复链路装了这个延迟器；定时任务、事件触发等其它调用 generateReply 的路径
// 没装，照旧在 generateReply 里同步改写。
type deferredReplyTone struct {
	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
	text   string
	err    error
}

type deferredReplyToneKey struct{}

func withDeferredReplyTone(ctx context.Context) (context.Context, *deferredReplyTone) {
	tone := &deferredReplyTone{}
	return context.WithValue(ctx, deferredReplyToneKey{}, tone), tone
}

func deferredReplyToneFromContext(ctx context.Context) *deferredReplyTone {
	tone, _ := ctx.Value(deferredReplyToneKey{}).(*deferredReplyTone)
	return tone
}

// start 在后台跑一次改写。同一轮里重新生成（换描述重试）时，旧的改写作废。
func (t *deferredReplyTone) start(ctx context.Context, compose func(context.Context) (string, error)) {
	toneCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	t.mu.Lock()
	if t.cancel != nil {
		t.cancel()
	}
	t.cancel, t.done, t.text, t.err = cancel, done, "", nil
	t.mu.Unlock()
	go func() {
		defer recoverGoroutinePanic("deferred_reply_tone")
		defer close(done)
		text, err := compose(toneCtx)
		t.mu.Lock()
		if t.done == done {
			t.text, t.err = text, err
		}
		t.mu.Unlock()
	}()
}

func (t *deferredReplyTone) pending() bool {
	if t == nil {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.done != nil
}

// wait 取出改写结果；改写失败或没写出东西时返回空串，调用方沿用草稿。
func (t *deferredReplyTone) wait(ctx context.Context) string {
	t.mu.Lock()
	done := t.done
	t.mu.Unlock()
	if done == nil {
		return ""
	}
	select {
	case <-done:
	case <-ctx.Done():
		return ""
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.err != nil {
		return ""
	}
	return strings.TrimSpace(t.text)
}

// reset 作废之前的改写：同一轮重新生成后，旧草稿的改写不能再被拿去发。
func (t *deferredReplyTone) reset() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.cancel != nil {
		t.cancel()
	}
	t.cancel, t.done, t.text, t.err = nil, nil, "", nil
}

// stop 结束本轮：还没跑完的改写不再需要（沉默、报错、被拦），别让它白白跑完。
func (t *deferredReplyTone) stop() {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.cancel != nil {
		t.cancel()
	}
}

// adoptDeferredTone 在审核放行后取回后台的改写稿；没有改写、改写失败或写空时沿用草稿。
//
// 审核看的是草稿：改写只许动措辞，控制标记、引用和 @ 都按草稿还原（见
// composeMaiBotReply），该不该发由草稿决定。审核拦下时调用方直接返回，改写随
// deferredReplyTone.stop 取消，不用等。账号安全的「已审」标记只认审过的那段文字，
// 改写稿和草稿不同时不打标记，合并转发卡片会照常再审一遍。
func adoptDeferredTone(ctx context.Context, tone *deferredReplyTone, event MessageEvent, reply string) string {
	if !tone.pending() || strings.TrimSpace(reply) == "" {
		return reply
	}
	toned := tone.wait(ctx)
	if toned == "" {
		return reply
	}
	// 草稿已经按投递方式处理过（consumeReplyControlIntent、prepareReplyDelivery），
	// 改写稿走同样两步，控制标记和草稿一致，只取正文。
	toned, _ = consumeReplyControlIntent(toned)
	toned, _ = prepareReplyDelivery(toned, event)
	if strings.TrimSpace(toned) == "" {
		return reply
	}
	return toned
}

// composeAndPrepareReply 是 generateReply 的收尾：maibot 回复器先改写措辞，再整理成
// 可发送的回复。主回复链路装了延迟器时，改写放到后台，这里先把草稿交回去审核。
func (r *Runtime) composeAndPrepareReply(ctx context.Context, cfg BotConfig, event MessageEvent, messages []llm.Message, draft string) (string, error) {
	if cfg.ReplyComposer != "maibot" {
		return r.prepareGeneratedReply(ctx, cfg, draft, event)
	}
	tone := deferredReplyToneFromContext(ctx)
	if tone == nil {
		return r.prepareGeneratedReply(ctx, cfg, r.composeMaiBotReply(ctx, cfg, event, messages, draft), event)
	}
	tone.reset()
	prepared, err := r.prepareGeneratedReply(ctx, cfg, draft, event)
	if err != nil || strings.TrimSpace(prepared) == "" {
		return prepared, err
	}
	tone.start(ctx, func(toneCtx context.Context) (string, error) {
		return r.prepareGeneratedReply(toneCtx, cfg, r.composeMaiBotReply(toneCtx, cfg, event, messages, draft), event)
	})
	return prepared, nil
}
