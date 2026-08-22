// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"testing"
	"time"
)

// 拟真停顿的目标是「别秒回」，不是「已经想了很久之后再多等一会儿」。
// 生成本身花掉的时间同样算数，模型慢的时候不该再空等。
func TestTypingDelayCountsGenerationTime(t *testing.T) {
	text := "端口被占了，先看看是谁占着。"
	full := ReplyStyleGroupmate.typingDelay(text)
	if full <= 0 {
		t.Fatal("群友风格应当有拟真停顿")
	}

	if got := ReplyStyleGroupmate.remainingTypingDelay(text, 0); got != full {
		t.Fatalf("没花时间时应当补满，got=%v want=%v", got, full)
	}
	if got := ReplyStyleGroupmate.remainingTypingDelay(text, full/2); got != full-full/2 {
		t.Fatalf("只该补差额，got=%v", got)
	}
	if got := ReplyStyleGroupmate.remainingTypingDelay(text, full+time.Second); got != 0 {
		t.Fatalf("生成已经超过停顿时长时不该再等，got=%v", got)
	}
	// 其它风格本来就没有停顿，不受影响。
	if got := ReplyStyleAssistant.remainingTypingDelay(text, 0); got != 0 {
		t.Fatalf("助手风格不该有停顿，got=%v", got)
	}
}

func TestReplyTurnElapsedFallsBackToZero(t *testing.T) {
	if got := replyTurnElapsed(context.Background()); got != 0 {
		t.Fatalf("没有记录起点时应当返回 0，got=%v", got)
	}
	ctx := withReplyTurnStart(context.Background(), time.Now().Add(-2*time.Second))
	if got := replyTurnElapsed(ctx); got < time.Second {
		t.Fatalf("应当算出已经过去约 2 秒，got=%v", got)
	}
	// 时钟回拨或起点在未来时不能返回负数。
	future := withReplyTurnStart(context.Background(), time.Now().Add(time.Minute))
	if got := replyTurnElapsed(future); got != 0 {
		t.Fatalf("起点在未来时应当返回 0，got=%v", got)
	}
}
