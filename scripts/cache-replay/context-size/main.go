// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

// Measure several text-only Responses suffix scopes from offline replay captures.
// This is an offline diagnostic, not a production token limit or a tokenizer.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/SuInk/diana/model/llm"
)

type wireRecord struct {
	FirstReplyStep bool `json:"first_reply_step"`
	Ordinal        int  `json:"ordinal"`
	Request        struct {
		Instructions string `json:"instructions"`
		Input        []struct {
			Type    string          `json:"type"`
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"input"`
	} `json:"request"`
}

type sampleMeasurement struct {
	Ordinal                 int   `json:"ordinal"`
	LastHistoryIndex        int   `json:"last_history_index"`
	CurrentIndex            int   `json:"current_index"`
	InputMessages           int   `json:"input_messages"`
	BeforeCurrentTokens     int64 `json:"before_current_tokens"`
	PostHistorySuffixTokens int64 `json:"post_history_suffix_tokens"`
	CurrentBlockTokens      int64 `json:"current_block_tokens"`
	RuntimeRecordTokens     int64 `json:"runtime_record_tokens"`
	AfterCurrentTokens      int64 `json:"after_current_tokens"`
}

func contentText(body json.RawMessage) string {
	var text string
	if len(body) == 0 || json.Unmarshal(body, &text) == nil {
		return text
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(body, &parts); err != nil {
		panic("unsupported Responses content shape")
	}
	for _, part := range parts {
		if part.Type != "input_text" && part.Type != "output_text" && part.Type != "text" {
			panic("context-size supports text-only captures")
		}
		text += part.Text
	}
	return text
}

func measure(path string, includeDetails bool) map[string]any {
	body, err := os.ReadFile(path)
	if err != nil {
		panic(err)
	}
	var records []wireRecord
	if err := json.Unmarshal(body, &records); err != nil {
		panic(err)
	}
	var heads []int64
	values := map[string][]int64{
		"tail": nil, "post_history_suffix": nil, "current_block": nil,
		"runtime_record": nil, "after_current": nil,
	}
	var details []sampleMeasurement
	for _, record := range records {
		if !record.FirstReplyStep {
			continue
		}
		lastHistory, current := -1, -1
		texts := make([]string, len(record.Request.Input))
		for index, message := range record.Request.Input {
			if message.Type != "" && message.Type != "message" {
				panic(fmt.Sprintf("unsupported Responses item %q at ordinal %d", message.Type, record.Ordinal))
			}
			texts[index] = contentText(message.Content)
			if current < 0 && message.Role == "user" && strings.HasPrefix(texts[index], "【当前需要回复的消息】") {
				current = index
			}
			if current < 0 && (message.Role == "assistant" || strings.HasPrefix(texts[index], "[历史 ")) {
				lastHistory = index
			}
		}
		if current < 0 {
			panic(fmt.Sprintf("current-message boundary missing at ordinal %d", record.Ordinal))
		}
		sample := sampleMeasurement{
			Ordinal: record.Ordinal, LastHistoryIndex: lastHistory, CurrentIndex: current,
			InputMessages: len(texts), CurrentBlockTokens: llm.EstimateTextTokens(texts[current]),
		}
		for index := lastHistory + 1; index < len(texts); index++ {
			tokens := llm.EstimateTextTokens(texts[index])
			sample.PostHistorySuffixTokens += tokens
			switch {
			case index > current:
				sample.AfterCurrentTokens += tokens
			case index < current && strings.HasPrefix(texts[index], "【运行时记录"):
				// Preserve the legacy tail exclusion, but expose these persisted records
				// in the conservative complete suffix and their own measurement.
				sample.RuntimeRecordTokens += tokens
			case index < current:
				sample.BeforeCurrentTokens += tokens
			}
		}
		values["tail"] = append(values["tail"], sample.BeforeCurrentTokens)
		values["post_history_suffix"] = append(values["post_history_suffix"], sample.PostHistorySuffixTokens)
		values["current_block"] = append(values["current_block"], sample.CurrentBlockTokens)
		values["runtime_record"] = append(values["runtime_record"], sample.RuntimeRecordTokens)
		values["after_current"] = append(values["after_current"], sample.AfterCurrentTokens)
		if includeDetails {
			details = append(details, sample)
		}
		heads = append(heads, llm.EstimateTextTokens(record.Request.Instructions))
	}
	if len(heads) == 0 {
		panic("no first reply steps; run analyze.py on a completed replay first")
	}
	sort.Slice(heads, func(i, j int) bool { return heads[i] < heads[j] })
	result := map[string]any{
		"path": path, "samples": len(heads),
		"estimate":     "Diana.EstimateTextTokens:text_only",
		"tail_scope":   "supplements_before_current_excluding_persisted_runtime_records",
		"suffix_scope": "all_text_after_inferred_history_including_runtime_current_and_after_current",
		"head_median":  heads[len(heads)/2],
	}
	for name, tokens := range values {
		sort.Slice(tokens, func(i, j int) bool { return tokens[i] < tokens[j] })
		result[name+"_min"] = tokens[0]
		result[name+"_median"] = tokens[len(tokens)/2]
		result[name+"_max"] = tokens[len(tokens)-1]
	}
	if includeDetails {
		result["sample_details"] = details
	}
	return result
}

func main() {
	includeDetails := flag.Bool("details", false, "include per-ordinal boundaries and text estimates without message bodies")
	flag.Parse()
	if flag.NArg() == 0 {
		fmt.Fprintln(os.Stderr, "usage: go run ./scripts/cache-replay/context-size [--details] <joined.json> [joined.json...]")
		os.Exit(2)
	}
	for _, path := range flag.Args() {
		if err := json.NewEncoder(os.Stdout).Encode(measure(path, *includeDetails)); err != nil {
			panic(err)
		}
	}
}
