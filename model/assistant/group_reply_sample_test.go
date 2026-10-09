// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// 旧配置里的抽样比例被忽略，保存后不再写出，普通群消息仍交给接话模型判断。
func TestLegacyReplySampleConfigIgnored(t *testing.T) {
	var bot BotConfig
	if err := json.Unmarshal([]byte(`{"reply_sample_percent":1}`), &bot); err != nil {
		t.Fatal(err)
	}
	var group GroupConfig
	if err := json.Unmarshal([]byte(`{"group_id":"20001","reply_sample_percent":1}`), &group); err != nil {
		t.Fatal(err)
	}
	for _, cfg := range []any{bot, group} {
		data, err := json.Marshal(cfg)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), "reply_sample_percent") {
			t.Fatalf("obsolete setting persisted: %s", data)
		}
	}
	h := newDisabledGroupSkipHarness(t, bot, true)
	h.runtime.prepareMessageEvent(context.Background(), disabledGroupSignalEvent())
	if h.provider.callCount() == 0 {
		t.Fatal("ordinary group message did not reach the router")
	}
}
