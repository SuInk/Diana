// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/llm"
)

// 线上 10-17：只读到 S45 一个赛季的公告，回复却列出「传送花」「兵线对齐」等整份
// 更新清单。复核把没原文的细节列回去，主模型改写一次后照发，复核不再跑第二次。
func TestResearchDetailReviewSendsDraftBackOnce(t *testing.T) {
	tool := &recordingSearchTool{output: `{"status":"ok","results":[{"title":"9月23日正式服S45赛季版本更新公告","url":"https://pvp.qq.com/808620","snippet":"王维上线"}]}`}
	client := &scriptedClient{responses: []string{
		`{"action":"tool","tool":"web_search","input":{"query":"王者荣耀 更新"}}`,
		`{"action":"final","content":"S45 上了王维，还加了传送花"}`,
		`{"unsupported":["传送花"]}`,
		`{"action":"final","content":"查到的是 S45 上了王维；传送花这些是印象，没查实"}`,
	}}
	runner, err := NewRunner(client, Config{MaxSteps: 4, ProtocolRepairLimit: 3, ResearchDetailReview: true}, NewToolRegistry(tool))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := runner.Run(context.Background(), Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "王者相比23年更新了哪些"}}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(resp.Text, "没查实") || len(client.requests) != 4 {
		t.Fatalf("text=%q requests=%d", resp.Text, len(client.requests))
	}
	review := requestContent(client.requests[2])
	if !strings.Contains(review, "王维上线") || !strings.Contains(review, "还加了传送花") {
		t.Fatalf("review request lacks evidence or draft:\n%s", review)
	}
	if repair := requestContent(client.requests[3]); !strings.Contains(repair, "找不到原文：传送花") {
		t.Fatalf("repair missing listed detail:\n%s", repair)
	}
}

func TestResearchDetailReviewPassesSupportedOrUnresearchedDrafts(t *testing.T) {
	tool := &recordingSearchTool{output: `{"status":"ok","results":[{"title":"S45","url":"https://pvp.qq.com/808620","snippet":"王维上线"}]}`}
	client := &scriptedClient{responses: []string{
		`{"action":"tool","tool":"web_search","input":{"query":"王者荣耀 更新"}}`,
		`{"action":"final","content":"S45 上了王维"}`,
		`{"unsupported":[]}`,
	}}
	runner, err := NewRunner(client, Config{MaxSteps: 4, ResearchDetailReview: true}, NewToolRegistry(tool))
	if err != nil {
		t.Fatal(err)
	}
	if resp, err := runner.Run(context.Background(), Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "王者更新了啥"}}}); err != nil || resp.Text != "S45 上了王维" || len(client.requests) != 3 {
		t.Fatalf("resp=%#v err=%v requests=%d", resp, err, len(client.requests))
	}

	chat := &scriptedClient{responses: []string{`{"action":"final","content":"晚安"}`}}
	runner, err = NewRunner(chat, Config{MaxSteps: 2, ResearchDetailReview: true}, NewToolRegistry(tool))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Run(context.Background(), Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "晚安"}}}); err != nil || len(chat.requests) != 1 {
		t.Fatalf("review ran without research: err=%v requests=%d", err, len(chat.requests))
	}
}

func TestParseUnsupportedDetails(t *testing.T) {
	for text, want := range map[string]string{
		"```json\n{\"unsupported\":[\"传送花\",\" \",\"兵线对齐\"]}\n```": "传送花、兵线对齐",
		`{"unsupported":[]}`: "",
		"看不懂":                "",
	} {
		if got := parseUnsupportedDetails(text); got != want {
			t.Errorf("parse(%q) = %q, want %q", text, got, want)
		}
	}
}
