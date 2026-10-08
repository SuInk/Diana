package assistant

import (
	"context"
	"errors"
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
