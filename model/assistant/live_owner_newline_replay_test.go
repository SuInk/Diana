// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/SuInk/diana/model/llm"
)

// 生产回放：「本群消息不换行」改成「换行即分条」、尾部补【主人】段，各自 A/B 对照。
//
// DIANA_REPLAY_OWNER_NEWLINE 指向从 debug-traces 导出的 JSON 数组，每项是
// {"kind": "newline"|"control"|"owner", "owner_alias": "...", "request": {...}}；
// request 是 trace 里 metadata.request 原样（已是隐私别名）。结果逐条写到
// DIANA_REPLAY_OUT（JSONL），人工看主人那几条；换行那几条按「中文 空格 中文」统计硬接。
// 不执行模型返回的工具，只看第一步 send 的正文。
func TestLiveOwnerNewlineReplay(t *testing.T) {
	path := os.Getenv("DIANA_REPLAY_OWNER_NEWLINE")
	if path == "" {
		t.Skip("set DIANA_REPLAY_OWNER_NEWLINE, DIANA_REPLAY_OUT and the DIANA_TEST_LLM_* variables")
	}
	client := liveLLMClient(t)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var fixtures []struct {
		Kind       string              `json:"kind"`
		File       string              `json:"file"`
		OwnerAlias string              `json:"owner_alias"`
		Request    llm.GenerateRequest `json:"request"`
	}
	if err := json.Unmarshal(raw, &fixtures); err != nil {
		t.Fatal(err)
	}
	out, err := os.Create(os.Getenv("DIANA_REPLAY_OUT"))
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	samples := 2
	oldRule := "你也一条消息只写一行，要换行的地方就拆成下一条发。"
	newRule := strings.TrimPrefix(promptGroupNoNewlineSplit, "大家的消息几乎不换行（只有 {percent}% 带换行），")
	totals := map[string][2]int{}
	kinds := os.Getenv("DIANA_REPLAY_KINDS") // 逗号分隔，空表示全跑
	armNames := []string{"A", "B"}
	if os.Getenv("DIANA_REPLAY_ONLY_B") == "1" {
		armNames = []string{"B"}
	}
	for _, fixture := range fixtures {
		if kinds != "" && !strings.Contains(","+kinds+",", ","+fixture.Kind+",") {
			continue
		}
		arms := map[string]llm.GenerateRequest{"A": fixture.Request}
		switch fixture.Kind {
		case "newline", "control":
			b := cloneReplayRequest(fixture.Request)
			replaced := 0
			for i := range b.Messages {
				if b.Messages[i].Role == llm.RoleSystem {
					replaced += strings.Count(b.Messages[i].Content, oldRule)
					b.Messages[i].Content = strings.ReplaceAll(b.Messages[i].Content, oldRule, newRule)
				}
			}
			if replaced == 0 {
				t.Fatalf("%s: old no-newline rule not found", fixture.File)
			}
			arms["B"] = b
		case "owner":
			b := cloneReplayRequest(fixture.Request)
			current := -1
			for i := range b.Messages {
				if b.Messages[i].Role == llm.RoleUser && strings.Contains(b.Messages[i].Content, "【当前需要回复的消息】") {
					current = i
				}
			}
			if current < 0 {
				t.Fatalf("%s: current message not found", fixture.File)
			}
			block := llm.Message{Role: llm.RoleSystem, Content: strings.ReplaceAll(promptOwnerAccount, "{owner}", fixture.OwnerAlias)}
			b.Messages = append(b.Messages[:current], append([]llm.Message{block}, b.Messages[current:]...)...)
			arms["B"] = b
		}
		for _, arm := range armNames {
			for sample := 1; sample <= samples; sample++ {
				ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
				resp, err := client.Generate(ctx, arms[arm])
				cancel()
				text, sends := "", 0
				if err == nil {
					text = resp.Text
					for _, call := range resp.ToolCalls {
						if value, ok := call.Arguments["text"].(string); ok && call.Name == "send" {
							sends++
							if text != "" {
								text += "\n<send>\n"
							}
							text += value
						}
					}
				}
				joined := replayJoinedBySpace.MatchString(text)
				t.Logf("%s %s %s#%d joined=%v err=%v %q", fixture.Kind, fixture.File, arm, sample, joined, err, text)
				line, _ := json.Marshal(map[string]any{"kind": fixture.Kind, "file": fixture.File, "arm": arm, "sample": sample,
					"text": text, "sends": sends, "joined": joined, "error": errorString(err)})
				out.Write(append(line, '\n'))
				if fixture.Kind != "owner" && err == nil {
					count := totals[fixture.Kind+arm]
					count[0]++
					if joined {
						count[1]++
					}
					totals[fixture.Kind+arm] = count
				}
			}
		}
	}
	for key, count := range totals {
		t.Logf("%s: %d/%d joined by space", key, count[1], count[0])
	}
}

// 中文（含常见中文标点）后面跟一个空格再接中文：把两句硬接成一行的写法。
var replayJoinedBySpace = regexp.MustCompile(`[\p{Han}，。！？（）～] [\p{Han}]`)

func cloneReplayRequest(req llm.GenerateRequest) llm.GenerateRequest {
	req.Messages = append([]llm.Message(nil), req.Messages...)
	return req
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
