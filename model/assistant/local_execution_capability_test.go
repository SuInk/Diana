// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"testing"
)

func TestCapabilityKnowledgeFindsLocalExecution(t *testing.T) {
	tool := NewCapabilityKnowledgePlugin().AgentTools()[0]
	for _, query := range []string{"Node.js 执行环境", "压缩包解压", "没有终端 编码代理"} {
		t.Run(query, func(t *testing.T) {
			raw, err := tool.Run(context.Background(), map[string]any{"query": query, "limit": 3})
			if err != nil {
				t.Fatal(err)
			}
			var result struct {
				Items []capabilitySearchHit `json:"items"`
			}
			if err := json.Unmarshal([]byte(raw), &result); err != nil {
				t.Fatal(err)
			}
			for _, item := range result.Items {
				if item.ID == "core:local-execution" {
					return
				}
			}
			t.Fatalf("local execution capability missing: %s", raw)
		})
	}
}
