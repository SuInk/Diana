// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"testing"
	"time"
)

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
