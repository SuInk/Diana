package assistant

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// 补充一被接受，正在生成的旧版本就该停下，不必等它跑完模型调用和发送前审核再重来。
func TestDirectReplySupplementInterruptsAttempt(t *testing.T) {
	p := &topicTestProvider{result: `{"relation":"supplement","confidence":0.99}`}
	r := topicTestRuntime(p)
	root := directedGroupMessage("root", "user", "stdout")
	ctx, finish := r.beginDirectReply(context.Background(), root)
	defer finish()
	attempt, done := r.beginDirectReplyAttempt(ctx)
	defer done()
	follow := directedGroupMessage("follow", "user", "stdout example")
	if _, merged := r.mergeIntoActiveDirectReply(ctx, follow, follow.RawMessage); !merged {
		t.Fatal("not merged")
	}
	if !errors.Is(context.Cause(attempt), errDirectReplySupplemented) {
		t.Fatalf("attempt not interrupted: %v", context.Cause(attempt))
	}
	if !directReplyAttemptSupplemented(attempt, context.Canceled) {
		t.Fatal("cancellation not recognised as supplement")
	}
}

// 工具调用进行中不打断：它可能正在写外部系统，等它回来再停。
func TestDirectReplySupplementWaitsForRunningTool(t *testing.T) {
	p := &topicTestProvider{result: `{"relation":"supplement","confidence":0.99}`}
	r := topicTestRuntime(p)
	root := directedGroupMessage("root", "user", "stdout")
	ctx, finish := r.beginDirectReply(context.Background(), root)
	defer finish()
	attempt, done := r.beginDirectReplyAttempt(ctx)
	defer done()
	r.noteDirectReplyTool(attempt, true)
	follow := directedGroupMessage("follow", "user", "stdout example")
	if _, merged := r.mergeIntoActiveDirectReply(ctx, follow, follow.RawMessage); !merged {
		t.Fatal("not merged")
	}
	if attempt.Err() != nil {
		t.Fatal("interrupted while a tool was running")
	}
	r.noteDirectReplyTool(attempt, false)
	if !errors.Is(context.Cause(attempt), errDirectReplySupplemented) {
		t.Fatalf("not interrupted after the tool returned: %v", context.Cause(attempt))
	}
}

// 重复的消息不需要重写，不打断。
func TestDirectReplyRepeatDoesNotInterrupt(t *testing.T) {
	p := &topicTestProvider{result: `{"relation":"repeat","confidence":0.99}`}
	r := topicTestRuntime(p)
	root := directedGroupMessage("root", "user", "茯砖茶是啥")
	ctx, finish := r.beginDirectReply(context.Background(), root)
	defer finish()
	attempt, done := r.beginDirectReplyAttempt(ctx)
	defer done()
	follow := directedGroupMessage("follow", "user", "茯砖茶是啥")
	r.mergeIntoActiveDirectReply(ctx, follow, follow.RawMessage)
	if attempt.Err() != nil {
		t.Fatal("repeat interrupted the attempt")
	}
}

// 工具已经写过外部系统，跑完后不能再打断，否则做完的事会被咽回去。
func TestDirectReplySupplementKeepsAttemptAfterSideEffect(t *testing.T) {
	p := &topicTestProvider{result: `{"relation":"supplement","confidence":0.99}`}
	r := topicTestRuntime(p)
	root := directedGroupMessage("root", "user", "stdout")
	ctx, finish := r.beginDirectReply(withExternalSideEffectLedger(context.Background()), root)
	defer finish()
	attempt, done := r.beginDirectReplyAttempt(ctx)
	defer done()
	r.noteDirectReplyTool(attempt, true)
	follow := directedGroupMessage("follow", "user", "stdout example")
	r.mergeIntoActiveDirectReply(ctx, follow, follow.RawMessage)
	markExternalSideEffect(attempt)
	r.noteDirectReplyTool(attempt, false)
	if attempt.Err() != nil {
		t.Fatalf("interrupted after an external write: %v", context.Cause(attempt))
	}
}

// Agent 正在跑时补充直接接进这一轮，不打断，也不在发送前判成落后。
func TestDirectReplySupplementJoinsRunningAgent(t *testing.T) {
	p := &topicTestProvider{result: `{"relation":"supplement","confidence":0.99}`}
	r := topicTestRuntime(p)
	root := directedGroupMessage("root", "user", "介绍下这款茶")
	ctx, finish := r.beginDirectReply(context.Background(), root)
	defer finish()
	attempt, done := r.beginDirectReplyAttempt(ctx)
	defer done()
	interjections, stop := r.startDirectReplyInterjections(attempt)
	defer stop()
	follow := directedGroupMessage("follow", "user", "顺便说下价格")
	if _, merged := r.mergeIntoActiveDirectReply(ctx, follow, follow.RawMessage); !merged {
		t.Fatal("not merged")
	}
	if attempt.Err() != nil {
		t.Fatalf("interrupted although the agent could take it: %v", context.Cause(attempt))
	}
	select {
	case <-interjections.Ready():
	default:
		t.Fatal("agent not notified")
	}
	taken := interjections.Take()
	if len(taken) != 1 || !strings.Contains(taken[0].Content, "顺便说下价格") {
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
	attempt, done := r.beginDirectReplyAttempt(ctx)
	defer done()
	_, stop := r.startDirectReplyInterjections(attempt)
	follow := directedGroupMessage("follow", "user", "顺便说下价格")
	r.mergeIntoActiveDirectReply(ctx, follow, follow.RawMessage)
	stop()
	if !r.directReplyHasNewSupplements(attempt) {
		t.Fatal("untaken supplement treated as answered")
	}
}

// 带图片的补充要随用户消息进提示词，仍然整轮重写。
func TestDirectReplyMediaSupplementStillInterrupts(t *testing.T) {
	p := &topicTestProvider{result: `{"relation":"supplement","confidence":0.99}`}
	r := topicTestRuntime(p)
	root := directedGroupMessage("root", "user", "这茶怎么样")
	ctx, finish := r.beginDirectReply(context.Background(), root)
	defer finish()
	attempt, done := r.beginDirectReplyAttempt(ctx)
	defer done()
	_, stop := r.startDirectReplyInterjections(attempt)
	defer stop()
	follow := directedGroupMessage("follow", "user", "就是这个")
	follow.Segments = append(follow.Segments, MessageSegment{Type: "image", Data: map[string]string{"url": "https://example.com/a.png"}})
	r.mergeIntoActiveDirectReply(ctx, follow, follow.RawMessage)
	if !errors.Is(context.Cause(attempt), errDirectReplySupplemented) {
		t.Fatalf("media supplement did not interrupt: %v", context.Cause(attempt))
	}
}
