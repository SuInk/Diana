package assistant

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// 补充被接受也不取消正在生成的这一版：掐掉请求会让流出去的字留成半截消息。
// 这一版跑完、发送前才判出落后，带着补充整轮重来。
func TestDirectReplySupplementDoesNotCancelAttempt(t *testing.T) {
	p := &topicTestProvider{result: `{"relation":"supplement","confidence":0.99}`}
	r := topicTestRuntime(p)
	root := directedGroupMessage("root", "user", "stdout")
	ctx, finish := r.beginDirectReply(withReplyTriggerGate(context.Background()), root)
	defer finish()
	attempt := r.directReplyAttemptContext(ctx)
	follow := directedGroupMessage("follow", "user", "stdout example")
	if _, merged := r.mergeIntoActiveDirectReply(ctx, follow, follow.RawMessage); !merged {
		t.Fatal("not merged")
	}
	if attempt.Err() != nil {
		t.Fatalf("attempt canceled: %v", context.Cause(attempt))
	}
	if err := r.interruptedReplyError(attempt, root); !errors.Is(err, errDirectReplySupplemented) {
		t.Fatalf("stale unsent answer passed the send gate: %v", err)
	}
	if !directReplyAttemptSupplemented(errDirectReplySupplemented) {
		t.Fatal("supplement error not recognised")
	}
}

// 这一版已经写过外部系统时不再重来：照发，补充结算时交回队列另起一轮。
func TestDirectReplySendGateKeepsAnswerAfterSideEffect(t *testing.T) {
	p := &topicTestProvider{result: `{"relation":"supplement","confidence":0.99}`}
	r := topicTestRuntime(p)
	root := directedGroupMessage("root", "user", "stdout")
	ctx, finish := r.beginDirectReply(withExternalSideEffectLedger(withReplyTriggerGate(context.Background())), root)
	defer finish()
	attempt := r.directReplyAttemptContext(ctx)
	follow := directedGroupMessage("follow", "user", "stdout example")
	if _, merged := r.mergeIntoActiveDirectReply(ctx, follow, follow.RawMessage); !merged {
		t.Fatal("not merged")
	}
	markExternalSideEffect(attempt)
	if err := r.interruptedReplyError(attempt, root); err != nil {
		t.Fatalf("answer with an external write was discarded: %v", err)
	}
}

// 重复的消息不需要重写，发送前也不判落后。
func TestDirectReplyRepeatDoesNotInvalidate(t *testing.T) {
	p := &topicTestProvider{result: `{"relation":"repeat","confidence":0.99}`}
	r := topicTestRuntime(p)
	root := directedGroupMessage("root", "user", "茯砖茶是啥")
	ctx, finish := r.beginDirectReply(context.Background(), root)
	defer finish()
	attempt := r.directReplyAttemptContext(ctx)
	follow := directedGroupMessage("follow", "user", "茯砖茶是啥")
	r.mergeIntoActiveDirectReply(ctx, follow, follow.RawMessage)
	if r.directReplyHasNewSupplements(attempt) {
		t.Fatal("repeat invalidated the answer")
	}
}

// Agent 正在跑时补充等着接进下一步规划，取走后这一版就覆盖到了。
func TestDirectReplySupplementJoinsRunningAgent(t *testing.T) {
	p := &topicTestProvider{result: `{"relation":"supplement","confidence":0.99}`}
	r := topicTestRuntime(p)
	root := directedGroupMessage("root", "user", "介绍下这款茶")
	ctx, finish := r.beginDirectReply(context.Background(), root)
	defer finish()
	attempt := r.directReplyAttemptContext(ctx)
	interjections, stop := r.startDirectReplyInterjections(attempt)
	defer stop()
	follow := directedGroupMessage("follow", "user", "顺便说下价格")
	if _, merged := r.mergeIntoActiveDirectReply(ctx, follow, follow.RawMessage); !merged {
		t.Fatal("not merged")
	}
	if attempt.Err() != nil {
		t.Fatalf("attempt canceled: %v", context.Cause(attempt))
	}
	if !interjections.Pending() {
		t.Fatal("agent not told a supplement is waiting")
	}
	taken := interjections.Take()
	if len(taken) != 1 || !strings.Contains(taken[0].Content, "顺便说下价格") || interjections.Pending() {
		t.Fatalf("taken = %#v", taken)
	}
	if r.directReplyHasNewSupplements(attempt) {
		t.Fatal("answer judged stale after taking the supplement")
	}
}

// Agent 没来得及取走补充就结束了，发送前照旧判成落后去整轮重写。
func TestDirectReplyUntakenInterjectionStillStale(t *testing.T) {
	p := &topicTestProvider{result: `{"relation":"supplement","confidence":0.99}`}
	r := topicTestRuntime(p)
	root := directedGroupMessage("root", "user", "介绍下这款茶")
	ctx, finish := r.beginDirectReply(context.Background(), root)
	defer finish()
	attempt := r.directReplyAttemptContext(ctx)
	_, stop := r.startDirectReplyInterjections(attempt)
	follow := directedGroupMessage("follow", "user", "顺便说下价格")
	r.mergeIntoActiveDirectReply(ctx, follow, follow.RawMessage)
	stop()
	if !r.directReplyHasNewSupplements(attempt) {
		t.Fatal("untaken supplement treated as answered")
	}
}

// 带图片的补充要随用户消息进提示词，不接进 Agent，也不取消，等这一版跑完整轮重写。
func TestDirectReplyMediaSupplementWaitsForRewrite(t *testing.T) {
	p := &topicTestProvider{result: `{"relation":"supplement","confidence":0.99}`}
	r := topicTestRuntime(p)
	root := directedGroupMessage("root", "user", "这茶怎么样")
	ctx, finish := r.beginDirectReply(context.Background(), root)
	defer finish()
	attempt := r.directReplyAttemptContext(ctx)
	interjections, stop := r.startDirectReplyInterjections(attempt)
	defer stop()
	follow := directedGroupMessage("follow", "user", "就是这个")
	follow.Segments = append(follow.Segments, MessageSegment{Type: "image", Data: map[string]string{"url": "https://example.com/a.png"}})
	r.mergeIntoActiveDirectReply(ctx, follow, follow.RawMessage)
	if attempt.Err() != nil {
		t.Fatalf("media supplement canceled the attempt: %v", context.Cause(attempt))
	}
	if interjections.Pending() {
		t.Fatal("media supplement interjected")
	}
	if !r.directReplyHasNewSupplements(attempt) {
		t.Fatal("media supplement not marked for rewrite")
	}
}
