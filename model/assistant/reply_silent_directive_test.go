// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"errors"
	"testing"
)

// 出站兜底：正文整段是「silent=true，理由」时按模型静默处理，理由不发出去。
func TestPrepareGeneratedReplyDropsSilentDirective(t *testing.T) {
	cfg := DefaultBotConfig()
	got, err := (&Runtime{}).prepareGeneratedReply(context.Background(), cfg, "silent=true，Mio 是在跟小汐说话，重启的也不是我（")
	if !errors.Is(err, errModelSilentFinish) || got != "" {
		t.Fatalf("prepareGeneratedReply() = %q, %v; want silent finish", got, err)
	}
}
