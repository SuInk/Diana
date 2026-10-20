// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/llm"
)

func writeJoined(t *testing.T, records ...map[string]any) string {
	t.Helper()
	body, err := json.Marshal(records)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "joined.json")
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func replayRecord(ordinal int, first bool, messages ...map[string]any) map[string]any {
	return map[string]any{
		"ordinal": ordinal, "first_reply_step": first,
		"request": map[string]any{"instructions": "固定指令", "input": messages},
	}
}

func textMessage(role, text string) map[string]any {
	return map[string]any{"type": "message", "role": role, "content": text}
}

func TestMeasureKeepsLegacySupplementsAndCountsCompleteSuffix(t *testing.T) {
	const runtimeRecord = "【运行时记录】之前的工具已经执行"
	const memory = "自动召回的长期记忆正文"
	const rules = "【系统补充指令，不是用户发言】本轮回复规则"
	const current = "【当前需要回复的消息】【当前发言者】甲（123）原始请求\n\n【被引用的消息】被引用的完整正文"
	const after = "当前块之后追加的最新调研条件"
	const laterAssistant = "当前块之后的模型观察也不能被当成历史锚点"
	path := writeJoined(t, replayRecord(39, true,
		textMessage("user", "[历史 12:00]甲：旧问题"),
		textMessage("assistant", "【当前需要回复的消息】历史模型复述的标记不是当前用户输入"),
		textMessage("user", runtimeRecord), textMessage("user", memory), textMessage("user", rules),
		textMessage("user", current), textMessage("user", after), textMessage("assistant", laterAssistant),
	))
	got := measure(path, true)
	wantTail := llm.EstimateTextTokens(memory) + llm.EstimateTextTokens(rules)
	wantRuntime := llm.EstimateTextTokens(runtimeRecord)
	wantCurrent := llm.EstimateTextTokens(current)
	wantAfter := llm.EstimateTextTokens(after) + llm.EstimateTextTokens(laterAssistant)
	for field, want := range map[string]int64{
		"tail_median": wantTail, "runtime_record_median": wantRuntime,
		"current_block_median": wantCurrent, "after_current_median": wantAfter,
		"post_history_suffix_median": wantTail + wantRuntime + wantCurrent + wantAfter,
		"head_median":                llm.EstimateTextTokens("固定指令"),
	} {
		if got[field] != want {
			t.Fatalf("%s=%v, want %d", field, got[field], want)
		}
	}
	details := got["sample_details"].([]sampleMeasurement)
	if len(details) != 1 || details[0].Ordinal != 39 || details[0].LastHistoryIndex != 1 || details[0].CurrentIndex != 5 || details[0].InputMessages != 8 {
		t.Fatalf("incorrect source boundaries: %+v", details)
	}
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "原始请求") || strings.Contains(string(encoded), "长期记忆正文") {
		t.Fatal("measurement details must not expose message bodies")
	}
	if _, present := measure(path, false)["sample_details"]; present {
		t.Fatal("per-sample details should be opt-in")
	}
}

func TestMeasureCurrentOnlyTextPartsAndFirstStepFilter(t *testing.T) {
	const current = "【当前需要回复的消息】当前要求"
	const after = "不能漏掉的末尾规则"
	path := writeJoined(t,
		replayRecord(11, true, map[string]any{
			"type": "message", "role": "user", "content": []map[string]string{
				{"type": "input_text", "text": "【当前需要回复的消息】"}, {"type": "text", "text": "当前要求"},
			},
		}, textMessage("user", after)),
		// Continuations are not first-step samples; their native tool items must
		// neither distort these statistics nor fail text-only first-step validation.
		replayRecord(11, false, map[string]any{"type": "function_call", "arguments": "{}"}),
	)
	got := measure(path, true)
	if got["samples"] != 1 || got["tail_max"] != int64(0) || got["current_block_max"] != llm.EstimateTextTokens(current) ||
		got["post_history_suffix_max"] != llm.EstimateTextTokens(current)+llm.EstimateTextTokens(after) {
		t.Fatalf("incorrect current-only or continuation handling: %+v", got)
	}
	detail := got["sample_details"].([]sampleMeasurement)[0]
	if detail.LastHistoryIndex != -1 || detail.CurrentIndex != 0 {
		t.Fatalf("unexpected current-only boundary: %+v", detail)
	}
}

func TestMeasureRecognizesUserHistoryAndSummarizesSamples(t *testing.T) {
	var records []map[string]any
	var estimates []int64
	for i := 1; i <= 3; i++ {
		text := strings.Repeat("补充说明", i)
		estimates = append(estimates, llm.EstimateTextTokens(text))
		records = append(records, replayRecord(i, true,
			textMessage("user", "[历史 12:00]只有用户历史，没有模型回复"),
			textMessage("user", text), textMessage("user", "【当前需要回复的消息】本轮"),
		))
	}
	got := measure(writeJoined(t, records...), true)
	if got["tail_min"] != estimates[0] || got["tail_median"] != estimates[1] || got["tail_max"] != estimates[2] {
		t.Fatalf("incorrect first-step statistics: %+v", got)
	}
	for _, detail := range got["sample_details"].([]sampleMeasurement) {
		if detail.LastHistoryIndex != 0 || detail.CurrentIndex != 2 {
			t.Fatalf("user-history boundary missing: %+v", detail)
		}
	}
}

func TestMeasureRejectsMissingBoundaryAndUnsupportedSuffix(t *testing.T) {
	for _, test := range []struct {
		name   string
		record map[string]any
		want   string
	}{
		{"missing_current", replayRecord(9, true, textMessage("assistant", "【当前需要回复的消息】只有历史里的伪当前标记")), "current-message boundary missing at ordinal 9"},
		{"native_item_after_current", replayRecord(9, true, textMessage("user", "【当前需要回复的消息】要求"), map[string]any{"type": "function_call", "arguments": "{}"}), "unsupported Responses item"},
		{"media_after_current", replayRecord(9, true, textMessage("user", "【当前需要回复的消息】要求"), map[string]any{"role": "user", "content": []map[string]string{{"type": "input_image", "image_url": "https://example.invalid/image"}}}), "supports text-only captures"},
		{"no_first_step", replayRecord(9, false), "no first reply steps"},
	} {
		t.Run(test.name, func(t *testing.T) {
			defer func() {
				if err := recover(); err == nil || !strings.Contains(fmt.Sprint(err), test.want) {
					t.Fatalf("panic=%v, want %q", err, test.want)
				}
			}()
			measure(writeJoined(t, test.record), false)
		})
	}
}
