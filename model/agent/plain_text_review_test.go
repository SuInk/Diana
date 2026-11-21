// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/llm"
)

// 复核要看得到这一轮能用的工具，才分得清「没沙盒跑不了」是实话还是推托
// （11-20 线上 run_command 一直可用）。
func TestUnfinishedClaimReviewSeesAvailableTools(t *testing.T) {
	client := &scriptedClient{responses: []string{`{"unfinished":"我这没沙盒跑不了"}`}}
	tool := &longDescriptionTestTool{description: "在工作区容器里用 bash 跑命令。python3/node 都有"}
	runner := &Runner{client: client, registry: NewToolRegistry(tool)}
	got := runner.reviewUnfinishedClaim(context.Background(), []llm.Message{{Role: llm.RoleUser, Content: "帮我算 e*pi 到 1000 位"}}, "我这没沙盒跑不了", nil)
	if got != "我这没沙盒跑不了" {
		t.Fatalf("verdict = %q", got)
	}
	input := client.requests[0].Messages[1].Content
	if !strings.Contains(input, "【能用的工具】\n- long_description：在工作区容器里用 bash 跑命令\n") {
		t.Fatalf("review input lacks the tool list: %q", input)
	}
}
