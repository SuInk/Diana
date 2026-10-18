// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

// Measure the trailing supplements of the text-only Responses replay captures.
// This is an offline diagnostic, not a production token limit or a tokenizer.
package main

import (
	"encoding/json"
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
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"input"`
	} `json:"request"`
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

func measure(path string) any {
	body, err := os.ReadFile(path)
	if err != nil {
		panic(err)
	}
	var records []wireRecord
	if err := json.Unmarshal(body, &records); err != nil {
		panic(err)
	}
	var tails, heads []int64
	for _, record := range records {
		if !record.FirstReplyStep {
			continue
		}
		lastHistory, current := -1, -1
		texts := make([]string, len(record.Request.Input))
		for index, message := range record.Request.Input {
			texts[index] = contentText(message.Content)
			if strings.HasPrefix(texts[index], "【当前需要回复的消息】") {
				current = index
				break
			}
			if message.Role == "assistant" || strings.HasPrefix(texts[index], "[历史 ") {
				lastHistory = index
			}
		}
		if current < 0 {
			panic(fmt.Sprintf("current-message boundary missing at ordinal %d", record.Ordinal))
		}
		var tail int64
		for index := lastHistory + 1; index < current; index++ {
			// This record belongs to the preceding persisted assistant reply.
			if !strings.HasPrefix(texts[index], "【运行时记录") {
				tail += llm.EstimateTextTokens(texts[index])
			}
		}
		tails = append(tails, tail)
		heads = append(heads, llm.EstimateTextTokens(record.Request.Instructions))
	}
	if len(tails) == 0 {
		panic("no first reply steps; run analyze.py on a completed replay first")
	}
	sort.Slice(tails, func(i, j int) bool { return tails[i] < tails[j] })
	sort.Slice(heads, func(i, j int) bool { return heads[i] < heads[j] })
	return map[string]any{
		"path": path, "samples": len(tails),
		"tail_min": tails[0], "tail_median": tails[len(tails)/2], "tail_max": tails[len(tails)-1],
		"head_median": heads[len(heads)/2],
	}
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: go run ./scripts/cache-replay/context-size <joined.json> [joined.json...]")
		os.Exit(2)
	}
	for _, path := range os.Args[1:] {
		if err := json.NewEncoder(os.Stdout).Encode(measure(path)); err != nil {
			panic(err)
		}
	}
}
