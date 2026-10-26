// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// 不过期的记忆 ExpiresAt 是零值。omitempty 对 time.Time 不起作用，零值会序列化成
// 0001-01-01，WebUI 按本地时区换算后显示成「1/1/1 06:55:25 过期」。
func TestStructuredMemoryOmitsZeroTimes(t *testing.T) {
	raw, err := json.Marshal(StructuredMemoryItem{ID: "m1", CreatedAt: time.Unix(1, 0)})
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"expires_at", "last_verified_at", "source_event_time"} {
		if strings.Contains(string(raw), `"`+key+`"`) {
			t.Fatalf("zero %s was serialized: %s", key, raw)
		}
	}
	raw, _ = json.Marshal(StructuredMemoryItem{ID: "m2", ExpiresAt: time.Unix(1800000000, 0)})
	if !strings.Contains(string(raw), `"expires_at"`) {
		t.Fatalf("non-zero expires_at was dropped: %s", raw)
	}
}
